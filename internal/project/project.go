// Package project wires `palbase project` over the cloud's control plane.
//
// A PROJECT IS A GROUP OF ENVIRONMENTS, and this sentence replaces the opposite
// one. The doc here used to claim the opposite — that a project in this cloud
// simply WAS a tenant (one microVM, one ref, one address), with no organisation
// above it and no environment set below. True when written, false since. It is
// paraphrased rather than quoted on purpose: the gate below refuses the literal
// words, and quoting them would keep the claim alive in the very file that
// retired it. The
// control plane grew `cloud_products` above the tenant row and a verb to add a
// second environment under it; the CLI surface says so in its own words
// (`cli.controller.ts`: "CLI'ın \"project\"i v2'nin ÜRÜNÜ, \"environment\"ı
// v2'nin PROJESİ"). Measured live 2026-09-11.
//
// The consequence is not cosmetic. While this doc was believed, `list` printed
// one row per ENVIRONMENT carrying the PRODUCT's name — so a project with two
// environments printed the same name twice, and `palbase link <name>` could not
// resolve it.
//
// So `list` prints PROJECTS with their environments nested, in ONE call. There
// is still no `use`: which environment a directory acts on is `palbase env
// use`, and which PROJECT it belongs to is `palbase link`. Two mechanisms for
// one question is how a person ends up pushing to the wrong place.
package project

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// REST is the control-plane transport subset these commands use.
// *transport.Client satisfies it; tests substitute a stub.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Bootstrapper reports the cloud's own facts — used to build a tenant address
// out of a ref without the CLI hard-coding a domain per environment.
type Bootstrapper interface {
	TenantDomain(ctx context.Context) (string, error)
}

// Resolvers lets the cobra wiring read lazily-built dependencies from main.go's
// PersistentPreRunE.
type Resolvers struct {
	REST  func() REST
	Cloud func() Bootstrapper
}

// Project is one tenant as the control plane reports it.
// Project, bulut düzleminin MÜŞTERİ yüzeyindeki proje şekli.
//
// HÜCRE VE YUVA BURADA YOK. İkisi de yerleşimin iç detayı: bir projenin hangi
// hücrede, hangi yuvada durduğunu bilmek kimseye bir şey yaptırmıyor, ama
// topolojimizi anlatıyor. Sunucu da artık göndermiyor.
//
// AD İNSANIN VERDİĞİ. `ref` kimliktir ve değişmez; ad değişir. Liste yalnız
// ref basarken sekiz projesi olan biri sekiz opak dizeye bakıyordu.
type Project struct {
	// ID is the PRODUCT's identity — what `palbase link` writes down. It does
	// not change; the name does.
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Environments []Environment `json:"environments"`
}

