package backend

// credentials.go — ONE identity resolver for every verb that touches a project.
//
// There used to be two identity mechanisms and neither was the one that worked.
// The cloud path (internal/auth) does Authorization Code + PKCE with a
// DPoP-bound token, stores a refresh token, and reads a Dashboard-issued PAT
// from PALBASE_ACCESS_TOKEN for anything headless — which is exactly what an AI
// agent in a container needs. The project path invented its own: sign in with a
// password, keep the access token, throw the refresh away. Measured on
// 2026-08-16, that token expires in 1800 seconds, which is why `palbase push`
// kept answering "that stack no longer accepts this session" half an hour into a
// working afternoon.
//
// So: one resolver, three sources, in the order that makes an agent's life
// simplest and a person's unchanged.
//
//  1. PALBASE_ACCESS_TOKEN — headless, no file, no browser, nothing to expire on
//     a build machine mid-run.
//  2. the credential store, keyed by URL — where `palbase start` writes the
//     local stack's own key, and where the browser login lands.
//  3. nothing, and then the refusal names both ways to fix it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CredentialSource says where an identity came from. It exists so the CLI can
// tell you which one it used: a person debugging "why is it pushing there" is
// usually looking at a stale environment variable.
type CredentialSource string

const (
	// SourceEnv is PALBASE_ACCESS_TOKEN — a Dashboard-issued, DPoP-bound PAT.
	SourceEnv CredentialSource = "environment"
	// SourceStore is ~/.palbase/credentials.json, keyed by target URL.
	SourceStore CredentialSource = "store"
)

// AccessTokenEnv is the variable the cloud CLI already reads. Named here rather
// than typed twice: internal/auth documents it as the headless path, and this
// resolver must agree with it exactly or an agent would be signed in for some
// verbs and not others.
const AccessTokenEnv = "PALBASE_ACCESS_TOKEN"

// ErrNoCredential is the refusal, kept as a sentinel so callers can add their
// own context without matching on wording.
var ErrNoCredential = errors.New("no credential for this project")

// Credential resolves the identity to use against one target.
func Credential(url string) (token string, source CredentialSource, err error) {
	if v := strings.TrimSpace(os.Getenv(AccessTokenEnv)); v != "" {
		return v, SourceEnv, nil
	}
	stored, err := readCredential(url)
	if err != nil {
		return "", "", err
	}
	if stored == "" {
		return "", "", fmt.Errorf(
			"%w: %s.\nFor a project on this machine, run `palbase start` — it writes the credential for you.\n"+
				"For a cloud project, run `palbase login`, or set %s to a Dashboard-issued token",
			ErrNoCredential, url, AccessTokenEnv)
	}
	return stored, SourceStore, nil
}

// StoreCredential records an identity for one target.
//
// The write is ATOMIC and the read-modify-write is done under a lock, because a
// developer machine runs more than one of these at a time: two agents in two
// panes, or a `start` finishing while a `link` writes. A torn credentials file
// signs you out of everything at once.
func StoreCredential(url, token string) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	unlock, err := lockCredentials(path)
	if err != nil {
		return err
	}
	defer unlock()

	store := credentials{Tokens: map[string]string{}}
	if raw, readErr := os.ReadFile(path); readErr == nil {
		_ = json.Unmarshal(raw, &store)
		if store.Tokens == nil {
			store.Tokens = map[string]string{}
		}
	}
	store.Tokens[url] = token

	blob, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(blob, '\n'), 0o600)
}

// ForgetCredential removes one target's identity — `palbase logout`.
func ForgetCredential(url string) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	unlock, err := lockCredentials(path)
	if err != nil {
		return err
	}
	defer unlock()

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var store credentials
	if err := json.Unmarshal(raw, &store); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	delete(store.Tokens, url)

	blob, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(blob, '\n'), 0o600)
}

func readCredential(url string) (string, error) {
	path, err := credentialsPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var store credentials
	if err := json.Unmarshal(raw, &store); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return store.Tokens[url], nil
}

// writeFileAtomic writes through a temp file in the same directory, so a reader
// sees either the old file or the new one and never a half-written map.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
