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
	// SourceEnv is PALBASE_ACCESS_TOKEN — a token supplied for headless use.
	SourceEnv CredentialSource = "environment"
	// SourceStore is ~/.palbase/credentials.json, keyed by target URL.
	SourceStore CredentialSource = "store"
	// SourceCloud is the control plane, answering for a project it owns. Not
	// cached: keys rotate, and a cached copy goes silently wrong the moment
	// somebody rotates from anywhere else.
	SourceCloud CredentialSource = "the cloud"
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

// keyPrefix is what every Palbase API key starts with: `pb_{ref}_{scope}{random}`.
// A Bearer token can never begin with it — a JWT starts with its base64url
// header — so the shape is a decision, not a guess.
const keyPrefix = "pb_"

// kindOf reads what a credential IS from the value itself.
//
// Shape-reading is used HERE and only here, at the one door where the kind is
// otherwise unknown: a value out of the environment arrives without provenance.
// Everywhere else the kind is recorded when the credential is stored, and that
// record wins — see Credential's ordering.
func kindOf(value string) Kind {
	if strings.HasPrefix(value, keyPrefix) {
		return KindKey
	}
	return KindPerson
}

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
	// THE KIND TRAVELS WITH THE VALUE, even out of the environment.
	//
	// This used to assume "there is no headless key" — true while the only cloud
	// was one you signed in to. A v2 project's management surface is opened by
	// that project's OWN secret key, and an agent holding one has nowhere else to
	// put it: there is no browser to sign in with and no stack on this machine to
	// read it from.
	//
	// Measured (2026-08-21, live): the same key opens
	// `https://<ref>.v2.palbase.studio/v1/management/whoami` with `apikey`, and
	// gets 401 as a Bearer. The credential was right; the PRESENTATION was wrong.
	if v := strings.TrimSpace(os.Getenv(AccessTokenEnv)); v != "" {
		return Credentials{Value: v, Kind: kindOf(v)}, SourceEnv, nil
	}

	// THE CLOUD ANSWERS FOR ITS OWN PROJECTS — the same shape as a stack on this
	// machine answering from its state directory, one step up.
	//
	// A cloud project's management surface is opened by that project's own
	// service-role key, and that key lives in the control plane's ledger. Before
	// this, a person who had signed in and linked a project still got "no
	// credential" from `status`, `deploys` and `push`: the chain was
	// login → link → BROKEN → push, and the only way through was to read a key
	// out of the database by hand.
	//
	// The fetch is gated by ownership on the server and cached here, so it costs
	// one round trip per project per machine.
	if CloudKeyFetcher != nil {
		if cred, ok := fetchCloudCredential(url); ok {
			return cred, SourceCloud, nil
		}
	}

	return Credentials{}, "", fmt.Errorf(
		// NOT "start writes the credential for you" any more: it does not write
		// one, and saying so sent people looking for a file that no longer
		// exists. A stack holds its own key in its state directory and this
		// resolver reads it from there, so the fix is to have the stack RUNNING,
		// not to have a copy of its key somewhere.
		"%w: %s.\nFor a project on this machine, `palbase start` brings its stack up — the stack holds its own key and this reads it from there.\n"+
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

// CloudKeyFetcher resolves a tenant address to that project's service-role key
// by asking the control plane, or returns an error when the caller does not own
// it (or is not signed in). Injected from main.go so this package stays off the
// auth and transport packages.
//
// Nil means "no cloud configured" — every local flow keeps working untouched.
var CloudKeyFetcher func(tenantURL string) (string, error)

// fetchCloudCredential asks the cloud for a project's key.
//
// IT IS NOT CACHED, deliberately. Caching it costs one round trip less per
// command and buys a whole class of failure: keys ROTATE, and a rotation
// performed anywhere — another machine, the dashboard, a teammate — leaves
// every cached copy silently wrong. The person then gets 401s from a CLI that
// is holding a key it believes is good, with no way to correct it short of
// deleting a file they do not know about.
//
// The control plane is the authority on a cloud project's key. Asking it each
// time is what "authority" means.
//
// A failure here is not reported as an error: the caller is about to produce a
// message that names every way to supply a credential, and "the cloud said no"
// is one reason among several — a stack on this machine, a key in the
// environment, or simply not being signed in are all still valid answers.
func fetchCloudCredential(url string) (Credentials, bool) {
	key, err := CloudKeyFetcher(url)
	if err != nil || strings.TrimSpace(key) == "" {
		return Credentials{}, false
	}
	return Credentials{Value: key, Kind: kindOf(key)}, true
}
