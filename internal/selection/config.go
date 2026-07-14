// Package selection owns the CLI's ONE local context: which Project and which
// Environment the current directory acts on.
//
// The canonical local config is `.palbase/config.json` v2 (spec §4):
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
// A Git branch is never a runtime selector. The old v1 file's bare `ref` and
// `default_env` are gone: `ref` was ambiguous (it named a "project" that was
// really an Environment) and `default_env` was the Palbase-branch selector the
// cutover removed. Migrate reads a v1 file once and rewrites it in place.
package selection

import (
	"encoding/json"
	"fmt"
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

// Config is `.palbase/config.json`.
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

// legacyConfig is the v1 file this CLI no longer writes. It is read ONLY by
// Migrate, which resolves its bare `ref` (an endpoint ref — what is now an
// Environment ref) into {project_id, environment_id} and rewrites the file.
type legacyConfig struct {
	Version      int    `json:"version"`
	Ref          string `json:"ref"`
	DefaultEnv   string `json:"default_env"`
	Mode         string `json:"mode"`
	GithubRepo   string `json:"github_repo"`
	IOSAppID     string `json:"ios_app_id"`
	MacOSAppID   string `json:"macos_app_id"`
	WebAppID     string `json:"web_app_id"`
	AndroidAppID string `json:"android_app_id"`
}

// IsLegacy reports whether raw JSON is a v1 config (no version, and a bare
// `ref`). A v2 file always carries `"version": 2`.
func (l legacyConfig) IsLegacy() bool { return l.Version < Version && l.Ref != "" }

// ConfigPath is `<dir>/.palbase/config.json`. dir "" means the cwd.
func ConfigPath(dir string) string {
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, ".palbase", "config.json")
}

// ErrNotSelected is returned when the cwd has no usable config and no
// --project override was passed. Callers branch on it with errors.Is to decide
// between prompting and failing loudly.
type ErrNotSelected struct{}

func (ErrNotSelected) Error() string {
	return "no project selected — run `palbase project use <projectId>` in this directory, or pass --project"
}

// Load reads the v2 config from dir. A missing file yields ErrNotSelected. A v1
// file yields ErrLegacyConfig so the caller can migrate it (Resolver does this
// automatically).
func Load(dir string) (*Config, error) {
	data, err := os.ReadFile(ConfigPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotSelected{}
		}
		return nil, fmt.Errorf("read %s: %w", ConfigPath(dir), err)
	}
	var legacy legacyConfig
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ConfigPath(dir), err)
	}
	if legacy.IsLegacy() {
		return nil, &ErrLegacyConfig{Dir: dir, Ref: legacy.Ref, legacy: legacy}
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ConfigPath(dir), err)
	}
	if cfg.Version != Version {
		return nil, fmt.Errorf("%s has version %d — this CLI writes version %d; delete it and run `palbase project use <projectId>`",
			ConfigPath(dir), cfg.Version, Version)
	}
	if cfg.ProjectID == "" {
		return nil, ErrNotSelected{}
	}
	return &cfg, nil
}

// ErrLegacyConfig marks a v1 `.palbase/config.json` found on disk. It carries
// the bare ref so Resolver can resolve it to {project_id, environment_id}.
type ErrLegacyConfig struct {
	Dir string
	Ref string

	legacy legacyConfig
}

func (e *ErrLegacyConfig) Error() string {
	return fmt.Sprintf("%s is a v1 config (bare ref %q) — it needs migrating to v2", ConfigPath(e.Dir), e.Ref)
}

// Save writes the config to <dir>/.palbase/config.json, always stamping the
// current Version so a hand-edited file can never claim to be something else.
func Save(dir string, cfg *Config) error {
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
	const entry = ".palbase/config.json"

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
