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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// flagKeyRE bounds a flag key to PalFlags' system-flag key rule (a letter, then
// letters / digits / underscores). Matches the SDK's FLAG_KEY_RE so a key the
// CLI accepts is a key PalFlags accepts.
var flagKeyRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

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
			// THE ENVELOPE, because that is what the stack sends:
			// ListFlags200JSONResponse{Flags: …}. This decoded a BARE ARRAY, so
			// every answer failed to unmarshal and fell through to "print the raw
			// body" below — which meant `flags list` printed `{"flags":[]}` at a
			// person forever, and the "no flags" line under it was unreachable.
			//
			// The raw-body fallback is GONE with it. "The stack's shape is the
			// stack's" reads like humility, but here it turned a decode bug into
			// a silently wrong answer that no test and no user could distinguish
			// from a real one. A shape this CLI cannot read is an error.
			// A POINTER, so "no `flags` key at all" is distinguishable from "the
			// key is there and empty". Decoding into a plain slice made every
			// JSON object without that key — `{"error":"unauthorized"}`, `null`,
			// a shape from some other route — unmarshal CLEANLY into len 0 and
			// print "this stack declares no flags". That is the same silent wrong
			// answer the raw-body fallback used to give, arriving through a
			// different door.
			var answer struct {
				Flags *[]struct {
					Key         string          `json:"key"`
					Type        string          `json:"type"`
					Value       json.RawMessage `json:"value"`
					Description string          `json:"description"`
				} `json:"flags"`
			}
			body := strings.TrimSpace(string(raw))
			if body == "" {
				return fmt.Errorf("this stack answered `flags list` with an empty body")
			}
			if err := json.Unmarshal(raw, &answer); err != nil {
				return fmt.Errorf("this stack answered something `flags list` cannot read: %s", body)
			}
			if answer.Flags == nil {
				return fmt.Errorf("this stack's answer carries no `flags`: %s", body)
			}
			defs := *answer.Flags
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
