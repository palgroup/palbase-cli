package backend

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func SourcePath(digest string) string {
	return "/v1/management/deployments/" + url.PathEscape(digest) + "/source"
}

// fetchDeployedSource downloads and extracts the source the target serves.
//
// STREAMED, not buffered. A deployed source tree is megabytes — centauri's is
// 3.4 MB over 225 entries — and the buffering door caps what it reads, so this
// verb used to unpack the first megabyte and fail on the seam.
func fetchDeployedSource(ctx context.Context, target Target, cred Credentials, name, dst string, w io.Writer) error {
	res, err := managementGet(ctx, target, cred, SourcePath("latest"))
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// The project answered and has nothing — either it has never deployed, or
		// its live version predates source retention. Both are states, and saying
		// which is the difference between waiting and acting.
		return fmt.Errorf("%s has no source to pull: %s", name, describeError(errorBody(res)))
	default:
		return fmt.Errorf("%s answered %d: %s", name, res.StatusCode, trimBody(errorBody(res)))
	}

	counted := &countingReader{r: res.Body}
	// Peek before extracting: an empty archive extracts into an empty directory
	// and reports success — a pull that silently replaced a project with
	// nothing — and gzip's own error for it ("EOF") names none of that.
	buffered := bufio.NewReader(counted)
	if _, err := buffered.Peek(1); err != nil {
		return fmt.Errorf("%s returned an empty archive", name)
	}

	if err := extractSourceTree(dst, buffered); err != nil {
		// A STREAM CUT SHORT IS NOT A BROKEN ARCHIVE. Tar reports both as
		// "unexpected EOF", and telling them apart is the difference between
		// retrying and filing a bug, so compare what arrived against what the
		// project said it was sending.
		if res.ContentLength >= 0 && counted.n < res.ContentLength {
			return fmt.Errorf("extract bundle: the connection delivered %d of %d bytes: %w", counted.n, res.ContentLength, err)
		}
		return fmt.Errorf("extract bundle: %w", err)
	}
	fmt.Fprintf(w, "✓ pulled environment %s (%d bytes)\n", name, counted.n)
	return nil
}

// errorBody reads a non-2xx body, which is a diagnostic and therefore small.
//
// NOT readCapped, deliberately: this is the LAST thing shown before a command
// fails, and refusing to print a diagnosis because it ran long would replace a
// partial explanation with none at all. A truncated error message is still an
// error message; a truncated payload is a lie. That is the whole line between
// this and every other capped read in the package.
func errorBody(res *http.Response) []byte {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, managementBodyLimit))
	return raw
}

// countingReader counts what actually came through, so the success line reports
// the bundle's real size and a short read can be named as one.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
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

