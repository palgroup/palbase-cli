package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/transport"
)

// ── the v2 deploy paths ─────────────────────────────────────────────────────

func TestDeployPaths_AreEnvironmentScoped(t *testing.T) {
	require.Equal(t,
		"/api/v2/projects/proj_1/environments/app1prod/deploy",
		DeployPath("proj_1", "app1prod"))
	require.Equal(t,
		"/api/v2/projects/proj_1/environments/app1prod/deployments",
		DeploymentsPath("proj_1", "app1prod"))
	require.Equal(t,
		"/api/v2/projects/proj_1/environments/app1prod/deployments/dep_1",
		DeploymentPath("proj_1", "app1prod", "dep_1"))
}

// fakeDeploy records every upload and status poll.
type fakeDeploy struct {
	uploads  []upload
	statuses []string
	// fail makes the FIRST n uploads fail with a transport-level error (a lost
	// response), so the retry can be observed.
	failFirst int
	// apiErr, when set, is returned instead: a server VERDICT, not a lost response.
	apiErr error
	reply  string
	// status is what the poll returns.
	status string
}

type upload struct {
	path           string
	fields         map[string]string
	idempotencyKey string
}

func (f *fakeDeploy) PostMultipart(_ context.Context, path string, _ []byte, fields map[string]string, key string) ([]byte, error) {
	f.uploads = append(f.uploads, upload{path: path, fields: fields, idempotencyKey: key})
	if f.apiErr != nil {
		return nil, f.apiErr
	}
	if len(f.uploads) <= f.failFirst {
		return nil, errors.New("Post: context deadline exceeded (Client.Timeout)")
	}
	reply := f.reply
	if reply == "" {
		reply = `{"data":{"workflowId":"wf","runId":"r","deploymentId":"dep_1"},"request_id":"req"}`
	}
	return []byte(reply), nil
}

func (f *fakeDeploy) Do(_ context.Context, _, path string, _, out any) error {
	f.statuses = append(f.statuses, path)
	st, ok := out.(*deploymentStatus)
	if !ok {
		return nil
	}
	status := f.status
	if status == "" {
		status = "succeeded"
	}
	st.Status = status
	st.Version = "abc1234"
	return nil
}

func palbaseProject(t *testing.T) selection.Selection {
	t.Helper()
	return selection.Selection{
		ProjectID:          "proj_1",
		Environment:        selection.Environment{ID: "env_prod", Ref: "app1prod", Slug: "production"},
		RepositoryProvider: selection.ProviderPalbase,
	}
}

