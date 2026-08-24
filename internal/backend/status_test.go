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

// `palbase status` names the FULL context — which project, which address —
// because "which runtime am I looking at" must never be a guess (UAT CLI-005).
//
// It used to ask the Studio over tRPC whenever the checkout was not linked, and
// these tests pinned the procedure name and the tRPC input. That arm is gone:
// there is one door, the project's own, and which project it opens comes from
// the link or the selection. What is worth pinning is therefore the ADDRESS the
// verb resolved and the deployment it read — see status_project_test.go.

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
	r := newRig(t)
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
	r := newRig(t)
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

// `palbase rollback` names the ENVIRONMENT, never a branch — and it acts at the
// project's own door.
//
// This used to assert a tRPC procedure's input. The procedure is gone: rollback
// activates a version the project already holds, over
// POST /v1/management/deployments/{digest}/activate, which is the only place
// that pointer lives. deployments_test.go drives that route; what is left worth
// pinning here is the REFUSAL when no project is named, because a rollback has
// no second authority to fall back to.
func TestRollbackWithoutAProjectRefusesActionably(t *testing.T) {
	selectiontest.Chdir(t)
	r := &rig{Fake: selectiontest.New(t)}
	r.Resolver = r.Fake.Resolver()

	_, err := r.Run(t, "rollback", "old1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--environment",
		"a person with no link must be told the other way to name a project")
}

// ── selection failure ───────────────────────────────────────────────────────

// A directory with NO selection fails with the actionable message, not a nil
// dereference and not a request to a nonsense URL.
func TestCommands_WithoutASelection_FailActionably(t *testing.T) {
	for _, argv := range [][]string{{"status"}, {"deploys"}, {"push"}, {"spec"}} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			selectiontest.Chdir(t) // no .palbase/config.json
			r := &rig{Fake: selectiontest.New(t)}
			r.Resolver = r.Fake.Resolver()

			_, err := r.Run(t, argv[0], argv[1:]...)
			require.Error(t, err)
			// `palbase project use` does NOT exist (verified against the built
			// binary, 2026-08-24) — it was advice nobody could follow. The
			// message has to name a command that is actually there.
			require.Contains(t, err.Error(), "palbase link")
			require.NotContains(t, err.Error(), "project use")
		})
	}
}

// `palbase status` reports the SDK the live artifact was built with and the
// majors the runtime can build — the answer this process cannot compute itself,
// because it does not run the runtime image.
func TestFormatSDK_ReportsVersionAndSupportedSet(t *testing.T) {
	v := "12.0.1"
	active := "86a01cf"
	out := formatSDK(&sdkStatus{Version: &v, SupportedMajors: []int{12, 13}}, nil, &active)

	require.Contains(t, out, "@palbase/backend 12.0.1")
	require.Contains(t, out, "major(s) 12, 13",
		"the SET must be named — 'you are behind 13' is wrong when 12 is supported, "+
			"and that premise is what made palbase build refuse pushes the platform accepted")
	require.Contains(t, out, "your last deploy (86a01cf)",
		"the reading comes from that deploy's manifest and must say so — unanchored, "+
			"it was read as a statement about the present and cost someone a downgrade plan")
}

// The question people bring to this line is 'can I move to the new major', and
// the historical set cannot answer it. When the channel has reported, say both.
func TestFormatSDK_NamesWhatTheRuntimeBuildsToday(t *testing.T) {
	v := "15.0.0"
	active := "86a01cf"
	out := formatSDK(&sdkStatus{Version: &v, SupportedMajors: []int{12, 13, 14, 15}},
		[]int{12, 13, 14, 15, 16, 17}, &active)

	require.Contains(t, out, "your last deploy (86a01cf) built with major(s) 12, 13, 14, 15")
	require.Contains(t, out, "the runtime today builds major(s) 12, 13, 14, 15, 16, 17")
}

// An image nothing has deployed with yet has reported no capability. Saying
// nothing is right; inventing a set, or echoing the historical one as if it were
// current, is the bug this whole line exists to fix.
func TestFormatSDK_SilentAboutAnUnreportedChannel(t *testing.T) {
	v := "15.0.0"
	out := formatSDK(&sdkStatus{Version: &v, SupportedMajors: []int{12, 15}}, nil, nil)

	require.Contains(t, out, "major(s) 12, 15")
	require.NotContains(t, out, "today builds")
}

// Not reported ⇒ print nothing. A never-deployed environment, or one whose active
// artifact predates manifest schema v2, must not be given a default that reads
// like a supported set.
func TestFormatSDK_SilentWhenUnreported(t *testing.T) {
	require.Empty(t, formatSDK(nil, nil, nil))
	require.Empty(t, formatSDK(&sdkStatus{}, nil, nil))
}

// The server can send a field the client silently drops, and nothing anywhere
// says so: channelSdkMajors reached statusOut and the renderer but never the
// struct the response is decoded into, so `palbase status` looked exactly like a
// platform that had not sent it. Decode a server-shaped payload and check every
// field survives the boundary.
func TestStatusResponse_DecodesEveryFieldTheServerSends(t *testing.T) {
	const payload = `{
	  "head": "0f9748ab",
	  "activeVersion": "0f9748ab",
	  "lastDeploy": {"status": "succeeded"},
	  "sdk": {"version": "17.3.0", "supportedMajors": [12, 17]},
	  "channelSdkMajors": [12, 13, 14, 15, 16, 17]
	}`

	var resp statusResponse
	require.NoError(t, json.Unmarshal([]byte(payload), &resp))

	require.Equal(t, "0f9748ab", *resp.Head)
	require.Equal(t, "0f9748ab", *resp.ActiveVersion)
	require.NotNil(t, resp.LastDeploy)
	require.NotNil(t, resp.SDK)
	require.Equal(t, []int{12, 17}, resp.SDK.SupportedMajors)
	require.Equal(t, []int{12, 13, 14, 15, 16, 17}, resp.ChannelSDKMajors,
		"the channel set must survive decoding — it was dropped here once, and the "+
			"missing line was indistinguishable from a platform that never reported")
}
