// Package testuser wires the `palbase test-user ...` subcommand group.
//
// Transport: Studio tRPC (`testData.*`), reached via the same user-JWT
// `internal/studio` client the `apps`/`secret`/`db` commands use. We talk to
// tRPC directly because the test-data feature is exposed ONLY as a tRPC
// router — there are no `/api/v1/...` REST routes for it, so the Management-API
// REST transport (used by `apikey`/`project`) cannot reach it.
//
// The procedures hit:
//
//	testData.testUserCreate      mutation {ref,count,withTokens} -> {users:[...]}
//	testData.createFromTemplate  mutation {ref,name}             -> {user,inserted,existing}
//	testData.listTemplates       query    {ref}                  -> [{name,email,tables}]
//	testData.testUsers           query    {ref}                  -> {users:[{id,email}]}
//	testData.cloneTestUser       mutation {ref,sourceUserId,...}  -> {user,inserted}
//	testData.deleteTestUser      mutation {ref,userId}           -> {ok}
//
// Studio runs the developer env-role authorization inside each tRPC procedure;
// a below-role / non-member caller surfaces as a FORBIDDEN error here. NO
// secret is generated client-side — palauth mints the passwords + tokens
// server-side; the CLI only displays them.
package testuser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// Studio is the tRPC transport subset the test-user commands need.
// *studio.Client satisfies it; tests substitute a server-backed client.
type Studio interface {
	Query(ctx context.Context, path string, input any, out any) error
	Mutation(ctx context.Context, path string, input any, out any) error
}

// Resolvers carries the lazily-built Studio client + the shared selection
// resolver, populated by PersistentPreRunE before any subcommand fires.
type Resolvers struct {
	Studio    func() Studio
	Selection func() *selection.Resolver
}

