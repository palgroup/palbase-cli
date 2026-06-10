package backend

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/transport"
)

// Compile-time guard: the real mgmt client must satisfy deployClient, so the
// Task 12 command can wire *transport.Client straight in. A signature drift
// breaks the build here, not at the wiring site.
var _ deployClient = (*transport.Client)(nil)

func TestResolveMode(t *testing.T) {
	cases := []struct {
		name string
		cfg  *auth.ProjectConfig
		want string
	}{
		{"explicit platform", &auth.ProjectConfig{Ref: "r", Mode: "platform"}, "platform"},
		{"explicit github", &auth.ProjectConfig{Ref: "r", Mode: "github"}, "github"},
		{"empty defaults to github", &auth.ProjectConfig{Ref: "r", Mode: ""}, "github"},
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

type fakeDeployClient struct {
	onPostMultipart func(path string, tarball []byte, fields map[string]string) ([]byte, error)
}

func (f *fakeDeployClient) PostMultipart(path string, tarball []byte, fields map[string]string) ([]byte, error) {
	return f.onPostMultipart(path, tarball, fields)
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
