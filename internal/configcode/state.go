package configcode

import (
	"encoding/json"
	"sort"
)

// State is the on-disk `.palbase/state.json` mirror.
//
// Faz 1 (read-only) NOTE: this is a MIRROR, not a push contract yet.
// `config pull` writes it to record what the server looked like at pull
// time so a future `config diff`/`config push` (Faz 2) can detect local
// edits and carry an expected_state_version for conflict detection.
//
// StateVersion mirrors the server's global state_version once such a GET
// exists. The server does not expose one in Faz 1, so it is written as 0
// (placeholder) today.
//
// TODO(Faz2): mirror the server-side state_version once the push
// contract + state_version GET land. Until then 0 means "unknown".
type State struct {
	// StateVersion mirrors the server's state_version. 0 = unknown
	// (no server endpoint yet — Faz 1 placeholder).
	StateVersion int64 `json:"state_version"`

	// Modules maps a module name (ModuleSerializer.Name) to its pulled
	// state — currently just the content hash of the written TOML.
	Modules map[string]ModuleState `json:"modules"`
}

// ModuleState records per-module pull metadata. Hash is the
// sha256:<hex> digest of the module's serialized TOML at pull time.
type ModuleState struct {
	Hash string `json:"hash"`
}

// newState returns an empty state with an initialised module map.
func newState() *State {
	return &State{Modules: map[string]ModuleState{}}
}

// marshal renders the state to deterministic, indented JSON. Map keys
// are sorted by encoding/json automatically, so repeated pulls of an
// unchanged server produce byte-identical state files (diff-stable).
func (s *State) marshal() ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ModuleNames returns the state's module keys in sorted order. Helper
// for tests + future diff output.
func (s *State) ModuleNames() []string {
	names := make([]string, 0, len(s.Modules))
	for k := range s.Modules {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
