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
	"time"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/spf13/cobra"
)

// deployClient is the Management-API surface the platform-mode deploy needs:
// PostMultipart uploads the tarball, and Do polls the deployment-status-by-id
// endpoint to terminal state. It is satisfied by *transport.Client (and the
// backend.REST accessor), so the command wires the authed mgmt client in
// directly; tests inject a fake.
type deployClient interface {
	PostMultipart(path string, tarball []byte, fields map[string]string) ([]byte, error)
	Do(ctx context.Context, method, path string, body, out any) error
}

// gitRunner runs an external command (default: git). Injected so the
// github-mode path is testable without forking a real git push.
type gitRunner func(name string, args ...string) error

// execGit forks a real command, wiring std streams so git's prompts and
// progress reach the user's terminal.
func execGit(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// resolveMode reads the linked project's deploy mode from .palbase/config.json
// in the cwd. github mode (deploy via `git push` → webhook) is OPT-IN and ALWAYS
// written explicitly: `palbase clone` of a GitHub-backed project writes
// `mode:"github"` + the repo URL (deploy.go runClone). Every other link path —
// the `palbase link` / status picker, `palbase web link`, a hand-written config —
// targets a PLATFORM-managed project (GitHub is optional; platform is the
// documented default), and those writers do NOT set mode. So an ABSENT mode means
// platform, not github: defaulting absent→github made a platform project's
// `palbase push` shell out to `git push` and fail "not a git repository" / "no
// configured push destination" with no way to reach the documented tarball path
// (wave-4 w4budget hit exactly this). github mode is never implicit, so this is a
// clean cutover — only an explicit "github" takes the git path.
func resolveMode() (string, error) {
	cfg, err := auth.LoadProjectConfig()
	if err != nil {
		return "", err
	}
	if cfg.Mode == "github" {
		return "github", nil
	}
	return "platform", nil
}

// pushDeps are the injected collaborators for runPush — the git runner
// (github mode), the mgmt deploy client (platform mode), the target branch,
// and the writer success output is reported to.
//
// ctx, pollInterval and pollTimeout drive the platform-mode wait-for-terminal
// loop; when unset they default (background ctx, 1.5s, 5m) so tests can shrink
// the interval/timeout and the command wires the real cobra context.
type pushDeps struct {
	git          gitRunner
	rest         deployClient
	branch       string
	out          io.Writer
	ctx          context.Context
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// runPush routes `palbase push` by the linked project's mode:
//   - github:   exec `git push` (orchestrator deploys via webhook).
//   - platform: build a tarball of the cwd and POST it to the Management
//     API deploy endpoint for the project.
//
// On success it prints a one-line confirmation so the command is never silent.
func runPush(d pushDeps) error {
	out := d.out
	if out == nil {
		out = os.Stdout
	}

	mode, err := resolveMode()
	if err != nil {
		return err
	}
	if mode == "github" {
		if err := d.git("git", "push"); err != nil {
			return err
		}
		fmt.Fprintln(out, "✓ pushed to GitHub — deploy will run via webhook")
		return nil
	}

	cfg, err := auth.LoadProjectConfig()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	tarball, err := BuildTarball(cwd)
	if err != nil {
		return fmt.Errorf("package backend: %w", err)
	}
	path := fmt.Sprintf("/api/v1/projects/%s/deploy", cfg.Ref)
	body, err := d.rest.PostMultipart(path, tarball, map[string]string{
		"branch":  d.branch,
		"message": "deploy via cli",
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "✓ deploy started for %s (branch %s)\n", cfg.Ref, d.branch)
	id := deploymentIDFromResponse(body)
	if id == "" {
		// No deployment id in the response (older server): nothing to poll, keep
		// the fire-and-forget behavior so we never hard-break.
		fmt.Fprintf(out, "  track it:   palbase status\n")
		return nil
	}
	fmt.Fprintf(out, "  deployment: %s\n", id)
	return waitForDeploy(d, cfg.Ref, id, out)
}

// deploymentStatus mirrors the /api/v1/projects/:ref/deployments/:id `data`
// payload: the terminal-progress fields the CLI polls to a decision.
type deploymentStatus struct {
	Status      string `json:"status"`
	CurrentStep string `json:"currentStep"`
	Error       string `json:"error"`
	Version     string `json:"version"`
}

// waitForDeploy polls the deployment-status endpoint until the deploy reaches a
// terminal state, then reports the outcome:
//   - succeeded → "✓ deploy succeeded (version <v>)", exit 0.
//   - failed    → "✗ deploy FAILED: <server error>" to stderr, non-zero exit
//     (the exact server error — the migration SQLSTATE / drift text — so scripts
//     and CI catch a silently-broken deploy).
//   - timeout   → a note pointing at `palbase status`, exit 0 (don't falsely
//     fail a slow-but-fine deploy; this is the exception, not the norm).
//
// Graceful degradation: if the status endpoint 404s (an un-upgraded server that
// lacks the route, or the row not yet visible), fall back to the fire-and-forget
// note and exit 0 — never hard-break against an old server.
func waitForDeploy(d pushDeps, ref, id string, out io.Writer) error {
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

	path := fmt.Sprintf("/api/v1/projects/%s/deployments/%s", ref, id)
	deadline := time.Now().Add(timeout)
	// stderr is where the machine-readable failure and the periodic dots go, so a
	// script capturing stdout for `--json`-style consumption isn't polluted.
	progress := os.Stderr

	fmt.Fprint(progress, "  deploying")
	for {
		var st deploymentStatus
		err := d.rest.Do(ctx, http.MethodGet, path, nil, &st)
		if err != nil {
			// 404 = old server without the status route (or the row isn't
			// queryable yet on the very first tick). Fall back to fire-and-forget
			// so we don't punish an un-upgraded server or a benign race.
			var apiErr *transport.APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
				fmt.Fprintln(progress)
				fmt.Fprintf(out, "  (deploy status unavailable on this server — track it: palbase status)\n")
				return nil
			}
			// A transient network error shouldn't abort the whole wait; keep
			// polling until the deadline, then surface it as a timeout note.
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
				// Non-zero exit so scripts/CI catch it. cobra prints the returned
				// error to stderr and exits 1; we've already printed the detailed
				// line above, so keep the returned error short.
				return fmt.Errorf("deploy failed")
			default:
				// still deploying (pending/deploying/…) — show progress.
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
// miss is non-fatal — the deploy already succeeded; we just skip the id line.
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

// cloneDeps are the injected collaborators for runClone — the git runner and
// the config writer (github mode), plus the bundle downloader (platform mode,
// wired by the Task 12 command constructor).
type cloneDeps struct {
	git      gitRunner
	mode     string
	repoURL  string
	ref      string
	branch   string
	writeCfg func(dir string, cfg *auth.ProjectConfig) error
	// download, when set, fetches+extracts the platform-mode bundle into ./<ref>
	// and writes the platform-mode config. Injected by the command constructor.
	download func(ref, branch string) error
}

// runClone routes `palbase clone` by the requested mode:
//   - github:   exec `git clone <url> <ref>`, then write a github-mode
//     .palbase/config.json into ./<ref>.
//   - platform: delegate to the injected bundle downloader; without one wired,
//     return a clear error (the download is wired in Task 12).
func runClone(d cloneDeps) error {
	if d.mode == "github" {
		if err := d.git("git", "clone", d.repoURL, d.ref); err != nil {
			return err
		}
		return d.writeCfg(d.ref, &auth.ProjectConfig{
			Ref: d.ref, DefaultEnv: d.branch, Mode: "github", GithubRepo: d.repoURL,
		})
	}
	// platform mode
	if d.download == nil {
		return fmt.Errorf("platform-mode clone is not yet available (bundle download not wired)")
	}
	return d.download(d.ref, d.branch)
}

// pullDeps are the injected collaborators for runPull — the git runner (github
// mode) and the bundle refetcher (platform mode).
type pullDeps struct {
	git     gitRunner
	refetch func() error
}

// runPull routes `palbase pull` by the linked project's mode:
//   - github:   exec `git pull`.
//   - platform: delegate to the injected refetcher; without one wired, return
//     a clear error.
func runPull(d pullDeps) error {
	mode, err := resolveMode()
	if err != nil {
		return err
	}
	if mode == "github" {
		return d.git("git", "pull")
	}
	if d.refetch == nil {
		return fmt.Errorf("platform-mode pull is not yet available (bundle refetch not wired)")
	}
	return d.refetch()
}

// pullResponse mirrors the backend.pull tRPC query result:
//
//	{ version: string, archive: string /* base64 */, size: number }
//
// Studio fetches the deployed bundle from the br-pod (/internal/pull/{envId}),
// base64-encodes it for JSON-RPC transport, and returns the version SHA from
// the X-Version header. We decode it client-side and extract via extractTarGz.
type pullResponse struct {
	Version string `json:"version"`
	Archive string `json:"archive"` // base64-encoded tar.gz
	Size    int    `json:"size"`
}

// platformPullBundle fetches the deployed bundle for ref/branch from Studio
// (backend.pull tRPC query), base64-decodes the archive, and extracts it into
// dst. dst must already exist (for pull it is cwd; for clone the caller creates
// it). Prints a one-line success message to w.
func platformPullBundle(ctx context.Context, r Resolvers, ref, branch, dst string, w io.Writer) error {
	input := map[string]any{"ref": ref}
	if branch != "" && branch != "main" {
		input["branch"] = branch
	}
	var resp pullResponse
	if err := r.Studio().Query(ctx, "backend.pull", input, &resp); err != nil {
		return fmt.Errorf("backend.pull: %w", err)
	}
	if resp.Archive == "" {
		return fmt.Errorf("backend.pull: server returned empty archive")
	}
	decoded, err := base64.StdEncoding.DecodeString(resp.Archive)
	if err != nil {
		return fmt.Errorf("decode bundle: %w", err)
	}
	if err := extractTarGz(dst, bytes.NewReader(decoded)); err != nil {
		return fmt.Errorf("extract bundle: %w", err)
	}
	version := resp.Version
	if version == "" {
		version = "(unknown)"
	}
	fmt.Fprintf(w, "✓ pulled %s (branch %s, version %s)\n", ref, branchLabel(branch), version)
	return nil
}

// branchLabel returns the branch for display; falls back to "main" when empty
// (the tRPC input omits "main" for back-compat, but the user should see it).
func branchLabel(b string) string {
	if b == "" {
		return "main"
	}
	return b
}

// ── cobra command constructors ──────────────────────────────────────────
//
// push/pull/clone are mode-aware: github mode shells out to git, platform mode
// rides the Management API. The REST accessor (r.REST) is only CALLED inside
// RunE, never at construction, so Commands(Resolvers{}) can't panic on a nil
// accessor (the structural registration tests build the tree with a zero
// Resolvers).

// newPushCmd wires `palbase push`. github mode: `git push` (orchestrator
// deploys via webhook). platform mode: tarball the cwd and POST it to the
// Management-API deploy endpoint.
func newPushCmd(r Resolvers) *cobra.Command {
	var branch string
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Deploy the current backend (github: git push; platform: tarball upload)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPush(pushDeps{
				git:    execGit,
				rest:   r.REST(),
				branch: branch,
				out:    cmd.OutOrStdout(),
				ctx:    cmd.Context(),
			})
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "main", "target branch")
	return cmd
}

// newPullCmd wires `palbase pull`. github mode: `git pull`. platform mode:
// fetches the deployed bundle from Studio (backend.pull tRPC) and extracts
// it over the cwd, updating tracked files in-place.
func newPullCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Update the local backend to the latest deployed version",
		RunE: func(cmd *cobra.Command, args []string) error {
			refetch := func() error {
				cfg, err := auth.LoadProjectConfig()
				if err != nil {
					return err
				}
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				return platformPullBundle(cmd.Context(), r, cfg.Ref, cfg.DefaultEnv, cwd, cmd.OutOrStdout())
			}
			return runPull(pullDeps{git: execGit, refetch: refetch})
		},
	}
}

// newCloneCmd wires `palbase clone <project>`. github mode: `git clone <url>
// <ref>` + write a github-mode link. platform mode: downloads the deployed
// bundle from Studio (backend.pull tRPC), extracts it into ./<ref>, and
// writes a platform-mode .palbase/config.json.
func newCloneCmd(r Resolvers) *cobra.Command {
	var branch string
	cmd := &cobra.Command{
		Use:   "clone <project>",
		Short: "Download a project locally (github: git clone; platform: bundle)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			mode, repoURL, err := lookupProjectMode(cmd.Context(), r, ref)
			if err != nil {
				return err
			}
			download := func(ref, branch string) error {
				dst := ref // clone into ./<ref>/
				if err := platformPullBundle(cmd.Context(), r, ref, branch, dst, cmd.OutOrStdout()); err != nil {
					return err
				}
				return auth.SaveProjectConfigIn(dst, &auth.ProjectConfig{
					Ref: ref, DefaultEnv: branch, Mode: "platform",
				})
			}
			return runClone(cloneDeps{
				git: execGit, mode: mode, repoURL: repoURL, ref: ref,
				branch: branch, writeCfg: auth.SaveProjectConfigIn,
				download: download,
			})
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "main", "branch to clone")
	return cmd
}

// lookupProjectMode resolves the deploy mode + (github-mode) repo URL for a
// project the caller wants to clone, reading both straight off the Management
// API's GET /api/v1/projects/{ref} response.
//
// That surface now returns `mode` ("github" | "platform") and `github_repo`
// (the "org/repo" full name, or null in platform mode) alongside
// id/ref/name/tier/region/status. So we read the authoritative mode from the
// server — no local-config inference. The GET also membership-checks: it 404s
// as project_not_found for a non-member or unknown ref, failing fast with a
// clear error before we touch the filesystem.
//
// For github mode the clone needs a cloneable URL; `github_repo` is the
// "org/repo" full name, so the clone URL is
// https://github.com/<org/repo>.git. Platform mode has no repo → empty URL.
func lookupProjectMode(ctx context.Context, r Resolvers, ref string) (mode, repoURL string, err error) {
	var resp struct {
		Mode       string `json:"mode"`
		GithubRepo string `json:"github_repo"` // "org/repo"; null decodes to ""
	}
	if err := r.REST().Do(ctx, http.MethodGet, "/api/v1/projects/"+ref, nil, &resp); err != nil {
		return "", "", err
	}
	return resp.Mode, repoURLFromFullName(resp.GithubRepo), nil
}

// repoURLFromFullName turns a GitHub "org/repo" full name into a cloneable
// https URL (https://github.com/org/repo.git). An empty full name (platform
// mode, where github_repo is null) yields an empty URL.
func repoURLFromFullName(fullName string) string {
	if fullName == "" {
		return ""
	}
	return "https://github.com/" + fullName + ".git"
}
