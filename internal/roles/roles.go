// Package roles is `palbase roles` — the surface a developer defines their
// application's roles and permissions on.
//
// There is no config file. Role definitions live in the environment and the
// TYPES come back to the project through `palbase spec`, so a developer writes
// no code to get a role and still cannot misspell a permission: the generated
// enum refuses to compile. That is the whole shape of this feature.
//
// Every verb here talks to the project's OWN door (`<stack>/admin/roles`),
// which is the door `palbase spec` already uses to download definitions. Two
// verbs reaching one endpoint by two different routes is how one of them
// eventually stops working while the other keeps passing.
package roles

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
)

type roleView struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	IsDefault   bool     `json:"isDefault"`
	Permissions []string `json:"permissions"`
	UserCount   int64    `json:"userCount"`
}

type rolesBody struct {
	Roles []roleView `json:"roles"`
}

// resolveProject finds the environment these verbs act on, and refuses clearly
// when the checkout names no address — "not linked" is a sentence a person can
// act on; a connection error to an empty URL is not.
func resolveProject(cmd *cobra.Command) (backend.Target, backend.Credentials, error) {
	target, err := backend.ResolveTargetFor(cmd)
	if err != nil {
		return backend.Target{}, backend.Credentials{}, err
	}
	if target.URL == "" {
		return backend.Target{}, backend.Credentials{}, fmt.Errorf(
			"%s names no address — run `palbase link <project>` first", target.Describe())
	}
	cred, _, err := backend.Credential(target.URL)
	if err != nil {
		return backend.Target{}, backend.Credentials{}, err
	}
	return target, cred, nil
}

// call sends one request to the project's door and SURFACES the server's own
// refusal. The stack already explains itself — which permission was malformed,
// which role is already the default, how many users a delete would revoke — and
// swallowing that body would leave the caller with a status code and no idea.
// rolePath, bir rol adını YOLA çevirir — birleştirerek değil, KAÇIRARAK.
//
// `"/admin/roles/" + name` bir URL kurmuyordu, bir dizgi kuruyordu; ve
// `http.NewRequest` o dizgiyi URL olarak ayrıştırdığı için addaki bir `?`
// sorgu dizesini başlatıyordu. Ölçüldü: `palbase roles delete 'x?confirm=true'`
// uca `DELETE /admin/roles/x?confirm=true` olarak iniyor, yani FR-003'ün
// veri-kaybı onayı hiç sorulmadan geçiliyor ve rolü taşıyan herkesin yetkisi
// siliniyordu. Aynı koşudaki TS istemcisi (`clients/auth.ts`) bu dersi
// `encodeURIComponent` ile zaten almıştı; Go tarafı almamıştı.
func rolePath(name string) string { return "/admin/roles/" + url.PathEscape(name) }

func call(cmd *cobra.Command, method, path string, body any) (*rolesBody, error) {
	target, cred, err := resolveProject(cmd)
	if err != nil {
		return nil, err
	}
	var payload []byte
	contentType := ""
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
		contentType = "application/json"
	}
	status, raw, err := backend.CallProject(cmd.Context(), target, cred, method, path, payload, contentType)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s answered %d: %s", target.Describe(), status, describe(raw))
	}
	var out rolesBody
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("could not read the answer from %s: %w", target.Describe(), err)
		}
	}
	return &out, nil
}

// describe pulls the human sentence out of the stack's error envelope, falling
// back to the raw body when it is shaped differently.
func describe(raw []byte) string {
	var env struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && env.Description != "" {
		return env.Description
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roles",
		Short: "Define the application roles and permissions of the selected environment",
		Long: "Define this environment's application roles and the permissions they carry.\n\n" +
			"  palbase roles create <name> [--default] [--permissions a.b,c.d]\n" +
			"  palbase roles list [--json]\n" +
			"  palbase roles delete <name> [--yes]\n\n" +
			"Roles live in the environment, not in a config file. Run `palbase spec` after\n" +
			"changing them and the generated client gets typed constants, so a misspelled\n" +
			"permission stops compiling instead of silently answering 403.\n\n" +
			"Permissions are `resource.action` (for example `posts.delete_any`). There are no\n" +
			"wildcards: a role has to name what it may do.",
	}
	cmd.AddCommand(createCmd(), listCmd(), deleteCmd(), assignCmd(), revokeCmd())
	return cmd
}

