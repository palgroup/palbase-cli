package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

func SourcePath(digest string) string {
	return "/v1/management/deployments/" + url.PathEscape(digest) + "/source"
}

// fetchDeployedSource downloads and extracts the source the target serves.
func fetchDeployedSource(ctx context.Context, target Target, cred Credentials, name, dst string, w io.Writer) error {
	status, body, err := managementCall(ctx, target, cred, http.MethodGet, SourcePath("latest"), nil, "")
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		// The project answered and has nothing — either it has never deployed, or
		// its live version predates source retention. Both are states, and saying
		// which is the difference between waiting and acting.
		return fmt.Errorf("%s has no source to pull: %s", name, describeError(body))
	default:
		return fmt.Errorf("%s answered %d: %s", name, status, trimBody(body))
	}
	if len(body) == 0 {
		// An empty archive extracts into an empty directory and reports success —
		// a pull that silently replaced a project with nothing.
		return fmt.Errorf("%s returned an empty archive", name)
	}
	if err := extractSourceTree(dst, bytes.NewReader(body)); err != nil {
		return fmt.Errorf("extract bundle: %w", err)
	}
	fmt.Fprintf(w, "✓ pulled environment %s (%d bytes)\n", name, len(body))
	return nil
}

// stackPush is the seam the flag→consent binding is asserted through.
//
// Reading the flags in a test and calling `pushURL` proves the URL builder and
// nothing else: a fresh verifier bound `breaking` to `GetBool("approve")` and no
// test noticed, which would have meant `--approve` silently opening the
// compatibility gate. The binding itself has to be observable, so the call goes
// through a variable a test can stand in front of.
var stackPush = runStackPush

func newPushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push",
		Args:  cobra.NoArgs,
		Short: "Deploy the current backend to the project this checkout is linked to",
		Long: `Deploy the backend in the current directory.

The project is built with its installed SDK, packaged and uploaded to the
linked stack's management API. There is no repository-driven deployment: ` + "`git push`" + ` deploys nothing.

This acts on the project this checkout is bound to. There is one addressing
mechanism — ` + "`palbase link`" + ` — and no flags that select a different one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := ReadTarget()
			if err != nil {
				return err
			}
			cred, _, err := Credential(target.URL)
			if err != nil {
				return err
			}
			approve, _ := cmd.Flags().GetBool("approve")
			breaking, _ := cmd.Flags().GetBool("accept-breaking")
			return stackPush(cmd.Context(), target, cred, approve, breaking, cmd.OutOrStdout())
		},
	}

	cmd.Flags().Bool("approve", false,
		"apply data-removing schema changes marked by the plan")
	cmd.Flags().Bool("accept-breaking", false,
		"break-glass: apply a schema the running release still declares it needs.\n"+
			"The ordinary path is two deploys — mark the column .ignored(), ship, then\n"+
			"drop it — and the refusal names it. This exists for the case that dance\n"+
			"cannot serve: the running release is already broken and the fix is the\n"+
			"very change being refused. Every use is logged on the stack, with the\n"+
			"serving digest and the objects it overrode.")
	return cmd
}

// newPullCmd wires `palbase pull`.
func newPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Args:  cobra.NoArgs,
		Short: "Update the local backend to the linked project's deployed version",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := ReadTarget()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := refuseDirtyTree(cwd); err != nil {
				return err
			}
			cred, _, err := Credential(target.URL)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", target.URL)
			return fetchDeployedSource(cmd.Context(), target, cred, target.URL, cwd, cmd.OutOrStdout())
		},
	}
}

// newCloneCmd downloads a cloud project and links the new checkout to it.
func newCloneCmd(r Resolvers) *cobra.Command {
	var dirFlag string
	cmd := &cobra.Command{
		Use:   "clone <project>",
		Args:  cobra.ExactArgs(1),
		Short: "Download a project locally and link it",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			given := strings.TrimSpace(args[0])

			// A PROJECT IS ITS REF, and the argument is what `palbase project
			// list` prints: the NAME in the first column or the REF in the
			// second. This used to take a management project id and nothing
			// else — a value this CLI shows on no surface at all, not even in
			// `project list --json` — so the documented argument could not be
			// obtained. Ölçüldü 25.08.2026: `project status 1jhp7jbrm` çalışırken
			// `clone 1jhp7jbrm` "böyle bir proje yok" diyordu.
			//
			// The download and the binding are the same two things `link` and
			// `pull` already do, by address, with no control plane in the path.
			if !strings.HasPrefix(given, managementProjectIDPrefix) {
				ref := given
				if named := refByProjectName(ctx, r, given); named != "" {
					ref = named
				}
				if !isCanonicalProjectRef(ref) {
					return fmt.Errorf(
						"%q is neither a project name nor a ref — `palbase project list` prints both", given)
				}
				// A name that merely LOOKS like a ref would otherwise be built
				// into an address nothing serves, and the failure would arrive as
				// a sentence about credentials. Refused here, while the listing
				// is in hand — and only when the listing actually answered.
				if refs, asked := knownRefs(ctx, r); asked && !slices.Contains(refs, ref) {
					return fmt.Errorf(
						"no project of yours is called %q — `palbase project list` prints the names and refs", given)
				}
				host := r.Endpoints().PublicHost
				if host == "" {
					return errors.New("this CLI has no tenant host configured, so a project cannot be reached by ref")
				}
				dir := dirFlag
				if dir == "" {
					dir = ref
				}
				target := Target{URL: "https://" + ref + "." + host}
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
				cred, _, credErr := Credential(target.URL)
				if credErr != nil {
					return credErr
				}
				if err := fetchDeployedSource(
					ctx, target, cred, target.URL, dir, cmd.OutOrStdout()); err != nil {
					return err
				}
				// Bound the way `link` binds, so push/pull/spec in the new
				// directory reach the project it came from.
				return inDir(dir, func() error { return WriteTarget(target) })
			}

			// Clone accepts a name or ref, as printed by project list.
			return fmt.Errorf(
				"%q is a management project id, and nothing in this CLI prints one.\n"+
					"  `palbase project list` prints the NAME and the REF — clone takes either",
				given)
		},
	}
	cmd.Flags().StringVar(&dirFlag, "dir", "", "Directory to clone into (default: the repo or environment name)")
	return cmd
}

// managementProjectIDPrefix is what a management project id starts with. Only a
// value shaped like one takes the management path; everything else is a name or
// a ref, which is what a person actually has.
const managementProjectIDPrefix = "proj_"

// inDir runs fn with dir as the working directory and restores the old one.
// WriteTarget writes beside the CURRENT directory by design — every other verb
// reads it that way — so a clone binds by stepping into what it just created.
func inDir(dir string, fn func() error) error {
	prev, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	defer func() { _ = os.Chdir(prev) }()
	return fn()
}
