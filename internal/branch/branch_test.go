package branch

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/stretchr/testify/require"
)

// restAgainst spins an httptest server and returns a real REST transport
// pointed at it (mirrors project_test.go).
func restAgainst(t *testing.T, h http.HandlerFunc) (REST, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	key, err := auth.NewDPoPKey()
	require.NoError(t, err)
	return transport.New(srv.URL, key, "pat_test"), srv
}

func okData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "request_id": "req_x"})
}

func TestBranchCreate_REST_202Handle(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})

	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	// --ref avoids depending on a linked project config in the test env.
	// --async keeps the 202-handle path (default is now sync-wait); --yes
	// skips the confirmation prompt.
	cmd.SetArgs([]string{"create", "staging", "--ref", "acme1234", "--kind", "staging", "--async", "--yes", "--json"})
	require.NoError(t, cmd.Execute())

	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/api/v1/projects/acme1234/branches", gotPath)
	require.Equal(t, "staging", gotBody["branchName"])
	require.Equal(t, "staging", gotBody["kind"])
	require.Equal(t, true, gotBody["deploy"]) // default ON
}

func TestBranchCreate_ForkFromFlag(t *testing.T) {
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "qa", "--ref", "acme1234", "--kind", "qa", "--fork-from", "main", "--async", "--yes", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "main", gotBody["forkFrom"])
	require.Equal(t, "qa", gotBody["kind"])
}

func TestBranchCreate_NoForkOmitsForkFrom(t *testing.T) {
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "staging", "--ref", "acme1234", "--async", "--yes", "--json"})
	require.NoError(t, cmd.Execute())
	_, has := gotBody["forkFrom"]
	require.False(t, has, "forkFrom omitted when --fork-from not passed")
}

func TestBranchCreate_NoDeployFlag(t *testing.T) {
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "preview-x", "--ref", "acme1234", "--kind", "preview", "--no-deploy", "--async", "--yes", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, false, gotBody["deploy"])
	require.Equal(t, "preview", gotBody["kind"])
}

func TestBranchList_REST(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/projects/acme1234/branches", r.URL.Path)
		okData(w, 200, []map[string]any{
			{"id": "b_main", "name": "main", "endpoint_ref": "acme1234m", "status": "active", "ephemeral": false, "url": "https://acme1234m.dev.palbase.studio"},
			{"id": "b_s", "name": "staging", "endpoint_ref": "acme1234s", "status": "active", "ephemeral": false, "url": "https://acme1234s.dev.palbase.studio"},
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"list", "--ref", "acme1234", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
}

func TestBranchDelete_WithYes_REST(t *testing.T) {
	var gotMethod, gotPath string
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-d", "runId": "run-d"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"delete", "staging", "--ref", "acme1234", "--yes", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, http.MethodDelete, gotMethod)
	require.Equal(t, "/api/v1/projects/acme1234/branches/staging", gotPath)
}

func TestBranchDelete_RefusesMainClientSide(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API to delete main")
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"delete", "main", "--ref", "acme1234", "--yes"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot delete the main branch")
}

func TestBranchDelete_AbortsWithoutConfirm(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API when the user declines")
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"delete", "staging", "--ref", "acme1234"})
	cmd.SetIn(strings.NewReader("n\n")) // decline
	require.NoError(t, cmd.Execute())
}

func TestBranchDelete_403DefaultBranchSurfacesAPIError(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "forbidden", "error_description": "Cannot delete the default branch.",
			"status": 403, "request_id": "req_e",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"delete", "staging", "--ref", "acme1234", "--yes"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	var apiErr *transport.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "forbidden", apiErr.Code)
}

// TestBranchArchive_REST locks the verb↔wire split: the user types `archive`,
// the wire path stays /hibernate (server route + 'hibernated' status are API
// surface — renamed only in UI/CLI copy).
func TestBranchArchive_REST(t *testing.T) {
	var gotMethod, gotPath string
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-h", "runId": "run-h"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"archive", "staging", "--ref", "acme1234", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/api/v1/projects/acme1234/branches/staging/hibernate", gotPath)
}

func TestBranchWake_REST(t *testing.T) {
	var gotPath string
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-w", "runId": "run-w"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"wake", "staging", "--ref", "acme1234", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "/api/v1/projects/acme1234/branches/staging/wake", gotPath)
}

func TestBranchArchive_RefusesMainClientSide(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API to archive main")
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"archive", "main", "--ref", "acme1234"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot archive the main branch")
}

func TestBranchCreate_404SurfacesAPIError(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "not_found", "error_description": "Project not found",
			"status": 404, "request_id": "req_e",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	// --yes/--async: the POST itself errors, so we just need past the
	// confirmation gate (tests run non-interactive) without entering the poll.
	cmd.SetArgs([]string{"create", "staging", "--ref", "nope", "--yes", "--async"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	require.Error(t, err)
	var apiErr *transport.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "not_found", apiErr.Code)
}

func TestBranchCreate_403ForbiddenSurfacesAPIError(t *testing.T) {
	// Free tier → the Studio service returns 403 forbidden; the CLI must
	// surface it as a clean APIError (the tier-gate is server-side).
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "forbidden", "error_description": "Branches require a Pro plan or higher.",
			"status": 403, "request_id": "req_e",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "staging", "--ref", "acme1234", "--yes", "--async"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	require.Error(t, err)
	var apiErr *transport.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "forbidden", apiErr.Code)
}