// Cmd returns the `test-user` parent command.
func Cmd(r Resolvers) *cobra.Command {
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

Templates live on the stack and take effect on
deploy — there is no way to author one from here, because git is the source of
truth for them.

Each environment verifies tokens against its OWN auth, so a minted token is only
valid on the environment that minted it. Override the target with --environment.

The minted users are is_test; the server mints their passwords + access tokens.`,
	}
	cmd.AddCommand(createCmd(r), listCmd(r), templatesCmd(r), cloneCmd(r), deleteCmd(r))
	return cmd
}

// mintedUser mirrors one minted user across every procedure that returns one.
type mintedUser struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	AccessToken string `json:"accessToken"`
}

type plainResult struct {
	Users []mintedUser `json:"users"`
}

// materializeResult mirrors createFromTemplate / cloneTestUser: the minted
// user's creds + a per-table count of inserted rows.
type materializeResult struct {
	User     mintedUser     `json:"user"`
	Inserted map[string]int `json:"inserted"`
	Existing bool           `json:"existing"`
}

type templateEntry struct {
	Name   string   `json:"name"`
	Email  *string  `json:"email"`
	Tables []string `json:"tables"`
}

type testUserEntry struct {
	ID    string  `json:"id"`
	Email *string `json:"email"`
}

type testUserList struct {
	Users []testUserEntry `json:"users"`
}

// envRef resolves the selected (or --environment overridden) environment.
func envRef(cmd *cobra.Command, r Resolvers) (string, error) {
	sel, err := r.Selection().Resolve(cmd.Context())
	if err != nil {
		return "", err
	}
	return sel.EnvironmentRef(), nil
}

func createCmd(r Resolvers) *cobra.Command {
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
			// TARGET-RELATIVE FIRST, like every other verb: a checkout linked to a
			// stack mints against THAT stack. Nobody should have to remember which
			// kind of project they are standing in.
			if target, cred, ok := linkedProject(); ok {
				if _, err := backend.PrintTargetFor(cmd); err != nil {
					return err
				}
				if count < 1 {
					return fmt.Errorf("--count must be at least 1")
				}
				// --count DOES apply to a template here: several instances of
				// one declared user is an ordinary thing to want, and each gets
				// its own copy of the declared rows.
				return createOnProject(cmd.Context(), target, cred, count, template, jsonOut, out)
			}

			ref, err := envRef(cmd, r)
			if err != nil {
				return err
			}

			// --template: mint ONE user + seed their data tree from the declared
			// template. --count is meaningless here (a template mints exactly one
			// user), so reject the combination rather than silently ignore it.
			if template != "" {
				if cmd.Flags().Changed("count") {
					return fmt.Errorf("--count cannot be combined with --template (a template mints exactly one user)")
				}
				var res materializeResult
				payload := map[string]any{"ref": ref, "name": template}
				if err := r.Studio().Mutation(cmd.Context(), "testData.createFromTemplate", payload, &res); err != nil {
					return err
				}
				if jsonOut {
					return encodeJSON(out, res)
				}
				fmt.Fprintf(out, "✓ created 1 test user from template %q\n", template)
				printUser(out, res.User)
				printInserted(out, res.Inserted)
				fmt.Fprintln(out, "  (creds shown once — store them now)")
				return nil
			}

			if count < 1 {
				return fmt.Errorf("--count must be at least 1")
			}
			var res plainResult
			payload := map[string]any{"ref": ref, "count": count, "withTokens": true}
			if err := r.Studio().Mutation(cmd.Context(), "testData.testUserCreate", payload, &res); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(out, res)
			}
			fmt.Fprintf(out, "✓ minted %d test user(s)\n", len(res.Users))
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tEMAIL\tPASSWORD\tTOKEN")
			for _, u := range res.Users {
				token := u.AccessToken
				if token == "" {
					token = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", u.ID, u.Email, u.Password, token)
			}
			_ = tw.Flush()
			fmt.Fprintln(out, "(creds shown once — store them now)")
			return nil
		},
	}
	cmd.Flags().StringVar(&template, "template", "", "Seed the minted user's data tree from a declared template")
	cmd.Flags().IntVar(&count, "count", 1, "Number of plain test users to mint (ignored with --template)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit creds+token as JSON (for scripting)")
	return cmd
}

func listCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List the selected environment's test users",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if target, cred, ok := linkedProject(); ok {
				if _, err := backend.PrintTargetFor(cmd); err != nil {
					return err
				}
				return listOnProject(cmd.Context(), target, cred, jsonOut, out)
			}

			ref, err := envRef(cmd, r)
			if err != nil {
				return err
			}

			var res testUserList
			if err := r.Studio().Query(cmd.Context(), "testData.testUsers", map[string]any{"ref": ref}, &res); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(out, res)
			}
			if len(res.Users) == 0 {
				fmt.Fprintln(out, "No test users in this environment.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tEMAIL")
			for _, u := range res.Users {
				fmt.Fprintf(tw, "%s\t%s\n", u.ID, derefOr(u.Email, "-"))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the list as JSON")
	return cmd
}

func templatesCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "templates",
		Args:  cobra.NoArgs,
		Short: "List the fixture-account templates on this stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if target, cred, ok := linkedProject(); ok {
				if _, err := backend.PrintTargetFor(cmd); err != nil {
					return err
				}
				return templatesOnProject(cmd.Context(), target, cred, jsonOut, out)
			}

			ref, err := envRef(cmd, r)
			if err != nil {
				return err
			}

			var res []templateEntry
			if err := r.Studio().Query(cmd.Context(), "testData.listTemplates", map[string]any{"ref": ref}, &res); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(out, res)
			}
			if len(res) == 0 {
				fmt.Fprintln(out, "No templates on this stack.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tFIXTURE EMAIL\tSEEDS")
			for _, t := range res {
				seeds := "-"
				if len(t.Tables) > 0 {
					seeds = strings.Join(t.Tables, ", ")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", t.Name, derefOr(t.Email, "-"), seeds)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the list as JSON")
	return cmd
}

func cloneCmd(r Resolvers) *cobra.Command {
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
the server generate them. Use --set to change a column on every copied row of a
table, which is how the clone gets its own name or handle:

  palbase test-user clone usr_123 --set profiles.display_name="Copy"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if (email == "") != (password == "") {
				return fmt.Errorf("--email and --password must be given together, or neither")
			}
			overrides, err := parseSets(sets)
			if err != nil {
				return err
			}

			if target, cred, ok := linkedProject(); ok {
				if _, err := backend.PrintTargetFor(cmd); err != nil {
					return err
				}
				if email != "" {
					// A stack mints a clone's credentials itself. Naming them
					// would be a promise this rail cannot keep, and silently
					// ignoring the flags would be worse.
					return fmt.Errorf("--email/--password are not available against a local stack: a clone is minted with generated credentials, which the command prints once")
				}
				return cloneOnProject(cmd.Context(), target, cred, args[0], overrides, jsonOut, out)
			}

			ref, err := envRef(cmd, r)
			if err != nil {
				return err
			}

			payload := map[string]any{"ref": ref, "sourceUserId": args[0]}
			if email != "" {
				payload["email"] = email
				payload["password"] = password
			}
			if len(overrides) > 0 {
				payload["overrides"] = overrides
			}

			var res materializeResult
			if err := r.Studio().Mutation(cmd.Context(), "testData.cloneTestUser", payload, &res); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(out, res)
			}
			fmt.Fprintf(out, "✓ cloned %s\n", args[0])
			printUser(out, res.User)
			printInserted(out, res.Inserted)
			fmt.Fprintln(out, "  (creds shown once — store them now)")
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "Fixed e-mail for the clone (requires --password)")
	cmd.Flags().StringVar(&password, "password", "", "Fixed password for the clone (requires --email)")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "Change a column on the copy: --set table.column=value (repeatable)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit creds+token as JSON (for scripting)")
	return cmd
}

func deleteCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <user-id>",
		Args:  cobra.ExactArgs(1),
		Short: "Purge a test user and everything that belongs to them",
		RunE: func(cmd *cobra.Command, args []string) error {
			if target, cred, ok := linkedProject(); ok {
				if _, err := backend.PrintTargetFor(cmd); err != nil {
					return err
				}
				return deleteOnProject(cmd.Context(), target, cred, args[0], cmd.OutOrStdout())
			}

			ref, err := envRef(cmd, r)
			if err != nil {
				return err
			}
			var res struct {
				OK bool `json:"ok"`
			}
			payload := map[string]any{"ref": ref, "userId": args[0]}
			if err := r.Studio().Mutation(cmd.Context(), "testData.deleteTestUser", payload, &res); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ deleted %s\n", args[0])
			return nil
		},
	}
	return cmd
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

func printUser(w io.Writer, u mintedUser) {
	fmt.Fprintf(w, "  id:       %s\n", u.ID)
	fmt.Fprintf(w, "  email:    %s\n", u.Email)
	fmt.Fprintf(w, "  password: %s\n", u.Password)
	if u.AccessToken != "" {
		fmt.Fprintf(w, "  token:    %s\n", u.AccessToken)
	}
}

func printInserted(w io.Writer, inserted map[string]int) {
	if len(inserted) == 0 {
		return
	}
	fmt.Fprintln(w, "  inserted:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for table, n := range inserted {
		fmt.Fprintf(tw, "    %s\t%d\n", table, n)
	}
	_ = tw.Flush()
}

func derefOr(s *string, fallback string) string {
	if s == nil || *s == "" {
		return fallback
	}
	return *s
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
