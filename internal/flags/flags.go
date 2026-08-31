// Package flags provides the `palbase flags` subcommand group: list / add /
// remove (this file) and `user set/unset/list/clear` (user.go). All of them
// write FLAGS ON THE STACK through the management API — they are the door, not
// a file authoring aid.
//
// A flag DEFINITION is a typed project-wide DEFAULT (key + type + default value
// + optional string variants + description). The per-user VALUE of a flag (an
// override / A-B assignment) is runtime state — set from the SDK, from Studio,
// or from `palbase flags user set`.
//
// IT USED TO WRITE config/flags.ts AND THAT FILE IS GONE (2026-08-29). Its
// header claimed the deploy "evals config/flags.ts and UPSERTS the definitions
// into PalFlags"; that stopped being true when the declaration applier was
// retired, and nothing said so. What a controller may SPELL is now a generated
// type (`palbase-stack.d.ts`), and what EXISTS is what these commands wrote.
//
// No secrets are involved (flags carry no credentials), so no command in this
// package touches the encrypted store.
package flags

import (
	"bytes"
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

// flagKeyRE bounds a flag key to PalFlags' system-flag key rule (a letter, then
// letters / digits / underscores). Matches the SDK's FLAG_KEY_RE so a key the
// CLI accepts is a key PalFlags accepts.
var flagKeyRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// flagDef is the CLI's in-memory model of one flag — the same normalized shape
// the SDK's FlagDef serializes. DefaultLiteral is the raw TS literal for the
// default value (`true` / `42` / `"light"`) so a round-trip through the
// generated file is lossless; ParsedDefault is the decoded value used for
// validation + display.
// REST reaches the linked stack's management surface.
type REST interface {
	Do(ctx context.Context, method, path string, body []byte) (int, []byte, error)
}

const flagsPath = "/v1/management/flags"

// call performs one verb against the flag store and reports the stack's own
// refusal rather than a generic one.
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

type flagDef struct {
	Key            string   `json:"key"`
	Type           string   `json:"type"`    // "boolean" | "number" | "string" | "json"
	DefaultLiteral string   `json:"default"` // raw TS literal, e.g. `false`, `10`, `"light"`, `{"daily":10}`
	Variants       []string `json:"variants,omitempty"`
	Description    string   `json:"description,omitempty"`
}

// Cmd returns the `palbase flags` parent command.
//
// list / add / remove act on the STACK. They used to edit config/flags.ts, which
// meant the panel could not change a flag without the next deploy putting it
// back — two writers, one of them silent. The `user` subgroup is a different
// thing entirely and always was: one person's override, live, never in git.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flags",
		Short: "Manage feature-flag definitions and per-user overrides",
		Long: `The feature-flag DEFINITIONS this project ships, and per-user overrides on
the running Environment.

  palbase flags list                       Show the flags this stack holds.
  palbase flags add <key> --type ... ...   Add or update a flag definition.
  palbase flags remove <key>               Remove a flag definition.
  palbase flags user ...                   Set/inspect/clear ONE user's overrides.

A flag definition is a typed project-wide DEFAULT (its type + default value, and
for string flags an optional list of allowed variants). THEY LIVE ON THE STACK and
take effect on the next deploy. They used to be declared in config/flags.ts and
upserted by every deploy, which never deleted one dropped from the file — so
` + "`remove`" + ` edited the file and left the live flag serving, and "removed" and
"still there" were both true. There is no file left to fall back to, so a removal
here is the whole operation.

A per-user VALUE is the other kind of thing: runtime state for one user in one
Environment, live the moment it is written, never in git. That is
` + "`palbase flags user`" + ` — including for the ` + "`palbase.`" + `-namespaced
keys a flag definition cannot carry at all.`,
	}
	cmd.AddCommand(listCmd(r), addCmd(r), removeCmd(r), userCmd(r))
	return cmd
}

