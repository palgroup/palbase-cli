// Package selection owns the CLI's ONE local context: which Project and which
// Environment the current directory acts on.
//
// The canonical local selection is `.palbase/selection.json` (spec §4):
//
//	{
//	  "version": 2,
//	  "project_id": "proj_...",
//	  "environment_id": "env_...",
//	  "repository_provider": "github"
//	}
//
// Organization is deliberately NOT a CLI context (spec §7.3): a selected
// project_id resolves its Organization server-side, so there is no
// `organization_id` here, no `palbase org`, and no `--organization`.
//
// A Git branch is never a runtime selector. Files using any other schema are
// rejected and must be replaced by linking this checkout again.
package selection

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Version is the only config schema the CLI writes or accepts.
const Version = 2

// Repository providers (spec §4). `palbase` is the platform-managed provider
// (tarball deploy today); `github` deploys on push via webhook.
const (
	ProviderPalbase = "palbase"
	ProviderGitHub  = "github"
)

// Config is `.palbase/selection.json`.
//
// The four canonical fields carry the selection. The `*_app_id` slots are local
// wiring for `palbase ios|macos|android|web link`: they remember WHICH app
// registration this checkout owns so a second `link` re-targets it instead of
// registering a duplicate. They are neither Organization context, tier, a bare
// ref, a branch selector, nor a credential.
type Config struct {
	Version            int    `json:"version"`
	ProjectID          string `json:"project_id"`
	EnvironmentID      string `json:"environment_id"`
	RepositoryProvider string `json:"repository_provider,omitempty"`

	IOSAppID     string `json:"ios_app_id,omitempty"`
	MacOSAppID   string `json:"macos_app_id,omitempty"`
	WebAppID     string `json:"web_app_id,omitempty"`
	AndroidAppID string `json:"android_app_id,omitempty"`
}

// ConfigPath is `<dir>/.palbase/selection.json`. dir "" means the cwd.
//
// NOT config.json, and the rename is a collision rather than a preference: the
// build writes the EVALUATED project configuration — the flags, buckets and
// notification providers config/*.ts declares — to `.palbase/config.json`,
// because that is the document a push ships and the stack applies. This file is
// something else entirely: which cloud project and environment this checkout has
// selected. Two writers, one path, and the reader here uses
// DisallowUnknownFields, so a single `palbase build` made every selection-bound
// command fail with `parse .palbase/config.json: json: unknown field "auth"` —
// measured on 2026-08-17 with `palbase test-user list`.
func ConfigPath(dir string) string {
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, ".palbase", "selection.json")
}

// ErrNotSelected is returned when the cwd has no config and no --project
// override was passed. Callers branch on it with errors.As to decide between
// prompting and failing loudly.
type ErrNotSelected struct{}

func (ErrNotSelected) Error() string {
	return "no project selected — run `palbase link <project>` in this directory, or pass --project/--environment"
}

// Load reads the current config from dir. A missing file yields ErrNotSelected.
// Unknown fields and unsupported schema versions are rejected.
func Load(dir string) (*Config, error) {
	data, err := os.ReadFile(ConfigPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotSelected{}
		}
		return nil, fmt.Errorf("read %s: %w", ConfigPath(dir), err)
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w — run `palbase link <project>` again", ConfigPath(dir), err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("parse %s: expected one JSON object — run `palbase link <project>` again", ConfigPath(dir))
	}
	if cfg.Version != Version {
		return nil, fmt.Errorf("%s has unsupported config version %d (expected %d) — run `palbase link <project>` again",
			ConfigPath(dir), cfg.Version, Version)
	}
	if cfg.ProjectID == "" || cfg.EnvironmentID == "" {
		return nil, fmt.Errorf("%s must contain project_id and environment_id — run `palbase link <project>` again", ConfigPath(dir))
	}
	if err := validateRepositoryProvider(cfg.RepositoryProvider); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ConfigPath(dir), err)
	}
	return &cfg, nil
}

// Save writes the selection to <dir>/.palbase/selection.json, always stamping the
// current Version so a hand-edited file can never claim to be something else.
func Save(dir string, cfg *Config) error {
	if cfg.ProjectID == "" || cfg.EnvironmentID == "" {
		return fmt.Errorf("config must contain project_id and environment_id")
	}
	if err := validateRepositoryProvider(cfg.RepositoryProvider); err != nil {
		return err
	}
	cfg.Version = Version
	palbaseDir := filepath.Dir(ConfigPath(dir))
	if err := os.MkdirAll(palbaseDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", palbaseDir, err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(ConfigPath(dir), append(data, '\n'), 0o644)
}

func validateRepositoryProvider(provider string) error {
	switch provider {
	case "", ProviderPalbase, ProviderGitHub:
		return nil
	default:
		return fmt.Errorf("repository_provider must be %q or %q (got %q)", ProviderPalbase, ProviderGitHub, provider)
	}
}

// ApplySelection atomically moves a local config to one server-resolved
// Project/Environment. App registrations cannot cross the Project boundary.
func ApplySelection(cfg *Config, sel Selection) {
	if cfg.ProjectID != sel.ProjectID {
		cfg.IOSAppID, cfg.MacOSAppID, cfg.WebAppID, cfg.AndroidAppID = "", "", "", ""
	}
	cfg.ProjectID = sel.ProjectID
	cfg.EnvironmentID = sel.Environment.ID
	cfg.RepositoryProvider = sel.RepositoryProvider
}

// AppID returns the locally-remembered app registration for a platform slot.
func (c *Config) AppID(platform string) string {
	switch platform {
	case "ios":
		return c.IOSAppID
	case "macos":
		return c.MacOSAppID
	case "web":
		return c.WebAppID
	case "android":
		return c.AndroidAppID
	}
	return ""
}

// SetAppID records one platform's app registration, leaving every sibling slot
// intact (iOS, macOS, Android and web each own an independent registration).
func (c *Config) SetAppID(platform, appID string) error {
	switch platform {
	case "ios":
		c.IOSAppID = appID
	case "macos":
		c.MacOSAppID = appID
	case "web":
		c.WebAppID = appID
	case "android":
		c.AndroidAppID = appID
	default:
		return fmt.Errorf("unsupported app slot %q", platform)
	}
	return nil
}

// EnsureGitignored keeps the per-machine selection out of git while leaving
// generated artifacts under .palbase (openapi.json, platform configs)
// trackable. An existing directory-wide `.palbase` rule is narrowed in place.
func EnsureGitignored(path string) error {
	const entry = ".palbase/selection.json"

	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(content), "\n")
	normalized := make([]string, 0, len(lines)+1)
	found := false
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case entry, ".palbase", ".palbase/":
			if !found {
				normalized = append(normalized, entry)
				found = true
			}
		default:
			normalized = append(normalized, line)
		}
	}

	updated := strings.Join(normalized, "\n")
	if !found {
		if updated != "" && !strings.HasSuffix(updated, "\n") {
			updated += "\n"
		}
		updated += entry + "\n"
	}
	if updated == string(content) {
		return nil
	}

	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat %s: %w", path, statErr)
	}
	return os.WriteFile(path, []byte(updated), mode)
}
