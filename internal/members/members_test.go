package members

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selectiontest"
	"github.com/palgroup/palbase-cli/internal/studio"
)

func studioAgainst(t *testing.T, h http.HandlerFunc) Studio {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(
		srv.URL,
		func(_ context.Context) (string, error) { return "test-token", nil },
		func(context.Context, string, string, string) (string, error) { return "test-proof", nil },
	)
}

func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

func inner(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var outer struct {
		JSON map[string]any `json:"json"`
	}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&outer))
	return outer.JSON
}

func run(t *testing.T, c Studio, args ...string) error {
	t.Helper()
	t.Chdir(t.TempDir())
	cmd := Cmd(Resolvers{
		Studio:    func() Studio { return c },
		Selection: selectiontest.Selected(t),
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	return cmd.Execute()
}

func TestMembersCmd_Subcommands(t *testing.T) {
	cmd := Cmd(Resolvers{})
	var got []string
	for _, c := range cmd.Commands() {
		got = append(got, c.Name())
	}
	sort.Strings(got)
	require.Equal(t, []string{"accept", "invitations", "invite", "list", "remove", "role"}, got)
}

// Membership lives on the PROJECT. The router is `projectMembers.*` and every
// input is keyed by `projectId` — `groupMembers` / `grpId` are gone, and a call
// to them would hit a router that no longer exists.
func TestMembers_CallTheProjectMembersRouter(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		procedure string
		wantBody  map[string]any
	}{
		{
			name: "invite", args: []string{"invite", "dev@example.com", "--role", "admin"},
			procedure: "/api/trpc/projectMembers.invite",
			wantBody:  map[string]any{"projectId": "proj_1", "email": "dev@example.com", "role": "admin"},
		},
		{
			name: "role", args: []string{"role", "usr_2", "admin"},
			procedure: "/api/trpc/projectMembers.changeMemberRole",
			wantBody:  map[string]any{"projectId": "proj_1", "userId": "usr_2", "role": "admin"},
		},
		{
			name: "remove", args: []string{"remove", "usr_2"},
			procedure: "/api/trpc/projectMembers.removeMember",
			wantBody:  map[string]any{"projectId": "proj_1", "userId": "usr_2"},
		},
		{
			name: "accept", args: []string{"accept", "inv_1"},
			procedure: "/api/trpc/projectMembers.acceptInvitation",
			wantBody:  map[string]any{"invitationId": "inv_1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var gotBody map[string]any
			c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotBody = inner(t, r)
				trpcOK(w, map[string]any{"id": "inv_1", "ok": true})
			})
			require.NoError(t, run(t, c, tc.args...))
			require.Equal(t, tc.procedure, gotPath)
			require.Equal(t, tc.wantBody, gotBody)
			require.NotContains(t, gotBody, "grpId", "groups are gone — membership is Project-scoped")
		})
	}
}

// `members list` reads the WRAPPED camelCase shape, and an admin-only
// pending-invitations FORBIDDEN must NOT fail the list a plain member can see.
func TestMembersList_DecodesWrappedCamelCase_AndToleratesForbiddenInvites(t *testing.T) {
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/projectMembers.members":
			trpcOK(w, map[string]any{
				"callerRole": "member",
				"members": []map[string]any{
					{"userId": "usr_1", "role": "owner", "joinedAt": "2026-06-24T20:37:17Z", "displayName": nil},
				},
			})
		case "/api/trpc/projectMembers.pendingInvitations":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"json":{"message":"FORBIDDEN"}}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	})
	require.NoError(t, run(t, c, "list"),
		"a FORBIDDEN pending-invitations read must not fail members list")
}

func TestInvitationsJSON_UsesOnlyEnvironmentRefSnakeCase(t *testing.T) {
	t.Chdir(t.TempDir())
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/trpc/projectMembers.listMyInvitations", r.URL.Path)
		trpcOK(w, []map[string]any{
			{
				"id":              "inv_1",
				"projectId":       "proj_1",
				"projectName":     "Acme",
				"role":            "member",
				"createdAt":       "2026-07-15T00:00:00Z",
				"environment_ref": "acmeprod",
			},
		})
	})

	var out bytes.Buffer
	cmd := Cmd(Resolvers{
		Studio:    func() Studio { return c },
		Selection: selectiontest.Selected(t),
	})
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"invitations", "--json"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.NoError(t, cmd.Execute())

	var rows []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "acmeprod", rows[0]["environment_ref"])
	require.NotContains(t, rows[0], "environmentRef")
}

func TestInvitationsJSON_DoesNotFallbackToCamelCaseEnvironmentRef(t *testing.T) {
	t.Chdir(t.TempDir())
	c := studioAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		trpcOK(w, []map[string]any{
			{
				"id":             "inv_1",
				"projectId":      "proj_1",
				"projectName":    "Acme",
				"role":           "member",
				"createdAt":      "2026-07-15T00:00:00Z",
				"environmentRef": "retired-value",
			},
		})
	})

	var out bytes.Buffer
	cmd := Cmd(Resolvers{
		Studio:    func() Studio { return c },
		Selection: selectiontest.Selected(t),
	})
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"invitations", "--json"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.NoError(t, cmd.Execute())

	var rows []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Nil(t, rows[0]["environment_ref"])
	require.NotContains(t, rows[0], "environmentRef")
}

func TestMembersInvite_RejectsAnInvalidRoleBeforeTheAPI(t *testing.T) {
	c := studioAgainst(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("must not call the API for an invalid role")
	})
	require.ErrorContains(t, run(t, c, "invite", "dev@example.com", "--role", "owner"), "--role must be")
}

// There is no --group flag left anywhere: the project comes from the selection
// (or the global --project).
func TestMembers_ExposeNoGroupFlag(t *testing.T) {
	for _, c := range Cmd(Resolvers{}).Commands() {
		require.Nil(t, c.Flags().Lookup("group"), "%s must not take --group", c.Name())
	}
}
