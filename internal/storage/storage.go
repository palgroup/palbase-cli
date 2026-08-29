// Package storage provides the `palbase storage` subcommand group:
// list / add / remove. These commands write BUCKETS ON THE STACK through the
// management API — they are the door, not a file authoring aid.
//
// THE DEPLOY DOES NOT RECONCILE THEM, and this comment used to say it did.
// Buckets live ON THE STACK: the declaration applier was retired deliberately
// (v2 S-005 — "settings have one door, and a second one is how the two
// disagree"), so `git push` evals nothing here and creates nothing. The push
// only CHECKS: an `@Upload` naming a bucket the stack does not hold is refused,
// because storage will not create one on demand.
//
// The stale sentence had a cost. A project whose config/storage.ts declared
// three webp variants was pushed to a fresh stack, deployed green, and held no
// buckets at all; the first upload would have 404'd (measured on `centauri`,
// 2026-08-24).
//
// IT USED TO WRITE config/storage.ts AND THAT FILE IS GONE (2026-08-29). The
// declaration was applied to nothing after the applier was retired, so a project
// could declare three buckets, deploy green, and hold none — measured on
// `centauri`, 2026-08-24. What a controller may SPELL is now a generated type
// (`palbase-stack.d.ts`), and what EXISTS is what these commands wrote.
//
// No secrets are involved (buckets carry no credentials), so these commands
// never touch the encrypted store.
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
// The SPELLING is the module's, not a third one. The storage module reads
// `fileSizeLimit` / `allowedMimeTypes`; this used to send snake_case, the
// management layer forwards raw bytes, and nothing complained — the bucket was
// created with no size limit and no type list, and the only way to notice was to
// upload a file that should have been refused.
type bucketDef struct {
	Name             string   `json:"name"`
	Public           bool     `json:"public"`
	FileSizeLimit    *int64   `json:"fileSizeLimit,omitempty"`
	AllowedMimeTypes []string `json:"allowedMimeTypes,omitempty"`

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

// variantSpec is one rendition as `--variant` writes it, in the shape the
// storage module reads (handler.go bucketDeclaration.Variants).
type variantSpec struct {
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Fit     string `json:"fit"`
	Format  string `json:"format"`
	Quality int    `json:"quality,omitempty"`
}

// variantNameRE mirrors the module's own check so a bad name is refused here
// rather than after a round trip.
var variantNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// validFits are the three the renderer implements; anything else is refused by
// the stack, and refusing it here says so with the list.
var validFits = map[string]bool{"cover": true, "contain": true, "inside": true}

// parseVariant reads `name=WxH:fit:format[:quality]`.
//
// WHY THE FLAG EXISTS AT ALL. The stack renders renditions, serves them at
// `?variant=`, and carries them through move/copy — the whole machine is built.
// Nothing could DECLARE one: `storage add` had three flags, the panel has no
// UI, and the config-file path was retired. A measured customer dropped image
// variants from their design because the product offered no way to ask for one.
func parseVariant(raw string) (string, variantSpec, error) {
	name, rest, ok := strings.Cut(raw, "=")
	if !ok || name == "" || rest == "" {
		return "", variantSpec{}, fmt.Errorf("--variant must be name=WxH:fit:format[:quality] (got %q)", raw)
	}
	if !variantNameRE.MatchString(name) {
		return "", variantSpec{}, fmt.Errorf("variant name %q must match %s", name, variantNameRE.String())
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 3 || len(parts) > 4 {
		return "", variantSpec{}, fmt.Errorf("--variant %q: expected WxH:fit:format[:quality]", raw)
	}
	w, h, dimOK := strings.Cut(parts[0], "x")
	if !dimOK {
		return "", variantSpec{}, fmt.Errorf("--variant %q: size must be WxH, e.g. 640x480", raw)
	}
	width, wErr := strconv.Atoi(w)
	height, hErr := strconv.Atoi(h)
	if wErr != nil || hErr != nil || width <= 0 || height <= 0 {
		return "", variantSpec{}, fmt.Errorf("--variant %q: width and height must be positive whole numbers", raw)
	}
	if !validFits[parts[1]] {
		return "", variantSpec{}, fmt.Errorf("--variant %q: fit must be cover, contain or inside", raw)
	}
	spec := variantSpec{Width: width, Height: height, Fit: parts[1], Format: parts[2]}
	if spec.Format == "" {
		return "", variantSpec{}, fmt.Errorf("--variant %q: a format is required (the stack refuses one it cannot render)", raw)
	}
	if len(parts) == 4 {
		q, qErr := strconv.Atoi(parts[3])
		if qErr != nil || q < 1 || q > 100 {
			return "", variantSpec{}, fmt.Errorf("--variant %q: quality must be a whole number 1-100", raw)
		}
		spec.Quality = q
	}
	return name, spec, nil
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
		publicFlag   bool
		maxSize      string
		mimeFlag     string
		variantFlags []string
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create or update a bucket",
		Long: `Create a bucket, or update it if it is already there.

  palbase storage add avatars --public --max-size 5MB --mime image/png,image/jpeg
  palbase storage add posts --variant card=640x480:cover:webp:82 --variant thumb=160x160:cover:webp

A --variant asks the stack to render that size when an image is uploaded; the
client then requests it with ?variant=<name>. Repeat the flag per rendition.

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
				def["fileSizeLimit"] = size
			}
			if mimeFlag != "" {
				mimes, err := parseMimes(mimeFlag)
				if err != nil {
					return err
				}
				def["allowedMimeTypes"] = mimes
			}
			if len(variantFlags) > 0 {
				variants := map[string]variantSpec{}
				for _, raw := range variantFlags {
					name, spec, vErr := parseVariant(raw)
					if vErr != nil {
						return vErr
					}
					if _, dup := variants[name]; dup {
						return fmt.Errorf("--variant %q declared twice", name)
					}
					variants[name] = spec
				}
				def["variants"] = variants
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
	cmd.Flags().StringArrayVar(&variantFlags, "variant", nil,
		"an image rendition, repeatable: name=WxH:fit:format[:quality] (fit: cover|contain|inside)")
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
				// A bucket's renditions. Shown because they are declared here and
				// nowhere else: without them the listing cannot answer "what did I
				// ask this bucket to render", which is the question you have right
				// after asking for one.
				Variants []struct {
					Name string `json:"name"`
				} `json:"variants"`
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
				if len(b.Variants) > 0 {
					names := make([]string, 0, len(b.Variants))
					for _, v := range b.Variants {
						names = append(names, v.Name)
					}
					sort.Strings(names)
					fmt.Fprintf(out, "  %-24s variants: %s\n", "", strings.Join(names, ", "))
				}
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
