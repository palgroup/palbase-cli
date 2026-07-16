package backend

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

func TestStudioConfigArtifactFetch_ValidatesBeforeOAuthNetwork(t *testing.T) {
	fake := selectiontest.New(t)
	fake.OK("GET /api/v2/apps/app_ios/config-artifact", map[string]any{
		"app_id": "app_ios", "environment_ref": "app1prod",
		"api_key":  "pb_app1prod_c01234567890123456789",
		"base_url": "https://app1prod.evil.palbase.studio", "platform": "ios",
	})

	oauthCalls := 0
	tenant := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		oauthCalls++
		selectiontest.WriteOK(w, http.StatusOK, map[string]any{"providers": map[string]any{}})
	})
	t.Cleanup(redirectHostTo(t, "app1prod.evil.palbase.studio", tenant))

	_, err := studioConfigArtifactFetch(fake.REST(), "dev.palbase.studio")(context.Background(), "app_ios", "app1prod")
	require.ErrorContains(t, err, "base_url")
	require.Zero(t, oauthCalls, "an untrusted artifact must not drive the OAuth network request")
}
