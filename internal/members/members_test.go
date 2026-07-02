package members

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

func studioAgainst(t *testing.T, h http.HandlerFunc) Studio {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) {
		return "test-token", nil
	})
}

func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// TestMembersList_DecodesWrappedCamelCase pins the groupMembers.members wire:
// the router returns {callerRole, members:[{userId,...}]} (camelCase, WRAPPED)
// — decoding it as a bare snake_case array was a real live-smoke failure.
// Pending invitations are admin-gated; a FORBIDDEN there must NOT fail list.
func TestMembersList_DecodesWrappedCamelCase(t *testing.T) {
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/groupMembers.members":
			trpcOK(w, map[string]any{
				"callerRole": "member",
				"members": []map[string]any{
					{"userId": "usr_1", "role": "owner", "joinedAt": "2026-06-24T20:37:17Z", "displayName": nil},
				},
			})
		case "/api/trpc/groupMembers.pendingInvitations":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"json":{"message":"FORBIDDEN"}}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }})
	cmd.SetArgs([]string{"list", "--group", "grp_1"})
	require.NoError(t, cmd.Execute(), "a FORBIDDEN pending-invitations read must not fail members list")
}

// TestMembersInvite_Wire pins invite input field names + role validation.
func TestMembersInvite_Wire(t *testing.T) {
	var body map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/trpc/groupMembers.invite", r.URL.Path)
		var outer struct {
			JSON map[string]any `json:"json"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&outer))
		body = outer.JSON
		trpcOK(w, map[string]any{"id": "inv_1"})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }})
	cmd.SetArgs([]string{"invite", "dev@example.com", "--group", "grp_1", "--role", "admin"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "grp_1", body["grpId"])
	require.Equal(t, "dev@example.com", body["email"])
	require.Equal(t, "admin", body["role"])

	// Invalid role rejected client-side, no API call.
	cmd = Cmd(Resolvers{Studio: func() Studio {
		t.Fatal("must not call the API for an invalid role")
		return nil
	}})
	cmd.SetArgs([]string{"invite", "dev@example.com", "--group", "grp_1", "--role", "owner"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	require.Error(t, cmd.Execute())
}
