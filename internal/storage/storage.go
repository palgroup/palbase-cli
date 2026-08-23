// Package storage provides the `palbase storage` subcommand group:
// list / add / remove. These commands are GUIDED authoring for the storage
// config-as-code surface — they read/write config/storage.ts (the typed DSL
// from @palbase/backend), so an author declares buckets without hand-writing
// TypeScript. On `git push`, the deploy evals config/storage.ts and reconciles
// the buckets against the tenant (create missing, update changed).
//
// The CLI is the SOLE author of config/storage.ts: every write regenerates the
// whole file from the current bucket set (deterministic template), and reads
// parse that same generated shape back. This sidesteps having to parse
// arbitrary TypeScript in Go — the file is config-as-code data the CLI fully
// owns. A user hand-edit that keeps the generated `name: bucket({ ... })` shape
// still round-trips; a free-form rewrite would not.
//
// No secrets are involved (buckets carry no credentials), so these commands
// never touch the encrypted store — pure local file authoring.
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// configPath is the project-relative path the deploy's br-pod evals.
const configPath = "config/storage.ts"

// bucketNameRE bounds a bucket name to the Storage module's allowed shape
// (lowercase/digits/dash/dot/underscore, must start alnum). Matches the SDK's
// BUCKET_NAME_RE so a name the CLI accepts is a name Storage accepts.
var bucketNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// mimeRE is the same loose type/subtype (wildcard ok) check the SDK's bucket()
// uses, so the CLI rejects a bad --mime before it reaches the generated file.
var mimeRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9!#$&^_.+-]*/(?:\*|[a-zA-Z0-9][a-zA-Z0-9!#$&^_.+-]*)$`)

// unitBytes are binary (IEC) multipliers, matching the SDK's parseFileSizeLimit
// and the Storage module's `bytes` parser: 5MB = 5 * 1024 * 1024.
var unitBytes = map[string]int64{
	"b":  1,
	"kb": 1024,
	"mb": 1024 * 1024,
	"gb": 1024 * 1024 * 1024,
	"tb": 1024 * 1024 * 1024 * 1024,
}

// bucketDef is the CLI's in-memory model of one bucket — the same normalized
// shape the SDK's BucketDef serializes (public, fileSizeLimit bytes-or-nil,
// allowedMimeTypes list-or-nil).
type bucketDef struct {
	Name             string   `json:"name"`
	Public           bool     `json:"public"`
	FileSizeLimit    *int64   `json:"file_size_limit,omitempty"`
	AllowedMimeTypes []string `json:"allowed_mime_types,omitempty"`

	// SizeLiteral is the raw TS literal fileSizeLimit was written as in the file
	// (`"25MB"` or `26214400`), kept so a rewrite re-emits it VERBATIM instead of
	// normalizing an author's human string into bytes. Empty for a bucket the CLI
	// is creating (the generator then falls back to the byte count).
	SizeLiteral string `json:"-"`
}

// Cmd returns the `palbase storage` parent command. It takes no resolvers:
// bucket authoring is purely local file I/O (no Studio / network). Buckets are
// the only storage config, so the verbs hang directly off `storage` (no
// intermediate `buckets` level — matches `flags add` / `notifications add`).
// REST reaches the linked stack's management surface.
type REST interface {
	Do(ctx context.Context, method, path string, body []byte) (int, []byte, error)
}

// Resolvers carries the lazily-built dependency; resolving announces the target.
type Resolvers struct {
	REST func(*cobra.Command) (REST, error)
}

const bucketsPath = "/v1/management/storage/buckets"

func call(r Resolvers, cmd *cobra.Command, method, path string, body []byte) ([]byte, error) {
	rest, err := r.REST(cmd)
	if err != nil {
		return nil, err
	}
	status, raw, err := rest.Do(cmd.Context(), method, path, body)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Description != "" {
			return nil, fmt.Errorf("%s: %s", e.Error, e.Description)
		}
		return nil, fmt.Errorf("the stack answered %d: %s", status, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Manage this project's storage buckets",
		Long: `The buckets this project stores files in.

  palbase storage list              Show the buckets on this stack.
  palbase storage add <name> ...    Create or update a bucket.
  palbase storage remove <name>     Remove a bucket.

They live ON THE STACK. They used to be declared in config/storage.ts and
reconciled on every push, which meant the panel could not change one without the
next deploy putting it back — and ` + "`remove`" + ` only edited the file, leaving the
live bucket and its files in place. Removing here removes.`,
	}
	cmd.AddCommand(bucketsListCmd(r), bucketsAddCmd(r), bucketsRemoveCmd(r))
	return cmd
}

// parseSize converts a --max-size value ("5MB", "20MB", "1024") to bytes.
// Binary units, case-insensitive; a bare number is bytes. Mirrors the SDK's
// parseFileSizeLimit so the CLI and the typed DSL agree.
func parseSize(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	re := regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([a-zA-Z]+)?$`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("--max-size must be \"<number><unit>\" like 5MB or 1GB (got %q)", raw)
	}
	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid --max-size number %q", m[1])
	}
	unit := "b"
	if m[2] != "" {
		unit = strings.ToLower(m[2])
	}
	mult, ok := unitBytes[unit]
	if !ok {
		return 0, fmt.Errorf("unknown --max-size unit %q — use B, KB, MB, GB, or TB", m[2])
	}
	bytes := val * float64(mult)
	if bytes < 0 {
		return 0, fmt.Errorf("--max-size must not be negative")
	}
	return int64(bytes + 0.5), nil
}

// parseSizeLiteral converts a fileSizeLimit TS literal as written in
// config/storage.ts — a quoted human string (`"25MB"`) or a bare byte count
// (`26214400`) — to bytes. Both forms are valid SDK input, so both must read
// back; parseSize handles the string form's units.
func parseSizeLiteral(lit string) (int64, error) {
	if strings.HasPrefix(lit, `"`) {
		unq, err := strconv.Unquote(lit)
		if err != nil {
			return 0, fmt.Errorf("not a valid string literal")
		}
		n, err := parseSize(unq)
		if err != nil {
			return 0, fmt.Errorf("expected a size like \"5MB\" or \"1GB\"")
		}
		return n, nil
	}
	return strconv.ParseInt(lit, 10, 64)
}

