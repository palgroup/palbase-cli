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
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/transport"
)

// ── the deploy paths ────────────────────────────────────────────────────────

func TestDeployPaths_AreEnvironmentScoped(t *testing.T) {
	// The push route names the Environment and nothing else: on this plane an
	// Environment IS a tenant.
	require.Equal(t, "/v1/cloud/projects/app1prod/push", PushPath("app1prod"))
	require.Equal(t,
		"/api/v2/projects/proj_1/environments/app1prod/deployments",
		DeploymentsPath("proj_1", "app1prod"))
}

// fakeDeploy records every call the push makes.
type fakeDeploy struct {
	calls []deployCall
	// apiErr, when set, is what the push call returns.
	apiErr error
	// result is what a successful push answers with.
	digest        string
	endpointCount int
	// onCall fires as each call is recorded, so a test can assert ORDER against
	// something that happens outside this client.
	onCall func()
}

type deployCall struct {
	method string
	path   string
	body   any
}

func (f *fakeDeploy) Do(_ context.Context, method, path string, body, out any) error {
	f.calls = append(f.calls, deployCall{method: method, path: path, body: body})
	if f.onCall != nil {
		f.onCall()
	}
	if f.apiErr != nil {
		return f.apiErr
	}
	res, ok := out.(*struct {
		Digest        string `json:"digest"`
		EndpointCount int    `json:"endpointCount"`
	})
	if !ok {
		return nil
	}
	res.Digest = f.digest
	if res.Digest == "" {
		res.Digest = "abc1234def5678"
	}
	res.EndpointCount = f.endpointCount
	if res.EndpointCount == 0 {
		res.EndpointCount = 47
	}
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

// seedBackendDir makes the cwd look like a deployable backend.
func seedBackendDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"app"}`), 0o644))
	t.Chdir(dir)
}

// stubBundler substitutes the bundling seam: production builds this project with
// its own node_modules and Bun, which a unit test has neither of.
func stubBundler(built *bool) (func(context.Context, string, io.Writer) ([]uploadUse, error), func(string) ([]byte, error)) {
	return func(context.Context, string, io.Writer) ([]uploadUse, error) {
			if built != nil {
				*built = true
			}
			return nil, nil
		}, func(string) ([]byte, error) {
			return []byte("gzip-artifact-bytes"), nil
		}
}

// THE PUSH CONTRACT. The CLIENT bundles and hands the plane an ARTIFACT.
//
// Where the bundler lives is decided by where the source enters the system:
// through the CLI it enters here. The plane's push route is a relay to the
// tenant's own management surface and has no builder to offer — this arm used to
// upload SOURCE to an ingress that would build it, and that ingress does not
// exist on this plane (measured live: 404).
func TestPush_PalbaseProvider_SendsTheBuiltArtifact(t *testing.T) {
	seedBackendDir(t)
	f := &fakeDeploy{}
	build, pack := stubBundler(nil)
	var out bytes.Buffer

	require.NoError(t, runPush(pushDeps{
		rest: f, sel: palbaseProject(t), out: &out,
		ctx: context.Background(), build: build, pack: pack,
	}))

	require.Len(t, f.calls, 1)
	require.Equal(t, http.MethodPost, f.calls[0].method)
	require.Equal(t, "/v1/cloud/projects/app1prod/push", f.calls[0].path)

	body, ok := f.calls[0].body.(map[string]any)
	require.True(t, ok, "the artifact travels as JSON, base64-encoded")
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("gzip-artifact-bytes")), body["artifact"])

	// ZERO ENDPOINTS IS A SILENT FAILURE, so the count is on the success line:
	// "pushed" alone is what let an artifact that serves nothing read as done.
	require.Contains(t, out.String(), "47 endpoint(s)")
	require.Contains(t, out.String(), "app1prod")
}

// A BUILD FAILURE SENDS NOTHING. Shipping a tree that does not compile is how a
// broken controller reaches a tenant and 500s on its first request.
func TestPush_BuildFailure_UploadsNothing(t *testing.T) {
	seedBackendDir(t)
	f := &fakeDeploy{}
	var out bytes.Buffer

	err := runPush(pushDeps{
		rest: f, sel: palbaseProject(t), out: &out,
		ctx: context.Background(),
		build: func(context.Context, string, io.Writer) ([]uploadUse, error) {
			return nil, errors.New("controllers/todo.controller.ts: return type must be a NAMED zod schema")
		},
		pack: func(string) ([]byte, error) {
			t.Fatal("nothing may be packed after a failed build")
			return nil, nil
		},
	})
	require.ErrorContains(t, err, "NAMED zod schema")
	require.Empty(t, f.calls, "a failed build must not reach the plane")
	require.Contains(t, out.String(), "nothing was pushed")
}

// The plane's verdict reaches the caller AS IS: `upload_bucket_missing` names
// the bucket somebody has to create, and swallowing it would leave a route that
// deploys green and 404s the first file.
func TestPush_PlaneVerdict_IsSurfaced(t *testing.T) {
	seedBackendDir(t)
	f := &fakeDeploy{apiErr: &transport.APIError{
		Code: "upload_bucket_missing", Status: http.StatusBadRequest,
		Description: "@Upload names bucket \"avatars\", which does not exist",
	}}
	build, pack := stubBundler(nil)

	err := runPush(pushDeps{
		rest: f, sel: palbaseProject(t), out: io.Discard,
		ctx: context.Background(), build: build, pack: pack,
	})
	require.ErrorContains(t, err, "upload_bucket_missing")
	require.Len(t, f.calls, 1)
}

func TestNewIdempotencyKey_IsRandomAndHex(t *testing.T) {
	a, b := transport.NewIdempotencyKey(), transport.NewIdempotencyKey()
	require.NotEqual(t, a, b)
	require.Len(t, a, 32)
	require.NotContains(t, a, "-")
}

// ── github provider ─────────────────────────────────────────────────────────

func TestPush_GitHubProvider_ExecsGitPush_AndNeverUploads(t *testing.T) {
	seedBackendDir(t)
	f := &fakeDeploy{}
	var got []string
	var out bytes.Buffer

	sel := palbaseProject(t)
	sel.RepositoryProvider = selection.ProviderGitHub
	mapped := "main"
	sel.Environment.SourceGitBranch = &mapped

	require.NoError(t, runPush(pushDeps{
		git:       func(name string, args ...string) error { got = append([]string{name}, args...); return nil },
		gitBranch: func() (string, error) { return "main", nil },
		rest:      f, sel: sel, out: &out, ctx: context.Background(),
	}))
	require.Equal(t, []string{"git", "push"}, got)
	require.Empty(t, f.calls, "the github provider deploys via webhook — it must not hand the plane an artifact")
	require.Contains(t, out.String(), "webhook")
}

func TestPush_GitHubProvider_PropagatesGitFailure(t *testing.T) {
	seedBackendDir(t)
	sel := palbaseProject(t)
	sel.RepositoryProvider = selection.ProviderGitHub
	mapped := "main"
	sel.Environment.SourceGitBranch = &mapped

	err := runPush(pushDeps{
		git:       func(string, ...string) error { return fmt.Errorf("rejected: non-fast-forward") },
		gitBranch: func() (string, error) { return "main", nil },
		rest:      &fakeDeploy{}, sel: sel, out: io.Discard, ctx: context.Background(),
	})
	require.ErrorContains(t, err, "non-fast-forward")
}

func TestPush_GitHubProvider_RejectsBranchThatDoesNotMapToSelectedEnvironment(t *testing.T) {
	seedBackendDir(t)
	sel := palbaseProject(t)
	sel.RepositoryProvider = selection.ProviderGitHub
	mapped := "develop"
	sel.Environment.SourceGitBranch = &mapped
	gitCalled := false

	err := runPush(pushDeps{
		git:       func(string, ...string) error { gitCalled = true; return nil },
		gitBranch: func() (string, error) { return "main", nil },
		sel:       sel,
		out:       io.Discard,
	})
	require.ErrorContains(t, err, `selected environment "production" maps Git branch "develop"`)
	require.False(t, gitCalled)
}

func TestPush_GitHubProvider_RejectsUnmappedEnvironment(t *testing.T) {
	sel := palbaseProject(t)
	sel.RepositoryProvider = selection.ProviderGitHub
	err := runPush(pushDeps{sel: sel, out: io.Discard})
	require.ErrorContains(t, err, "has no mapped Git branch")
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
		sel := palbaseProject(t)
		sel.RepositoryProvider = selection.ProviderGitHub
		mapped := "main"
		sel.Environment.SourceGitBranch = &mapped
		require.NoError(t, runPull(pullDeps{
			sel:       sel,
			git:       func(name string, args ...string) error { got = append([]string{name}, args...); return nil },
			gitBranch: func() (string, error) { return "main", nil },
		}))
		require.Equal(t, []string{"git", "pull"}, got)
	})
	t.Run("palbase refetches the bundle", func(t *testing.T) {
		called := false
		require.NoError(t, runPull(pullDeps{
			sel:     palbaseProject(t),
			refetch: func() error { called = true; return nil },
		}))
		require.True(t, called)
	})
}

func TestPull_GitHubProvider_RejectsWrongBranch(t *testing.T) {
	sel := palbaseProject(t)
	sel.RepositoryProvider = selection.ProviderGitHub
	mapped := "release"
	sel.Environment.SourceGitBranch = &mapped
	err := runPull(pullDeps{
		sel:       sel,
		git:       func(string, ...string) error { return nil },
		gitBranch: func() (string, error) { return "main", nil },
	})
	require.ErrorContains(t, err, `maps Git branch "release"`)
}

func TestRepoURLFromFullName(t *testing.T) {
	require.Equal(t, "https://github.com/org/repo.git", repoURLFromFullName("org/repo"))
	require.Empty(t, repoURLFromFullName(""))
	require.Equal(t, "repo", repoDirFromFullName("org/repo"))
}

// `palbase pull` brings back the SOURCE the project deployed, from the project.
//
// It used to ask the Studio over tRPC, and the Studio kept a copy of every push.
// This plane keeps none — a push is unpacked, built, and the tree deleted — so
// the project stores it beside its artifact and answers for it. `latest` is what
// is asked for: nobody knows their digest before they have pulled once.
func TestPullReadsTheSourceTheProjectServes(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		asked = req.URL.Path
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(tinyTarGz(t))
	}))
	defer srv.Close()

	dst := t.TempDir()
	err := fetchDeployedSource(context.Background(), Target{URL: srv.URL},
		Credentials{Value: "k", Kind: KindKey}, "app1prod", dst, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "/v1/management/deployments/latest/source", asked)
	require.FileExists(t, filepath.Join(dst, "hello.txt"))
}

// A project with nothing stored says so, and the CLI relays it. Reporting
// success here would replace a checkout with an empty directory.
func TestPullRefusesWhenTheProjectKeptNoSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found","error_description":"no source is kept for d1"}`))
	}))
	defer srv.Close()

	dst := t.TempDir()
	err := fetchDeployedSource(context.Background(), Target{URL: srv.URL},
		Credentials{Value: "k", Kind: KindKey}, "app1prod", dst, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no source is kept")
	require.Empty(t, dirEntries(t, dst), "a refused pull wrote into the checkout")
}

