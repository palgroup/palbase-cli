package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// deployStateFile is the per-clone record of the last code version pulled for
// each branch. It is the "base" for git-like optimistic-concurrency pushes: the
// CLI sends branch_heads[branch] as baseSHA, and the server rejects (409
// stale_base) if its HEAD has moved past it. Kept SEPARATE from
// .palbase/state.json (config-as-code module hashes) so the two don't collide.
// Gitignored per-clone, like .env.local.
const deployStateFile = ".palbase/deploy-state.json"

// deployState is the on-disk shape of deploy-state.json.
type deployState struct {
	// BranchHeads maps a branch name ("main", "qa", …) to the short SHA the
	// CLI last pulled for it. "" / missing = unknown base (no stale check).
	BranchHeads map[string]string `json:"branch_heads,omitempty"`
	PulledAt    string            `json:"pulled_at,omitempty"`
}

// loadDeployState reads deploy-state.json under dir. A missing file is not an
// error — it yields a zero-value state (no known base).
func loadDeployState(dir string) (*deployState, error) {
	b, err := os.ReadFile(filepath.Join(dir, deployStateFile))
	if err != nil {
		if os.IsNotExist(err) {
			return &deployState{BranchHeads: map[string]string{}}, nil
		}
		return nil, err
	}
	var s deployState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.BranchHeads == nil {
		s.BranchHeads = map[string]string{}
	}
	return &s, nil
}

// recordPulledVersion persists branch_heads[branch] = version after a pull, so
// the next push on that branch can declare its base. branch "" means the
// default branch; we normalise it to "main" (the server's default-branch name).
func recordPulledVersion(dir, branch, version, pulledAt string) error {
	s, err := loadDeployState(dir)
	if err != nil {
		return err
	}
	s.BranchHeads[normalizeBranch(branch)] = version
	s.PulledAt = pulledAt
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, ".palbase"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, deployStateFile), b, 0o644)
}

// baseSHAFor returns the recorded base SHA for a branch, or "" if unknown.
func baseSHAFor(dir, branch string) string {
	s, err := loadDeployState(dir)
	if err != nil {
		return ""
	}
	return s.BranchHeads[normalizeBranch(branch)]
}

func normalizeBranch(branch string) string {
	if branch == "" {
		return "main"
	}
	return branch
}
