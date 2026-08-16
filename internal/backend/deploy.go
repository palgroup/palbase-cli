package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/hook"
	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/spf13/cobra"
)

// deployClient is the Management-API surface the palbase-mode deploy needs:
// PostMultipart uploads the tarball (with its Idempotency-Key), and Do polls the
// deployment-status-by-id route to a terminal state. *transport.Client satisfies
// it; tests inject a fake.
type deployClient interface {
	PostMultipart(ctx context.Context, path string, tarball []byte, fields map[string]string, idempotencyKey string) ([]byte, error)
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

// DeployPath is the v2 deploy ingress for one Environment.
func DeployPath(projectID, environmentRef string) string {
	return "/api/v2/projects/" + projectID + "/environments/" + environmentRef + "/deploy"
}

// DeploymentPath is the v2 status-by-id route for one deployment.
func DeploymentPath(projectID, environmentRef, deploymentID string) string {
	return "/api/v2/projects/" + projectID + "/environments/" + environmentRef + "/deployments/" + deploymentID
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
	git          gitRunner
	gitBranch    gitBranchResolver
	rest         deployClient
	sel          selection.Selection
	out          io.Writer
	ctx          context.Context
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
	// Gate the upload on a local pre-deploy validation (the github arm leaves this
	// to the pre-push hook). A user-code error (bad decorator, return type, major
	// skew) fails here so nothing broken is uploaded; environment problems (no
	// node, registry down) warn inside runBuild and return nil, so the server
	// still gates the real deploy.
	if err := runBuild(ctx, cwd, out); err != nil {
		fmt.Fprintln(out, "✗ build failed — nothing was pushed (fix the errors above)")
		return err
	}
	tarball, err := BuildTarball(cwd)
	if err != nil {
		return fmt.Errorf("package backend: %w", err)
	}

	// ONE key for this deploy, reused across every retry of the SAME upload.
	// That is the whole contract: a push whose response is lost to a timeout is
	// retried on the same key and REPLAYS the first response instead of starting
	// a second deploy. Minting a fresh key per attempt would deploy twice.
	key := d.idempotencyKey
	if key == "" {
		key = transport.NewIdempotencyKey()
	}
	retries := d.uploadRetries
	if retries == 0 {
		retries = 2
	}

	path := DeployPath(d.sel.ProjectID, d.sel.EnvironmentRef())
	body, err := postDeployWithRetry(ctx, d.rest, path, tarball,
		map[string]string{"message": "deploy via cli"}, key, retries, os.Stderr)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "✓ deploy started for environment %s\n", d.sel.EnvironmentRef())
	id := deploymentIDFromResponse(body)
	if id == "" {
		// No deployment id in the response: nothing to poll. Keep the
		// fire-and-forget behavior rather than hard-failing a landed deploy.
		fmt.Fprintf(out, "  track it:   palbase status\n")
		return nil
	}
	fmt.Fprintf(out, "  deployment: %s\n", id)
	return waitForDeploy(d, id, out)
}

