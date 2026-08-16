package backend

// stack_login.go — keeping a shipped key current.
//
// What used to live here was a second way to authenticate: sign in to a project
// with an email and password, keep the access token, throw the refresh away.
// Measured on 2026-08-16 that token lives 1800 seconds, which is why `palbase
// push` kept answering "that stack no longer accepts this session" half an hour
// into an afternoon. Identity now has ONE resolver (credentials.go) and that
// path is gone rather than patched.
//
// What remains is the other half of the same problem: the key an app SHIPS.

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// rewriteAppKeys puts a project's CURRENT publishable key into every committed
// app config in this checkout THAT POINTS AT THAT PROJECT.
//
// The key ships inside the app, so a project rebuilt from nothing — a fresh
// install, a wiped test rail — leaves every linked app holding one that no
// longer exists. The failure then lands on the device ("invalid_api_key") while
// the same person's terminal signs in perfectly, which reads as the app being
// wrong rather than out of date.
//
// baseURL is what keeps that from becoming a worse failure. A checkout can carry
// slots for more than one target — an app with a cloud past and a local present
// has both — and writing this key into a slot whose base_url is somebody else's
// produces a config that points at one host holding another's credential.
// Measured on 2026-08-16: the web slot still named a cloud environment while its
// key had been replaced with a local project's. Stale is obvious; mixed is not.
func rewriteAppKeys(baseURL, anonKey string, w io.Writer) error {
	target := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	for _, dir := range appConfigDirs() {
		path := filepath.Join(dir, "palbase-config.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var entry pullSpecConfigEntry
		if err := json.Unmarshal(raw, &entry); err != nil || entry.APIKey == anonKey {
			continue
		}
		if strings.TrimSuffix(strings.TrimSpace(entry.BaseURL), "/") != target {
			continue // another target's slot; not this project's to rewrite
		}
		entry.APIKey = anonKey
		blob, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintf(w, "✓ %s now carries this project's current key — rebuild the app\n", path)
	}
	return nil
}

// appConfigDirs is every directory a link may have written an app config into.
func appConfigDirs() []string {
	dirs := []string{webArtifactsDir}
	for _, platform := range []string{"ios", "macos", "android"} {
		dirs = append(dirs, filepath.Join(nativeArtifactsDir, platform))
	}
	return dirs
}

// stackClient talks to one project, trusting a self-signed certificate only
// when the link said to.
func stackClient(t Target) *http.Client {
	c := &http.Client{Timeout: 5 * time.Minute}
	if t.Insecure {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in at link time
	}
	return c
}
