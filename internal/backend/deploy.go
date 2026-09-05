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

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// deployClient is the Management-API surface the palbase-mode deploy needs: one
// call that hands the built artifact to the plane. *transport.Client satisfies
// it; tests inject a fake.
//
// It used to upload a multipart source tarball and then POLL a deployment id to
// a terminal state, because the ingress it spoke to built the source. This plane
// has no such ingress: its push route is a synchronous relay to the tenant's own
// management surface, so the answer IS the outcome — there is nothing to poll.
type deployClient interface {
	Do(ctx context.Context, method, path string, body, out any) error
	// DoIdempotent carries a replay key. The artifact upload uses it so a
	// request that timed out AFTER the plane accepted it is not retried into a
	// second deploy of the same bytes.
	DoIdempotent(ctx context.Context, method, path string, body, out any, idempotencyKey string) error
}

// PushPath is where an ARTIFACT is handed to the plane for one Environment.
//
// The ref is the whole selector: this plane's push route names the Environment
// and nothing else, because an Environment IS a tenant here.
func PushPath(environmentRef string) string {
	return "/v1/cloud/projects/" + url.PathEscape(environmentRef) + "/push"
}

// DeploymentsPath is the v2 deploy-history route for one Environment.
func DeploymentsPath(projectID, environmentRef string) string {
	return "/api/v2/projects/" + projectID + "/environments/" + environmentRef + "/deployments"
}

func SourcePath(digest string) string {
	return "/v1/management/deployments/" + url.PathEscape(digest) + "/source"
}

// PullBundle downloads an Environment's ACTIVE deployed source into dst,
// creating dst if needed.
//
// Exported for `project create`, which materializes the template provisioning
// just seeded as version 1. That is deliberately the SAME download `clone` and
// `pull` use: a new project's starting code is the code the server actually
// deployed, so there is no second skeleton to drift from it.
func PullBundle(ctx context.Context, r Resolvers, environmentRef, dst string, w io.Writer) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return pullBundle(ctx, r, environmentRef, dst, w)
}

// pullBundle fetches the Environment's deployed source tree and extracts it into
// dst, which must already exist. `ref` is the ENVIRONMENT ref — the only
// selector; there is no branch.
//
// It used to ask the Studio for it over tRPC, and the Studio held a copy of
// every push. This plane keeps no such copy: a push is unpacked, built and the
// tree deleted. So the project now keeps it beside its artifact and answers for
// it — which also means this asks the SAME authority that served the code, over
// the same door as everything else, instead of a second service that had to be
// told about every deploy to stay right.
func pullBundle(ctx context.Context, r Resolvers, ref, dst string, w io.Writer) error {
	host := r.Endpoints().PublicHost
	if host == "" {
		return errors.New("this CLI has no tenant host configured, so a project cannot be reached by ref")
	}
	target := Target{URL: "https://" + ref + "." + host}
	cred, _, err := Credential(target.URL)
	if err != nil {
		return err
	}
	return fetchDeployedSource(ctx, target, cred, ref, dst, w)
}

// fetchDeployedSource is the transfer itself, given a project already resolved.
//
// Split from pullBundle because the two do different jobs: one decides WHICH
// project, the other reads it. Keeping them together meant the read could only
// be exercised through an address built from a configured host — which is to say
// it could not be exercised at all.
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

// ── cobra command constructors ──────────────────────────────────────────
//
// push/pull/clone are provider-aware: github shells out to git, palbase rides
// the Management API. The resolvers are only CALLED inside RunE, never at
// construction, so Commands(Resolvers{}) can't panic on a nil accessor (the
// structural registration tests build the tree with a zero Resolvers).

// newPushCmd wires `palbase push` — the deploy verb. It always targets the
// SELECTED Environment; there is no --branch.
// stackPush is the seam the flag→consent binding is asserted through.
//
// Reading the flags in a test and calling `pushURL` proves the URL builder and
// nothing else: a fresh verifier bound `breaking` to `GetBool("approve")` and no
// test noticed, which would have meant `--approve` silently opening the
// compatibility gate. The binding itself has to be observable, so the call goes
// through a variable a test can stand in front of.
var stackPush = runStackPush

func newPushCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push",
		Args:  cobra.NoArgs,
		Short: "Deploy the current backend to the project this checkout is linked to",
		Long: `Deploy the backend in the current directory.

The directory is packaged and uploaded to the linked project's deploy ingress,
carrying an Idempotency-Key so a timed-out push can be retried without deploying
twice. There is no repository-driven rail: ` + "`git push`" + ` deploys nothing.

This acts on the project this checkout is bound to. There is one addressing
mechanism — ` + "`palbase link`" + ` — and no flags that select a different one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// TARGET-RELATIVE (design-management-api.md §10). A checkout linked
			// to a stack pushes to THAT stack, which applies its own schema and
			// activates its own code with no control plane in the path. Without
			// a link this is the cloud path, which resolves a project and an
			// environment first.
			//
			// One verb either way: a person should not have to remember which
			// kind of push their project uses — the link they already made is
			// what decides.
			if target, err := ReadTarget(); err == nil {
				// The global --project/--environment select a CLOUD environment,
				// and this checkout is bound to a project. Ignoring them here
				// was silent: somebody who believed the help text could push to
				// the linked stack while thinking they had picked staging, and
				// the banner — which exists to catch exactly that — would
				// confirm the wrong place.
				if err := refuseCloudSelectionFlags(cmd, target); err != nil {
					return err
				}
				cred, _, tokErr := Credential(target.URL)
				if tokErr != nil {
					return tokErr
				}
				approve, _ := cmd.Flags().GetBool("approve")
				breaking, _ := cmd.Flags().GetBool("accept-breaking")
				return stackPush(cmd.Context(), target, cred, approve, breaking, cmd.OutOrStdout())
			}

			// A FLAG THAT IS ACCEPTED AND IGNORED IS WORSE THAN ONE THAT DOES NOT
			// EXIST — this command says so itself, a few lines up.
			//
			// `--accept-breaking` opens the stack's compatibility gate, and only
			// the stack has that gate. On the cloud path it reached nothing:
			// accepted, dropped, and the push went out under the ordinary rules
			// while the operator believed they had forced it. Refused by name
			// instead, the way `--project` and `--environment` are.
			if f := cmd.Flags().Lookup("accept-breaking"); f != nil && f.Changed {
				return fmt.Errorf(
					"--accept-breaking is for a stack you are linked to: it opens THAT stack's " +
						"compatibility gate, and this push goes to a cloud environment, which has " +
						"no such gate to open. Drop the flag, or run the push from a linked checkout")
			}
			// NO SECOND WAY IN (FR-013). Above this line a bound checkout has
			// already been served, by the address its committed file names. What
			// used to follow resolved through `.palbase/selection.json` — a file
			// nothing writes any more, so only a pre-cutover checkout could enter
			// here, and it entered a SECOND path to the same cloud.
			return selection.ErrNotSelected{}
		},
	}

	// ONE flag for every dangerous thing a push can do, because a person facing
	// a refusal should not have to learn which of several flags this particular
	// refusal wants. It approves what the plan MARKED: a schema change that
	// removes data, and replacing a secret the target already holds.
	cmd.Flags().Bool("approve", false,
		"apply the changes the plan marked: data-removing schema changes, and replacing a secret already set there")
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
func newPullCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Args:  cobra.NoArgs,
		Short: "Update the local backend to the environment's deployed version",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Look before writing: a pull replaces the backend in this directory
			// with the one the project is serving, and the moment it hurts is
			// the moment somebody had unsaved edits.
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := refuseDirtyTree(cwd); err != nil {
				return err
			}

			// TARGET-RELATIVE, like push, status, deploys and apikey already are.
			//
			// The transfer was target-relative all along — `pullBundle` builds the
			// project's address and asks ITS management API. Only the question of
			// WHICH ref went through the cloud selection, and that selection
			// demands a management project id: a value this CLI prints nowhere and
			// the person does not have. So in a checkout bound to a project, the
			// one verb that could not answer was the one whose answer was already
			// in the directory.
			//
			// ÖLÇÜLDÜ 25.08.2026 (palaicloud): `pull` ve `clone`, aynı bağlantı
			// üzerinden `status`/`deploys`/`apikey` çalışırken `not_found (404):
			// böyle bir proje yok` diyordu. Bağlı bir checkout'ta sorulacak soru
			// zaten yok — bağlandığı proje, çekeceği projedir.
			if target, terr := ReadTarget(); terr == nil && strings.TrimSpace(target.URL) != "" {
				if err := refuseCloudSelectionFlags(cmd, target); err != nil {
					return err
				}
				cred, _, credErr := Credential(target.URL)
				if credErr != nil {
					return credErr
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", target.URL)
				return fetchDeployedSource(
					cmd.Context(), target, cred, target.URL, cwd, cmd.OutOrStdout())
			}

			// NO SECOND WAY IN (FR-013). Above this line a bound checkout has
			// already been served, by the address its committed file names. What
			// used to follow resolved through `.palbase/selection.json` — a file
			// nothing writes any more, so only a pre-cutover checkout could enter
			// here, and it entered a SECOND path to the same cloud.
			return selection.ErrNotSelected{}
		},
	}
}

// newCloneCmd wires `palbase clone <project>`: clone the project's code into
// ./<dir> and select it (config v2 = project + its production environment).
func newCloneCmd(r Resolvers) *cobra.Command {
	var dirFlag string
	cmd := &cobra.Command{
		Use:   "clone <project>",
		Args:  cobra.ExactArgs(1),
		Short: "Download a project locally and select it",
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
				if !selection.IsCanonicalEnvironmentRef(ref) {
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

			// A MANAGEMENT PROJECT ID IS NOT SOMETHING ANYBODY CAN TYPE.
			//
			// This used to be the whole command, and it went through
			// `/api/v2/projects/{id}` to resolve the project, then wrote the
			// clone's binding into `.palbase/selection.json` — a file
			// `ReadTarget` stopped reading when the second addressing mechanism
			// was retired (FR-013). So the branch produced a directory that
			// every later verb reported as "not linked", reached by a value the
			// CLI prints on no surface at all, not even in
			// `project list --json`. Refusing names what to type instead.
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

// repoURLFromFullName turns a GitHub "org/repo" full name into a cloneable
// https URL. An empty full name (palbase provider) yields an empty URL.
func repoURLFromFullName(fullName string) string {
	if fullName == "" {
		return ""
	}
	return "https://github.com/" + fullName + ".git"
}

// repoDirFromFullName is the directory `git clone` would create for "org/repo".
func repoDirFromFullName(fullName string) string {
	for i := len(fullName) - 1; i >= 0; i-- {
		if fullName[i] == '/' {
			return fullName[i+1:]
		}
	}
	return fullName
}
