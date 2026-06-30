package project

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/stretchr/testify/require"
)

// studioAgainst spins an httptest server and returns a *studio.Client backed
// by it (mirrors db_test.go's helper).
func studioAgainst(t *testing.T, h http.HandlerFunc) *studio.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) {
		return "test-token", nil
	})
}

// trpcOK writes a tRPC success envelope (mirrors db_test.go).
func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// innerInput decodes the inner {"json":{...}} of a tRPC POST body.
func innerInput(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var outer struct {
		JSON map[string]any `json:"json"`
	}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&outer))
	return outer.JSON
}

// restAgainst spins an httptest server with the given handler and returns
// a real REST transport pointed at it, signing with a real DPoP key.
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

func TestProjectList_REST(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		okData(w, 200, []map[string]any{
			{"ref": "abcd1234", "name": "Demo", "tier": "free", "region": "northeurope", "status": "ready"},
		})
	})

	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"list", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	require.Equal(t, http.MethodGet, gotMethod)
	require.Equal(t, "/api/v1/projects", gotPath)
	require.Equal(t, "DPoP pat_test", gotAuth)
}

func TestProjectCreate_REST_202Handle(t *testing.T) {
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/projects", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})

	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	require.NoError(t, os.Chdir(dir))

	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "abcd1234", "--name", "Demo", "--github-account", "personal", "--repo", "demo-repo", "--tier", "pro", "--json", "--yes"})
	require.NoError(t, cmd.Execute())

	require.Equal(t, "abcd1234", gotBody["ref"])
	require.Equal(t, "Demo", gotBody["name"])
	require.Equal(t, "personal", gotBody["githubAccount"])
	require.Equal(t, "demo-repo", gotBody["repoName"])
	require.Equal(t, "pro", gotBody["tier"])
	// No org layer: the owner is the authenticated user, server-derived.
	_, hasOrg := gotBody["orgId"]
	require.False(t, hasOrg, "create body must not carry orgId (org layer removed)")
}

func TestCreate_NoGithub_SendsPlatformBodyAndWritesMode(t *testing.T) {
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/projects", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})

	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	require.NoError(t, os.Chdir(dir))

	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "todoapp", "--name", "Todo App", "--yes"})
	require.NoError(t, cmd.Execute())

	// No github flags → the keys must be ABSENT from the payload entirely
	// (the server contract treats "both absent" as platform mode; an empty
	// string would trip its exactly-one/shape validation).
	_, hasAccount := gotBody["githubAccount"]
	require.False(t, hasAccount, "platform-mode body must omit githubAccount entirely, got %v", gotBody["githubAccount"])
	_, hasRepo := gotBody["repoName"]
	require.False(t, hasRepo, "platform-mode body must omit repoName entirely, got %v", gotBody["repoName"])

	cfg, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "todoapp", cfg.Ref)
	require.Equal(t, "main", cfg.DefaultEnv)
	require.Equal(t, "platform", cfg.Mode)
	require.Equal(t, "", cfg.GithubRepo)
}

func TestCreate_WithGithub_SendsGithubBodyAndWritesMode(t *testing.T) {
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})

	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	require.NoError(t, os.Chdir(dir))

	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "abcd1234", "--name", "Demo", "--github-account", "personal", "--repo", "demo-repo", "--yes"})
	require.NoError(t, cmd.Execute())

	require.Equal(t, "personal", gotBody["githubAccount"])
	require.Equal(t, "demo-repo", gotBody["repoName"])

	cfg, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "abcd1234", cfg.Ref)
	require.Equal(t, "github", cfg.Mode)
	require.Equal(t, "demo-repo", cfg.GithubRepo)
}

