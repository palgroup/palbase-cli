// This file is the LIVE half of `palbase flags` — the per-user OVERRIDE
// commands (`palbase flags user ...`).
//
// The rest of the package authors config/flags.ts, the git-authoritative
// DEFINITION of a flag (type + Environment-wide default) that a deploy upserts.
// An override is the opposite kind of thing: runtime state for ONE end user in
// ONE Environment, never in git, effective the moment it is written. Support
// turning `palbase.debug_console` on for the single user they are helping is
// the reason this exists.
//
// Transport: REST, straight at the project, like every other verb in this CLI.
//
// It used to be Studio tRPC, and the note here explained why: those routes are
// gated by RequireServiceRole and "the Management API has NO user-flags route,
// so tRPC is the only reachable client". The premise stopped being true — a
// linked project resolves its OWN service-role key (the control plane brokers it
// for a cloud project, the state directory answers for a local stack), which is
// exactly the credential those routes want. tRPC was the only reachable client
// only while nothing could open the door.
//
// Routes hit (v2/internal/modules/user-flags/internal/server/router.go):
//
//	PUT    /v1/user-flags/users/{userId}/{key}   {value}   -> {key,value,source}
//	DELETE /v1/user-flags/users/{userId}/{key}             -> {key,value,source}
//	userFlags.users.deleteAll mutation {ref,userId}           -> {deleted}
//	userFlags.users.get       query    {ref,userId}           -> {values,fetched_at}
//	userFlags.system.list     query    {ref}                  -> [{key,value,...}]
//
// DOTTED KEYS ARE FIRST-CLASS HERE. `flags add` rejects them because a
// `palbase.`-namespaced key cannot be declared in config/flags.ts; that
// rejection is about the FILE, not about the flag, and must never leak into
// these commands.
package flags

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// userFlagsPath is one user's override collection; overridePath is one key of
// it. The user id travels in the PATH and the environment does not travel at
// all — the project answers for itself, so there is no `ref` to get wrong.
func userFlagsPath(userID string) string {
	return "/v1/user-flags/users/" + url.PathEscape(userID)
}

func overridePath(userID, key string) string {
	return userFlagsPath(userID) + "/" + url.PathEscape(key)
}

const systemFlagsPath = "/v1/user-flags/system"

// overrideCall performs one override verb and reads the {key,value,source} the
// server answers with — the shape PUT and DELETE share.
func overrideCall(r Resolvers, cmd *cobra.Command, method, path string, body []byte) (overrideResult, error) {
	raw, err := call(r, cmd, method, path, body)
	if err != nil {
		return overrideResult{}, err
	}
	var res overrideResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return overrideResult{}, fmt.Errorf("read the answer: %w", err)
	}
	return res, nil
}

// Resolvers carries the lazily-built clients, populated by PersistentPreRunE
// before any subcommand fires.
//
// ONE SEAM NOW. The definition half (list/add/remove) and the override half
// (`flags user …`) both act on the linked project over REST; they were two
// transports only because the override routes had no reachable client. Selection
// remains for --project / --environment.
type Resolvers struct {
	REST      func(*cobra.Command) (REST, error)
	Selection func() *selection.Resolver
}