// A 200 with no body is the worst answer of the three: it extracts cleanly into
// nothing and reports a successful pull.
func TestPullRefusesAnEmptyArchive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dst := t.TempDir()
	err := fetchDeployedSource(context.Background(), Target{URL: srv.URL},
		Credentials{Value: "k", Kind: KindKey}, "app1prod", dst, io.Discard)
	require.ErrorContains(t, err, "empty archive")
}

func dirEntries(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	return entries
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

// A VERSION ON THIS PLANE IS AN ARTIFACT DIGEST — 64 hex characters. Printed in
// full it overruns the column and pushes every field after it off the line,
// which is what `palbase deploys` did the first time it had a real row to show.
func TestDeployVersion_IsShortened(t *testing.T) {
	full := "26420de2c2ef675c1e8116cb5bd5ed881761c32038af4573febbb9c6b1642188"
	require.Equal(t, "26420de2c2ef", deployVersion(&full))

	shortSHA := "abc1234"
	require.Equal(t, "abc1234", deployVersion(&shortSHA), "a value already short is left alone")
	require.Equal(t, "-", deployVersion(nil), "a failed attempt has no version and says so")
}

// THE DECLARED FIXTURES REACH THE STACK, AND THEY REACH IT FIRST.
//
// A release is graded before it gets traffic: the stack mints one identity per
// DECLARED fixture and runs the project's suites as them. Those declarations
// live in config/test-users.ts and reach the stack through
// PUT /v1/management/test-users/templates — which nothing called. Measured
// 2026-08-24: pushing todoapp to a freshly provisioned tenant was refused with
// "no test identity named \"demo\" … this run has none", and it would have been
// refused forever, because templates only ever arrived with a deploy that
// passed. A project declaring test users could not reach a NEW environment.
func TestPushShipsTheDeclaredTestUsersBeforeTheArtifact(t *testing.T) {
	seedBackendDir(t)
	var order []string
	f := &fakeDeploy{onCall: func() { order = append(order, "artifact") }}
	build, pack := stubBundler(nil)

	err := runPush(pushDeps{
		rest: f, sel: palbaseProject(t), out: io.Discard,
		ctx: context.Background(), build: build, pack: pack,
		shipTestUsers: func(context.Context, string, io.Writer) error {
			order = append(order, "fixtures")
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"fixtures", "artifact"}, order,
		"the fixtures must be on the stack BEFORE the release is graded against them")
}

// A stack that refuses the declaration stops the push. Continuing would send an
// artifact whose own tests cannot pass, and report the refusal as a test failure
// — sending somebody to read suites that were never the problem.
func TestPushStopsWhenTheFixturesAreRefused(t *testing.T) {
	seedBackendDir(t)
	f := &fakeDeploy{}
	build, pack := stubBundler(nil)

	err := runPush(pushDeps{
		rest: f, sel: palbaseProject(t), out: io.Discard,
		ctx: context.Background(), build: build, pack: pack,
		shipTestUsers: func(context.Context, string, io.Writer) error {
			return errors.New("the stack refused the declaration")
		},
	})
	require.ErrorContains(t, err, "refused the declaration")
	require.Empty(t, f.calls, "an artifact was sent after the fixtures were refused")
}

// A project that declares none says nothing and writes nothing: most projects
// declare no fixtures, and a line about an empty declaration would be noise on
// every push.
func TestShippingIsSilentWhenNothingIsDeclared(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".palbase"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".palbase", "config.json"),
		[]byte(`{"storage":{"buckets":[]}}`), 0o644))

	var out bytes.Buffer
	require.NoError(t, shipDeclaredTestUsers(context.Background(), dir, &out))
	require.Empty(t, out.String())
}
