package backend

// target.go — the stack this checkout talks to, and the credential for it.
//
// The CLI is TARGET-RELATIVE: a command that touches one tenant works against
// whatever it was pointed at, and only who authenticates changes
// (design-management-api.md §10). So two things have to be written down — where
// the target is, and who you are to it — and they belong in different places.
//
// The TARGET is a fact about the project: it goes in `.palbase/target.json`
// beside the rest of the checkout's committed configuration, so a colleague who
// clones the repository pushes to the same stack without being told.
//
// The CREDENTIAL is a fact about the person: it goes under the user's home
// directory, keyed by target, and never near the repository. A token committed
// by accident is a token in every clone and every CI log.
import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Target is what `palbase link` writes and `login`/`push` read.
type Target struct {
	// URL is the stack's address. Its presence is what makes this a DIRECT
	// target — nothing resolves it, and no control plane is asked.
	URL string `json:"url"`
	// AnonKey is the stack's PUBLISHABLE key. It is recorded because the auth
	// routes are the customer contract and expect it on every call — signing in
	// is something an app does, and the CLI signs in the same way. It is not a
	// secret: it opens only what row-level security lets an anonymous caller
	// reach, which is why it may sit in a committed file.
	//
	// The management routes take the opposite view and want ONLY the person's
	// token: when a key is present the stack mints the identity from it, and a
	// person is invisible.
	AnonKey string `json:"anon_key,omitempty"`
	// Insecure records that this stack still serves the certificate its first
	// boot generated. Remembered rather than retyped, because a flag somebody
	// has to repeat is a flag they will eventually paste at the wrong stack.
	Insecure bool `json:"insecure,omitempty"`
}

func targetPath() string { return filepath.Join(nativeArtifactsDir, "target.json") }

// WriteTarget records the stack this checkout talks to.
func WriteTarget(t Target) error {
	if err := os.MkdirAll(nativeArtifactsDir, 0o755); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(targetPath(), append(blob, '\n'), 0o644)
}

// ReadTarget returns the linked stack, or an error naming the command that
// would fix it. A tool that says "not linked" without saying how to link is a
// tool that sends people to the documentation for one line.
func ReadTarget() (Target, error) {
	raw, err := os.ReadFile(targetPath())
	if errors.Is(err, os.ErrNotExist) {
		return Target{}, errors.New(
			"this checkout is not linked to a stack — run `palbase link https://your-stack` first")
	}
	if err != nil {
		return Target{}, err
	}
	var t Target
	if err := json.Unmarshal(raw, &t); err != nil {
		return Target{}, fmt.Errorf("read %s: %w", targetPath(), err)
	}
	if strings.TrimSpace(t.URL) == "" {
		return Target{}, fmt.Errorf("%s names no stack — run `palbase link` again", targetPath())
	}
	return t, nil
}

// credentials is the whole store: tokens by target URL.
type credentials struct {
	Tokens map[string]string `json:"tokens"`
}

func credentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".palbase", "credentials.json"), nil
}

// SaveToken records the access token for one target.
func SaveToken(url, token string) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	store := credentials{Tokens: map[string]string{}}
	if raw, err := os.ReadFile(path); err == nil {
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
	// 0600: this file is the ability to deploy to every stack in it.
	return os.WriteFile(path, append(blob, '\n'), 0o600)
}

// LoadToken returns the token for a target, or an error naming what to run.
func LoadToken(url string) (string, error) {
	path, err := credentialsPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("not signed in to %s — run `palbase login`", url)
	}
	if err != nil {
		return "", err
	}
	var store credentials
	if err := json.Unmarshal(raw, &store); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	token := store.Tokens[url]
	if token == "" {
		return "", fmt.Errorf("not signed in to %s — run `palbase login`", url)
	}
	return token, nil
}
