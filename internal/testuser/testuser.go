// Package testuser wires the `palbase test-user ...` subcommand group.
//
// Transport: the project's OWN management surface, over REST — the same door
// the panel and the deploy use.
//
//	POST   /v1/management/test-users            mint (plain, or from a template)
//	GET    /v1/management/test-users            list
//	GET    /v1/management/test-users/templates  the fixture accounts declared here
//	POST   /v1/management/test-users/clone      copy a user's whole data tree
//	DELETE /v1/management/test-users/{id}       purge one
//
// There used to be a SECOND transport underneath these verbs: a tRPC path to
// the Studio, taken whenever the checkout was not linked. Two protocols answered
// the same question through two gates and returned two shapes — `id` on one wire
// and `user_id` on the other — so the CLI carried two decoders and two sets of
// rules about which flags worked. It is one door now, and which project it opens
// comes from the link OR the selection (see backend.ResolveTarget).
//
// NO secret is generated client-side: the project mints the passwords and tokens
// and this only displays them, once.
package testuser

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// Cmd returns the `test-user` parent command.
func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test-user",
		Short: "Create, list, clone and delete disposable test users",
		Long: `Manage disposable is_test users for the SELECTED environment.

  palbase test-user create                     Mint 1 plain test user.
  palbase test-user create --count 5           Mint 5 plain test users.
  palbase test-user create --template demo     Mint 1 user + seed their data
                                               tree from a declared template.
  palbase test-user templates                  List templates declared in
                                               this stack.
  palbase test-user list                       List this environment's test users.
  palbase test-user clone <id> --email x@y.z --password ...
                                               Copy a test user's data tree onto
                                               a new user.
  palbase test-user delete <id>                Purge a test user.

Templates are declared in config/test-users.ts and carried to the stack by
` + "`palbase push`" + `. There is no way to author one from here — no verb in
this CLI writes a template.

Each environment verifies tokens against its OWN auth, so a minted token is only
valid on the environment that minted it.

The global --project / --environment flags select a CLOUD environment. In a
checkout linked to a project they do not apply and are REFUSED — run
` + "`palbase link <ref>`" + ` to point the checkout at another project.

The minted users are is_test; the server mints their passwords + access tokens.`,
	}
	cmd.AddCommand(createCmd(), listCmd(), templatesCmd(), cloneCmd(), deleteCmd())
	return cmd
}

func createCmd() *cobra.Command {
	var (
		template string
		count    int
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Args:  cobra.NoArgs,
		Short: "Mint test user(s) for the selected environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			target, cred, err := resolveProject(cmd)
			if err != nil {
				return err
			}
			if _, err := backend.PrintTargetFor(cmd); err != nil {
				return err
			}
			if count < 1 {
				return fmt.Errorf("--count must be at least 1")
			}
			// --count APPLIES to a template: several instances of one declared
			// user is an ordinary thing to want, and each gets its own copy of
			// the declared rows.
			return createOnProject(cmd.Context(), target, cred, count, template, jsonOut, out)
		},
	}
	cmd.Flags().StringVar(&template, "template", "", "Seed the minted user's data tree from a declared template")
	cmd.Flags().IntVar(&count, "count", 1, "Number of test users to mint")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit creds+token as JSON (for scripting)")
	return cmd
}

func listCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List the selected environment's test users",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, cred, err := resolveProject(cmd)
			if err != nil {
				return err
			}
			if _, err := backend.PrintTargetFor(cmd); err != nil {
				return err
			}
			return listOnProject(cmd.Context(), target, cred, jsonOut, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the list as JSON")
	return cmd
}

func templatesCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "templates",
		Args:  cobra.NoArgs,
		Short: "List the fixture-account templates on this stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, cred, err := resolveProject(cmd)
			if err != nil {
				return err
			}
			if _, err := backend.PrintTargetFor(cmd); err != nil {
				return err
			}
			return templatesOnProject(cmd.Context(), target, cred, jsonOut, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the list as JSON")
	return cmd
}

func cloneCmd() *cobra.Command {
	var (
		email    string
		password string
		sets     []string
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "clone <user-id>",
		Args:  cobra.ExactArgs(1),
		Short: "Copy a test user's data tree onto a new test user",
		Long: `Copy a test user's whole data tree onto a newly minted test user.

Rows are re-keyed, not duplicated: the copy gets its own primary keys, the new
user owns them, and foreign keys within the copied tree are remapped.

Give --email and --password together for fixed credentials, or neither to have
the project generate them. Use --set to change a column on every copied row of a
table, which is how the clone gets its own name or handle:

  palbase test-user clone usr_123 --set profiles.display_name="Copy"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if (email == "") != (password == "") {
				return fmt.Errorf("--email and --password must be given together, or neither")
			}
			overrides, err := parseSets(sets)
			if err != nil {
				return err
			}
			target, cred, err := resolveProject(cmd)
			if err != nil {
				return err
			}
			if _, err := backend.PrintTargetFor(cmd); err != nil {
				return err
			}
			return cloneOnProject(cmd.Context(), target, cred, args[0], email, password,
				overrides, jsonOut, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "Fixed e-mail for the clone (requires --password)")
	cmd.Flags().StringVar(&password, "password", "", "Fixed password for the clone (requires --email)")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "Change a column on the copy: --set table.column=value (repeatable)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit creds+token as JSON (for scripting)")
	return cmd
}

func deleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <user-id>",
		Args:  cobra.ExactArgs(1),
		Short: "Purge a test user and everything that belongs to them",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, cred, err := resolveProject(cmd)
			if err != nil {
				return err
			}
			if _, err := backend.PrintTargetFor(cmd); err != nil {
				return err
			}
			return deleteOnProject(cmd.Context(), target, cred, args[0], cmd.OutOrStdout())
		},
	}
}

// parseSets turns repeated `--set table.column=value` flags into the
// { <table>: { <column>: value } } shape the clone procedure takes.
//
// Values are parsed leniently, the same way the Studio row editor does: `null`
// becomes null and anything that is valid JSON is decoded, so numbers and
// booleans arrive typed; everything else stays the literal string. Column names
// are validated server-side against the live schema, never here.
func parseSets(sets []string) (map[string]map[string]any, error) {
	if len(sets) == 0 {
		return nil, nil
	}
	out := map[string]map[string]any{}
	for _, raw := range sets {
		key, value, found := strings.Cut(raw, "=")
		if !found {
			return nil, fmt.Errorf("--set %q must be table.column=value", raw)
		}
		table, column, found := strings.Cut(key, ".")
		if !found || table == "" || column == "" {
			return nil, fmt.Errorf("--set %q must name a table and a column: table.column=value", raw)
		}
		var parsed any = value
		if value == "null" {
			parsed = nil
		} else {
			var decoded any
			if err := json.Unmarshal([]byte(value), &decoded); err == nil {
				parsed = decoded
			}
		}
		if out[table] == nil {
			out[table] = map[string]any{}
		}
		out[table][column] = parsed
	}
	return out, nil
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
