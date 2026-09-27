package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A COMMITTED `local/` IS NAMED (FR-020). `link` keeps
// `palbase/environments/local/` out of git from now on, but an ignore rule does
// not untrack what a repository already holds: every clone still builds its
// debug variant against the laptop that committed it.

// committedLocalAdvice is the doctor line for it, exactly as printed.
const committedLocalAdvice = "  ✗ local      palbase/environments/local/ is committed — it holds one machine's stack address and key, " +
	"which every teammate's debug build would use; `git rm -r --cached palbase/environments/local`, then commit\n"

// gitRepoIn makes dir a real git repository and runs git there; global and
// system configuration point at nothing, so a person's hooks never reach it.
func gitRepoIn(t *testing.T, dir string) func(args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH — whether a file is tracked cannot be measured")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoError(t, err, "git %s\n%s", strings.Join(args, " "), out)
	}
	git("init", "-q")
	return git
}

// runDoctorIn runs the production `palbase doctor` in dir, against a cloud
// that answers nothing, and returns what it printed.
func runDoctorIn(t *testing.T, dir string) string {
	t.Helper()
	cloud := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(cloud.Close)
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_PLATFORM_URL", cloud.URL)
	t.Setenv("PALBASE_AUTH_URL", cloud.URL)
	t.Setenv("PALBASE_ACCESS_TOKEN", "")

	root := newRootCmd()
	root.SetArgs([]string{"doctor"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	require.NoError(t, root.Execute())
	return out.String()
}

func writeLocalConfig(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "palbase", "environments", "local", "android-config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"base_url":"http://127.0.0.1:54321"}`), 0o600))
}

func TestDoctorNamesACommittedLocalStackDirectory(t *testing.T) {
	dir := t.TempDir()
	git := gitRepoIn(t, dir)
	writeLocalConfig(t, dir)
	git("add", "--", "palbase")

	require.Contains(t, runDoctorIn(t, dir), committedLocalAdvice)
}

// ON DISK AND IGNORED, as `link` leaves it, is the healthy state: nothing said.
func TestDoctorSaysNothingOfALocalDirectoryGitIgnores(t *testing.T) {
	dir := t.TempDir()
	git := gitRepoIn(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("palbase/environments/local/\n"), 0o644))
	writeLocalConfig(t, dir)
	git("add", "-A")

	require.NotContains(t, runDoctorIn(t, dir), "✗ local")
}