// overrideKeyRE mirrors palflags' own key rule
// (modules/user-flags/internal/validate/validate.go:43): dot-separated
// segments, each starting with a letter. It is DELIBERATELY looser than this
// package's flagKeyRE — the dotted `palbase.` namespace is the whole point of
// the override commands.
//
// (modules/user-flags/internal/handler/error.go:40 still describes the
// single-segment `^[a-zA-Z][a-zA-Z0-9_]*$` in its 400 text; the regex the
// server actually compiles is the dotted one. The error string is stale, not
// the rule.)
var overrideKeyRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)*$`)

// maxOverrideKeyLength mirrors validate.MaxKeyLength.
const maxOverrideKeyLength = 128

// overrideResult is the wire shape of userFlags.users.put / .delete
// (handler/user_flags.go setOverrideResponse + deleteOverrideResponse).
type overrideResult struct {
	Key    string          `json:"key"`
	Value  json.RawMessage `json:"value"`
	Source string          `json:"source"`
}

// mergedSnapshot is the wire shape of userFlags.users.get — the EFFECTIVE
// per-user view (Environment defaults with the user's overrides applied). It
// carries no per-key source; see listUserCmd for what that costs.
type mergedSnapshot struct {
	Values    map[string]json.RawMessage `json:"values"`
	FetchedAt string                     `json:"fetched_at"`
}

// systemFlag is the subset of userFlags.system.list this file reads.
type systemFlag struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// deleteAllResult is the wire shape of userFlags.users.deleteAll.
type deleteAllResult struct {
	Deleted int `json:"deleted"`
}

func userCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Set, inspect and clear per-user flag overrides on the running Environment",
		Long: `Override a flag for ONE end user, live on the selected Environment.

  palbase flags user set <user-id> <key> --type <t> --value <v>
  palbase flags user unset <user-id> <key>
  palbase flags user list <user-id>
  palbase flags user clear <user-id>

These commands do NOT touch config/flags.ts. config/flags.ts declares a flag's
type and its Environment-wide DEFAULT and is applied by a deploy; an override is
runtime state for one user, effective immediately, and travels with nothing.

Dotted ` + "`palbase.`" + `-namespaced keys work here — overriding
` + "`palbase.debug_console`" + ` for the one user you are helping, while
production stays off, is what this is for. (` + "`palbase flags add`" + ` rejects
dotted keys only because they cannot be declared in config/flags.ts.)

The flag must already exist on the Environment: an override of an undeclared key
is a 404 (` + "`key_not_found`" + `). Target another Environment with --environment.`,
	}
	cmd.AddCommand(setUserCmd(r), unsetUserCmd(r), listUserCmd(r), clearUserCmd(r))
	return cmd
}

func setUserCmd(r Resolvers) *cobra.Command {
	var typeFlag, valueFlag string
	cmd := &cobra.Command{
		Use:   "set <user-id> <key>",
		Short: "Set a flag override for one user",
		Long: `Set a flag override for one user on the selected Environment.

  palbase flags user set usr_42 palbase.debug_console --type boolean --value true
  palbase flags user set usr_42 max_uploads           --type number  --value 50
  palbase flags user set usr_42 theme                 --type string  --value dark
  palbase flags user set usr_42 limits                --type json    --value '{"daily":10}'

--type says how to SERIALISE what you typed; it is not sent to the server. The
wire body is ` + "`{\"value\": <raw JSON>}`" + ` with no type field at all — the
flag's declared type lives on the Environment's flag definition, and the server
rejects a value that does not match it (400 ` + "`type_mismatch`" + `).

  boolean  exactly true or false      -> JSON true / false
  number   a numeric literal          -> JSON number
  string   taken verbatim             -> JSON string ("dark")
  json     any JSON literal, parsed   -> that literal (this is the server's
                                         "object" value type; objects nest at
                                         most 3 deep)

--type is REQUIRED and never inferred. Guessing would turn a string flag whose
value is literally "true" into a JSON boolean, and the write would silently
succeed with the wrong type.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			userID, key := args[0], args[1]
			if err := checkOverrideKey(key); err != nil {
				return err
			}
			if !cmd.Flags().Changed("type") {
				return fmt.Errorf("--type is required (one of boolean, number, string, json)")
			}
			if !cmd.Flags().Changed("value") {
				return fmt.Errorf("--value is required")
			}
			raw, err := encodeOverrideValue(typeFlag, valueFlag)
			if err != nil {
				return err
			}
			ref, err := envRef(cmd, r)
			if err != nil {
				return err
			}

			body, err := json.Marshal(map[string]any{"value": raw})
			if err != nil {
				return err
			}
			res, err := overrideCall(r, cmd, http.MethodPut, overridePath(userID, key), body)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "✓ %s = %s for user %s (%s)\n", res.Key, res.Value, userID, ref)
			fmt.Fprintln(out, "  live now — this is runtime state, not config/flags.ts")
			return nil
		},
	}
	cmd.Flags().StringVar(&typeFlag, "type", "", "How to serialise --value: boolean, number, string, or json (required)")
	cmd.Flags().StringVar(&valueFlag, "value", "", "The override value, read per --type (required)")
	return cmd
}

func unsetUserCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unset <user-id> <key>",
		Short: "Remove one flag override from a user",
		Long: `Remove one flag override, so the user falls back to the Environment value.

  palbase flags user unset usr_42 palbase.debug_console

The flag itself is untouched — only this user's override row goes away. The
Environment value that now applies is printed back.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			userID, key := args[0], args[1]
			if err := checkOverrideKey(key); err != nil {
				return err
			}
			ref, err := envRef(cmd, r)
			if err != nil {
				return err
			}

			res, err := overrideCall(r, cmd, http.MethodDelete, overridePath(userID, key), nil)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "✓ removed override %s for user %s (%s)\n", key, userID, ref)
			fmt.Fprintf(out, "  user now sees the environment value: %s\n", res.Value)
			return nil
		},
	}
	return cmd
}

func clearUserCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear <user-id>",
		Short: "Remove ALL flag overrides from a user",
		Long: `Remove every flag override a user has, in one call.

  palbase flags user clear usr_42

The user falls back to the Environment values for all of them. Flag definitions
are untouched. Overrides are cheap to re-set, so there is no confirmation
prompt — but check the user id, because it is not undone for you.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			userID := args[0]
			ref, err := envRef(cmd, r)
			if err != nil {
				return err
			}

			raw, err := call(r, cmd, http.MethodDelete, userFlagsPath(userID), nil)
			if err != nil {
				return err
			}
			var res deleteAllResult
			if err := json.Unmarshal(raw, &res); err != nil {
				return fmt.Errorf("read the answer: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "✓ removed %d override(s) for user %s (%s)\n", res.Deleted, userID, ref)
			return nil
		},
	}
	return cmd
}

// userFlagRow is one line of `flags user list` (and one --json element).
type userFlagRow struct {
	Key string `json:"key"`
	// Value is what the user EFFECTIVELY sees.
	Value json.RawMessage `json:"value"`
	// EnvironmentValue is the flag's Environment-wide value.
	EnvironmentValue json.RawMessage `json:"environment_value"`
	// Overridden is INFERRED — see listUserCmd's note.
	Overridden bool `json:"overridden"`
}

func listUserCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list <user-id>",
		Short: "Show a user's effective flag values and which are overridden",
		Long: `Show what one user actually sees, next to the Environment value.

  palbase flags user list usr_42

