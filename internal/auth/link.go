package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ProjectConfig holds the linked project configuration (.palbase/config.json).
//
// Persists the project's `ref` because every downstream command
// (`backend init --ref`, `backend deploy --ref`) keys off the ref —
// the bare project ID isn't part of any URL or runtime context.
type ProjectConfig struct {
	Ref        string `json:"ref"`
	DefaultEnv string `json:"default_env"`
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

	// Default to the project's main branch (CLI-2): a fresh `palbase link`
	// should pull/dev against main, not staging. `palbase branch switch`
	// changes the active branch per-project.
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
// cwd — needed by `palbase pull` clone-mode, which creates a fresh
// <projectName>/ tree and writes the link config into it rather than into
// the directory the command was launched from.
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

// LoadProjectConfig reads .palbase/config.json from the current directory.
func LoadProjectConfig() (*ProjectConfig, error) {
	data, err := os.ReadFile(filepath.Join(".palbase", "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("project not linked — run: palbase link <ref>")
		}
		return nil, fmt.Errorf("read project config: %w", err)
	}

	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse project config: %w", err)
	}
	if cfg.Ref == "" {
		return nil, fmt.Errorf(".palbase/config.json missing ref — run: palbase link <ref>")
	}
	return &cfg, nil
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
