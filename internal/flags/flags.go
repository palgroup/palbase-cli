// Package flags provides the `palbase flags` subcommand group: list / add /
// remove (this file) and `user set/unset/list/clear` (user.go). The first three
// are GUIDED authoring for the feature-flag config-as-code surface — they
// read/write config/flags.ts (the typed DSL from @palbase/backend), so an author
// declares flag DEFINITIONS without hand-writing TypeScript. On `git push`, the
// deploy evals config/flags.ts and UPSERTS the flag definitions into PalFlags
// (create-or-update; never auto-deleted).
//
// A flag DEFINITION is a typed project-wide DEFAULT (key + type + default value
// + optional string variants + description). The per-user VALUE of a flag (an
// override / A-B assignment) is runtime state, never in git — set from the SDK,
// from Studio, or from `palbase flags user set` (user.go, the ONLY part of this
// package that touches the network).
//
// The CLI is the SOLE author of config/flags.ts: every write regenerates the
// whole file from the current flag set (deterministic template), and reads parse
// that same generated shape back. This sidesteps having to parse arbitrary
// TypeScript in Go — the file is config-as-code data the CLI fully owns.
//
// No secrets are involved (flags carry no credentials), so no command in this
// package touches the encrypted store.
package flags

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
const configPath = "config/flags.ts"

// flagKeyRE bounds a flag key to PalFlags' system-flag key rule (a letter, then
// letters / digits / underscores). Matches the SDK's FLAG_KEY_RE so a key the
// CLI accepts is a key PalFlags accepts.
var flagKeyRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// flagDef is the CLI's in-memory model of one flag — the same normalized shape
// the SDK's FlagDef serializes. DefaultLiteral is the raw TS literal for the
// default value (`true` / `42` / `"light"`) so a round-trip through the
// generated file is lossless; ParsedDefault is the decoded value used for
// validation + display.
type flagDef struct {
	Key            string   `json:"key"`
	Type           string   `json:"type"`    // "boolean" | "number" | "string"
	DefaultLiteral string   `json:"default"` // raw TS literal, e.g. `false`, `10`, `"light"`
	Variants       []string `json:"variants,omitempty"`
	Description    string   `json:"description,omitempty"`
}

// Cmd returns the `palbase flags` parent command. list / add / remove are pure
// local file I/O and use nothing from Resolvers; the `user` subgroup talks to
// the running Environment over Studio tRPC (see user.go).
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flags",
		Short: "Manage feature-flag definitions (config/flags.ts) and per-user overrides",
		Long: `Author the project's feature-flag DEFINITIONS declaratively in config/flags.ts,
and override a flag for a single end user on the running Environment.

  palbase flags list                       Show the flags declared in config/flags.ts.
  palbase flags add <key> --type ... ...   Add or update a flag definition.
  palbase flags remove <key>               Remove a flag definition entry.
  palbase flags user ...                   Set/inspect/clear ONE user's overrides.

A flag definition is a typed project-wide DEFAULT (its type + default value, and
for string flags an optional list of allowed variants). config/flags.ts is
git-authoritative: commit it and ` + "`git push`" + ` to deploy. The deploy upserts
the flag definitions (create-or-update, idempotent); it never deletes a flag
dropped from the file (removing here leaves the live flag in place — see
` + "`remove`" + `).

A per-user VALUE is the other kind of thing: runtime state for one user in one
Environment, live the moment it is written, never in git. That is
` + "`palbase flags user`" + ` — including for the ` + "`palbase.`" + `-namespaced
keys that cannot be declared in config/flags.ts at all.`,
	}
	cmd.AddCommand(listCmd(), addCmd(), removeCmd(), userCmd(r))
	return cmd
}

func addCmd() *cobra.Command {
	var (
		typeFlag     string
		defaultFlag  string
		variantsFlag string
		descFlag     string
	)
	cmd := &cobra.Command{
		Use:   "add <key>",
		Short: "Add or update a flag definition in config/flags.ts",
		Long: `Add a flag definition to config/flags.ts (or update it if it already exists).

  palbase flags add new_dashboard --type boolean --default false --description "Roll out the new dashboard"
  palbase flags add max_uploads   --type number  --default 10
  palbase flags add theme         --type string  --default light --variants light,dark,system

--variants is only valid for --type string (the allowed values the flag may
take); the --default must be one of them. Idempotent: running it again with the
same key updates the existing entry rather than duplicating it. Commit
config/flags.ts and ` + "`git push`" + ` to apply.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if !flagKeyRE.MatchString(key) {
				return fmt.Errorf("invalid flag key %q — must start with a letter and contain only letters, digits, and underscores", key)
			}
			if !cmd.Flags().Changed("type") {
				return fmt.Errorf("--type is required (one of boolean, number, string)")
			}
			if !cmd.Flags().Changed("default") {
				return fmt.Errorf("--default is required")
			}

			def, err := buildFlagDef(key, typeFlag, defaultFlag, variantsFlag, descFlag)
			if err != nil {
				return err
			}

			declared, err := readConfig()
			if err != nil {
				return err
			}
			_, existed := declared[key]
			declared[key] = def
			if err := writeConfig(declared); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			verb := "added"
			if existed {
				verb = "updated"
			}
			fmt.Fprintf(out, "✓ %s flag %q in %s\n", verb, key, configPath)
			fmt.Fprintf(out, "  %s\n", describe(def))
			fmt.Fprintf(out, "  commit %s and `git push` to deploy\n", configPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&typeFlag, "type", "", "Flag type: boolean, number, or string (required)")
	cmd.Flags().StringVar(&defaultFlag, "default", "", "Default value — true/false, a number, or a string (required)")
	cmd.Flags().StringVar(&variantsFlag, "variants", "", "Comma-separated allowed values (only for --type string), e.g. light,dark,system")
	cmd.Flags().StringVar(&descFlag, "description", "", "Human description of what the flag controls")
	return cmd
}

func removeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <key>",
		Short: "Remove a flag definition entry from config/flags.ts",
		Long: `Remove a flag definition entry from config/flags.ts.

This only edits the config file — it does NOT delete the live flag definition.
The deploy is upsert-only: it never auto-deletes a flag dropped from config (that
could break clients still reading it); the live flag definition is left in place.
To actually delete it, remove it from the Studio Feature Flags page after pushing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			declared, err := readConfig()
			if err != nil {
				return err
			}
			if _, ok := declared[key]; !ok {
				return fmt.Errorf("no flag %q declared in %s", key, configPath)
			}
			delete(declared, key)
			if err := writeConfig(declared); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "✓ removed flag %q from %s\n", key, configPath)
			fmt.Fprintln(out, "  NOTE: the live flag definition is NOT deleted — the deploy is upsert-only")
			fmt.Fprintln(out, "  (never auto-deletes). Delete it from Studio if intended.")
			return nil
		},
	}
	return cmd
}

func listCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the flags declared in config/flags.ts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			declared, err := readConfig()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			keys := make([]string, 0, len(declared))
			for k := range declared {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if jsonOut {
				defs := []flagDef{}
				for _, k := range keys {
					defs = append(defs, declared[k])
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(defs)
			}
			if len(declared) == 0 {
				fmt.Fprintf(out, "no flags declared in %s\n", configPath)
				fmt.Fprintln(out, "  add one: palbase flags add <key> --type boolean --default false")
				return nil
			}
			fmt.Fprintf(out, "flags declared in %s:\n", configPath)
			for _, k := range keys {
				fmt.Fprintf(out, "  %-24s %s\n", k, describe(declared[k]))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// buildFlagDef validates the add-command inputs (mirroring the SDK's flag()
// rules) and produces a normalized flagDef. The --default string is interpreted
// per --type: a boolean parses "true"/"false"; a number parses a numeric
// literal; a string is taken verbatim. variants are only valid for string and
// the default must be one of them.
func buildFlagDef(key, typeFlag, defaultFlag, variantsFlag, descFlag string) (flagDef, error) {
	switch typeFlag {
	case "boolean", "number", "string":
	default:
		return flagDef{}, fmt.Errorf("invalid --type %q — must be boolean, number, or string", typeFlag)
	}

	def := flagDef{Key: key, Type: typeFlag, Description: strings.TrimSpace(descFlag)}

	switch typeFlag {
	case "boolean":
		switch strings.ToLower(strings.TrimSpace(defaultFlag)) {
		case "true":
			def.DefaultLiteral = "true"
		case "false":
			def.DefaultLiteral = "false"
		default:
			return flagDef{}, fmt.Errorf("--default for a boolean flag must be true or false (got %q)", defaultFlag)
		}
	case "number":
		// Validate it's a finite numeric literal; re-emit the canonical form.
		n, err := strconv.ParseFloat(strings.TrimSpace(defaultFlag), 64)
		if err != nil {
			return flagDef{}, fmt.Errorf("--default for a number flag must be a number (got %q)", defaultFlag)
		}
		def.DefaultLiteral = strconv.FormatFloat(n, 'f', -1, 64)
	case "string":
		def.DefaultLiteral = strconv.Quote(defaultFlag)
	}

	// variants are only valid for string flags.
	if variantsFlag != "" {
		if typeFlag != "string" {
			return flagDef{}, fmt.Errorf("--variants is only valid for --type string (got --type %s)", typeFlag)
		}
		variants, err := parseVariants(variantsFlag)
		if err != nil {
			return flagDef{}, err
		}
		def.Variants = variants
		// default (a string) must be one of the variants.
		found := false
		for _, v := range variants {
			if v == defaultFlag {
				found = true
				break
			}
		}
		if !found {
			return flagDef{}, fmt.Errorf("--default %q is not one of --variants [%s]", defaultFlag, strings.Join(variants, ", "))
		}
	}

	return def, nil
}

// parseVariants splits + validates a comma-separated --variants list. Dedupes
// while preserving first-seen order; rejects an empty entry or an empty list.
func parseVariants(raw string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		v := strings.TrimSpace(part)
		if v == "" {
			return nil, fmt.Errorf("--variants must not contain an empty value")
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--variants must list at least one value")
	}
	return out, nil
}

// describe renders a one-line human summary of a flag for list/add output.
func describe(d flagDef) string {
	parts := []string{fmt.Sprintf("%s = %s", d.Type, d.DefaultLiteral)}
	if len(d.Variants) > 0 {
		parts = append(parts, "variants: "+strings.Join(d.Variants, ", "))
	}
	if d.Description != "" {
		parts = append(parts, strconv.Quote(d.Description))
	}
	return strings.Join(parts, ", ")
}
