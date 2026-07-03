package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ProjectConfig holds the linked project configuration (.palbase/config.json).
//
// Persists the project's `ref` because every downstream command
// (`backend init --ref`, `backend deploy --ref`) keys off the ref —
// the bare project ID isn't part of any URL or runtime context.
type ProjectConfig struct {
	Ref        string `json:"ref"`
	DefaultEnv string `json:"default_env"`
	// Mode is "platform" (GitHub-free, deploy via tarball) or "github"
	// (deploy via git push → webhook). Empty defaults to "github" for
	// backward compatibility with pre-existing linked projects.
	Mode       string `json:"mode,omitempty"`
	GithubRepo string `json:"github_repo,omitempty"`
	// IOSAppID is the registered ios app `palbase ios link` bound this project
	// to. `palbase ios use <branch>` reads it so it can refresh the config
	// without re-resolving the group's ios app every time. Empty until an
	// ios app is linked.
	IOSAppID string `json:"ios_app_id,omitempty"`
}

// Project represents a project as the Studio tRPC layer returns it.
// Mirrors the columns project.list selects from control-pg's projects
// table.
type Project struct {
	ID     string `json:"id"`
	Ref    string `json:"ref"`
	Name   string `json:"name"`
	Tier   string `json:"tier"`
	Region string `json:"region"`
	Status string `json:"status"`
}

// PlatformAPI abstracts project listing for testing. Production
// implementations call Studio's tRPC project.list endpoint with the
// CLI's bearer token.
type PlatformAPI interface {
	ListProjects(ctx context.Context, token string) ([]Project, error)
}

// Linker handles project linking.
type Linker struct {
	AuthClient  *Client
	PlatformAPI PlatformAPI
	Output      io.Writer
	// SelectFn lets the caller pick when multiple projects are visible
	// and the user didn't pass `--ref`. CLI wires a numeric prompt;
	// tests inject a deterministic stub.
	SelectFn func(projects []Project) (*Project, error)
}

// Link links the current directory to a project. `wantRef` is the
// project ref the user passed (positional arg or --ref); empty means
// "ask the user to pick from project.list".
func (l *Linker) Link(ctx context.Context, wantRef string) error {
	token, err := l.AuthClient.GetValidToken(ctx)
	if err != nil {
		return err
	}

	projects, err := l.PlatformAPI.ListProjects(ctx, token)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		return fmt.Errorf("no projects found — create one at the Palbase Studio dashboard first")
	}

	var selected *Project
	if wantRef != "" {
		for i := range projects {
			if projects[i].Ref == wantRef {
				selected = &projects[i]
				break
			}
		}
		if selected == nil {
			return fmt.Errorf("no project with ref %q found in your account", wantRef)
		}
	} else if len(projects) == 1 {
		// Only one project? skip the picker — the answer is obvious.
		selected = &projects[0]
	} else {
		selected, err = l.SelectFn(projects)
		if err != nil {
			return err
		}
	}

	// Default to the project's main branch (CLI-2): a fresh link should
	// serve against main, not staging. `palbase branch switch` changes the
	// active branch per-project.
	cfg := &ProjectConfig{Ref: selected.Ref, DefaultEnv: "main"}
	if err := SaveProjectConfig(cfg); err != nil {
		return err
	}
	if err := ensureGitignore(); err != nil {
		return err
	}

	fmt.Fprintf(l.Output, "✓ Linked to %s (%s)\n", selected.Name, selected.Ref)
	return nil
}

// SaveProjectConfig writes .palbase/config.json in the current directory.
func SaveProjectConfig(cfg *ProjectConfig) error {
	return SaveProjectConfigIn(".", cfg)
}

// SaveProjectConfigIn writes <dir>/.palbase/config.json. Same as
// SaveProjectConfig but lets the caller target a directory other than the
// cwd.
func SaveProjectConfigIn(dir string, cfg *ProjectConfig) error {
	palbaseDir := filepath.Join(dir, ".palbase")
	if err := os.MkdirAll(palbaseDir, 0755); err != nil {
		return fmt.Errorf("create .palbase directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(filepath.Join(palbaseDir, "config.json"), data, 0644)
}

// UnlinkProjectConfig removes the .palbase/config.json link in the current
// directory, detaching the cwd from its project. Idempotent: a missing
// link is a no-op (not an error), so `palbase mobile unlink ios` can be
// re-run safely. Only the link file is removed — generated code, the
// Xcode build phase, and .gitignore entries are left untouched.
func UnlinkProjectConfig() error {
	path := filepath.Join(".palbase", "config.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove project config: %w", err)
	}
	return nil
}

// LoadProjectConfig reads .palbase/config.json from the current directory.
func LoadProjectConfig() (*ProjectConfig, error) {
	data, err := os.ReadFile(filepath.Join(".palbase", "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("project not linked — run from a project directory or pass --ref to pick a project")
		}
		return nil, fmt.Errorf("read project config: %w", err)
	}

	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse project config: %w", err)
	}
	if cfg.Ref == "" {
		return nil, fmt.Errorf(".palbase/config.json has a missing ref — clone the project with `palbase clone <ref>` or run from a project directory")
	}
	return &cfg, nil
}

// ErrNotLinked is returned by ResolveProjectRef when the cwd has no usable
// .palbase/config.json (missing, or present without a ref) and no --ref
// override was passed.
//
// Subcommands catch this with errors.Is to decide whether to prompt the user
// for a project (the interactive picker in backend.resolveOrLinkRef) or fail
// loudly. The message is the canonical "not linked" guidance shown to users.
var ErrNotLinked = errors.New("project not linked — pass --ref or run from a project directory")

// ResolveProjectRef resolves the linked project ref. Order:
//  1. --ref flag override (returned verbatim)
//  2. .palbase/config.json's ref (link writes it)
//  3. ErrNotLinked — caller decides whether to prompt or fail.
//
// This is the single source of truth for "which project am I in"; every
// cwd-scoped subcommand (backend/db/secret/notifications) calls it. Returning
// the ErrNotLinked sentinel (rather than bubbling LoadProjectConfig's raw
// "not linked" string) lets callers branch on errors.Is — the backend serve
// path uses that to auto-link via project.list before failing.
func ResolveProjectRef(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	cfg, err := LoadProjectConfig()
	if err != nil {
		// LoadProjectConfig returns a not-linked / missing-ref error when there's
		// no usable config; collapse those to the ErrNotLinked sentinel. Any other
		// error (read failure, malformed JSON) is a real problem — surface it.
		if os.IsNotExist(errors.Unwrap(err)) || strings.Contains(err.Error(), "not linked") || strings.Contains(err.Error(), "missing ref") {
			return "", ErrNotLinked
		}
		return "", err
	}
	if cfg.Ref == "" {
		return "", ErrNotLinked
	}
	return cfg.Ref, nil
}

func ensureGitignore() error {
	content, err := os.ReadFile(".gitignore")
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}

	if containsLine(string(content), ".palbase/") {
		return nil
	}

	f, err := os.OpenFile(".gitignore", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open .gitignore: %w", err)
	}
	defer f.Close()

	if len(content) > 0 && content[len(content)-1] != '\n' {
		fmt.Fprintln(f)
	}
	fmt.Fprintln(f, ".palbase/")
	return nil
}

func containsLine(s, line string) bool {
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if s[start:i] == line {
				return true
			}
			start = i + 1
		}
	}
	return false
}