// postDeployWithRetry uploads the tarball, retrying ONLY transport-level
// failures (a timeout / connection reset — the case where the deploy may well
// have STARTED and we simply never saw the answer) on the SAME Idempotency-Key.
// An *APIError is a server verdict, not a lost response: it is returned as-is.
func postDeployWithRetry(
	ctx context.Context,
	rest deployClient,
	path string,
	tarball []byte,
	fields map[string]string,
	key string,
	retries int,
	progress io.Writer,
) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		body, err := rest.PostMultipart(ctx, path, tarball, fields, key)
		if err == nil {
			return body, nil
		}
		lastErr = err

		var apiErr *transport.APIError
		if errors.As(err, &apiErr) {
			// The server answered. 409 idempotency_key_in_progress means the FIRST
			// attempt is still running: the retry is safe to wait on, but it is not
			// an extra deploy either way — surface it with the actionable message.
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt < retries {
			fmt.Fprintf(progress, "upload did not complete (%v) — retrying with the same Idempotency-Key (no double deploy)\n", err)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return nil, lastErr
}

// deploymentStatus mirrors the deployments/{id} `data` payload: the fields the
// CLI polls to a decision.
type deploymentStatus struct {
	Status      string `json:"status"`
	CurrentStep string `json:"currentStep"`
	Error       string `json:"error"`
	Version     string `json:"version"`
}

// waitForDeploy polls the deployment-status route until the deploy reaches a
// terminal state, then reports the outcome:
//   - succeeded → "✓ deploy succeeded (version <v>)", exit 0.
//   - failed    → "✗ deploy FAILED: <server error>" to stderr, non-zero exit
//     (the exact server error — the migration SQLSTATE / drift text — so scripts
//     and CI catch a silently-broken deploy).
//   - timeout   → a note pointing at `palbase status`, exit 0 (don't falsely fail
//     a slow-but-fine deploy).
func waitForDeploy(d pushDeps, id string, out io.Writer) error {
	ctx := d.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	interval := d.pollInterval
	if interval <= 0 {
		interval = 1500 * time.Millisecond
	}
	timeout := d.pollTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	path := DeploymentPath(d.sel.ProjectID, d.sel.EnvironmentRef(), id)
	deadline := time.Now().Add(timeout)
	// stderr carries the machine-readable failure and the progress dots, so a
	// script capturing stdout isn't polluted.
	progress := os.Stderr

	fmt.Fprint(progress, "  deploying")
	for {
		var st deploymentStatus
		err := d.rest.Do(ctx, http.MethodGet, path, nil, &st)
		if err != nil {
			var apiErr *transport.APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
				// The row isn't queryable yet on the very first tick. Don't punish a
				// benign race — fall back to fire-and-forget.
				fmt.Fprintln(progress)
				fmt.Fprintf(out, "  (deploy status not available yet — track it: palbase status)\n")
				return nil
			}
			// A transient network error shouldn't abort the whole wait; keep polling
			// until the deadline, then surface it as a timeout note.
			if time.Now().After(deadline) {
				fmt.Fprintln(progress)
				fmt.Fprintf(out, "  deploy still running after %s (last error polling status: %v); check `palbase status`\n", timeout, err)
				return nil
			}
		} else {
			switch st.Status {
			case "succeeded":
				fmt.Fprintln(progress)
				version := st.Version
				if version == "" {
					version = "(unknown)"
				}
				fmt.Fprintf(out, "✓ deploy succeeded (version %s)\n", version)
				return nil
			case "failed":
				fmt.Fprintln(progress)
				msg := st.Error
				if msg == "" {
					msg = "deploy failed (no error detail from server)"
				}
				fmt.Fprintf(progress, "✗ deploy FAILED: %s\n", msg)
				// Non-zero exit so scripts/CI catch it. The detailed line is already
				// printed, so keep the returned error short.
				return fmt.Errorf("deploy failed")
			default:
				fmt.Fprint(progress, ".")
			}
		}

		if time.Now().After(deadline) {
			fmt.Fprintln(progress)
			fmt.Fprintf(out, "  deploy still running after %s; check `palbase status`\n", timeout)
			return nil
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(progress)
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// deploymentIDFromResponse extracts the deploymentId from the deploy endpoint's
// `{ "data": { "deploymentId": ... }, "request_id": ... }` envelope. A parse
// miss is non-fatal — the deploy already started; we just skip the id line.
func deploymentIDFromResponse(body []byte) string {
	var env struct {
		Data struct {
			DeploymentID string `json:"deploymentId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	return env.Data.DeploymentID
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

// pullResponse mirrors the backend.pull tRPC query result:
//
//	{ version: string, archive: string /* base64 */, size: number }
//
// Studio fetches the deployed bundle from the br-pod, base64-encodes it for
// JSON-RPC transport, and returns the version SHA. We decode it client-side and
// extract via extractTarGz.
type pullResponse struct {
	Version string `json:"version"`
	Archive string `json:"archive"` // base64-encoded tar.gz
	Size    int    `json:"size"`
}

// pullBundle fetches the Environment's deployed bundle from Studio
// (backend.pull), base64-decodes it and extracts it into dst. dst must already
// exist. `ref` is the ENVIRONMENT ref — the only selector; there is no branch.
// PullBundle downloads an Environment's ACTIVE deployed bundle into dst,
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

func pullBundle(ctx context.Context, r Resolvers, ref, dst string, w io.Writer) error {
	var resp pullResponse
	if err := r.Studio().Query(ctx, "backend.pull", map[string]any{"ref": ref}, &resp); err != nil {
		return fmt.Errorf("backend.pull: %w", err)
	}
	if resp.Archive == "" {
		return fmt.Errorf("backend.pull: server returned empty archive")
	}
	decoded, err := base64.StdEncoding.DecodeString(resp.Archive)
	if err != nil {
		return fmt.Errorf("decode bundle: %w", err)
	}
	if err := extractSourceTree(dst, bytes.NewReader(decoded)); err != nil {
		return fmt.Errorf("extract bundle: %w", err)
	}
	version := resp.Version
	if version == "" {
		version = "(unknown)"
	}
	fmt.Fprintf(w, "✓ pulled environment %s (version %s)\n", ref, version)
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

Override the target with the global --project / --environment flags.`,
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
				token, _, tokErr := Credential(target.URL)
				if tokErr != nil {
					return tokErr
				}
				accept, _ := cmd.Flags().GetBool("accept-data-loss")
				return runStackPush(cmd.Context(), target, token, accept, cmd.OutOrStdout())
			}

			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
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

	// Declared here so the linked-stack branch can read it. It means nothing
	// on the cloud path, where a destructive schema change goes through review
	// rather than through a flag.
	cmd.Flags().Bool("accept-data-loss", false,
		"with a linked stack: also run the schema changes that destroy data")
	return cmd
}

// newPullCmd wires `palbase pull`.
func newPullCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Args:  cobra.NoArgs,
		Short: "Update the local backend to the environment's deployed version",
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
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