func addCmd(r Resolvers) *cobra.Command {
	var (
		typeFlag     string
		defaultFlag  string
		variantsFlag string
		descFlag     string
	)
	cmd := &cobra.Command{
		Use:   "add <key>",
		Short: "Declare or update a flag on this stack",
		Long: `Declare a flag: its type, its project-wide default, and for string flags the
variants it may take.

  palbase flags add new_dashboard --type boolean --default false --description "Roll out the new dashboard"
  palbase flags add theme --type string --default '"system"' --variants light,dark,system
  palbase flags add limits --type json --default '{"daily":10}'

It lands on the stack immediately. There is no file to commit and no deploy to
wait for — a deploy carries code and schema, not settings.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if !flagKeyRE.MatchString(key) {
				return fmt.Errorf("invalid flag key %q: a letter, then letters, digits or underscores", key)
			}
			if typeFlag == "" {
				return fmt.Errorf("--type is required (boolean, number, string or json)")
			}
			switch typeFlag {
			case "boolean", "number", "string", "json":
			default:
				return fmt.Errorf("--type %q: must be boolean, number, string or json", typeFlag)
			}
			if strings.TrimSpace(defaultFlag) == "" {
				return fmt.Errorf("--default is required: a flag with no default has no answer for a caller who has never been given one")
			}
			if !json.Valid([]byte(defaultFlag)) {
				return fmt.Errorf("--default %q is not valid JSON (a string default is quoted: --default '\"system\"')", defaultFlag)
			}
			def := map[string]any{
				"type":  typeFlag,
				"value": json.RawMessage(defaultFlag),
			}
			if descFlag != "" {
				def["description"] = descFlag
			}
			if variantsFlag != "" {
				def["variants"] = strings.Split(variantsFlag, ",")
			}
			body, err := json.Marshal(def)
			if err != nil {
				return err
			}
			if _, err := call(r, cmd, http.MethodPut, flagsPath+"/"+url.PathEscape(key), body); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ %s declared (%s = %s)\n", key, typeFlag, defaultFlag)
			return nil
		},
	}
	cmd.Flags().StringVar(&typeFlag, "type", "", "boolean | number | string | json")
	cmd.Flags().StringVar(&defaultFlag, "default", "", "the project-wide default, as JSON")
	cmd.Flags().StringVar(&variantsFlag, "variants", "", "for string flags: the allowed values, comma-separated")
	cmd.Flags().StringVar(&descFlag, "description", "", "what this flag is for")
	return cmd
}

func removeCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <key>",
		Short: "Remove a flag from this stack",
		Long: `Remove a flag definition from the stack.

It is gone: there is no file left to fall back to, so this is the whole
operation rather than half of one. It used to edit config/flags.ts and leave the
live flag in place, which meant "removed" and "still there" were both true.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := call(r, cmd, http.MethodDelete, flagsPath+"/"+url.PathEscape(args[0]), nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ removed flag %q\n", args[0])
			return nil
		},
	}
}

func listCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the flags this stack declares",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := call(r, cmd, http.MethodGet, flagsPath, nil)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				fmt.Fprintln(out, strings.TrimSpace(string(raw)))
				return nil
			}
			var defs []struct {
				Key         string          `json:"key"`
				Type        string          `json:"type"`
				Value       json.RawMessage `json:"value"`
				Description string          `json:"description"`
			}
			if err := json.Unmarshal(raw, &defs); err != nil {
				// The stack's shape is the stack's; printing it beats guessing.
				fmt.Fprintln(out, strings.TrimSpace(string(raw)))
				return nil
			}
			if len(defs) == 0 {
				fmt.Fprintln(out, "this stack declares no flags")
				fmt.Fprintln(out, "  add one: palbase flags add <key> --type boolean --default false")
				return nil
			}
			sort.Slice(defs, func(a, b int) bool { return defs[a].Key < defs[b].Key })
			fmt.Fprintln(out, "flags on this stack:")
			for _, d := range defs {
				line := fmt.Sprintf("  %-24s %s = %s", d.Key, d.Type, strings.TrimSpace(string(d.Value)))
				if d.Description != "" {
					line += "   " + d.Description
				}
				fmt.Fprintln(out, line)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the stack's answer as JSON")
	return cmd
}

// buildFlagDef validates the add-command inputs (mirroring the SDK's flag()
// rules) and produces a normalized flagDef. The --default string is interpreted
// per --type: a boolean parses "true"/"false"; a number parses a numeric
// literal; a string is taken verbatim; json parses a JSON object. variants are
// only valid for string and the default must be one of them.
func buildFlagDef(key, typeFlag, defaultFlag, variantsFlag, descFlag string) (flagDef, error) {
	switch typeFlag {
	case "boolean", "number", "string", "json":
	default:
		return flagDef{}, fmt.Errorf("invalid --type %q — must be boolean, number, string, or json", typeFlag)
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
	case "json":
		// A JSON OBJECT, not any JSON: PalFlags' `object` value type decodes to a
		// map and rejects an array or a scalar, so catch it here rather than at
		// deploy time. Compacting normalises away whatever whitespace the shell
		// carried in and re-emits a canonical one-line object literal (valid TS).
		var obj map[string]any
		if err := json.Unmarshal([]byte(defaultFlag), &obj); err != nil {
			return flagDef{}, fmt.Errorf("--default for a json flag must be a JSON object like '{\"daily\":10}' (got %q): %w", defaultFlag, err)
		}
		if obj == nil {
			return flagDef{}, fmt.Errorf("--default for a json flag must be a JSON object, not null (got %q)", defaultFlag)
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(defaultFlag)); err != nil {
			return flagDef{}, fmt.Errorf("--default for a json flag must be valid JSON (got %q): %w", defaultFlag, err)
		}
		def.DefaultLiteral = buf.String()
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
