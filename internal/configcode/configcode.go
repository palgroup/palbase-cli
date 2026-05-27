// Package configcode implements config-as-code Faz 1: the read-only
// `palbase backend config pull`. It fetches each module's current
// configuration from Studio's tRPC GET APIs and serializes it to
// declarative TOML files under `config/` in the project directory,
// plus a `.palbase/state.json` mirror.
//
// READ-ONLY. There is no push / manifest apply in Faz 1 — see the plan
// at docs/superpowers/plans/2026-05-27-config-as-code-implementation.md
// (Faz 1 section). `config pull` produces a *mirror* of server state so
// developers can review config in git; it is NOT yet a push contract.
//
// # Layout (KİLİTLİ KARAR)
//
//	my-backend/
//	├── config/
//	│   ├── auth.toml
//	│   ├── storage.toml
//	│   ├── documents.toml
//	│   ├── flags.toml          ← reference impl (this task)
//	│   └── notifications.toml
//	└── .palbase/state.json     ← state_version + per-module hash mirror
//
// # Extension point
//
// Each module implements [ModuleSerializer] in its own file
// (flags.go, auth.go, …) and registers itself via [Register] from an
// init() function. The `config pull` command ([Pull]) iterates the
// registry, so adding a new module is a single new file + one Register
// call — no edits to shared code. flags.go is the reference impl;
// auth/storage/documents/notifications are TODO stubs the parallel
// subagents fill in.
//
// # Determinism
//
// TOML is marshaled via github.com/BurntSushi/toml, which encodes struct
// fields in declaration order and sorts map keys, so `config pull`
// produces diff-stable output across runs. Serializers MUST use structs
// (and, where a dynamic keyed collection is unavoidable, map[string]T —
// the encoder sorts those keys) rather than arbitrary `any` blobs whose
// ordering is undefined.
//
// # Secrets
//
// Secret fields serialize as the reference string `@secret/<NAME>`,
// never the actual value. flags has no secrets; auth/notifications will,
// so the convention is documented here and enforced per-serializer.
package configcode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/palgroup/palbase-cli/internal/studio"
)

// ConfigDir is the project-relative directory config-as-code files live
// in. Chosen so it does not collide with the bundler's EntryDir
// (endpoints/) or any deploy/ignore path — see the plan's collision
// check.
const ConfigDir = "config"

// StateFile is the project-relative path of the state mirror.
const StateFile = ".palbase/state.json"

// SecretRefPrefix is prepended to a secret's NAME to produce its
// reference form. A serializer must emit "@secret/GOOGLE_OAUTH_SECRET"
// in place of any real secret value so `config pull` never writes a
// plaintext secret to disk. (flags has none; relevant for
// auth/notifications.)
const SecretRefPrefix = "@secret/"

// SecretRef returns the reference form for a named secret.
func SecretRef(name string) string { return SecretRefPrefix + name }

// ModuleSerializer turns one module's live server config into a TOML
// file on disk. Each module (flags, auth, storage, documents,
// notifications) implements this in its own file and registers itself
// via [Register]. The `config pull` command iterates the registry.
//
// Implementations MUST:
//   - return deterministic bytes (struct-based encoding, no map ordering
//     surprises) so output is diff-stable;
//   - never serialize a secret value — emit [SecretRef] instead;
//   - return an empty (or header-only) document when the module has no
//     config, rather than failing, so a fresh project still pulls clean.
type ModuleSerializer interface {
	// Name is the module identifier used as the key in state.json
	// (e.g. "flags"). Stable across versions.
	Name() string

	// Filename is the config/-relative file the module writes
	// (e.g. "flags.toml").
	Filename() string

	// Pull fetches the module's config for ref via the Studio client and
	// returns its serialized TOML bytes. It does NOT write to disk — the
	// caller ([Pull]) owns file I/O so it can hash + mirror state
	// uniformly across modules.
	Pull(ctx context.Context, ref string, sc *studio.Client) ([]byte, error)
}

// registry holds every registered serializer. Populated by each module
// file's init(); read by [Pull] and [Serializers].
var registry []ModuleSerializer

// Register adds a serializer to the package registry. Called from a
// module file's init(). Panics on a duplicate Name() so a copy-paste
// slip in a parallel subagent's stub is caught at startup, not silently
// shadowed.
func Register(s ModuleSerializer) {
	for _, existing := range registry {
		if existing.Name() == s.Name() {
			panic(fmt.Sprintf("configcode: duplicate serializer %q", s.Name()))
		}
	}
	registry = append(registry, s)
}

// Serializers returns the registered serializers sorted by Name() so the
// pull order (and thus state.json + log output) is deterministic
// regardless of init() ordering.
func Serializers() []ModuleSerializer {
	out := make([]ModuleSerializer, len(registry))
	copy(out, registry)
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// PullResult reports what one serializer produced, for the caller's log
// output. Empty content (a module with no config) still writes a file
// and records a hash so drift detection in later phases is consistent.
//
// Err is non-nil when that module's pull failed (e.g. the tenant hasn't
// provisioned that module's tables yet). A per-module failure does NOT
// abort the whole pull — the other modules still write — so a project
// that uses some modules but not others (storage not initialised, etc.)
// can still snapshot the modules it does use. The command layer surfaces
// Err as a warning and exits non-zero only if EVERY module failed.
type PullResult struct {
	Module   string
	Filename string
	Bytes    int
	Err      error
}

// Pull runs every registered serializer against ref, writes each result
// to <projectDir>/config/<filename>, and writes the state mirror to
// <projectDir>/.palbase/state.json. It is the command-layer entry point.
//
// Faz 1 is read-only: state.json mirrors per-module content hashes and a
// placeholder state_version (the server exposes no state_version GET yet
// — see [State]). The function returns per-module results for the caller
// to print.
func Pull(ctx context.Context, projectDir, ref string, sc *studio.Client) ([]PullResult, error) {
	sers := Serializers()
	if len(sers) == 0 {
		return nil, fmt.Errorf("no module serializers registered")
	}

	configPath := filepath.Join(projectDir, ConfigDir)
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", configPath, err)
	}

	results := make([]PullResult, 0, len(sers))
	state := newState()
	failed := 0

	for _, s := range sers {
		body, err := s.Pull(ctx, ref, sc)
		if err != nil {
			// Per-module failure (e.g. tenant hasn't provisioned this
			// module's tables) is non-fatal: record it, skip the file,
			// keep pulling the rest. The command layer warns and only
			// errors out if every module failed.
			failed++
			results = append(results, PullResult{Module: s.Name(), Filename: s.Filename(), Err: err})
			continue
		}
		dest := filepath.Join(configPath, s.Filename())
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", dest, err)
		}
		state.Modules[s.Name()] = ModuleState{Hash: hashContent(body)}
		results = append(results, PullResult{
			Module:   s.Name(),
			Filename: s.Filename(),
			Bytes:    len(body),
		})
	}

	// Every module failed → nothing was pulled; surface as an error
	// rather than silently writing an empty state file.
	if failed == len(sers) {
		return results, fmt.Errorf("all %d modules failed to pull", failed)
	}

	statePath := filepath.Join(projectDir, StateFile)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(statePath), err)
	}
	stateBytes, err := state.marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(statePath, stateBytes, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", statePath, err)
	}

	return results, nil
}

// hashContent returns the sha256:<hex> digest of a serialized module
// file. Stored in state.json so a later `config diff`/push can detect
// whether the local file changed relative to the last pull.
func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