This acts on the project this checkout is bound to, and on the environment
resolved for this call — ` + "`--env`" + ` names one, ` + "`palbase env use`" + ` remembers one, and
with more than one and neither given the push REFUSES rather than guessing.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := PrintResolvedFor(cmd)
			if err != nil {
				return err
			}
			target := resolved.Acting()
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
		Short: "Replace the backend in this directory with one environment's deployed version",
		Long: `Replace the backend in this directory with what an environment is serving.

THIS OVERWRITES YOUR SOURCE, so it says which environment it is taking it from
before it does — and refuses if the tree is dirty. The environment is resolved
for this call: ` + "`--env`" + ` names one, ` + "`palbase env use`" + ` remembers one, and with more
than one and neither given it refuses rather than guessing. Pulling production
over a branch that tracks staging is exactly the accident that costs a day.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// THE BANNER COMES FIRST, BEFORE THE DIRTY-TREE CHECK.
			//
			// This verb replaces the source in front of somebody, so which
			// environment it is reading from is the one thing they must see —
			// and if the tree is dirty the refusal then arrives with the target
			// already named, rather than as a bare complaint about git.
			resolved, err := PrintResolvedFor(cmd)
			if err != nil {
				return err
			}
			target := resolved.Acting()
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
			return fetchDeployedSource(cmd.Context(), target, cred, target.URL, cwd, cmd.OutOrStdout())
		},
	}
}

// newCloneCmd downloads a cloud project and links the new checkout to it.
func newCloneCmd(r Resolvers) *cobra.Command {
	var dirFlag string
	var envFlag string
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
				// THE ARGUMENT IS A PROJECT, and a project is resolved to its
				// PRODUCT. The old path resolved a name to one ENVIRONMENT's ref
				// through `/v1/cloud/projects` — which returns a row per
				// environment carrying the product's name, so two environments
				// under one project looked like a collision and the name went
				// unresolved. `palbase clone todoapp` then built
				// `https://todoapp.<host>` out of anything ref-shaped.
				product, envs, err := productByName(ctx, r, given)
				if err != nil {
					return err
				}
				ref, refErr := linkEnvironmentRef(product, envs, envFlag)
				if refErr != nil {
					return refErr
				}
				envName := envFlag
				for _, e := range envs {
					if e.Ref == ref {
						envName = e.Name
					}
				}
				host := r.Endpoints().PublicHost
				if host == "" {
					return errors.New("this CLI has no tenant host configured, so a project cannot be reached by ref")
				}
				// THE DIRECTORY IS NAMED AFTER THE PROJECT, not after a ref.
				// `1jhp7jbrm/` is a directory nobody recognises a week later.
				dir := dirFlag
				if dir == "" {
					dir = product.Name
				}
				target := Target{URL: "https://" + ref + "." + host}
				// CLONE ANNOUNCES WHERE IT IS CLONING FROM, like every other
				// verb. It resolves an environment — the source it downloads is
				// one environment's deployed code — and a verb that resolves
				// without saying so is how "this went somewhere I did not mean"
				// becomes visible only afterwards.
				fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s/%s\n", product.Name, envName)
				// MADE HERE, REMOVED IF NOTHING ARRIVES: a clone that failed
				// must not leave a directory named after the project, because
				// the next reader cannot tell an empty clone from a clone that
				// is still running.
				existed := dirExists(dir)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
				cleanup := func() { reapEmptyClone(dir, existed) }
				cred, _, credErr := Credential(target.URL)
				if credErr != nil {
					cleanup()
					return credErr
				}
				if err := fetchDeployedSource(
					ctx, target, cred, target.URL, dir, cmd.OutOrStdout()); err != nil {
					cleanup()
					return err
				}
				// BOUND BY IDENTITY, AND THE SELECTION IS SET.
				//
				// Without the selection a freshly cloned multi-environment
				// project would refuse the very next command — correct by the
				// rules and useless as an experience. The clone KNOWS which
				// environment it took the source from, so it remembers that one.
				return inDir(dir, func() error {
					if err := WriteLinkedIdentity(product, Target{}); err != nil {
						return err
					}
					root, wdErr := os.Getwd()
					if wdErr != nil {
						return wdErr
					}
					if err := WriteSelection(root, Selection{
						Project: product.ID, Env: envName, Ref: ref,
					}); err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "▸ %s/%s\n", product.Name, envName)
					return nil
				})
			}

			// Clone accepts a name or ref, as printed by project list.
			return fmt.Errorf(
				"%q is a management project id, and nothing in this CLI prints one.\n"+
					"  `palbase project list` prints the NAME and the REF — clone takes either",
				given)
		},
	}
	cmd.Flags().StringVar(&dirFlag, "dir", "", "Directory to clone into (default: the project's name)")
	cmd.Flags().StringVar(&envFlag, "from-env", "", "environment to take the source from (default: the project's only one)")
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

// dirExists answers whether a path is already there, without creating it.
func dirExists(dir string) bool {
	_, err := os.Stat(dir)
	return err == nil
}

// reapEmptyClone removes a directory `clone` made and could not fill.
//
// A WRITER MUST NOT LEAVE WHAT IT COULD NOT PRODUCE. `clone` creates the
// directory before the download because the download writes into it, so a
// failure — nothing deployed yet, no credential, a dropped connection — used to
// leave an empty directory named after the project. The next reader cannot tell
// that from a clone still in progress, and `ls` says the project is here when
// none of it is.
//
// IT ONLY REMOVES WHAT IT MADE, and only while empty: a directory that was
// already there belongs to whoever put it there, and one with files in it may
// hold a partial download somebody wants to look at.
//
// IT RETURNS THE DECISION AND NOT THE SYSCALL'S OUTCOME, and that distinction
// took two mutations to get right.
//
// `os.Remove` refuses a non-empty directory by itself, so a test that only
// looks at the filesystem stays green however this function decides — it
// measures the kernel, not the rule. Returning `os.Remove(dir) == nil` did not
// fix that either: a wrong decision then produced `false` because the OS said
// no, which is the same answer a right decision gives. So the value returned is
// what was DECIDED, computed before anything is touched.
func reapEmptyClone(dir string, existedBefore bool) bool {
	if existedBefore {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return false
	}
	_ = os.Remove(dir)
	return true
}
