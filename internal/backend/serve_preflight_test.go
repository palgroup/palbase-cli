package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// branchPreflightError is the pure status→guidance mapping that decides whether
// `palbase serve` can back local dev with a branch's deployed stack.
func TestBranchPreflightError(t *testing.T) {
	tests := []struct {
		name      string
		branch    string
		found     *servedBranch
		wantErr   bool
		wantPhras string // substring the message must contain (when wantErr)
	}{
		{
			name:      "missing branch → push first",
			branch:    "qa",
			found:     nil,
			wantErr:   true,
			wantPhras: "git push origin qa",
		},
		{
			name:      "missing branch suggests create",
			branch:    "qa",
			found:     nil,
			wantErr:   true,
			wantPhras: "palbase branch create qa",
		},
		{
			name:    "active → ok",
			branch:  "main",
			found:   &servedBranch{Name: "main", Status: "active"},
			wantErr: false,
		},
		{
			name:    "empty status → ok (tolerant)",
			branch:  "main",
			found:   &servedBranch{Name: "main", Status: ""},
			wantErr: false,
		},
		{
			name:      "creating → wait",
			branch:    "qa",
			found:     &servedBranch{Name: "qa", Status: "creating"},
			wantErr:   true,
			wantPhras: "still provisioning",
		},
		{
			name:      "hibernated → wake",
			branch:    "qa",
			found:     &servedBranch{Name: "qa", Status: "hibernated"},
			wantErr:   true,
			wantPhras: "palbase branch wake qa",
		},
		{
			name:      "paused → wake",
			branch:    "qa",
			found:     &servedBranch{Name: "qa", Status: "paused"},
			wantErr:   true,
			wantPhras: "wake",
		},
		{
			name:      "deleted → recreate",
			branch:    "qa",
			found:     &servedBranch{Name: "qa", Status: "deleted"},
			wantErr:   true,
			wantPhras: "palbase branch create qa",
		},
		{
			name:    "unknown status → serve anyway (no error)",
			branch:  "qa",
			found:   &servedBranch{Name: "qa", Status: "weird"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := branchPreflightError(tt.branch, tt.found)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil")
				}
				if tt.wantPhras != "" && !strings.Contains(err.Error(), tt.wantPhras) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantPhras)
				}
			} else if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
		})
	}
}