// Environment is one tenant under a project: its own microVM, its own database,
// its own keys.
type Environment struct {
	Ref    string `json:"ref"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Tenant is what `create`, `status` and `delete` act on — ONE environment,
// named by its ref. They are not confused about the model: minting a project
// mints its first environment, and deleting one deletes that tenant's data.
//
// EXPORTED because it IS the `/v1/cloud/projects` contract, and the e2e suite
// measures that contract against the live control plane. A second copy of the
// shape declared in the test would drift from this one silently.
type Tenant struct {
	Ref   string  `json:"ref"`
	Name  *string `json:"name"`
	Phase string  `json:"phase"`
}

func (t Tenant) displayName() string {
	if t.Name == nil || *t.Name == "" {
		return "(unnamed)"
	}
	return *t.Name
}

// displayName, adı olmayan bir projeyi kimliğiyle gösterir.
//
// Boş bırakmak, tabloda adsız bir sütun boşluğu bırakırdı ve "adı yok" ile
// "sunucu adı unuttu" aynı görünürdü.
func (p Project) displayName() string {
	if p.Name == "" {
		return p.ID
	}
	return p.Name
}

// Cmd returns the `palbase project` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Create, list, inspect, and delete Palbase cloud projects",
	}
	cmd.AddCommand(
		createCmd(r),
		listCmd(r),
		statusCmd(r),
		deleteCmd(r),
	)
	return cmd
}

func createCmd(r Resolvers) *cobra.Command {
	var tier string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Args:  cobra.ExactArgs(1),
		Short: "Create a project — one tenant, one address",
		Long: `Create a project on the Palbase cloud.

Provisioning is synchronous: the command returns once the tenant is running, so
the address it prints is one you can link immediately.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var p Tenant
			body := map[string]any{"name": args[0], "tier": tier}
			if err := r.REST().Do(cmd.Context(), http.MethodPost, "/v1/cloud/projects", body, &p); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), p)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Created %s — %s (%s)\n", p.displayName(), p.Ref, p.Phase)

			// The address is the point of the whole command, so it is built
			// here rather than left for the person to assemble. The domain
			// comes from the cloud itself: hard-coding one would make this
			// binary wrong on every deployment but the one it was built for.
			domain, err := r.Cloud().TenantDomain(cmd.Context())
			if err != nil || domain == "" {
				fmt.Fprintf(out, "\nLink it with: palbase link https://%s.<your-cloud-domain>\n", p.Ref)
				return nil
			}
			fmt.Fprintf(out, "\nLink it with:\n  palbase link https://%s.%s\n", p.Ref, domain)
			return nil
		},
	}
	cmd.Flags().StringVar(&tier, "tier", "free", "capacity tier for the new project")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func listCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List the projects you own",
		RunE: func(cmd *cobra.Command, args []string) error {
			// ONE CALL. The environments come back inside their project, so a
			// listing of M projects costs one request rather than M+1.
			var rows []Project
			if err := r.REST().Do(cmd.Context(), http.MethodGet, "/api/v2/projects", nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No projects yet — create one with `palbase project create <name>`.")
				return nil
			}
			// THE ENVIRONMENTS ARE NESTED, not flattened into sibling rows.
			//
			// Flattened is what the old listing did — one row per environment,
			// each carrying the PROJECT's name — so two environments printed
			// the same name twice and a reader could not tell a second
			// environment from a second project.
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "PROJECT\tENVIRONMENT\tREF\tSTATUS")
			for _, p := range rows {
				if len(p.Environments) == 0 {
					fmt.Fprintf(tw, "%s\t(none)\t\t\n", p.displayName())
					continue
				}
				for i, e := range p.Environments {
					name := p.displayName()
					if i > 0 {
						// The project is printed once; the rows under it belong
						// to it. Repeating the name would read as two projects.
						name = ""
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, e.Name, e.Ref, e.Status)
				}
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func statusCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status <ref>",
		Args:  cobra.ExactArgs(1),
		Short: "Show one project's name and phase",
		RunE: func(cmd *cobra.Command, args []string) error {
			var p Tenant
			path := "/v1/cloud/projects/" + url.PathEscape(args[0])
			if err := r.REST().Do(cmd.Context(), http.MethodGet, path, nil, &p); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), p)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "Name\t%s\n", p.displayName())
			fmt.Fprintf(tw, "Ref\t%s\n", p.Ref)
			fmt.Fprintf(tw, "Phase\t%s\n", p.Phase)
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func deleteCmd(r Resolvers) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <ref>",
		Args:  cobra.ExactArgs(1),
		Short: "Permanently delete a project and its data",
		Long: `Delete a project.

This removes the tenant, its microVM and its disk. There is no undo and no
grace period, which is why it asks first.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			if !yes {
				// A confirmation nobody can accidentally satisfy: typing the
				// ref means having read which project this is.
				fmt.Fprintf(cmd.OutOrStdout(), "This deletes %s and its data permanently.\nType the ref to confirm: ", ref)
				var typed string
				if _, err := fmt.Fscanln(cmd.InOrStdin(), &typed); err != nil {
					return fmt.Errorf("aborted")
				}
				if typed != ref {
					return fmt.Errorf("aborted — %q does not match %q", typed, ref)
				}
			}
			path := "/v1/cloud/projects/" + url.PathEscape(ref)
			if err := r.REST().Do(cmd.Context(), http.MethodDelete, path, nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %s\n", ref)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
