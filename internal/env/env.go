// Package env wires `palbase env` — the environments of the linked project.
//
// THIS COMMAND EXISTED BEFORE. On 2026-08-17 it was introduced as "the switch
// and only the switch": what the CLI keeps is the one thing that belongs in a
// checkout, which environment this code acts on. It was retired at the cutover
// on the belief that a project in this cloud IS its environment — true at the
// time, false since the control plane grew `cloud_products` and a second
// environment per product. It comes back because the premise changed.
//
// CREATE AND DELETE COME BACK WITH IT, and that reverses the earlier judgement
// on purpose: the 17.08 commit left them out because they are "control-plane
// acts with a web surface that does them better — confirmations, membership,
// billing consequences". Those consequences did not vanish, so they are carried
// rather than dropped: `create` prints what the new environment will cost
// before it asks, and `delete` makes you type the ref.
package env

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// REST is the control-plane transport subset these commands use.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Resolvers lets the cobra wiring read lazily-built dependencies from main.go.
type Resolvers struct {
	REST func() REST
}

// Cmd returns the `palbase env` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "List, select, create and delete this project's environments",
		Long: `The environments of the project this checkout is linked to.

A project is one thing; the environments under it are where code runs. Which one
a verb acts on is resolved per call:

  --env <name>          this call only
  PALBASE_ENV=<name>    this shell
  palbase env use <n>   remembered for this checkout, on this machine

The remembered choice is NEVER committed. A selection in version control is one
that switches a colleague from staging to production the moment they pull.`,
	}
	cmd.AddCommand(listCmd(r), useCmd(r), createCmd(r), deleteCmd(r))
	return cmd
}

// environment is one row as the control plane reports it.
type environment struct {
	Ref    string `json:"ref"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type project struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Environments []environment `json:"environments"`
}

// linkedProject answers which product this checkout belongs to, refusing with
// the fix rather than with a diagnosis.
func linkedProject(cmd *cobra.Command, r Resolvers) (project, error) {
	target, err := backend.ReadLinkedProject()
	if err != nil {
		return project{}, err
	}
	if strings.TrimSpace(target.Project) == "" {
		return project{}, fmt.Errorf(
			"this checkout is linked to %s, which is one installation with one environment — "+
				"`palbase env` lists a cloud project's environments", target.URL)
	}
	var rows []project
	if err := r.REST().Do(cmd.Context(), http.MethodGet, "/api/v2/projects", nil, &rows); err != nil {
		return project{}, err
	}
	for _, p := range rows {
		if p.ID == target.Project {
			return p, nil
		}
	}
	return project{}, fmt.Errorf("no project of yours has the id %q — run `palbase link` again", target.Project)
}

func listCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List this project's environments and mark the selected one",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := linkedProject(cmd, r)
			if err != nil {
				return err
			}
			selected := ""
			if sel, selErr := backend.ReadSelection("."); selErr == nil && sel.Project == p.ID {
				selected = sel.Ref
			}
			out := cmd.OutOrStdout()
			if len(p.Environments) == 0 {
				fmt.Fprintf(out, "%s has no environments yet — `palbase env create <name>` makes one.\n", p.Name)
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "\tNAME\tREF\tSTATUS")
			for _, e := range p.Environments {
				mark := " "
				if e.Ref == selected {
					// THE MARK IS THE POINT of listing at all: "which one am I
					// on" is the question a person opens this command with.
					mark = "*"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, e.Name, e.Ref, e.Status)
			}
			return tw.Flush()
		},
	}
}

func useCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Args:  cobra.ExactArgs(1),
		Short: "Remember one environment for this checkout (on this machine)",
		Long: `Remember which environment this checkout acts on.

The record lives beside your credentials, never in the repository: a selection
in version control switches a colleague from staging to production the moment
they pull your branch.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := linkedProject(cmd, r)
			if err != nil {
				return err
			}
			named := strings.TrimSpace(args[0])
			for _, e := range p.Environments {
				if !strings.EqualFold(e.Name, named) && e.Ref != named {
					continue
				}
				root, wdErr := os.Getwd()
				if wdErr != nil {
					return wdErr
				}
				if err := backend.WriteSelection(root, backend.Selection{
					Project: p.ID, Env: e.Name, Ref: e.Ref,
				}); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "▸ %s/%s\n", p.Name, e.Name)
				return nil
			}
			return fmt.Errorf("%q is not an environment of %s.\n%s", named, p.Name, listing(p.Environments))
		},
	}
}

func listing(envs []environment) string {
	// Same shape as the resolver's refusal (internal/backend): the refs line up,
	// because this list is a menu somebody is about to type from.
	widest := 0
	for _, e := range envs {
		if n := len([]rune(e.Name)); n > widest {
			widest = n
		}
	}
	rows := make([]string, 0, len(envs))
	for _, e := range envs {
		rows = append(rows, fmt.Sprintf("  %-*s   %s", widest, e.Name, e.Ref))
	}
	return strings.Join(rows, "\n")
}

