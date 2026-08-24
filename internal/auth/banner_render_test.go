package auth

import (
	"os"
	"testing"
)

// Renders the sign-in exactly as a person sees it, so a change to the wording is
// reviewed as WORDING and not as a diff of format strings.
func TestRenderSignInForReview(t *testing.T) {
	if os.Getenv("PALBASE_RENDER_BANNER") == "" {
		t.Skip("set PALBASE_RENDER_BANNER=1 to print the sign-in as a person sees it")
	}
	printDeployment(os.Stdout, "dev", "https://api.v2.palbase.studio")
	printSignInBanner(os.Stdout,
		"https://app.v2.palbase.studio/auth/login?auth_request_id=ar_01a031ae-4aea-7964-bc46-03530c6242a4", false)
}
