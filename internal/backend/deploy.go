package backend

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/palgroup/palbase-cli/internal/auth"
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
// (github mode), the mgmt deploy client (platform mode), and the target
// branch.
type pushDeps struct {
	git    gitRunner
	rest   deployClient
	branch string
}

// runPush routes `palbase push` by the linked project's mode:
//   - github:   exec `git push` (orchestrator deploys via webhook).
//   - platform: build a tarball of the cwd and POST it to the Management
//     API deploy endpoint for the project.
func runPush(d pushDeps) error {
	mode, err := resolveMode()
	if err != nil {
		return err
	}
	if mode == "github" {
		return d.git("git", "push")
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
	_, err = d.rest.PostMultipart(path, tarball, map[string]string{
		"branch":  d.branch,
		"message": "deploy via cli",
	})
	return err
}