func createCmd(r Resolvers) *cobra.Command {
	var tier string
	var yes bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Args:  cobra.ExactArgs(1),
		Short: "Add an environment to this project",
		Long: `Add an environment to the project this checkout is linked to.

An environment is a tenant of its own: its own microVM, its own database, its
own keys. It bills for what it uses out of the organisation's pooled quota, so
this command prints that consequence before it asks.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := linkedProject(cmd, r)
			if err != nil {
				return err
			}
			name := strings.TrimSpace(args[0])
			out := cmd.OutOrStdout()

			// THE BILLING CONSEQUENCE IS PRINTED BEFORE THE QUESTION.
			//
			// When this verb was left out of the CLI in August, the reason
			// given was that the web surface does it better — "confirmations,
			// membership, billing consequences". Bringing it back without the
			// consequence would drop exactly the thing that justified leaving
			// it out.
			fmt.Fprintf(out, "This creates a new environment %q under %s.\n", name, p.Name)
			// THE ENVELOPE IS NOT THIS COMMAND'S DECISION when nobody names one.
			// The control plane owns the plan catalogue and picks the smallest
			// envelope the organisation's plan allows; a constant here would be
			// a policy this side does not own, and it would silently detach the
			// day that catalogue's order changes.
			envelope := "your plan's smallest allowed (chosen by the control plane)"
			if tier != "" {
				envelope = tier
			}
			fmt.Fprintf(out, "  compute envelope   %s\n", envelope)
			fmt.Fprintln(out, "  billing            its own microVM and disk; it draws on your")
			fmt.Fprintln(out, "                     organisation's pooled quota from the moment it runs")
			if !yes {
				fmt.Fprint(out, "Type the name to confirm: ")
				var typed string
				if _, scanErr := fmt.Fscanln(cmd.InOrStdin(), &typed); scanErr != nil {
					return fmt.Errorf("aborted")
				}
				if typed != name {
					return fmt.Errorf("aborted — %q does not match %q", typed, name)
				}
			}

			var created struct {
				Ref   string `json:"ref"`
				Name  string `json:"name"`
				Phase string `json:"phase"`
			}
			// THE FIELD IS OMITTED, NOT DEFAULTED: the server's contract reads
			// "tier opsiyonel çünkü ortamın zarfını PLAN seçebilir" — sending a
			// value always would answer a question that was deliberately left
			// to the plan.
			body := map[string]any{"name": name}
			if tier != "" {
				body["tier"] = tier
			}
			if err := r.REST().Do(cmd.Context(), http.MethodPost,
				"/v1/cloud/projects/"+url.PathEscape(p.ID)+"/environments", body, &created); err != nil {
				return err
			}
			fmt.Fprintf(out, "Created %s — %s (%s)\n", created.Name, created.Ref, created.Phase)
			fmt.Fprintf(out, "\n  palbase env use %s\n", created.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&tier, "tier", "",
		"compute envelope for the new environment (default: the smallest your plan allows)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func deleteCmd(r Resolvers) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Args:  cobra.ExactArgs(1),
		Short: "Permanently delete one environment and its data",
		Long: `Delete an environment.

This removes the tenant, its microVM and its disk. There is no undo and no
grace period, which is why it asks for the ref rather than a yes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := linkedProject(cmd, r)
			if err != nil {
				return err
			}
			named := strings.TrimSpace(args[0])
			var found *environment
			for i := range p.Environments {
				if strings.EqualFold(p.Environments[i].Name, named) || p.Environments[i].Ref == named {
					found = &p.Environments[i]
					break
				}
			}
			if found == nil {
				return fmt.Errorf("%q is not an environment of %s.\n%s", named, p.Name, listing(p.Environments))
			}

			out := cmd.OutOrStdout()
			if !yes {
				// A CONFIRMATION NOBODY CAN SATISFY BY ACCIDENT: typing the ref
				// means having read which environment this is.
				fmt.Fprintf(out, "This deletes %s/%s (%s) and its data permanently.\nType the ref to confirm: ",
					p.Name, found.Name, found.Ref)
				var typed string
				if _, scanErr := fmt.Fscanln(cmd.InOrStdin(), &typed); scanErr != nil {
					return fmt.Errorf("aborted")
				}
				if typed != found.Ref {
					return fmt.Errorf("aborted — %q does not match %q", typed, found.Ref)
				}
			}
			if err := r.REST().Do(cmd.Context(), http.MethodDelete,
				"/v1/cloud/projects/"+url.PathEscape(found.Ref), nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(out, "Deleted %s (%s)\n", found.Name, found.Ref)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
