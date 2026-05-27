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
	"errors"
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

// --- Faz 2: push (single module, idempotent) -------------------------
//
// `palbase backend config push` applies a local config/<module>.toml to
// the server via that module's SET tRPC. Only flags implements it this
// phase (it has a working userFlags.system.put); auth/storage/documents
// have no SET path yet, so they don't satisfy [ModulePusher] and report
// [ErrPushNotImplemented] (Faz 3).
//
// Push is UPSERT-only: it sets every entry present in the local TOML. A
// flag that exists on the server but is ABSENT from the local file is
// NOT deleted — destructive sync (delete orphans) is deferred to Faz 3
// (atomic activation + explicit confirmation), because a missing/wrong
// TOML must never silently drop a live production flag. The orphan keys
// are surfaced as a warning instead.
//
// # state_version conflict (client-side hash-compare)
//
// There is NO server-side state_version GET/SET yet (Faz 1 noted this;
// true server-side versioning is Faz 3 — needs a control-pg migration +
// Studio change). Faz 2 delivers the conflict CONTRACT without a server
// change via option 1: before applying, push re-pulls the module's
// current server state, hashes the serialized bytes, and compares to the
// hash stored in .palbase/state.json at the last pull. If the server
// state changed since that pull (someone edited via the dashboard) the
// stored hash won't match → CONFLICT, push is rejected with
// [ErrStateConflict] and NO SET call is made. The user re-pulls to
// reconcile. This is real drift protection with zero server changes.

// ErrStateConflict is returned by [Push] when the module's current server
// state no longer matches the hash recorded in .palbase/state.json at the
// last pull — i.e. someone changed config out-of-band (dashboard) since
// the local mirror was taken. The push is rejected before any SET call.
var ErrStateConflict = errors.New("remote config changed since last pull; run `palbase backend config pull` to reconcile, then re-apply")

// ErrPushNotImplemented is returned by [Push] for a module that has a
// pull serializer but no push support yet (auth/storage/documents — Faz
// 3). It is a sentinel so the command layer can distinguish "not yet
// supported" from a real failure.
var ErrPushNotImplemented = errors.New("push not implemented for this module yet (Faz 3)")

// ModulePusher applies a local config/<module>.toml to the server. Only
// modules with a working SET tRPC implement it (flags this phase). It is
// SEPARATE from [ModuleSerializer] on purpose: the Faz 1 pull serializers
// (auth/storage/documents) stay untouched and simply don't satisfy this
// interface, so [Push] reports [ErrPushNotImplemented] for them rather
// than forcing every serializer to carry a no-op Push.
//
// Implementations MUST be idempotent: pushing a TOML that already matches
// the server makes ZERO SET calls. They diff the parsed local file against
// the current server state and only SET what changed.
type ModulePusher interface {
	ModuleSerializer

	// Push applies tomlBytes (the on-disk config/<module>.toml) to the
	// server for ref. It diffs against current server state and issues a
	// SET only for changed/new entries; unchanged input → no calls. It
	// returns the number of entries it set and any server-orphan keys
	// (present server-side, absent locally) it intentionally did NOT
	// delete (Faz 2 is upsert-only). It does NOT perform the conflict
	// check — [Push] owns that uniformly across modules.
	Push(ctx context.Context, ref string, sc *studio.Client, tomlBytes []byte) (PushApplied, error)
}

// PushApplied reports what a [ModulePusher.Push] changed, for the caller's
// log output. Set is the count of entries written; Orphans are server-side
// keys absent from the local file that were left untouched (upsert-only);
// Ignored lists entries whose local definition carries fields the SET API
// can't write (e.g. flag variants — see flagsSerializer.Push) so the user
// knows part of the file did not round-trip.
type PushApplied struct {
	Set     int
	Orphans []string
	Ignored []string
}

// PushResult is the command-layer outcome of pushing one module.
type PushResult struct {
	Module   string
	Filename string
	PushApplied
}

