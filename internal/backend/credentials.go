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
	"net/http"
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
	// SourceLocalStack is a stack running on this machine, answering with the
	// key it generated for itself. Nothing was copied to make this work.
	SourceLocalStack CredentialSource = "this machine"
)

// AccessTokenEnv is the variable the cloud CLI already reads. Named here rather
// than typed twice: internal/auth documents it as the headless path, and this
// resolver must agree with it exactly or an agent would be signed in for some
// verbs and not others.
const AccessTokenEnv = "PALBASE_ACCESS_TOKEN"

// ErrNoCredential is the refusal, kept as a sentinel so callers can add their
// own context without matching on wording.
var ErrNoCredential = errors.New("no credential for this project")

// Kind is WHAT a credential is, because a project accepts its two credentials in
// two different headers and neither works in the other's place.
//
// A person's token is a Bearer. The operator's secret key is an `apikey`, and
// sending it as a Bearer gets "this stack did not issue that token" — which is
// true and useless, because the caller was holding a credential the stack
// accepts and presenting it the one way it does not. So the kind travels with
// the value instead of being guessed from its shape at each call site.
type Kind string

const (
	// KindPerson is a token from the project's own auth module, carrying the
	// management claim. What `palbase login` and the panel's sign-in produce.
	KindPerson Kind = "person"
	// KindKey is the project's secret key — what `palbase start` writes for a
	// stack it just brought up, because there is nobody to sign in as yet.
	KindKey Kind = "key"
)

// Credentials is one identity, and how to present it.
type Credentials struct {
	Value string `json:"value"`
	Kind  Kind   `json:"kind"`
}

// Apply puts this credential on a request in the form its project accepts.
func (c Credentials) Apply(req *http.Request) {
	switch c.Kind {
	case KindKey:
		req.Header.Set("apikey", c.Value)
	default:
		req.Header.Set("Authorization", "Bearer "+c.Value)
	}
}

// Credential resolves the identity to use against one target.
//
// THE SPECIFIC CREDENTIAL BEATS THE AMBIENT ONE, which is the same precedence
// the project's own door settled on: a credential stored FOR THIS ADDRESS was
// put there deliberately, by `palbase start` or `palbase login`, while
// PALBASE_ACCESS_TOKEN is exported once and applies to everything.
//
// It used to be the other way round, and the result was silent: an agent in a
// container with a Dashboard token exported ran `palbase start`, start wrote the
// stack's own key for that address, and nothing used it — every call carried the
// PAT as a Bearer, the stack answered "this stack did not issue that token", and
// the refusal advised running `palbase start`, which they had just done.
func Credential(url string) (cred Credentials, source CredentialSource, err error) {
	// A STACK ON THIS MACHINE ANSWERS FOR ITSELF, from the one file that already
	// holds its key.
	//
	// `palbase start` used to COPY that key into the credential store, which made
	// a second copy of a secret this design otherwise refuses to duplicate — the
	// same reason there is no .env and no `secret pull`. Worse, a copy has to be
	// kept in step: `stop` left it behind, and it survived a `--reset` that gave
	// the stack a new key. The state directory is the original; reading it is
	// one line and cannot go stale.
	if key, ok := localStackKey(url); ok {
		return Credentials{Value: key, Kind: KindKey}, SourceLocalStack, nil
	}

	stored, err := readCredential(url)
	if err != nil {
		return Credentials{}, "", err
	}
	if stored.Value != "" {
		return stored, SourceStore, nil
	}
	// A Dashboard-issued PAT is a person's, which is why this needs no kind: the
	// variable exists for the headless case, and there is no headless key.
	if v := strings.TrimSpace(os.Getenv(AccessTokenEnv)); v != "" {
		return Credentials{Value: v, Kind: KindPerson}, SourceEnv, nil
	}
	return Credentials{}, "", fmt.Errorf(
		"%w: %s.\nFor a project on this machine, run `palbase start` — it writes the credential for you.\n"+
			"For a cloud project, run `palbase login`, or set %s to a Dashboard-issued token",
		ErrNoCredential, url, AccessTokenEnv)
}

// StoreCredential records an identity for one target.
//
// The write is ATOMIC and the read-modify-write is done under a lock, because a
// developer machine runs more than one of these at a time: two agents in two
// panes, or a `start` finishing while a `link` writes. A torn credentials file
// signs you out of everything at once.
func StoreCredential(url string, cred Credentials) error {
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

	store := credentials{Credentials: map[string]Credentials{}}
	if raw, readErr := os.ReadFile(path); readErr == nil {
		_ = json.Unmarshal(raw, &store)
		if store.Credentials == nil {
			store.Credentials = map[string]Credentials{}
		}
	}
	store.Credentials[url] = cred

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
	delete(store.Credentials, url)

	blob, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(blob, '\n'), 0o600)
}

func readCredential(url string) (Credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return Credentials{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, nil
	}
	if err != nil {
		return Credentials{}, err
	}
	var store credentials
	if err := json.Unmarshal(raw, &store); err != nil {
		return Credentials{}, fmt.Errorf("read %s: %w", path, err)
	}
	return store.Credentials[url], nil
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

// localStackKey answers with the secret key of a stack running on this machine,
// found by ADDRESS in the register `palbase start` keeps.
//
// The register maps a project group to a URL; the key lives in that group's own
// state directory, written once by the stack's `--init-env`. So the CLI reads
// what the containers read, and a `--reset` that regenerates the keys is picked
// up on the next call rather than leaving a stale copy behind.
func localStackKey(url string) (string, bool) {
	group, ok := groupOfLocalStack(url)
	if !ok {
		return "", false
	}
	dir, err := stackStateDir(group)
	if err != nil {
		return "", false
	}
	key, err := valueFromEnvFile(filepath.Join(dir, ".env"), "PALBASE_SERVICE_ROLE_KEY")
	if err != nil || strings.TrimSpace(key) == "" {
		return "", false
	}
	return key, true
}
