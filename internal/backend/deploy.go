package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/spf13/cobra"
)

// deployClient is the multipart-POST surface the platform-mode deploy needs.
// It is satisfied by *transport.Client (PostMultipart), so the real command
// (Task 12) wires the authed mgmt client in directly; tests inject a fake.
type deployClient interface {
	PostMultipart(path string, tarball []byte, fields map[string]string) ([]byte, error)
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
// in the cwd. An explicit "platform" routes to the tarball-upload path; any
// other value (including the empty/legacy default) routes to github (`git
// push`), preserving back-compat for projects linked before the mode field.
func resolveMode() (string, error) {
	cfg, err := auth.LoadProjectConfig()
	if err != nil {
		return "", err
	}
	if cfg.Mode == "platform" {
		return "platform", nil
	}
	return "github", nil
}

// pushDeps are the injected collaborators for runPush — the git runner
// (github mode), the mgmt deploy client (platform mode), the target branch,
// and the writer success output is reported to.
type pushDeps struct {
	git    gitRunner
	rest   deployClient
	branch string
	out    io.Writer
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
	if id := deploymentIDFromResponse(body); id != "" {
		fmt.Fprintf(out, "  deployment: %s\n", id)
	}
	fmt.Fprintf(out, "  track it:   palbase status\n")
	return nil
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
			return runPush(pushDeps{git: execGit, rest: r.REST(), branch: branch, out: cmd.OutOrStdout()})
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "main", "target branch")
	return cmd
}

// newPullCmd wires `palbase pull`. github mode: `git pull`. platform mode:
// bundle refetch — not yet wired, so runPull returns a clear error there.
func newPullCmd(_ Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Update the local backend to the latest deployed version",
		RunE: func(cmd *cobra.Command, args []string) error {
			// platform-mode bundle refetch is not yet available → runPull returns
			// a clear error in platform mode (nil refetch); github mode execs
			// `git pull`.
			return runPull(pullDeps{git: execGit, refetch: nil})
		},
	}
}

// newCloneCmd wires `palbase clone <project>`. github mode: `git clone <url>
// <ref>` + write a github-mode link. platform mode: bundle download — not yet
// wired, so runClone returns a clear error there.
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
			return runClone(cloneDeps{
				git: execGit, mode: mode, repoURL: repoURL, ref: ref,
				branch: branch, writeCfg: auth.SaveProjectConfigIn,
				download: nil, // platform-mode bundle download not yet wired
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