// pusherFor returns the registered serializer for module name if it also
// implements [ModulePusher]. ok is false when the module is unknown or has
// no push support (the latter being the Faz 3 case).
func pusherFor(name string) (ModulePusher, bool) {
	for _, s := range Serializers() {
		if s.Name() != name {
			continue
		}
		p, ok := s.(ModulePusher)
		return p, ok
	}
	return nil, false
}

// Push applies <projectDir>/config/<module>.toml to the server for ref,
// gated by client-side state_version conflict detection, and refreshes
// the module's hash in .palbase/state.json on success.
//
// Flow (see the package's Faz 2 comment for the contract):
//  1. Resolve the module's pusher; unknown/unsupported → ErrPushNotImplemented.
//  2. Load .palbase/state.json (absent → fresh state).
//  3. CONFLICT CHECK: re-pull the module's current server state, hash the
//     serialized bytes, compare to state.Modules[module].Hash. A stored
//     hash that differs (or, defensively, an absent baseline when the
//     server is non-empty) → ErrStateConflict, no SET call.
//  4. APPLY: read the local TOML and delegate to the pusher, which diffs
//     vs the server and SETs only changes (idempotent: no change → no call).
//  5. Refresh state.json: re-pull post-apply, hash, store under the module
//     key (preserving other modules' hashes).
func Push(ctx context.Context, projectDir, ref, module string, sc *studio.Client) (PushResult, error) {
	pusher, ok := pusherFor(module)
	if !ok {
		// Distinguish "no such module" from "module exists but no push yet".
		for _, s := range Serializers() {
			if s.Name() == module {
				return PushResult{}, fmt.Errorf("%s: %w", module, ErrPushNotImplemented)
			}
		}
		return PushResult{}, fmt.Errorf("unknown module %q", module)
	}

	statePath := filepath.Join(projectDir, StateFile)
	state, err := loadState(statePath)
	if err != nil {
		return PushResult{}, err
	}

	// Conflict check: hash the server's current serialized state and
	// compare to the last-pull hash. Reusing the pull serializer means the
	// bytes are produced identically to what `config pull` stored, so the
	// hashes are directly comparable.
	serverBytes, err := pusher.Pull(ctx, ref, sc)
	if err != nil {
		return PushResult{}, fmt.Errorf("read current server state: %w", err)
	}
	serverHash := hashContent(serverBytes)
	baseline, haveBaseline := state.Modules[module]
	if !haveBaseline {
		// No last-pull baseline for this module. We cannot prove the local
		// file reflects current server state, so refuse rather than risk
		// overwriting a dashboard change we never saw.
		return PushResult{}, fmt.Errorf("%w (no baseline in %s for %q — pull first)", ErrStateConflict, StateFile, module)
	}
	if baseline.Hash != serverHash {
		return PushResult{}, ErrStateConflict
	}

	// Apply: read the local TOML and delegate the diff+SET to the module.
	localPath := filepath.Join(projectDir, ConfigDir, pusher.Filename())
	localBytes, err := os.ReadFile(localPath)
	if err != nil {
		return PushResult{}, fmt.Errorf("read %s: %w", localPath, err)
	}
	applied, err := pusher.Push(ctx, ref, sc, localBytes)
	if err != nil {
		return PushResult{}, fmt.Errorf("push %s: %w", module, err)
	}

	// Refresh state.json with the post-apply server hash so a subsequent
	// push sees an up-to-date baseline. Re-pull rather than hashing the
	// local file: the server is the source of truth and may normalise
	// values (e.g. number formatting), so the stored hash must reflect
	// what the server actually holds.
	newBytes, err := pusher.Pull(ctx, ref, sc)
	if err != nil {
		return PushResult{}, fmt.Errorf("refresh server state after push: %w", err)
	}
	state.Modules[module] = ModuleState{Hash: hashContent(newBytes)}
	if err := writeState(statePath, state); err != nil {
		return PushResult{}, err
	}

	return PushResult{Module: module, Filename: pusher.Filename(), PushApplied: applied}, nil
}

// writeState marshals state and writes it to path, creating the parent
// directory. Shared by Push (and mirrors the inline write in Pull).
func writeState(statePath string, state *State) error {
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(statePath), err)
	}
	b, err := state.marshal()
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(statePath, b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", statePath, err)
	}
	return nil
}
