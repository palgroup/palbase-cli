package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/hook"
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
}

// gitRunner runs an external command (default: git). Injected so the
// github-mode path is testable without forking a real git push.
type gitRunner func(name string, args ...string) error

type gitBranchResolver func() (string, error)

// execGit forks a real command, wiring std streams so git's prompts and
// progress reach the user's terminal.
func execGit(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func currentGitBranch() (string, error) {
	out, err := exec.Command("git", "branch", "--show-current").Output()
	if err != nil {
		return "", fmt.Errorf("read current Git branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", fmt.Errorf("current checkout is detached — switch to the Git branch mapped to the selected environment")
	}
	return branch, nil
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

// pushDeps are the injected collaborators for runPush — the git runner (github
// provider), the mgmt deploy client (palbase provider), the resolved
// Project/Environment, and the writer success output is reported to.
//
// ctx, pollInterval and pollTimeout drive the wait-for-terminal loop; when unset
// they default (background ctx, 1.5s, 5m) so tests can shrink them.
type pushDeps struct {
	git       gitRunner
	gitBranch gitBranchResolver
	rest      deployClient
	sel       selection.Selection
	out       io.Writer
	ctx       context.Context
	// build and pack are the bundling seam. Production builds this project with
	// its own node_modules and packs the result; tests substitute both so the
	// upload contract can be asserted without Bun and a real controller tree.
	build        func(context.Context, string, io.Writer) ([]uploadUse, error)
	pack         func(string) ([]byte, error)
	pollInterval time.Duration
	pollTimeout  time.Duration
	// idempotencyKey is the key the tarball upload rides. Empty => minted per
	// invocation (production); tests pin it to assert the header.
	idempotencyKey string
	// uploadRetries bounds the same-key retries of a TIMED-OUT upload. Default 2.
	uploadRetries int
}

// runPush routes `palbase push` by the project's repository provider:
//   - github:  exec `git push` (the webhook deploys into the mapped Environment).
//   - palbase: tarball the cwd and POST it to the SELECTED Environment's deploy
//     ingress, carrying an Idempotency-Key.
func runPush(d pushDeps) error {
	out := d.out
	if out == nil {
		out = os.Stdout
	}
	ctx := d.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	if d.sel.RepositoryProvider == selection.ProviderGitHub {
		if err := requireMappedGitBranch(d.sel, d.gitBranch); err != nil {
			return err
		}
		// Wire (or self-heal to v2) the pre-push hook before pushing so the
		// deploy-validation gate runs on THIS push. Best-effort — never blocks the
		// push. Only a github-provider project has a git checkout to hook.
		if cwd, err := os.Getwd(); err == nil {
			hook.Ensure(cwd, out)
		}
		if err := d.git("git", "push"); err != nil {
			return err
		}
		fmt.Fprintln(out, "✓ pushed to GitHub — the mapped environment deploys via webhook")
		return nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// THE CLIENT BUNDLES. WHERE THE BUNDLER LIVES IS DECIDED BY WHERE THE SOURCE
	// ENTERS THE SYSTEM: through the CLI it enters here, so it is built here;
	// through a git webhook it enters the cloud, so the cloud builds it. The
	// plane's push route is a RELAY — it takes an artifact and hands it to the
	// tenant's own management surface — and it has no builder to offer.
	//
	// This arm used to upload a SOURCE tarball to a deploy ingress that would
	// build it. No such surface exists on this plane: measured live 2026-08-24,
	// `POST /api/v2/projects/{id}/environments/{ref}/deploy` answered
	// `not_found`, so `palbase push` could not deploy to a v2 project at all.
	build := d.build
	if build == nil {
		build = buildStackArtifact
	}
	pack := d.pack
	if pack == nil {
		pack = BuildStackTarball
	}

	uses, err := build(ctx, cwd, out)
	if err != nil {
		fmt.Fprintln(out, "✗ build failed — nothing was pushed (fix the errors above)")
		return err
	}
	// The @Upload bucket gate is the PLANE's: it holds the environment's bucket
	// list and answers `upload_bucket_missing` before anything activates. A
	// second copy here would be a second place for the two to disagree.
	_ = uses

	tarball, err := pack(cwd)
	if err != nil {
		return fmt.Errorf("package backend: %w", err)
	}
	fmt.Fprintf(out, "sending %d KB\n", len(tarball)/1024)

	var res struct {
		Digest        string `json:"digest"`
		EndpointCount int    `json:"endpointCount"`
	}
	if err := d.rest.Do(ctx, http.MethodPost,
		PushPath(d.sel.EnvironmentRef()),
		map[string]any{"artifact": base64.StdEncoding.EncodeToString(tarball)}, &res); err != nil {
		return err
	}

	// ZERO ENDPOINTS IS A SILENT FAILURE and the plane refuses it; saying the
	// count here is what makes the success line checkable rather than decorative.
	fmt.Fprintf(out, "✓ live on %s: %d endpoint(s), %s\n",
		d.sel.EnvironmentRef(), res.EndpointCount, short(res.Digest))
	fmt.Fprintln(out, "  the contract just changed — run `palbase spec` to bring the committed client level")
	return nil
}

// cloneDeps are the injected collaborators for runClone.
type cloneDeps struct {
	git      gitRunner
	provider string
	repoURL  string
	dir      string
	// download, when set, fetches+extracts the palbase-provider bundle into dir.
	download func(dir string) error
	// insideRepo reports whether dir already sits in a git work tree; nil uses
	// the real probe. Tests substitute it to drive both branches.
	insideRepo func(dir string) bool
	out        io.Writer
	writeCfg   func(dir string, cfg *selection.Config) error
	cfg        *selection.Config
}

// runClone routes `palbase clone` by the project's repository provider:
//   - github:  `git clone <url> <dir>`, then write config v2 into <dir>.
//   - palbase: delegate to the injected bundle downloader.
func runClone(d cloneDeps) error {
	if d.provider == selection.ProviderGitHub {
		if err := d.git("git", "clone", d.repoURL, d.dir); err != nil {
			return err
		}
		// Wire core.hooksPath now rather than waiting for the first `npm install`
		// (prepare) — a fresh clone should push through the gate immediately.
		hook.Ensure(d.dir, os.Stdout)
		return d.writeCfg(d.dir, d.cfg)
	}
	if d.download == nil {
		return fmt.Errorf("palbase-provider clone is not available (bundle download not wired)")
	}
	if err := d.download(d.dir); err != nil {
		return err
	}
	// The bundle no longer carries the platform's own .git (it used to, and
	// pulling it over a checkout was a data-loss bug wearing an idempotence
	// costume). A palbase-provider clone therefore has to start the repository
	// itself — but only when nothing already contains this path, so cloning into
	// a monorepo does not plant a nested .git.
	insideRepo := d.insideRepo
	if insideRepo == nil {
		insideRepo = dirIsInsideGitRepo
	}
	out := d.out
	if out == nil {
		out = os.Stdout
	}
	if !insideRepo(d.dir) {
		if err := quietGit(d.dir, "init", "-b", "main"); err != nil {
			fmt.Fprintf(out, "note: could not initialise a git repository in %s (%v)\n", d.dir, err)
		} else {
			fmt.Fprintln(out, "initialized a git repository (branch main)")
		}
	}
	return d.writeCfg(d.dir, d.cfg)
}

// quietGit runs git without wiring the user's terminal to it. The probe below
// EXPECTS a failure outside a repository, and execGit would print git's "fatal:
// not a git repository" straight at someone who did nothing wrong.
func quietGit(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

// dirIsInsideGitRepo reports whether dir already sits inside a work tree, so a
// clone into a monorepo subdirectory reuses that repository instead of nesting.
// A git that is missing or errors counts as "not a repo" — the worst case is an
// init that then fails and is reported.
func dirIsInsideGitRepo(dir string) bool {
	return quietGit(dir, "rev-parse", "--is-inside-work-tree") == nil
}

// pullDeps are the injected collaborators for runPull.
type pullDeps struct {
	git       gitRunner
	gitBranch gitBranchResolver
	sel       selection.Selection
	refetch   func() error
}

// runPull routes `palbase pull` by the project's repository provider.
func runPull(d pullDeps) error {
	if d.sel.RepositoryProvider == selection.ProviderGitHub {
		if err := requireMappedGitBranch(d.sel, d.gitBranch); err != nil {
			return err
		}
		return d.git("git", "pull")
	}
	if d.refetch == nil {
		return fmt.Errorf("palbase-provider pull is not available (bundle refetch not wired)")
	}
	return d.refetch()
}

func requireMappedGitBranch(sel selection.Selection, resolve gitBranchResolver) error {
	mapped := ""
	if sel.Environment.SourceGitBranch != nil {
		mapped = strings.TrimSpace(*sel.Environment.SourceGitBranch)
	}
	label := sel.Environment.Slug
	if label == "" {
		label = sel.EnvironmentRef()
	}
	if mapped == "" {
		return fmt.Errorf("selected environment %q has no mapped Git branch — run `palbase env branch <git-branch>` before push/pull", label)
	}
	if resolve == nil {
		resolve = currentGitBranch
	}
	current, err := resolve()
	if err != nil {
		return err
	}
	if current != mapped {
		return fmt.Errorf(
			"selected environment %q maps Git branch %q, but the current branch is %q — switch branches or select the matching environment",
			label, mapped, current,
		)
	}
	return nil
}

// SourcePath is where a project hands back the tree one of its versions was
// built from. `latest` is the version it is serving right now — the only one a
// person can name before they have pulled the project once.
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
func newPushCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push",
		Args:  cobra.NoArgs,
		Short: "Deploy the current backend — to the linked stack, or to the selected environment",
		Long: `Deploy the backend in the current directory.

  repository_provider = palbase: the directory is packaged and uploaded to the
      SELECTED environment's deploy ingress, carrying an Idempotency-Key so a
      timed-out push can be retried without deploying twice.
  repository_provider = github: runs ` + "`git push`" + `; the webhook deploys into the
      environment mapped to the pushed Git branch.

The global --project / --environment flags select a CLOUD environment. In a
checkout linked to a project they do not apply, and saying so is the point: a
flag that is accepted and ignored is worse than one that is refused.`,
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
				return runStackPush(cmd.Context(), target, cred, approve, cmd.OutOrStdout())
			}

			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", sel.Describe())
			return runPush(pushDeps{
				git:       execGit,
				gitBranch: currentGitBranch,
				rest:      r.REST(),
				sel:       sel,
				out:       cmd.OutOrStdout(),
				ctx:       cmd.Context(),
			})
		},
	}

	// ONE flag for every dangerous thing a push can do, because a person facing
	// a refusal should not have to learn which of several flags this particular
	// refusal wants. It approves what the plan MARKED: a schema change that
	// removes data, and replacing a secret the target already holds.
	cmd.Flags().Bool("approve", false,
		"apply the changes the plan marked: data-removing schema changes, and replacing a secret already set there")
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

			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", sel.Describe())
			refetch := func() error {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				return pullBundle(cmd.Context(), r, sel.EnvironmentRef(), cwd, cmd.OutOrStdout())
			}
			return runPull(pullDeps{
				git: execGit, gitBranch: currentGitBranch, sel: sel, refetch: refetch,
			})
		},
	}
}

