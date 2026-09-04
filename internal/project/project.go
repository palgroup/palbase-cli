// Package project wires `palbase project` over the v2 cloud's control plane
// (`/v1/cloud/projects*`, Authorization: Bearer <session token>).
//
// A project in the v2 cloud IS a tenant: one microVM, one ref, one address.
// There is no Organization above it and no Environment set below it — the v1
// shape carried both, and the CLI's own rules record why that was misleading:
// the real entity was always the project, and "environment" was a presentation
// label. A branch that needs its own database is its own project.
//
// So the verbs are flat. `create` mints a tenant and hands back the address
// `palbase link` wants; `list`, `status` and `delete` name it by ref. There is
// no `use`: the linked target is what a directory acts on, and `palbase link`
// already writes it. Two mechanisms for "which project is this directory" is
// how a person ends up pushing to the wrong one.
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
	Ref   string  `json:"ref"`
	Name  *string `json:"name"`
	Phase string  `json:"phase"`
}

// displayName, adı olmayan bir projeyi ref'iyle gösterir.
//
// Boş bırakmak, tabloda adsız bir sütun boşluğu bırakırdı ve "adı yok" ile
// "sunucu adı unuttu" aynı görünürdü.
func (p Project) displayName() string {
	if p.Name == nil || *p.Name == "" {
		return "(unnamed)"
	}
	return *p.Name
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
			var p Project
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
			var rows []Project
			if err := r.REST().Do(cmd.Context(), http.MethodGet, "/v1/cloud/projects", nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No projects yet — create one with `palbase project create <name>`.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tREF\tPHASE")
			for _, p := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", p.displayName(), p.Ref, p.Phase)
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
			var p Project
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
