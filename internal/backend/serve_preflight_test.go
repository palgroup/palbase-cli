package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/selection"
)

// backendDepMissing decides whether `palbase serve` must run `npm install`
// before bundling controllers/. Regression guard: the old flat-redesign dropped
// the auto-install with `init`, so a `git clone` + `palbase serve` left
// @palbase/backend absent and every controller silently skipped. serve must
// detect the absence (install) and trust a present dir (skip the slow install).
func TestBackendDepMissing(t *testing.T) {
	tests := []struct {
		name      string
		installed bool // node_modules/@palbase/backend present on disk
		want      bool // backendDepMissing == true → serve should install
	}{
		{name: "fresh clone, no node_modules → missing", installed: false, want: true},
		{name: "installed → present", installed: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.installed {
				pkgDir := filepath.Join(dir, "node_modules", "@palbase", "backend")
				if err := os.MkdirAll(pkgDir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(pkgDir, "package.json"),
					[]byte(`{"name":"@palbase/backend"}`), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			if got := backendDepMissing(dir); got != tt.want {
				t.Fatalf("backendDepMissing(%q) = %v, want %v", dir, got, tt.want)
			}
		})
	}
}

// devServerToolMissing decides whether `palbase serve` must install a
// dev-server runtime tool (e.g. zod-to-json-schema) the deployed br-pod ships
// globally. Missing → /openapi.json would omit request/response schemas, so
// serve installs it (--no-save).
func TestDevServerToolMissing(t *testing.T) {
	const pkg = "zod-to-json-schema"
	tests := []struct {
		name      string
		installed bool
		want      bool
	}{
		{name: "absent → missing (openapi would be bodyless)", installed: false, want: true},
		{name: "present → not missing", installed: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.installed {
				pkgDir := filepath.Join(dir, "node_modules", pkg)
				if err := os.MkdirAll(pkgDir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(pkgDir, "package.json"),
					[]byte(`{"name":"`+pkg+`"}`), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			if got := devServerToolMissing(dir, pkg); got != tt.want {
				t.Fatalf("devServerToolMissing(%q,%q) = %v, want %v", dir, pkg, got, tt.want)
			}
		})
	}
}

// preflightServeEnvironment is the pure status→guidance mapping that decides
// whether `palbase serve` can back local dev with the selected environment's
// deployed stack. It names ENVIRONMENTS — a status that used to suggest
// `palbase branch wake` now suggests `palbase env wake`, because the branch
// command no longer exists and the old copy would be a dead end.
func TestPreflightServeEnvironment(t *testing.T) {
	tests := []struct {
		name      string
		env       selection.Environment
		wantErr   bool
		wantPhras string
	}{
		{
			name: "active → ok",
			env:  selection.Environment{Slug: "production", Status: "active"},
		},
		{
			name: "empty status → ok (tolerant)",
			env:  selection.Environment{Slug: "production", Status: ""},
		},
		{
			name:      "creating → wait",
			env:       selection.Environment{Slug: "staging", Status: "creating"},
			wantErr:   true,
			wantPhras: "still provisioning",
		},
		{
			name:      "archived → wake",
			env:       selection.Environment{Slug: "staging", Status: "archived"},
			wantErr:   true,
			wantPhras: "palbase env wake staging",
		},
		{
			name:      "asleep → wake",
			env:       selection.Environment{Slug: "staging", Status: "asleep"},
			wantErr:   true,
			wantPhras: "palbase env wake staging",
		},
		{
			name:      "deleted → recreate from a SOURCE ENVIRONMENT",
			env:       selection.Environment{Slug: "staging", Status: "deleted"},
			wantErr:   true,
			wantPhras: "palbase env create staging --from production",
		},
		{
			name: "unknown status → serve anyway (visible, not blocking)",
			env:  selection.Environment{Slug: "staging", Status: "weird"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := preflightServeEnvironment(tt.env)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("preflightServeEnvironment(%+v) = %v, want nil", tt.env, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("preflightServeEnvironment(%+v) = nil, want an error", tt.env)
			}
			if !strings.Contains(err.Error(), tt.wantPhras) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantPhras)
			}
			// The retired command names must never come back in guidance copy.
			for _, dead := range []string{"palbase branch", "git push origin"} {
				if strings.Contains(err.Error(), dead) {
					t.Fatalf("preflight guidance still references the retired %q: %s", dead, err.Error())
				}
			}
		})
	}
}
