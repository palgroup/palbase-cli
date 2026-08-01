package backend

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

func ptr[T any](v T) *T { return &v }

func decodeTRPCInput(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	// tRPC queries carry `?input={"json":{...}}`; mutations carry it in the body.
	raw := r.URL.Query().Get("input")
	if raw == "" {
		var body struct {
			JSON map[string]any `json:"json"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		return body.JSON
	}
	var wrapper struct {
		JSON map[string]any `json:"json"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &wrapper))
	return wrapper.JSON
}

// `palbase status` names the FULL context — which project, which environment,
// which endpoint — because "which runtime am I looking at" must never be a guess
// (UAT CLI-005). And it sends only the ENVIRONMENT ref: there is no branch.
func TestStatus_NamesTheContextAndSendsOnlyTheRef(t *testing.T) {
	var gotInput map[string]any
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "backend.status") {
			gotInput = decodeTRPCInput(t, req)
			trpcOK(w, map[string]any{
				"head":          "abc1234",
				"activeVersion": "abc1234",
				"lastDeploy": map[string]any{
					"status": "succeeded", "version": "abc1234",
					"updatedAt": time.Now().Add(-3 * time.Minute).Format(time.RFC3339),
				},
			})
			return
		}
		trpcOK(w, map[string]any{})
	})

	out, err := r.Run(t, "status")
	require.NoError(t, err)

	require.Equal(t, map[string]any{"ref": "app1prod"}, gotInput)
	require.NotContains(t, gotInput, "branch")

	require.Contains(t, out, "project:      proj_1")
	require.Contains(t, out, "environment:  production (app1prod)")
	require.Contains(t, out, "endpoint:     https://app1prod.dev.palbase.studio")
	require.Contains(t, out, "repository:   palbase")
	require.Contains(t, out, "last deploy: SUCCEEDED (3m ago)")
	requireNoV1(t, r.Fake)
}

func TestStatusJSON_CarriesProjectIdAndEnvironmentId(t *testing.T) {
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		trpcOK(w, map[string]any{"head": "abc", "activeVersion": "abc"})
	})
	out, err := r.Run(t, "status", "--json")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	// The canonical metadata pair (spec §7.4): projectId names the product,
	// environmentId names the runtime. No branch identity anywhere.
	require.Equal(t, "proj_1", got["projectId"])
	require.Equal(t, "env_prod", got["environmentId"])
	require.Equal(t, "app1prod", got["environment_ref"])
	require.NotContains(t, got, "environmentRef")
	require.NotContains(t, out, "branch")
}

// A succeeded deploy that still carries an error is "succeeded with warnings" —
// a green-looking deploy that produced zero endpoints must not read as fine.
func TestFormatLastDeploy(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		d    *lastDeploy
		want []string
		not  []string
	}{
		{name: "never deployed", d: nil},
		{
			name: "succeeded",
			d:    &lastDeploy{Status: "succeeded", Version: ptr("abc"), UpdatedAt: ptr(now.Add(-2 * time.Hour).Format(time.RFC3339))},
			want: []string{"last deploy: SUCCEEDED (2h ago)"},
		},
		{
			name: "succeeded WITH warnings",
			d: &lastDeploy{Status: "succeeded", Error: ptr("zero endpoints collected"),
				UpdatedAt: ptr(now.Format(time.RFC3339))},
			want: []string{"SUCCEEDED WITH WARNINGS", "zero endpoints collected"},
		},
		{
			name: "failed carries the server reason",
			d:    &lastDeploy{Status: "failed", Error: ptr("42P07: relation exists")},
			want: []string{"FAILED", "42P07: relation exists"},
		},
		{
			name: "unparseable timestamp drops the age, never invents one",
			d:    &lastDeploy{Status: "succeeded", UpdatedAt: ptr("not-a-time")},
			want: []string{"last deploy: SUCCEEDED\n"},
			not:  []string{"ago"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatLastDeploy(tt.d, now)
			if tt.d == nil {
				require.Empty(t, got)
				return
			}
			for _, w := range tt.want {
				require.Contains(t, got, w)
			}
			for _, n := range tt.not {
				require.NotContains(t, got, n)
			}
			// Whatever it prints, it never names a branch — there is none.
			require.NotContains(t, got, "branch")
		})
	}
}

