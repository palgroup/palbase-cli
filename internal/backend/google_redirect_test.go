package backend

import "testing"

// TestGoogleRedirectURIFromClientID pins the conversion the CLI does
// so customer apps don't have to type the reversed-DNS callback URL.
// The redirect URI rides in the per-env Palbase-Info.plist's `oauth` block
// (the SOLE config source after the JSON-config cutover).
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