// TestCreate_GithubFlagMatrix locks the flag-combination contract:
//   - neither flag  → platform mode, github keys ABSENT from the payload
//   - both flags    → github mode, both keys present
//   - exactly one   → client-side validation error, no request sent
//
// This mirrors the server contract (send NEITHER for platform mode, BOTH
// for github mode, exactly one = validation error).
func TestCreate_GithubFlagMatrix(t *testing.T) {
	tests := []struct {
		name        string
		extraArgs   []string
		wantErr     string // "" = expect success
		wantAccount string // expected githubAccount value; "" = key must be absent
		wantRepo    string // expected repoName value; "" = key must be absent
	}{
		{
			name:      "neither flag → platform mode, keys omitted",
			extraArgs: nil,
		},
		{
			name:        "both flags → github mode, keys present",
			extraArgs:   []string{"--github-account", "personal", "--repo", "demo-repo"},
			wantAccount: "personal",
			wantRepo:    "demo-repo",
		},
		{
			name:      "only --github-account → error, no request",
			extraArgs: []string{"--github-account", "personal"},
			wantErr:   "use both --github-account and --repo for GitHub mode, or neither for platform mode",
		},
		{
			name:      "only --repo → error, no request",
			extraArgs: []string{"--repo", "demo-repo"},
			wantErr:   "use both --github-account and --repo for GitHub mode, or neither for platform mode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody map[string]any
			requestSent := false
			c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
				requestSent = true
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
			})

			dir := t.TempDir()
			wd, _ := os.Getwd()
			t.Cleanup(func() { _ = os.Chdir(wd) })
			require.NoError(t, os.Chdir(dir))

			cmd := Cmd(Resolvers{REST: func() REST { return c }})
			args := append([]string{"create", "abcd1234", "--name", "Demo", "--yes"}, tt.extraArgs...)
			cmd.SetArgs(args)
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			err := cmd.Execute()

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				require.False(t, requestSent, "half-specified github flags must fail before any request is sent")
				return
			}
			require.NoError(t, err)
			require.True(t, requestSent)

			gotAccount, hasAccount := gotBody["githubAccount"]
			gotRepo, hasRepo := gotBody["repoName"]
			if tt.wantAccount == "" {
				require.False(t, hasAccount, "payload must omit githubAccount entirely, got %v", gotAccount)
			} else {
				require.Equal(t, tt.wantAccount, gotAccount)
			}
			if tt.wantRepo == "" {
				require.False(t, hasRepo, "payload must omit repoName entirely, got %v", gotRepo)
			} else {
				require.Equal(t, tt.wantRepo, gotRepo)
			}
		})
	}
}

func TestProjectStatus_REST(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/projects/abcd1234", r.URL.Path)
		okData(w, 200, map[string]any{
			"ref": "abcd1234", "name": "Demo", "tier": "free",
			"region": "northeurope", "status": "provisioning",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"status", "abcd1234", "--json"})
	require.NoError(t, cmd.Execute())
}

func TestProjectStatus_404SurfacesAPIError(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "project_not_found", "error_description": "no such project",
			"status": 404, "request_id": "req_e",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"status", "zzzz"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	require.Error(t, err)
	var apiErr *transport.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "project_not_found", apiErr.Code)
}

// --- project delete ----------------------------------------------------------

// TestProjectDelete_Yes calls project.delete with --yes (no prompt) and
// verifies the tRPC path + input shape and the success line.
func TestProjectDelete_Yes(t *testing.T) {
	var gotPath string
	var gotInput map[string]any

	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		gotPath = r.URL.Path
		gotInput = innerInput(t, r)
		// project.delete returns void — empty json null is fine.
		trpcOK(w, nil)
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{
		REST:   func() REST { return nil }, // not called by delete
		Studio: func() *studio.Client { return c },
	})
	cmd.SetArgs([]string{"delete", "myproj123", "--yes"})
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "/api/trpc/project.delete", gotPath)
	require.Equal(t, "myproj123", gotInput["ref"])
	require.Equal(t, "myproj123", gotInput["confirmRef"])
	require.Contains(t, out.String(), "✓ deleted project myproj123")
}

// TestProjectDelete_ConfirmPrompt_Correct simulates a user typing the correct
// ref at the interactive prompt and verifies the delete proceeds.
func TestProjectDelete_ConfirmPrompt_Correct(t *testing.T) {
	called := false
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		trpcOK(w, nil)
	})

	// Pipe the correct ref as stdin.
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, _ = w.WriteString("myproj123\n")
	w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin; r.Close() })

	var out strings.Builder
	cmd := Cmd(Resolvers{
		REST:   func() REST { return nil },
		Studio: func() *studio.Client { return c },
	})
	cmd.SetArgs([]string{"delete", "myproj123"})
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.True(t, called, "Studio must be called when confirmation matches")
	require.Contains(t, out.String(), "✓ deleted project myproj123")
}

// TestProjectDelete_ConfirmPrompt_Wrong verifies that a wrong confirmation
// cancels the delete without calling Studio at all.
func TestProjectDelete_ConfirmPrompt_Wrong(t *testing.T) {
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Studio must NOT be called when confirmation is wrong")
	})

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, _ = w.WriteString("WRONGREF\n")
	w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin; r.Close() })

	cmd := Cmd(Resolvers{
		REST:   func() REST { return nil },
		Studio: func() *studio.Client { return c },
	})
	cmd.SetArgs([]string{"delete", "myproj123"})
	cmd.SetOut(&strings.Builder{})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err = cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "delete cancelled")
}

// TestProjectDelete_StudioError surfaces tRPC errors to the caller.
func TestProjectDelete_StudioError(t *testing.T) {
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"json": map[string]any{
					"message": "FORBIDDEN",
					"code":    -32001,
					"data":    map[string]any{"code": "FORBIDDEN", "httpStatus": 403},
				},
			},
		})
	})

	cmd := Cmd(Resolvers{
		REST:   func() REST { return nil },
		Studio: func() *studio.Client { return c },
	})
	cmd.SetArgs([]string{"delete", "myproj123", "--yes"})
	cmd.SetOut(&strings.Builder{})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "FORBIDDEN")
}

var _ = context.Background
