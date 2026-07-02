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
	"encoding/json"
	"fmt"
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
}

// Cmd returns the `palbase storage` parent command. It takes no resolvers:
// bucket authoring is purely local file I/O (no Studio / network). Buckets are
// the only storage config, so the verbs hang directly off `storage` (no
// intermediate `buckets` level — matches `flags add` / `notifications add`).
func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Manage storage config-as-code (config/storage.ts buckets)",
		Long: `Author the project's storage buckets declaratively in config/storage.ts.

  palbase storage list              Show the buckets declared in config/storage.ts.
  palbase storage add <name> ...    Add or update a bucket entry.
  palbase storage remove <name>     Remove a bucket entry.

config/storage.ts is git-authoritative: commit it and ` + "`git push`" + ` to deploy.
The deploy creates missing buckets and updates changed ones; it never deletes a
bucket dropped from the file (removing here leaves the live bucket + its files
in place — see ` + "`remove`" + `).`,
	}
	cmd.AddCommand(bucketsListCmd(), bucketsAddCmd(), bucketsRemoveCmd())
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

func bucketsAddCmd() *cobra.Command {
	var (
		publicFlag bool
		maxSize    string
		mimeFlag   string
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or update a bucket in config/storage.ts",
		Long: `Add a bucket entry to config/storage.ts (or update it if it already exists).

  palbase storage add avatars --public --max-size 5MB --mime image/png,image/jpeg

Idempotent: running it again with the same name updates the existing entry
rather than duplicating it. Commit config/storage.ts and ` + "`git push`" + ` to apply.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !bucketNameRE.MatchString(name) {
				return fmt.Errorf("invalid bucket name %q — must match %s", name, bucketNameRE.String())
			}

			def := bucketDef{Name: name, Public: publicFlag}
			if maxSize != "" {
				size, err := parseSize(maxSize)
				if err != nil {
					return err
				}
				def.FileSizeLimit = &size
			}
			if mimeFlag != "" {
				mimes, err := parseMimes(mimeFlag)
				if err != nil {
					return err
				}
				def.AllowedMimeTypes = mimes
			}

			buckets, err := readConfig()
			if err != nil {
				return err
			}
			_, existed := buckets[name]
			buckets[name] = def
			if err := writeConfig(buckets); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			verb := "added"
			if existed {
				verb = "updated"
			}
			fmt.Fprintf(out, "✓ %s bucket %q in %s\n", verb, name, configPath)
			fmt.Fprintf(out, "  %s\n", describe(def))
			fmt.Fprintf(out, "  commit %s and `git push` to deploy\n", configPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&publicFlag, "public", false, "Serve the bucket's files without a signed URL")
	cmd.Flags().StringVar(&maxSize, "max-size", "", "Per-file size limit, e.g. 5MB, 20MB, 1GB (omit for no limit)")
	cmd.Flags().StringVar(&mimeFlag, "mime", "", "Comma-separated allowed MIME types, e.g. image/png,image/jpeg (omit to allow any)")
	return cmd
}

func bucketsRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a bucket entry from config/storage.ts",
		Long: `Remove a bucket entry from config/storage.ts.

This only edits the config file — it does NOT delete the live bucket or its
files. The deploy never auto-deletes a bucket dropped from config (that would
lose data); the live bucket is left as an orphan. To actually delete it, remove
it from the Studio Storage page after pushing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			buckets, err := readConfig()
			if err != nil {
				return err
			}
			if _, ok := buckets[name]; !ok {
				return fmt.Errorf("no bucket %q declared in %s", name, configPath)
			}
			delete(buckets, name)
			if err := writeConfig(buckets); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "✓ removed bucket %q from %s\n", name, configPath)
			fmt.Fprintln(out, "  NOTE: the live bucket and its files are NOT deleted — the deploy leaves")
			fmt.Fprintln(out, "  it as an orphan (never auto-deletes). Delete it from Studio if intended.")
			return nil
		},
	}
	return cmd
}

func bucketsListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the buckets declared in config/storage.ts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			buckets, err := readConfig()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			names := make([]string, 0, len(buckets))
			for n := range buckets {
				names = append(names, n)
			}
			sort.Strings(names)
			if jsonOut {
				defs := []bucketDef{}
				for _, n := range names {
					defs = append(defs, buckets[n])
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(defs)
			}
			if len(buckets) == 0 {
				fmt.Fprintf(out, "no buckets declared in %s\n", configPath)
				fmt.Fprintln(out, "  add one: palbase storage add <name> [--public] [--max-size 5MB] [--mime image/png]")
				return nil
			}
			fmt.Fprintf(out, "buckets declared in %s:\n", configPath)
			for _, n := range names {
				fmt.Fprintf(out, "  %-20s %s\n", n, describe(buckets[n]))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
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
