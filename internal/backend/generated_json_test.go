package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteGeneratedConfigJSON_NoOAuth pins the JSON shape projects
// without OAuth providers see — `oauth` block must be omitted, not
// emitted as an empty object. The SDK reads this with key-presence
// checks; an empty `{}` would still trigger the oauth-loaded path
// and the SDK would fall through to "configured providers: none".
func TestWriteGeneratedConfigJSON_NoOAuth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "PalbaseGenerated.json")
	cfg := swiftGeneratedConfig{
		URL:    "https://abc.dev.palbase.studio",
		APIKey: "pb_abc_cXXXX",
		Branch: "main",
	}
	if err := writeGeneratedConfigJSON(path, cfg); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["oauth"]; ok {
		t.Errorf("expected no `oauth` key when cfg.OAuth is nil, got: %v", out["oauth"])
	}
	if _, ok := out["source"]; ok {
		t.Errorf("PalbaseGenerated.json must not carry a 'source' key — runtime routes by url alone")
	}
}

// TestWriteGeneratedConfigJSON_OAuth pins the JSON shape projects
// with Apple + Google providers see — Apple is `{enabled: true}` only,
// Google carries client_id + redirect_uri. Wire keys are snake_case
// because Swift's .convertFromSnakeCase decoder handles them.
func TestWriteGeneratedConfigJSON_OAuth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "PalbaseGenerated.json")
	cfg := swiftGeneratedConfig{
		URL:    "https://abc.dev.palbase.studio",
		APIKey: "pb_abc_cXXXX",
		Branch: "main",
		OAuth: &swiftOAuthConfig{
			Apple: &swiftOAuthApple{Enabled: true},
			Google: &swiftOAuthGoogle{
				Enabled:     true,
				ClientID:    "1234567890-abc.apps.googleusercontent.com",
				RedirectURI: "com.googleusercontent.apps.1234567890-abc:/oauthredirect",
			},
		},
	}
	if err := writeGeneratedConfigJSON(path, cfg); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	oauth, ok := out["oauth"].(map[string]any)
	if !ok {
		t.Fatalf("expected `oauth` block, got %v", out["oauth"])
	}
	apple, ok := oauth["apple"].(map[string]any)
	if !ok || apple["enabled"] != true {
		t.Errorf("apple shape wrong: %v", oauth["apple"])
	}
	google, ok := oauth["google"].(map[string]any)
	if !ok {
		t.Fatalf("google missing: %v", oauth["google"])
	}
	if google["enabled"] != true {
		t.Errorf("google.enabled wrong: %v", google["enabled"])
	}
	if google["client_id"] != "1234567890-abc.apps.googleusercontent.com" {
		t.Errorf("google.client_id wrong: %v", google["client_id"])
	}
	if google["redirect_uri"] != "com.googleusercontent.apps.1234567890-abc:/oauthredirect" {
		t.Errorf("google.redirect_uri wrong: %v", google["redirect_uri"])
	}
}

// TestGoogleRedirectURIFromClientID pins the conversion the CLI does
// so customer apps don't have to type the reversed-DNS callback URL.
func TestGoogleRedirectURIFromClientID(t *testing.T) {
	tests := []struct {
		clientID string
		want     string
	}{
		{
			clientID: "1234567890-abc.apps.googleusercontent.com",
			want:     "com.googleusercontent.apps.1234567890-abc:/oauthredirect",
		},
		{
			clientID: "123.apps.googleusercontent.com",
			want:     "com.googleusercontent.apps.123:/oauthredirect",
		},
		{
			// Non-standard (no suffix): fall back to raw client_id —
			// SDK error message will surface the mismatch clearly.
			clientID: "weird-client-id",
			want:     "com.googleusercontent.apps.weird-client-id:/oauthredirect",
		},
	}
	for _, tt := range tests {
		got := googleRedirectURIFromClientID(tt.clientID)
		if got != tt.want {
			t.Errorf("googleRedirectURIFromClientID(%q) = %q, want %q", tt.clientID, got, tt.want)
		}
	}
}