// newCloneCmd wires `palbase clone <projectId>`: clone the project's code into
// ./<dir> and select it (config v2 = project + its production environment).
func newCloneCmd(r Resolvers) *cobra.Command {
	var dirFlag string
	cmd := &cobra.Command{
		Use:   "clone <projectId>",
		Args:  cobra.ExactArgs(1),
		Short: "Download a project locally and select it",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := args[0]
			ctx := cmd.Context()

			detail, err := selection.GetProject(ctx, r.REST(), projectID)
			if err != nil {
				return err
			}
			envs, err := selection.ListEnvironments(ctx, r.REST(), projectID)
			if err != nil {
				return err
			}
			target, ok := selection.DefaultEnvironment(envs)
			if !ok {
				return fmt.Errorf("project %s has no visible environment yet", projectID)
			}
			provider, err := detail.RepositoryProvider()
			if err != nil {
				return err
			}
			dir := dirFlag
			if dir == "" {
				dir = target.Slug
				if detail.GithubRepo != "" {
					dir = repoDirFromFullName(detail.GithubRepo)
				}
			}
			cfg := &selection.Config{
				ProjectID:          projectID,
				EnvironmentID:      target.ID,
				RepositoryProvider: provider,
			}
			return runClone(cloneDeps{
				git:      execGit,
				provider: provider,
				repoURL:  repoURLFromFullName(detail.GithubRepo),
				dir:      dir,
				cfg:      cfg,
				writeCfg: selection.Save,
				download: func(dst string) error {
					return PullBundle(ctx, r, target.Ref, dst, cmd.OutOrStdout())
				},
			})
		},
	}
	cmd.Flags().StringVar(&dirFlag, "dir", "", "Directory to clone into (default: the repo or environment name)")
	return cmd
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
