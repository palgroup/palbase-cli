package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/palgroup/palbase-cli/internal/transport"
)

// Compile-time guard: the real mgmt client must satisfy deployClient, so the
// Task 12 command can wire *transport.Client straight in. A signature drift
// breaks the build here, not at the wiring site.
var _ deployClient = (*transport.Client)(nil)

// Compile-time guard: the real mgmt client must satisfy the backend.REST
// accessor type (Do + PostMultipart) that main.go wires into Resolvers.REST.
// A signature drift breaks the build here, not at the main.go wiring site.
var _ REST = (*transport.Client)(nil)

func TestResolveMode(t *testing.T) {
	cases := []struct {
		name string
		cfg  *auth.ProjectConfig
		want string
	}{
		{"explicit platform", &auth.ProjectConfig{Ref: "r", Mode: "platform"}, "platform"},
		{"explicit github", &auth.ProjectConfig{Ref: "r", Mode: "github"}, "github"},
		// Absent mode defaults to PLATFORM: github is opt-in and always written
		// explicitly by `palbase clone` of a GitHub project; every other link path
		// (picker / web link / hand-written config) targets a platform project and
		// omits mode. Defaulting absent→github made platform `push` run `git push`
		// and fail (wave-4 w4budget). Only explicit "github" takes the git path.
		{"empty defaults to platform", &auth.ProjectConfig{Ref: "r", Mode: ""}, "platform"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := auth.SaveProjectConfigIn(dir, c.cfg); err != nil {
				t.Fatal(err)
			}
			wd, _ := os.Getwd()
			t.Cleanup(func() { _ = os.Chdir(wd) })
			_ = os.Chdir(dir)
			got, err := resolveMode()
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("resolveMode = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPush_GithubMode_ExecsGitPush(t *testing.T) {
	dir := t.TempDir()
	_ = auth.SaveProjectConfigIn(dir, &auth.ProjectConfig{Ref: "r", DefaultEnv: "main", Mode: "github"})
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(dir)

	var gotArgs []string
	runner := func(name string, args ...string) error {
		gotArgs = append([]string{name}, args...)
		return nil
	}
	if err := runPush(pushDeps{git: runner, rest: nil, branch: "main"}); err != nil {
		t.Fatalf("runPush: %v", err)
	}
	if len(gotArgs) < 2 || gotArgs[0] != "git" || gotArgs[1] != "push" {
		t.Fatalf("git args = %v, want git push prefix", gotArgs)
	}
}

func TestPush_PlatformMode_BuildsAndPostsTarball(t *testing.T) {
	dir := t.TempDir()
	_ = auth.SaveProjectConfigIn(dir, &auth.ProjectConfig{Ref: "todoapp", DefaultEnv: "main", Mode: "platform"})
	_ = os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export const x=1"), 0o644)
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(dir)

	var posted struct {
		path   string
		hadTar bool
	}
	fakeREST := &fakeDeployClient{onPostMultipart: func(path string, tarball []byte, fields map[string]string) ([]byte, error) {
		posted.path = path
		posted.hadTar = len(tarball) > 0
		return []byte(`{"deploymentId":"dep_1"}`), nil
	}}
	if err := runPush(pushDeps{git: func(string, ...string) error { return nil }, rest: fakeREST, branch: "main"}); err != nil {
		t.Fatalf("runPush: %v", err)
	}
	if posted.path != "/api/v1/projects/todoapp/deploy" {
		t.Fatalf("path = %q", posted.path)
	}
	if !posted.hadTar {
		t.Fatal("expected a tarball in the multipart post")
	}
}

func TestPush_PrintsSuccess(t *testing.T) {
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })

	t.Run("platform mode prints deploy-started + deployment id", func(t *testing.T) {
		dir := t.TempDir()
		_ = auth.SaveProjectConfigIn(dir, &auth.ProjectConfig{Ref: "todoapp", DefaultEnv: "main", Mode: "platform"})
		_ = os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export const x=1"), 0o644)
		_ = os.Chdir(dir)

		rest := &fakeDeployClient{onPostMultipart: func(string, []byte, map[string]string) ([]byte, error) {
			// the real deploy route wraps the payload in a {data:...} envelope
			return []byte(`{"data":{"workflowId":"wf1","runId":"r1","deploymentId":"dep_42"},"request_id":"req_x"}`), nil
		}}
		var out bytes.Buffer
		if err := runPush(pushDeps{git: func(string, ...string) error { return nil }, rest: rest, branch: "main", out: &out}); err != nil {
			t.Fatalf("runPush: %v", err)
		}
		got := out.String()
		if !strings.Contains(got, "✓ deploy started for todoapp") {
			t.Fatalf("missing success line; got:\n%s", got)
		}
		if !strings.Contains(got, "dep_42") {
			t.Fatalf("missing deployment id; got:\n%s", got)
		}
	})

	t.Run("github mode prints pushed-to-github", func(t *testing.T) {
		dir := t.TempDir()
		_ = auth.SaveProjectConfigIn(dir, &auth.ProjectConfig{Ref: "r", DefaultEnv: "main", Mode: "github"})
		_ = os.Chdir(dir)

		var out bytes.Buffer
		if err := runPush(pushDeps{git: func(string, ...string) error { return nil }, branch: "main", out: &out}); err != nil {
			t.Fatalf("runPush: %v", err)
		}
		if !strings.Contains(out.String(), "✓ pushed to GitHub") {
			t.Fatalf("missing github success line; got:\n%s", out.String())
		}
	})
}

type fakeDeployClient struct {
	onPostMultipart func(path string, tarball []byte, fields map[string]string) ([]byte, error)
	// onDo stubs the status-poll GET. When nil, Do returns a terminal
	// "succeeded" status so the older push tests (which only assert the
	// deploy-started lines) don't hang on the poll loop.
	onDo func(ctx context.Context, method, path string, body, out any) error
}

func (f *fakeDeployClient) PostMultipart(path string, tarball []byte, fields map[string]string) ([]byte, error) {
	return f.onPostMultipart(path, tarball, fields)
}

func (f *fakeDeployClient) Do(ctx context.Context, method, path string, body, out any) error {
	if f.onDo != nil {
		return f.onDo(ctx, method, path, body, out)
	}
	// Default: report the deploy as already succeeded on the first poll.
	if st, ok := out.(*deploymentStatus); ok {
		*st = deploymentStatus{Status: "succeeded", Version: "sha-default"}
	}
	return nil
}

// deployStartedBody is the deploy POST's success envelope carrying a
// deploymentId, so runPush proceeds into the poll loop.
func deployStartedBody(id string) []byte {
	return []byte(fmt.Sprintf(`{"data":{"workflowId":"wf1","runId":"r1","deploymentId":%q},"request_id":"req_x"}`, id))
}

// pushForPoll runs runPush in a temp platform-mode project with a fast poll
// interval and the given status-poll stub, returning combined output. stderr is
// redirected into the same buffer so the ✗/dots land where the test can read
// them (waitForDeploy writes failures + progress to os.Stderr).
func pushForPoll(t *testing.T, onDo func(ctx context.Context, method, path string, body, out any) error) (string, error) {
	t.Helper()
	dir := t.TempDir()
	_ = auth.SaveProjectConfigIn(dir, &auth.ProjectConfig{Ref: "todoapp", DefaultEnv: "main", Mode: "platform"})
	_ = os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export const x=1"), 0o644)
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(dir)

	// Capture os.Stderr (waitForDeploy's failure/progress sink) via an OS pipe.
	origStderr := os.Stderr
	rPipe, wPipe, _ := os.Pipe()
	os.Stderr = wPipe
	t.Cleanup(func() { os.Stderr = origStderr })

	rest := &fakeDeployClient{
		onPostMultipart: func(string, []byte, map[string]string) ([]byte, error) {
			return deployStartedBody("dep_42"), nil
		},
		onDo: onDo,
	}
	var stdout bytes.Buffer
	err := runPush(pushDeps{
		git:          func(string, ...string) error { return nil },
		rest:         rest,
		branch:       "main",
		out:          &stdout,
		pollInterval: time.Millisecond,
		pollTimeout:  2 * time.Second,
	})

	_ = wPipe.Close()
	os.Stderr = origStderr
	var stderr bytes.Buffer
	_, _ = stderr.ReadFrom(rPipe)
	return stdout.String() + stderr.String(), err
}

func TestPush_Poll_SucceededExitsZeroWithVersion(t *testing.T) {
	out, err := pushForPoll(t, func(_ context.Context, _, _ string, _, out any) error {
		if st, ok := out.(*deploymentStatus); ok {
			*st = deploymentStatus{Status: "succeeded", Version: "sha-deadbeef"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error on success, got: %v", err)
	}
	if !strings.Contains(out, "✓ deploy succeeded (version sha-deadbeef)") {
		t.Fatalf("missing success line; got:\n%s", out)
	}
}

func TestPush_Poll_FailedExitsNonZeroWithServerError(t *testing.T) {
	const serverErr = `push to backend: migration failed: ERROR: column "id" cannot be cast automatically to type bigint (SQLSTATE 42804)`
	out, err := pushForPoll(t, func(_ context.Context, _, _ string, _, out any) error {
		if st, ok := out.(*deploymentStatus); ok {
			*st = deploymentStatus{Status: "failed", CurrentStep: "Pushing to runtime", Error: serverErr}
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected a non-nil error (non-zero exit) on a failed deploy")
	}
	// The EXACT server error (SQLSTATE / drift text) must reach the user.
	if !strings.Contains(out, "✗ deploy FAILED:") || !strings.Contains(out, "SQLSTATE 42804") {
		t.Fatalf("failed deploy did not surface the server error; got:\n%s", out)
	}
}

func TestPush_Poll_TimeoutExitsZeroWithNote(t *testing.T) {
	// Never terminal: always "deploying" → the loop must hit the deadline and
	// return nil (don't falsely fail a slow-but-fine deploy).
	out, err := pushForPoll(t, func(_ context.Context, _, _ string, _, out any) error {
		if st, ok := out.(*deploymentStatus); ok {
			*st = deploymentStatus{Status: "deploying", CurrentStep: "Building"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error on timeout (slow-but-fine deploy), got: %v", err)
	}
	if !strings.Contains(out, "deploy still running after") || !strings.Contains(out, "palbase status") {
		t.Fatalf("missing timeout note; got:\n%s", out)
	}
}

func TestPush_Poll_404FallsBackToFireAndForget(t *testing.T) {
	// An un-upgraded server without the status route 404s — fall back to the
	// fire-and-forget note and exit 0, never hard-break.
	out, err := pushForPoll(t, func(_ context.Context, _, _ string, _, _ any) error {
		return &transport.APIError{Code: "not_found", Status: http.StatusNotFound}
	})
	if err != nil {
		t.Fatalf("expected nil error on 404 fallback, got: %v", err)
	}
	if !strings.Contains(out, "deploy status unavailable") || !strings.Contains(out, "palbase status") {
		t.Fatalf("missing fallback note; got:\n%s", out)
	}
}

// fakeStudioServer builds an httptest.Server that acts as a minimal tRPC
// endpoint for backend.pull. It returns a tar.gz containing the given files
// (name → content), base64-encoded in the tRPC success envelope.
func fakeStudioServer(t *testing.T, version string, files map[string]string) *studio.Client {
	t.Helper()
	// Build a real tar.gz to return.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprint(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gw.Close()
	archiveB64 := base64.StdEncoding.EncodeToString(buf.Bytes())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// tRPC success envelope wrapping {version, archive, size}.
		resp := map[string]any{
			"result": map[string]any{
				"data": map[string]any{
					"json": map[string]any{
						"version": version,
						"archive": archiveB64,
						"size":    len(buf.Bytes()),
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, nil)
}

func TestClone_GithubMode_ExecsGitClone(t *testing.T) {
	var gotArgs []string
	runner := func(name string, args ...string) error { gotArgs = append([]string{name}, args...); return nil }

	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(dir)

	err := runClone(cloneDeps{
		git:      runner,
		mode:     "github",
		repoURL:  "https://github.com/palcore/app.git",
		ref:      "app",
		branch:   "main",
		writeCfg: auth.SaveProjectConfigIn,
	})
	if err != nil {
		t.Fatalf("runClone: %v", err)
	}
	if len(gotArgs) < 3 || gotArgs[0] != "git" || gotArgs[1] != "clone" {
		t.Fatalf("git args = %v, want git clone <url>", gotArgs)
	}
}

func TestPull_GithubMode_ExecsGitPull(t *testing.T) {
	dir := t.TempDir()
	_ = auth.SaveProjectConfigIn(dir, &auth.ProjectConfig{Ref: "r", DefaultEnv: "main", Mode: "github"})
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(dir)

	var gotArgs []string
	runner := func(name string, args ...string) error { gotArgs = append([]string{name}, args...); return nil }
	if err := runPull(pullDeps{git: runner}); err != nil {
		t.Fatalf("runPull: %v", err)
	}
	if len(gotArgs) < 2 || gotArgs[1] != "pull" {
		t.Fatalf("git args = %v, want git pull", gotArgs)
	}
}

func TestClone_PlatformMode_WithoutDownloaderErrors(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(dir)
	err := runClone(cloneDeps{
		git:      func(string, ...string) error { return nil },
		mode:     "platform",
		ref:      "app",
		branch:   "main",
		writeCfg: auth.SaveProjectConfigIn,
	})
	if err == nil {
		t.Fatal("expected platform-mode clone without a downloader to error")
	}
}

// fakeProjectREST stubs the REST surface lookupProjectMode hits. Its Do mirrors
// the real transport.Client.Do contract: that method unwraps the `{data: {...}}`
// success envelope and unmarshals the INNER project object into out — so here we
// populate out with just that inner object (mode + github_repo), not the
// envelope. github_repo is null in platform mode (repo == "").
type fakeProjectREST struct {
	mode string
	repo string // "org/repo" full name; "" → github_repo null
}

func (f *fakeProjectREST) Do(ctx context.Context, method, path string, body, out any) error {
	payload := map[string]any{"ref": "todoapp", "mode": f.mode, "github_repo": nil}
	if f.repo != "" {
		payload["github_repo"] = f.repo
	}
	b, _ := json.Marshal(payload)
	return json.Unmarshal(b, out)
}

func (f *fakeProjectREST) PostMultipart(path string, tarball []byte, fields map[string]string) ([]byte, error) {
	return nil, nil
}

func TestLookupProjectMode_Github(t *testing.T) {
	r := Resolvers{REST: func() REST { return &fakeProjectREST{mode: "github", repo: "palcore/app"} }}
	mode, repoURL, err := lookupProjectMode(context.Background(), r, "todoapp")
	if err != nil {
		t.Fatal(err)
	}
	if mode != "github" {
		t.Fatalf("mode=%q", mode)
	}
	if repoURL != "https://github.com/palcore/app.git" {
		t.Fatalf("repoURL=%q", repoURL)
	}
}

func TestLookupProjectMode_Platform(t *testing.T) {
	r := Resolvers{REST: func() REST { return &fakeProjectREST{mode: "platform"} }}
	mode, repoURL, err := lookupProjectMode(context.Background(), r, "todoapp")
	if err != nil {
		t.Fatal(err)
	}
	if mode != "platform" {
		t.Fatalf("mode=%q", mode)
	}
	if repoURL != "" {
		t.Fatalf("repoURL=%q", repoURL)
	}
}

// TestClone_PlatformMode_DownloadsAndExtractsBundleAndWritesConfig tests the
// happy path: a wired download func is called, extracts files into ./<ref>/,
// and writes platform-mode .palbase/config.json.
func TestClone_PlatformMode_DownloadsAndExtractsBundleAndWritesConfig(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(dir)

	sc := fakeStudioServer(t, "sha-abc123", map[string]string{
		"index.ts": "export const x = 1",
	})
	r := Resolvers{Studio: func() *studio.Client { return sc }}

	var out bytes.Buffer
	var downloadCalled bool
	download := func(ref, branch string) error {
		downloadCalled = true
		dst := ref
		if err := platformPullBundle(context.Background(), r, ref, branch, dst, &out); err != nil {
			return err
		}
		return auth.SaveProjectConfigIn(dst, &auth.ProjectConfig{
			Ref: ref, DefaultEnv: branch, Mode: "platform",
		})
	}
	err := runClone(cloneDeps{
		git:      func(string, ...string) error { return nil },
		mode:     "platform",
		ref:      "todoapp",
		branch:   "main",
		writeCfg: auth.SaveProjectConfigIn,
		download: download,
	})
	if err != nil {
		t.Fatalf("runClone: %v", err)
	}
	if !downloadCalled {
		t.Fatal("download func was not called")
	}
	// Bundle was extracted into ./todoapp/
	if _, err := os.Stat(filepath.Join("todoapp", "index.ts")); err != nil {
		t.Fatalf("expected todoapp/index.ts to exist: %v", err)
	}
	// Config was written with mode=platform — read it directly to avoid needing
	// a LoadProjectConfigFrom helper that doesn't exist.
	cfgData, err := os.ReadFile(filepath.Join("todoapp", ".palbase", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg auth.ProjectConfig
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.Mode != "platform" {
		t.Fatalf("config mode = %q, want platform", cfg.Mode)
	}
	if cfg.Ref != "todoapp" {
		t.Fatalf("config ref = %q", cfg.Ref)
	}
	// Success line was printed
	if !strings.Contains(out.String(), "✓ pulled todoapp") {
		t.Fatalf("missing success line; got: %s", out.String())
	}
}

// TestPull_PlatformMode_RefetchesBundle tests that runPull with a wired
// refetch func downloads and overwrites files in cwd.
func TestPull_PlatformMode_RefetchesBundle(t *testing.T) {
	dir := t.TempDir()
	_ = auth.SaveProjectConfigIn(dir, &auth.ProjectConfig{
		Ref: "todoapp", DefaultEnv: "main", Mode: "platform",
	})
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(dir)

	sc := fakeStudioServer(t, "sha-def456", map[string]string{
		"controllers/todo.controller.ts": "// updated",
	})
	r := Resolvers{Studio: func() *studio.Client { return sc }}

	var out bytes.Buffer
	var refetchCalled bool
	refetch := func() error {
		refetchCalled = true
		cfg, err := auth.LoadProjectConfig()
		if err != nil {
			return err
		}
		return platformPullBundle(context.Background(), r, cfg.Ref, cfg.DefaultEnv, dir, &out)
	}
	if err := runPull(pullDeps{git: func(string, ...string) error { return nil }, refetch: refetch}); err != nil {
		t.Fatalf("runPull: %v", err)
	}
	if !refetchCalled {
		t.Fatal("refetch func was not called")
	}
	// File was extracted into cwd.
	if _, err := os.Stat(filepath.Join(dir, "controllers", "todo.controller.ts")); err != nil {
		t.Fatalf("expected controllers/todo.controller.ts to exist: %v", err)
	}
	if !strings.Contains(out.String(), "✓ pulled todoapp") {
		t.Fatalf("missing success line; got: %s", out.String())
	}
}