func bucketsAddCmd(r Resolvers) *cobra.Command {
	var (
		publicFlag bool
		maxSize    string
		mimeFlag   string
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create or update a bucket",
		Long: `Create a bucket, or update it if it is already there.

  palbase storage add avatars --public --max-size 5MB --mime image/png,image/jpeg

Idempotent, and it lands on the stack immediately — there is no file to commit
and no deploy to wait for.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !bucketNameRE.MatchString(name) {
				return fmt.Errorf("invalid bucket name %q — must match %s", name, bucketNameRE.String())
			}
			def := map[string]any{"name": name, "public": publicFlag}
			if maxSize != "" {
				size, err := parseSize(maxSize)
				if err != nil {
					return err
				}
				def["file_size_limit"] = size
			}
			if mimeFlag != "" {
				mimes, err := parseMimes(mimeFlag)
				if err != nil {
					return err
				}
				def["allowed_mime_types"] = mimes
			}
			body, err := json.Marshal(def)
			if err != nil {
				return err
			}
			if _, err := call(r, cmd, http.MethodPut, bucketsPath+"/"+url.PathEscape(name), body); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ bucket %q ready\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&publicFlag, "public", false, "anyone with the URL may read it")
	cmd.Flags().StringVar(&maxSize, "max-size", "", "per-file ceiling, e.g. 5MB")
	cmd.Flags().StringVar(&mimeFlag, "mime", "", "allowed MIME types, comma-separated")
	return cmd
}

func bucketsRemoveCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a bucket",
		Long: `Remove a bucket from the stack.

DESTRUCTIVE: a bucket holds files. This used to edit config/storage.ts and leave
the live bucket and its objects in place, which made the command look reversible
while the real state never changed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := call(r, cmd, http.MethodDelete, bucketsPath+"/"+url.PathEscape(args[0]), nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ bucket %q removed\n", args[0])
			return nil
		},
	}
}

func bucketsListCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show the buckets on this stack",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := call(r, cmd, http.MethodGet, bucketsPath, nil)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				fmt.Fprintln(out, strings.TrimSpace(string(raw)))
				return nil
			}
			var buckets []struct {
				Name        string `json:"name"`
				Public      bool   `json:"public"`
				ObjectCount int    `json:"object_count"`
				TotalBytes  int64  `json:"total_bytes"`
			}
			if err := json.Unmarshal(raw, &buckets); err != nil {
				fmt.Fprintln(out, strings.TrimSpace(string(raw)))
				return nil
			}
			if len(buckets) == 0 {
				fmt.Fprintln(out, "this stack has no buckets")
				fmt.Fprintln(out, "  add one: palbase storage add avatars --public")
				return nil
			}
			sort.Slice(buckets, func(a, b int) bool { return buckets[a].Name < buckets[b].Name })
			fmt.Fprintln(out, "buckets on this stack:")
			for _, b := range buckets {
				vis := "private"
				if b.Public {
					vis = "public"
				}
				fmt.Fprintf(out, "  %-24s %-8s %d file(s), %d bytes\n", b.Name, vis, b.ObjectCount, b.TotalBytes)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the stack's answer as JSON")
	return cmd
}

// parseMimes splits + validates a comma-separated --mime list. Dedupes while
// preserving first-seen order; rejects a malformed type/subtype.
func parseMimes(raw string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		m := strings.TrimSpace(part)
		if m == "" {
			continue
		}
		if !mimeRE.MatchString(m) {
			return nil, fmt.Errorf("invalid MIME type %q — expected type/subtype, e.g. image/png", m)
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out, nil
}

// describe renders a one-line human summary of a bucket for list/add output.
func describe(b bucketDef) string {
	access := "private"
	if b.Public {
		access = "public"
	}
	size := "no size limit"
	if b.FileSizeLimit != nil {
		size = humanBytes(*b.FileSizeLimit)
	}
	mimes := "any type"
	if len(b.AllowedMimeTypes) > 0 {
		mimes = strings.Join(b.AllowedMimeTypes, ", ")
	}
	return fmt.Sprintf("%s, %s, %s", access, size, mimes)
}

// humanBytes renders a byte count using the same binary units the CLI accepts,
// preferring the largest unit that divides evenly (so 5242880 -> "5MB").
func humanBytes(n int64) string {
	type unit struct {
		name string
		mult int64
	}
	units := []unit{{"TB", unitBytes["tb"]}, {"GB", unitBytes["gb"]}, {"MB", unitBytes["mb"]}, {"KB", unitBytes["kb"]}}
	for _, u := range units {
		if n >= u.mult && n%u.mult == 0 {
			return fmt.Sprintf("%d%s", n/u.mult, u.name)
		}
	}
	return fmt.Sprintf("%dB", n)
}
