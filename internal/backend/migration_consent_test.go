package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfirmDestructive covers the y/n/flag/non-TTY decision matrix.
func TestConfirmDestructive(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		isTTY bool
		flag  bool
		want  bool
		err   bool
	}{
		{"flag short-circuits regardless of input", "n\n", true, true, true, false},
		{"flag short-circuits on non-tty", "", false, true, true, false},
		{"tty yes lowercase", "y\n", true, false, true, false},
		{"tty yes word", "yes\n", true, false, true, false},
		{"tty yes uppercase", "Y\n", true, false, true, false},
		{"tty no", "n\n", true, false, false, false},
		{"tty empty defaults to no", "\n", true, false, false, false},
		{"tty garbage is no", "maybe\n", true, false, false, false},
		{"non-tty no flag aborts with error", "", false, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := confirmDestructive(strings.NewReader(tc.in), tc.isTTY, tc.flag)
			if tc.err {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "--accept-data-loss")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestDecideDestructive covers the higher-level plan→decision branching.
func TestDecideDestructive(t *testing.T) {
	t.Run("no drops proceeds silently", func(t *testing.T) {
		var out bytes.Buffer
		ok, err := decideDestructive(migrationPlan{}, false, strings.NewReader(""), false, &out)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Empty(t, out.String())
	})

	t.Run("empty drops proceed with a note, no prompt", func(t *testing.T) {
		var out bytes.Buffer
		plan := migrationPlan{
			Drops:       []destructiveOp{{Kind: "column", Table: "todos", Column: "tag", RowCount: 5, NonNull: 0}},
			HasDataLoss: false,
		}
		// Non-TTY + no flag, but no data loss → must still proceed.
		ok, err := decideDestructive(plan, false, strings.NewReader(""), false, &out)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Contains(t, out.String(), "no data loss")
	})

	t.Run("data loss with flag proceeds", func(t *testing.T) {
		var out bytes.Buffer
		plan := migrationPlan{
			Drops:       []destructiveOp{{Kind: "column", Table: "todos", Column: "priority", RowCount: 200, NonNull: 142}},
			HasDataLoss: true,
		}
		ok, err := decideDestructive(plan, true, strings.NewReader(""), false, &out)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Contains(t, out.String(), "142 non-null value")
	})

	t.Run("data loss non-tty no flag aborts", func(t *testing.T) {
		var out bytes.Buffer
		plan := migrationPlan{
			Drops:       []destructiveOp{{Kind: "table", Table: "legacy", RowCount: 7}},
			HasDataLoss: true,
		}
		ok, err := decideDestructive(plan, false, strings.NewReader(""), false, &out)
		require.Error(t, err)
		assert.False(t, ok)
		assert.Contains(t, out.String(), "DROP TABLE legacy (7 row(s)")
	})

	t.Run("data loss tty y proceeds", func(t *testing.T) {
		var out bytes.Buffer
		plan := migrationPlan{
			Drops:       []destructiveOp{{Kind: "column", Table: "todos", Column: "priority", RowCount: 9, NonNull: 9}},
			HasDataLoss: true,
		}
		ok, err := decideDestructive(plan, false, strings.NewReader("y\n"), true, &out)
		require.NoError(t, err)
		assert.True(t, ok)
	})
}

// pushPlanStudio records tRPC paths and serves the plan + deploy surface so the
// orchestration tests can assert migrationPlan precedes deploy and that the
// acceptDataLoss flag flows into the deploy input.
type pushPlanStudio struct {
	mu           sync.Mutex
	calls        []string
	deployInputs []map[string]any
	planHasLoss  bool
	planNonNull  int64
}

func (rs *pushPlanStudio) resolvers(t *testing.T) Resolvers {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		body, _ := io.ReadAll(r.Body)
		rs.mu.Lock()
		rs.calls = append(rs.calls, path)
		rs.mu.Unlock()
		ok := func(data any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": data}}})
		}
		switch path {
		case "/api/trpc/backend.migrationPlan":
			ok(map[string]any{
				"drops": []map[string]any{
					{"kind": "column", "table": "todos", "column": "priority", "rowCount": 200, "nonNull": rs.planNonNull},
				},
				"unsupported": []any{},
				"hasDataLoss": rs.planHasLoss,
			})
		case "/api/trpc/backend.deploy":
			// Decode the tRPC input envelope {json: {...}} to capture acceptDataLoss.
			var env struct {
				JSON map[string]any `json:"json"`
			}
			_ = json.Unmarshal(body, &env)
			rs.mu.Lock()
			rs.deployInputs = append(rs.deployInputs, env.JSON)
			rs.mu.Unlock()
			ok(map[string]any{"version": "v2", "files": 1})
		case "/api/trpc/userFlags.system.list", "/api/trpc/notifications.providers.list":
			ok([]any{})
		case "/api/trpc/auth.providers.list":
			ok(map[string]any{"configureAvailable": false, "providers": []any{}})
		case "/api/trpc/storage.buckets.list":
			ok(map[string]any{"buckets": []any{}})
		case "/api/trpc/documents.rules.list":
			ok(map[string]any{"rules": []any{}})
		case "/api/trpc/apikey.reveal":
			ok(map[string]any{"endpointRef": "abc123m", "anonKey": "pb_abc123m_canon"})
		default:
			t.Errorf("unexpected tRPC path: %s", path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := studio.New(srv.URL, func(_ context.Context) (string, error) { return "tok", nil })
	return Resolvers{
		Studio:    func() *studio.Client { return c },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	}
}

func (rs *pushPlanStudio) sequence() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]string, len(rs.calls))
	copy(out, rs.calls)
	return out
}

// seedSchema writes a minimal schema.ts into the cwd so push runs the plan
// round-trip (locateSchemaFile finds it).
func seedSchema(t *testing.T) {
	t.Helper()
	require.NoError(t, os.WriteFile("schema.ts", []byte("export default {}\n"), 0o644))
	// Endpoints dir so bundleCwd has content to pack.
	require.NoError(t, os.MkdirAll(filepath.Join("endpoints", "hello"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join("endpoints", "hello", "get.ts"), []byte("export default {}\n"), 0o644))
}

// TestPush_PlanPrecedesDeploy_WithFlag: a data-losing plan + --accept-data-loss
// proceeds, the plan call comes before deploy, and deploy carries acceptDataLoss.
func TestPush_PlanPrecedesDeploy_WithFlag(t *testing.T) {
	chdirLinked(t, "abc123")
	seedSchema(t)
	rs := &pushPlanStudio{planHasLoss: true, planNonNull: 142}
	cmd := newPushCmd(rs.resolvers(t))
	cmd.SetArgs([]string{"--no-types", "--accept-data-loss"})
	require.NoError(t, cmd.Execute())

	calls := rs.sequence()
	iPlan := indexOf(calls, "/api/trpc/backend.migrationPlan")
	iDeploy := indexOf(calls, "/api/trpc/backend.deploy")
	require.GreaterOrEqual(t, iPlan, 0, "plan must run")
	require.GreaterOrEqual(t, iDeploy, 0, "deploy must run")
	require.Less(t, iPlan, iDeploy, "plan precedes deploy")

	require.Len(t, rs.deployInputs, 1)
	assert.Equal(t, true, rs.deployInputs[0]["acceptDataLoss"] == true, "deploy must carry acceptDataLoss=true")
}

// TestPush_DataLoss_NonTTY_NoFlag_Aborts: a data-losing plan with no flag and a
// non-interactive shell must abort BEFORE deploy.
func TestPush_DataLoss_NonTTY_NoFlag_Aborts(t *testing.T) {
	chdirLinked(t, "abc123")
	seedSchema(t)
	rs := &pushPlanStudio{planHasLoss: true, planNonNull: 142}
	cmd := newPushCmd(rs.resolvers(t))
	cmd.SetArgs([]string{"--no-types"})
	err := cmd.Execute()
	require.Error(t, err, "data loss without consent in non-TTY must abort")

	calls := rs.sequence()
	require.GreaterOrEqual(t, indexOf(calls, "/api/trpc/backend.migrationPlan"), 0, "plan must run")
	assert.Equal(t, -1, indexOf(calls, "/api/trpc/backend.deploy"), "deploy must NOT run")
	assert.Empty(t, rs.deployInputs)
}

// TestPush_EmptyDrop_NoFlag_Proceeds: a destructive-but-no-data-loss plan
// proceeds without a flag/prompt, and deploy does NOT carry acceptDataLoss.
func TestPush_EmptyDrop_NoFlag_Proceeds(t *testing.T) {
	chdirLinked(t, "abc123")
	seedSchema(t)
	rs := &pushPlanStudio{planHasLoss: false, planNonNull: 0}
	cmd := newPushCmd(rs.resolvers(t))
	cmd.SetArgs([]string{"--no-types"})
	require.NoError(t, cmd.Execute())

	require.Len(t, rs.deployInputs, 1)
	_, present := rs.deployInputs[0]["acceptDataLoss"]
	assert.False(t, present, "empty drop must not set acceptDataLoss on deploy")
}