WHY TWO COLUMNS: the server's per-user read returns the MERGED view (values
only) — there is no endpoint that reports a per-key source for another user. So
this reads the merged view AND the Environment's flag list, and calls a key
overridden when the two differ. An override deliberately set to the SAME value
as the Environment is therefore indistinguishable from no override, and reads as
"environment". That is the honest limit of the API, not a display choice.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			userID := args[0]
			ref, err := envRef(cmd, r)
			if err != nil {
				return err
			}

			rawMerged, err := call(r, cmd, http.MethodGet, userFlagsPath(userID), nil)
			if err != nil {
				return err
			}
			var merged mergedSnapshot
			if err := json.Unmarshal(rawMerged, &merged); err != nil {
				return fmt.Errorf("read the user's flags: %w", err)
			}
			rawSystem, err := call(r, cmd, http.MethodGet, systemFlagsPath, nil)
			if err != nil {
				return err
			}
			var system []systemFlag
			if err := json.Unmarshal(rawSystem, &system); err != nil {
				return fmt.Errorf("read the environment's flags: %w", err)
			}

			envValues := make(map[string]json.RawMessage, len(system))
			for _, f := range system {
				envValues[f.Key] = f.Value
			}

			keys := make([]string, 0, len(merged.Values))
			for k := range merged.Values {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			rows := make([]userFlagRow, 0, len(keys))
			for _, k := range keys {
				effective := merged.Values[k]
				env := envValues[k]
				rows = append(rows, userFlagRow{
					Key:              k,
					Value:            effective,
					EnvironmentValue: env,
					Overridden:       len(env) > 0 && !sameJSON(effective, env),
				})
			}

			out := cmd.OutOrStdout()
			if jsonOut {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintf(out, "no flags on environment %s\n", ref)
				return nil
			}
			fmt.Fprintf(out, "flag values for user %s on %s:\n\n", userID, ref)
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "KEY\tVALUE\tSOURCE\tENVIRONMENT")
			for _, row := range rows {
				source := "environment"
				if row.Overridden {
					source = "override"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", row.Key, row.Value, source, blankIfEmpty(row.EnvironmentValue))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(out, "\n  source is inferred by comparing the two values — an override set to the")
			fmt.Fprintln(out, "  environment value reads as \"environment\" (the API exposes no per-user source)")
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// encodeOverrideValue turns (--type, --value) into the raw JSON the override
// wire carries. The type is a CLI-side SERIALISATION instruction only: palflags'
// PUT body is `{"value": <raw JSON>}` with no type field, and it validates the
// JSON against the flag's own declared value_type. Every branch either produces
// the exact JSON kind named or fails — nothing is coerced.
func encodeOverrideValue(typeFlag, value string) (json.RawMessage, error) {
	switch typeFlag {
	case "boolean":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true":
			return json.RawMessage("true"), nil
		case "false":
			return json.RawMessage("false"), nil
		default:
			return nil, fmt.Errorf("--value for --type boolean must be true or false (got %q)", value)
		}
	case "number":
		n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return nil, fmt.Errorf("--value for --type number must be a number (got %q)", value)
		}
		return json.RawMessage(strconv.FormatFloat(n, 'f', -1, 64)), nil
	case "string":
		// json.Marshal, not strconv.Quote: Quote emits Go escapes (\x00) that
		// are not valid JSON, so a control character would produce a body the
		// server cannot parse.
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("--value is not encodable as a JSON string: %w", err)
		}
		return raw, nil
	case "json":
		// Compact both validates and normalises, so the body is what was meant
		// rather than whatever whitespace the shell carried in.
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(value)); err != nil {
			return nil, fmt.Errorf("--value for --type json must be valid JSON (got %q): %w", value, err)
		}
		return json.RawMessage(buf.Bytes()), nil
	default:
		return nil, fmt.Errorf("invalid --type %q — must be boolean, number, string, or json", typeFlag)
	}
}

// checkOverrideKey mirrors palflags' key rule locally so the two opaque
// positional arguments cannot be swapped silently: a user id (`usr_42`,
// `550e8400-e29b-...`) is not a legal flag key, so the swap is caught here with
// the argument order spelled out rather than as a bare 400 from the server.
func checkOverrideKey(key string) error {
	if len(key) > maxOverrideKeyLength {
		return fmt.Errorf("flag key %q is longer than %d characters", key, maxOverrideKeyLength)
	}
	if !overrideKeyRE.MatchString(key) {
		return fmt.Errorf(
			"invalid flag key %q — dot-separated segments of letters, digits and underscores (e.g. palbase.debug_console); the argument order is <user-id> <key>",
			key)
	}
	return nil
}

// envRef resolves the selected (or --environment / --project overridden)
// Environment to its wire ref.
func envRef(cmd *cobra.Command, r Resolvers) (string, error) {
	sel, err := r.Selection().Resolve(cmd.Context())
	if err != nil {
		return "", err
	}
	return sel.EnvironmentRef(), nil
}

// sameJSON compares two raw JSON values by their compacted bytes. Both sides
// come out of the same JSONB column type, so representation is consistent.
func sameJSON(a, b json.RawMessage) bool {
	return bytes.Equal(compactJSON(a), compactJSON(b))
}

func compactJSON(raw json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}

func blankIfEmpty(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "-"
	}
	return string(raw)
}
