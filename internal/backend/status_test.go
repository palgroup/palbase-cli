package backend

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

func ptr[T any](v T) *T { return &v }

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
	// IT PINNED `--environment`, and that assertion is why the refusal kept
	// offering a flag that names nothing: with no --project the resolver reads
	// .palbase/selection.json for the project id before the environment flag is
	// ever consulted, and a checkout with no link has no such file. The other
	// way to name a project is `palbase link <ref>` — a bare ref resolves to
	// `<ref>.<PublicHost>` — so that is what a person with no link is told.
	require.Contains(t, err.Error(), "palbase link <ref>",
		"a person with no link must be told the other way to name a project")
	require.NotContains(t, err.Error(), "--environment",
		"the refusal offers a flag that cannot select without --project")
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
