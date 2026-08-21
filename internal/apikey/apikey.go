// Package apikey wires `palbase apikey` over the v2 cloud.
//
// A v2 project has exactly TWO keys and they are minted with it: a publishable
// key clients ship (`pb_<ref>_c…`) and a service-role key that opens the
// project's own management surface (`pb_<ref>_s…`). There is no arbitrary set
// of named keys to create and revoke, so `create` and `revoke` are gone — verbs
// for a shape that does not exist here.
//
// WHERE EACH VERB LIVES, and why the split is not arbitrary:
//
//   - `list` reads the project's OWN management surface, target-relative like
//     every other verb that touches one project (`/v1/management/keys`).
//   - `reveal` and `rotate` go to the CLOUD, because the service-role key is a
//     secret the project will not hand out about itself and minting is the
//     control plane's job (it is the party that can tell the cell about the new
//     keys). The recorded rule is the same one: a command that touches ONE
//     project is target-relative; one that needs the plane above it is not.
package apikey

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
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Target names the project this directory acts on and reads its management
// surface. Implemented by the backend package, injected to keep this package
// off it.
type Target interface {
	// Ref is the project's ref, and false when this directory is linked to
	// something that is not a project on this cloud (a stack on this machine).
	Ref() (string, bool)
	// Describe is the address, for the banner.
	Describe() string
	// GetJSON reads a path on the linked project's own management surface.
	GetJSON(ctx context.Context, path string, out any) error
}

// Resolvers carries the lazily-built dependencies.
type Resolvers struct {
	REST   func() REST
	Target func() (Target, error)
}

// Keys is what the cloud reports for a project.
type Keys struct {
	AnonKey        string `json:"anonKey"`
	ServiceRoleKey string `json:"serviceRoleKey"`
}

// Cmd returns the `palbase apikey` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apikey",
		Short: "Show and rotate this project's API keys",
	}
	cmd.AddCommand(listCmd(r), revealCmd(r), rotateCmd(r))
	return cmd
}

// listCmd reads the project's own surface: the publishable key is not a secret
// and the project will say it about itself.
func listCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List this project's keys (the secret one is masked)",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := r.Target()
			if err != nil {
				return err
			}
			var got struct {
				Publishable string `json:"publishable"`
			}
			if err := target.GetJSON(cmd.Context(), "/v1/management/keys", &got); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), map[string]string{"publishable": got.Publishable})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "▸ %s\n", target.Describe())
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "publishable\t%s\n", got.Publishable)
			// The secret is NOT printed here. A verb people run to see what
			// exists should not spray a secret into a terminal, a screen share,
			// or a scrollback buffer. `reveal` asks for it on purpose.
			fmt.Fprintf(tw, "service-role\t(hidden — `palbase apikey reveal` prints it)\n")
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func revealCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "reveal",
		Args:  cobra.NoArgs,
		Short: "Print the service-role key — the one that opens the management surface",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := cloudRef(r)
			if err != nil {
				return err
			}
			var keys Keys
			if err := r.REST().Do(cmd.Context(), http.MethodGet,
				"/v1/cloud/projects/"+url.PathEscape(ref)+"/keys", nil, &keys); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), keys)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "publishable\t%s\n", keys.AnonKey)
			fmt.Fprintf(tw, "service-role\t%s\n", keys.ServiceRoleKey)
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func rotateCmd(r Resolvers) *cobra.Command {
	var yes bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rotate",
		Args:  cobra.NoArgs,
		Short: "Replace this project's keys — atomic, no grace period",
		Long: `Replace this project's keys.

The old keys stop working the moment this returns; there is no window where both
are valid. The project restarts to pick the new ones up, so it is briefly
unavailable — and anything holding the old key (a deployed app, a CI job) needs
the new one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := cloudRef(r)
			if err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(cmd.OutOrStdout(),
					"This replaces %s's keys. Anything holding the old key stops working, and the project restarts.\nType the ref to confirm: ", ref)
				var typed string
				if _, err := fmt.Fscanln(cmd.InOrStdin(), &typed); err != nil {
					return fmt.Errorf("aborted")
				}
				if typed != ref {
					return fmt.Errorf("aborted — %q does not match %q", typed, ref)
				}
			}
			var keys Keys
			if err := r.REST().Do(cmd.Context(), http.MethodPost,
				"/v1/cloud/projects/"+url.PathEscape(ref)+"/keys/rotate", nil, &keys); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), keys)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Rotated %s\n", ref)
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "publishable\t%s\n", keys.AnonKey)
			fmt.Fprintf(tw, "service-role\t%s\n", keys.ServiceRoleKey)
			_ = tw.Flush()
			// Nothing to write locally: this CLI asks the cloud for a project's
			// key every time rather than keeping a copy, precisely so a rotation
			// cannot leave a stale one behind.
			fmt.Fprintln(out, "\nThe project is restarting; give it a moment before the new key answers.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// cloudRef names the cloud project this directory acts on, or says why it
// cannot — a stack on this machine has no cloud ref, and asking the control
// plane about it would be asking the wrong authority.
func cloudRef(r Resolvers) (string, error) {
	target, err := r.Target()
	if err != nil {
		return "", err
	}
	ref, ok := target.Ref()
	if !ok {
		return "", fmt.Errorf("%s is not a project on this cloud — its keys are its own", target.Describe())
	}
	return ref, nil
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

var _ = context.Background