func createCmd() *cobra.Command {
	var isDefault bool
	var description string
	var permissions []string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create or update a role definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			body := map[string]any{
				"isDefault":   isDefault,
				"description": description,
				// Never nil: the stack reads this as the role's COMPLETE permission
				// set, and `null` there would read as "no permissions" on one side
				// and "unchanged" on the other.
				"permissions": append([]string{}, permissions...),
			}
			out, err := call(cmd, "PUT", rolePath(name), body)
			if err != nil {
				return err
			}
			// SAYIM YANITTAN GELİR, VE YANIT ROLÜ TAŞIMIYORSA SUSAR.
			//
			// `n := 0` başlangıcı, rolü yanıtta bulamayan bir turu başarılı bir
			// "0 permission(s)" gibi yazdırıyordu — kullanıcı az önce üç izin
			// verdiği rol için sıfır görüyordu ve doğru olan hangisiydi
			// bilemiyordu. Bir sayı basmak, o sayıyı BİLMEYİ gerektirir.
			n := -1
			for _, r := range out.Roles {
				if r.Name == name {
					n = len(r.Permissions)
				}
			}
			counted := fmt.Sprintf("%d permission(s)", n)
			if n < 0 {
				counted = "written"
			}
			suffix := ""
			if isDefault {
				suffix = " (default — every new sign-up gets it)"
			}
			cmd.Printf("✓ %s (%s)%s\n", name, counted, suffix)
			return nil
		},
	}
	cmd.Flags().BoolVar(&isDefault, "default", false, "grant this role to every user at sign-up")
	cmd.Flags().StringVar(&description, "description", "", "what this role is for")
	cmd.Flags().StringSliceVar(&permissions, "permissions", nil,
		"permissions this role carries, as resource.action (comma separated)")
	return cmd
}

func listCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list [userId]",
		Short: "List role definitions, or the roles one user holds",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Bir kullanıcı adlandırıldıysa soru DEĞİŞİR: ortamın tanımları değil,
			// o kişinin taşıdıkları. İki ayrı fiil yerine tek fiil, çünkü soran
			// kişi ikisini de "roller" diye arıyor.
			if len(args) == 1 {
				return printUserRoles(cmd, args[0], asJSON)
			}
			out, err := call(cmd, "GET", "/admin/roles", nil)
			if err != nil {
				return err
			}
			if asJSON {
				// NOTHING but JSON: the consumer here is a script, and one stray
				// heading turns a pipe into a parse error.
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			if len(out.Roles) == 0 {
				cmd.Println("No roles defined. Create one with `palbase roles create <name>`.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ROLE\tDEFAULT\tUSERS\tPERMISSIONS")
			for _, r := range out.Roles {
				def := ""
				if r.IsDefault {
					def = "yes"
				}
				perms := append([]string{}, r.Permissions...)
				sort.Strings(perms)
				shown := strings.Join(perms, ", ")
				if shown == "" {
					shown = "—"
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", r.Name, def, r.UserCount, shown)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the answer as JSON and nothing else")
	return cmd
}

func deleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a role definition",
		Long: "Delete a role definition.\n\n" +
			"If any user still holds the role the environment refuses and says how many —\n" +
			"repeat with --yes to revoke them along with it. A role nobody holds is deleted\n" +
			"without asking: the friction belongs on real loss, not on every delete.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			// Onay bir SORGU parametresi, ve ad ne olursa olsun öyle kalır:
			// yol kaçırılarak, onay ayrıca kurularak birleşiyor.
			path := rolePath(name)
			if yes {
				path += "?" + url.Values{"confirm": {"true"}}.Encode()
			}
			if _, err := call(cmd, "DELETE", path, nil); err != nil {
				return err
			}
			cmd.Printf("✓ %s deleted\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "revoke the role from everyone who holds it")
	return cmd
}