func TestHumanizeAgo(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{50 * time.Hour, "2d ago"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, humanizeAgo(tt.d))
	}
}

// ── deploys ─────────────────────────────────────────────────────────────────

func TestDeploys_ReadsTheV2EnvironmentHistory(t *testing.T) {
	r := newRig(t, nil)
	const route = "/api/v2/projects/proj_1/environments/app1prod/deployments"
	r.Fake.OK("GET "+route, map[string]any{
		"deployments": []map[string]any{
			{"status": "failed", "trigger": "cli", "error": "42P07: relation exists\nsecond line",
				"createdAt": "2026-07-01T10:00:00Z"},
			{"status": "succeeded", "version": "abc1234", "trigger": "webhook",
				"error": "zero endpoints collected", "createdAt": "2026-07-01T09:00:00Z"},
		},
	})

	out, err := r.Run(t, "deploys")
	require.NoError(t, err)

	req, ok := r.Fake.Find("GET " + route)
	require.True(t, ok, "got %v", r.Fake.Routes())
	require.Equal(t, "limit=20", req.Query)

	require.Contains(t, out, "FAILED")
	require.Contains(t, out, "42P07: relation exists")
	require.NotContains(t, out, "second line", "the table shows the FIRST line; --json carries the rest")
	// A succeeded row carrying an error is WARN, never a clean success.
	require.Contains(t, out, "WARN")
	// There is no BRANCH column left to print.
	require.NotContains(t, out, "BRANCH")
	requireNoV1(t, r.Fake)
}

func TestDeploys_Empty(t *testing.T) {
	r := newRig(t, nil)
	r.Fake.OK("GET /api/v2/projects/proj_1/environments/app1prod/deployments",
		map[string]any{"deployments": []any{}})
	out, err := r.Run(t, "deploys")
	require.NoError(t, err)
	require.Contains(t, out, "no deploy attempts yet")
}

func TestTruncateNote(t *testing.T) {
	require.Equal(t, "abc", truncateNote("abc", 10))
	require.Equal(t, "abc…", truncateNote("abcdef", 4))
	require.Equal(t, "one", firstLine("one\ntwo"))
}

// ── rollback ────────────────────────────────────────────────────────────────

func TestRollback_TargetsTheEnvironmentNotABranch(t *testing.T) {
	var gotInput map[string]any
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "backend.rollback") {
			gotInput = decodeTRPCInput(t, req)
			trpcOK(w, map[string]any{"status": "ok", "version": "new1", "rolled_back_from": "old1"})
			return
		}
		trpcOK(w, map[string]any{})
	})

	out, err := r.Run(t, "rollback", "old1")
	require.NoError(t, err)
	require.Equal(t, map[string]any{"ref": "app1prod", "version": "old1"}, gotInput)
	require.NotContains(t, gotInput, "branch")
	require.Contains(t, out, "rolled back environment app1prod")
}

// ── selection failure ───────────────────────────────────────────────────────

// A directory with NO selection fails with the actionable message, not a nil
// dereference and not a request to a nonsense URL.
func TestCommands_WithoutASelection_FailActionably(t *testing.T) {
	for _, argv := range [][]string{{"status"}, {"deploys"}, {"push"}, {"web", "spec"}, {"ios", "spec"}} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			selectiontest.Chdir(t) // no .palbase/config.json
			r := &rig{Fake: selectiontest.New(t)}
			r.Resolver = r.Fake.Resolver()

			_, err := r.Run(t, argv[0], argv[1:]...)
			require.Error(t, err)
			require.Contains(t, err.Error(), "palbase project use")
		})
	}
}
