package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// ProjectConfig holds the linked project configuration (.palbase/config.json).
type ProjectConfig struct {
	ProjectID  string `json:"project_id"`
	DefaultEnv string `json:"default_env"`
}

// Project represents a project from the Platform API.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PlatformAPI abstracts Platform API calls for testing.
type PlatformAPI interface {
	ListProjects(ctx context.Context, token string) ([]Project, error)
}

// HTTPPlatformAPI implements PlatformAPI using HTTP calls.
type HTTPPlatformAPI struct {
	BaseURL    string
	HTTPClient HTTPClient
}

// ListProjects fetches projects from the Platform API.
func (a *HTTPPlatformAPI) ListProjects(ctx context.Context, token string) ([]Project, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+"/api/v1/projects", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list projects failed (%d): %s", resp.StatusCode, string(body))
	}

	var projects []Project
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		return nil, fmt.Errorf("parse projects: %w", err)
	}
	return projects, nil
}

// Linker handles project linking.
type Linker struct {
	AuthClient  *Client
	PlatformAPI PlatformAPI
	Output      io.Writer
	SelectFn    func(projects []Project) (*Project, error)
}

// Link links the current directory to a project.
func (l *Linker) Link(ctx context.Context, projectID string) error {
	token, err := l.AuthClient.GetValidToken(ctx)
	if err != nil {
		return err
	}

	var selected *Project

	if projectID != "" {
		selected = &Project{ID: projectID}
	} else {
		projects, err := l.PlatformAPI.ListProjects(ctx, token)
		if err != nil {
			return err
		}
		if len(projects) == 0 {
			return fmt.Errorf("no projects found — create one in the Palbase Studio")
		}
		selected, err = l.SelectFn(projects)
		if err != nil {
			return err
		}
	}

	cfg := &ProjectConfig{ProjectID: selected.ID, DefaultEnv: "staging"}

	if err := SaveProjectConfig(cfg); err != nil {
		return err
	}
	if err := ensureGitignore(); err != nil {
		return err
	}

	fmt.Fprintf(l.Output, "✓ Linked to project %s\n", selected.ID)
	return nil
}

// SaveProjectConfig writes .palbase/config.json in the current directory.
func SaveProjectConfig(cfg *ProjectConfig) error {
	if err := os.MkdirAll(".palbase", 0755); err != nil {
		return fmt.Errorf("create .palbase directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(filepath.Join(".palbase", "config.json"), data, 0644)
}

// LoadProjectConfig reads .palbase/config.json from the current directory.
func LoadProjectConfig() (*ProjectConfig, error) {
	data, err := os.ReadFile(filepath.Join(".palbase", "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("project not linked — run: palbase link")
		}
		return nil, fmt.Errorf("read project config: %w", err)
	}

	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse project config: %w", err)
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