// seedBackendDir makes the cwd look like a deployable backend so runBuild's
// no-op path is taken (no controllers/ → nothing to validate) and BuildTarball
// has something to package.
func seedBackendDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"app"}`), 0o644))
	t.Chdir(dir)
}

// THE deploy contract. The upload must go to the SELECTED ENVIRONMENT's v2
// ingress, and it MUST carry an Idempotency-Key — that header is the only thing
// standing between a timed-out push and a second deploy.
func TestPush_PalbaseProvider_PostsToV2WithAnIdempotencyKey(t *testing.T) {
	seedBackendDir(t)
	f := &fakeDeploy{}
	var out bytes.Buffer

	require.NoError(t, runPush(pushDeps{
		rest: f, sel: palbaseProject(t), out: &out,
		ctx: context.Background(), pollInterval: time.Millisecond,
	}))

	require.Len(t, f.uploads, 1)
	require.Equal(t, "/api/v2/projects/proj_1/environments/app1prod/deploy", f.uploads[0].path)
	require.NotEmpty(t, f.uploads[0].idempotencyKey, "the deploy upload MUST carry an Idempotency-Key")
	// The multipart form carries only the message: the Environment is in the URL,
	// and there is no `branch` field left to send.
	require.Equal(t, map[string]string{"message": "deploy via cli"}, f.uploads[0].fields)

	require.Equal(t, []string{"/api/v2/projects/proj_1/environments/app1prod/deployments/dep_1"}, f.statuses)
	require.Contains(t, out.String(), "deploy succeeded (version abc1234)")
}

// A TIMED-OUT upload is retried on the SAME key. Minting a fresh key per attempt
// is exactly the bug that deploys twice.
func TestPush_TimedOutUpload_RetriesWithTheSameKey(t *testing.T) {
	seedBackendDir(t)
	f := &fakeDeploy{failFirst: 1}

	require.NoError(t, runPush(pushDeps{
		rest: f, sel: palbaseProject(t), out: io.Discard,
		ctx: context.Background(), pollInterval: time.Millisecond, uploadRetries: 2,
	}))

	require.Len(t, f.uploads, 2, "the lost upload must be retried")
	require.Equal(t, f.uploads[0].idempotencyKey, f.uploads[1].idempotencyKey,
		"the retry MUST reuse the first key — a new key is a second deploy")
	require.NotEmpty(t, f.uploads[0].idempotencyKey)
}

// A server VERDICT (any *APIError, including the 409 in-progress) is NOT a lost
// response: retrying it would be pointless at best and confusing at worst.
func TestPush_ServerError_IsNotRetried(t *testing.T) {
	seedBackendDir(t)
	f := &fakeDeploy{apiErr: &transport.APIError{
		Code: "idempotency_key_in_progress", Status: http.StatusConflict,
		Description: "a request with this key is still running",
	}}

	err := runPush(pushDeps{
		rest: f, sel: palbaseProject(t), out: io.Discard,
		ctx: context.Background(), uploadRetries: 2,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "idempotency_key_in_progress")
	require.Len(t, f.uploads, 1, "a server verdict must not be retried")
}

func TestPush_EachInvocationMintsItsOwnKey(t *testing.T) {
	seedBackendDir(t)
	first := &fakeDeploy{}
	second := &fakeDeploy{}
	for _, f := range []*fakeDeploy{first, second} {
		require.NoError(t, runPush(pushDeps{
			rest: f, sel: palbaseProject(t), out: io.Discard,
			ctx: context.Background(), pollInterval: time.Millisecond,
		}))
	}
	require.NotEqual(t, first.uploads[0].idempotencyKey, second.uploads[0].idempotencyKey,
		"two separate deploys are two separate mutations")
}

func TestNewIdempotencyKey_IsRandomAndHex(t *testing.T) {
	a, b := transport.NewIdempotencyKey(), transport.NewIdempotencyKey()
	require.NotEqual(t, a, b)
	require.Len(t, a, 32)
	require.NotContains(t, a, "-")
}

// A failed deploy exits NON-ZERO carrying the server's reason — a silently
// broken deploy that scripts read as success is the failure mode this guards.
func TestPush_FailedDeploy_ExitsNonZero(t *testing.T) {
	seedBackendDir(t)
	f := &fakeDeploy{status: "failed"}

	err := runPush(pushDeps{
		rest: f, sel: palbaseProject(t), out: io.Discard,
		ctx: context.Background(), pollInterval: time.Millisecond,
	})
	require.ErrorContains(t, err, "deploy failed")
}

// ── github provider ─────────────────────────────────────────────────────────

func TestPush_GitHubProvider_ExecsGitPush_AndNeverUploads(t *testing.T) {
	seedBackendDir(t)
	f := &fakeDeploy{}
	var got []string
	var out bytes.Buffer

	sel := palbaseProject(t)
	sel.RepositoryProvider = selection.ProviderGitHub

	require.NoError(t, runPush(pushDeps{
		git:  func(name string, args ...string) error { got = append([]string{name}, args...); return nil },
		rest: f, sel: sel, out: &out, ctx: context.Background(),
	}))
	require.Equal(t, []string{"git", "push"}, got)
	require.Empty(t, f.uploads, "the github provider deploys via webhook — it must not upload a tarball")
	require.Contains(t, out.String(), "webhook")
}

func TestPush_GitHubProvider_PropagatesGitFailure(t *testing.T) {
	seedBackendDir(t)
	sel := palbaseProject(t)
	sel.RepositoryProvider = selection.ProviderGitHub

	err := runPush(pushDeps{
		git:  func(string, ...string) error { return fmt.Errorf("rejected: non-fast-forward") },
		rest: &fakeDeploy{}, sel: sel, out: io.Discard, ctx: context.Background(),
	})
	require.ErrorContains(t, err, "non-fast-forward")
}

// ── clone / pull ────────────────────────────────────────────────────────────

func TestRunClone_GitHubProvider_ClonesThenWritesConfigV2(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var got []string
	cfg := &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_prod",
		RepositoryProvider: selection.ProviderGitHub,
	}
	require.NoError(t, runClone(cloneDeps{
		git: func(name string, args ...string) error {
			got = append([]string{name}, args...)
			return os.MkdirAll("todoapp", 0o755)
		},
		provider: selection.ProviderGitHub,
		repoURL:  "https://github.com/pal-salih/todoapp.git",
		dir:      "todoapp",
		cfg:      cfg,
		writeCfg: selection.Save,
	}))
	require.Equal(t, []string{"git", "clone", "https://github.com/pal-salih/todoapp.git", "todoapp"}, got)

	loaded, err := selection.Load("todoapp")
	require.NoError(t, err)
	require.Equal(t, "proj_1", loaded.ProjectID)
	require.Equal(t, "env_prod", loaded.EnvironmentID)
	require.Equal(t, selection.ProviderGitHub, loaded.RepositoryProvider)
}

func TestRunPull_RoutesByProvider(t *testing.T) {
	t.Run("github runs git pull", func(t *testing.T) {
		var got []string
		require.NoError(t, runPull(pullDeps{
			provider: selection.ProviderGitHub,
			git:      func(name string, args ...string) error { got = append([]string{name}, args...); return nil },
		}))
		require.Equal(t, []string{"git", "pull"}, got)
	})
	t.Run("palbase refetches the bundle", func(t *testing.T) {
		called := false
		require.NoError(t, runPull(pullDeps{
			provider: selection.ProviderPalbase,
			refetch:  func() error { called = true; return nil },
		}))
		require.True(t, called)
	})
}

func TestRepoURLFromFullName(t *testing.T) {
	require.Equal(t, "https://github.com/org/repo.git", repoURLFromFullName("org/repo"))
	require.Empty(t, repoURLFromFullName(""))
	require.Equal(t, "repo", repoDirFromFullName("org/repo"))
}

// pullBundle asks Studio for the ENVIRONMENT's bundle by ref — one input, no
// branch.
func TestPullBundle_SendsOnlyTheEnvironmentRef(t *testing.T) {
	var gotInput map[string]any
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "backend.pull") {
			gotInput = decodeTRPCInput(t, req)
			trpcOK(w, map[string]any{"version": "abc", "archive": base64.StdEncoding.EncodeToString(tinyTarGz(t)), "size": 1})
			return
		}
		trpcOK(w, map[string]any{})
	})

	dst := t.TempDir()
	require.NoError(t, pullBundle(context.Background(), r.Resolvers(), "app1prod", dst, io.Discard))
	require.Equal(t, map[string]any{"ref": "app1prod"}, gotInput)
	require.NotContains(t, gotInput, "branch")
	require.FileExists(t, filepath.Join(dst, "hello.txt"))
}

func tinyTarGz(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	body := []byte("hi")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "hello.txt", Mode: 0o644, Size: int64(len(body))}))
	_, err := tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	return buf.Bytes()
}

func TestDeploymentIDFromResponse(t *testing.T) {
	require.Equal(t, "dep_9",
		deploymentIDFromResponse([]byte(`{"data":{"deploymentId":"dep_9"},"request_id":"r"}`)))
	require.Empty(t, deploymentIDFromResponse([]byte(`not json`)))
}
