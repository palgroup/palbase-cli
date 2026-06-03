package backend

import (
	"strings"
	"testing"
)

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
