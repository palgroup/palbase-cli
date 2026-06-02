package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/studio"
)

// checkoutStudio is a Studio mock for `mobile checkout`: apikey.reveal
// (the codegen leg) fails with the configurable status, so the test can
// drive checkout's failure-rollback path.
type checkoutStudio struct {
	revealStatus int // 0 → 200 OK; else error status for apikey.reveal
}

func (cs *checkoutStudio) resolvers(t *testing.T) Resolvers {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cs.revealStatus != 0 {
			http.Error(w, `{"error":{"json":{"message":"No endpoint_ref for project ref","code":-32004,"data":{"code":"NOT_FOUND","httpStatus":404}}}}`, cs.revealStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{
			"endpointRef":    "erkut1230qe6um",
			"publishableKey": "pb_erkut1230qe6um_ctest",
			"keys":           []any{},
		}}}})
	}))
	t.Cleanup(srv.Close)
	c := studio.New(srv.URL, func(_ context.Context) (string, error) { return "tok", nil })
	return Resolvers{
		Studio:    func() *studio.Client { return c },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	}
}

// TestMobileCheckout_RollsBackOnCodegenFailure: when the new branch's
// codegen fails (e.g. the branch doesn't exist → apikey.reveal 404),
// `mobile checkout` must leave .palbase/config.json on the ORIGINAL
// branch — not strand the link on a broken branch where every later
// command fails. Mirrors git checkout: a failed switch is a no-op.
func TestMobileCheckout_RollsBackOnCodegenFailure(t *testing.T) {
	chdirLinked(t, "erkut1230qe6u") // seeds default_env=main

	cs := &checkoutStudio{revealStatus: http.StatusNotFound}
	cmd := newMobileCheckoutCmd(cs.resolvers(t))
	cmd.SetArgs([]string{"qa"})
	require.Error(t, cmd.Execute(), "checkout to a branch whose codegen fails must return an error")

	cfg, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "main", cfg.DefaultEnv,
		"failed checkout must roll back to the original branch, not strand config on the broken one")
}

// (The success path — checkout persists the new branch + regenerates —
// is covered by live smoke; a unit test can't fully mock the codegen
// OpenAPI fetch, which goes to the resolved endpoint host. This file
// pins the rollback invariant, the part that had the bug.)