// shrinkPoll speeds the sync-wait loop for tests (default interval is 2s) and
// restores it after. Pairs with t.Cleanup.
func shrinkPoll(t *testing.T) {
	t.Helper()
	prevInterval, prevTimeout := branchPollInterval, branchPollTimeout
	branchPollInterval = 2 * time.Millisecond
	branchPollTimeout = 2 * time.Second
	t.Cleanup(func() { branchPollInterval, branchPollTimeout = prevInterval, prevTimeout })
}

// forceInteractive makes isInteractive() return true for the test so the
// confirmation prompt path (which otherwise short-circuits in CI) runs.
func forceInteractive(t *testing.T) {
	t.Helper()
	prev := isInteractive
	isInteractive = func() bool { return true }
	t.Cleanup(func() { isInteractive = prev })
}

// forceNonInteractive pins isInteractive() to false. Some CI/dev shells leave
// stdin as a char device under `go test`, so we set it explicitly rather than
// depend on the ambient environment.
func forceNonInteractive(t *testing.T) {
	t.Helper()
	prev := isInteractive
	isInteractive = func() bool { return false }
	t.Cleanup(func() { isInteractive = prev })
}

// TestBranchCreate_DefaultSyncWaitsForReady: the default (no --async) create
// POSTs, then polls the branch list until the branch flips to "active",
// surfacing its URL. Asserts the list was polled more than once (proving the
// wait loop ran through a "creating" cycle).
func TestBranchCreate_DefaultSyncWaitsForReady(t *testing.T) {
	shrinkPoll(t)
	var posts, gets int
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			posts++
			okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
		case http.MethodGet:
			gets++
			status := "creating"
			url := ""
			if gets >= 3 { // first two polls still provisioning, then ready
				status = "active"
				url = "https://acme1234s.dev.palbase.studio"
			}
			okData(w, 200, []map[string]any{
				{"id": "b_s", "name": "staging", "endpoint_ref": "acme1234s", "status": status, "ephemeral": false, "url": url},
			})
		}
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "staging", "--ref", "acme1234", "--yes", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	require.Equal(t, 1, posts, "create should POST exactly once")
	require.GreaterOrEqual(t, gets, 3, "should poll the list until active")
	// --json in sync mode emits the final active branch row (with URL).
	require.Contains(t, out.String(), "https://acme1234s.dev.palbase.studio")
	require.Contains(t, out.String(), `"status": "active"`)
}

// TestBranchCreate_SyncDetectsProvisionFailure: once the branch has been seen
// "creating", a later poll where it has vanished (compensation flipped it to
// "deleted", which the list filters out) is treated as a failure.
func TestBranchCreate_SyncDetectsProvisionFailure(t *testing.T) {
	shrinkPoll(t)
	var gets int
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1"})
			return
		}
		gets++
		if gets == 1 {
			okData(w, 200, []map[string]any{
				{"id": "b_s", "name": "staging", "endpoint_ref": "acme1234s", "status": "creating"},
			})
			return
		}
		// branch vanished → compensation rolled it back
		okData(w, 200, []map[string]any{})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "staging", "--ref", "acme1234", "--yes"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "provisioning failed")
}

// TestBranchCreate_AsyncReturns202 keeps the old async behaviour behind the
// flag: --async returns the workflow handle without polling.
func TestBranchCreate_AsyncReturns202(t *testing.T) {
	var gets int
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
		}
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-async", "runId": "run-1"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "staging", "--ref", "acme1234", "--async", "--yes", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	require.Equal(t, 0, gets, "--async must NOT poll the list")
	require.Contains(t, out.String(), "wf-async")
}

// TestBranchCreate_ConfirmDeclineAborts: interactive, no --yes, user answers
// "n" → no POST is sent.
func TestBranchCreate_ConfirmDeclineAborts(t *testing.T) {
	forceInteractive(t)
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API when the user declines")
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "staging", "--ref", "acme1234"})
	cmd.SetIn(strings.NewReader("n\n"))
	require.NoError(t, cmd.Execute())
}

// TestBranchCreate_ConfirmAcceptProceeds: interactive, user answers "y" →
// the create POST is sent (then sync-wait runs and finds it active).
func TestBranchCreate_ConfirmAcceptProceeds(t *testing.T) {
	forceInteractive(t)
	shrinkPoll(t)
	var posted bool
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = true
			okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1"})
			return
		}
		okData(w, 200, []map[string]any{
			{"id": "b_s", "name": "staging", "endpoint_ref": "acme1234s", "status": "active", "url": "https://acme1234s.dev.palbase.studio"},
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "staging", "--ref", "acme1234"})
	cmd.SetIn(strings.NewReader("y\n"))
	require.NoError(t, cmd.Execute())
	require.True(t, posted, "accepting the prompt should send the create POST")
}

// TestBranchCreate_NonInteractiveWithoutYesErrors: a piped/CI shell that omits
// --yes must fail loudly naming the flag, never hang on the prompt.
func TestBranchCreate_NonInteractiveWithoutYesErrors(t *testing.T) {
	forceNonInteractive(t)
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API without confirmation")
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "staging", "--ref", "acme1234"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--yes")
}
