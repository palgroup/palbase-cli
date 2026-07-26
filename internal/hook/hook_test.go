package hook

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitInit makes a minimal git repo in dir so Ensure sees a real .git and can
// read/write core.hooksPath through real git.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
}

func hooksPath(t *testing.T, dir string) string {
	t.Helper()
	out, _ := exec.Command("git", "-C", dir, "config", "--get", "core.hooksPath").Output()
	return strings.TrimSpace(string(out))
}

func TestEnsure(t *testing.T) {
	// The two marker-less v1 bodies must upgrade; a foreign body must not.
	v1 := knownV1Bodies[0]
	foreign := "#!/bin/sh\necho my own hook\n"

	tests := []struct {
		name        string
		gitRepo     bool
		preHook     string // hook body to seed; "" = none
		preHooksCfg string // core.hooksPath to seed; "" = leave unset
		wantV2      bool   // hook file should end up == Body
		wantUnchgd  bool   // hook file should be byte-identical to preHook
		wantMsg     string // substring required in the reported output
		wantCfg     string // expected core.hooksPath after (checked only if non-"")
	}{
		{
			name: "fresh install writes v2 + sets hooksPath", gitRepo: true,
			wantV2: true, wantMsg: "installed hooks/pre-push", wantCfg: "hooks",
		},
		{
			name: "marker-less v1 upgrades to v2", gitRepo: true, preHook: v1,
			wantV2: true, wantMsg: "updated hooks/pre-push to v2", wantCfg: "hooks",
		},
		{
			name: "v2 is a no-op", gitRepo: true, preHook: Body,
			wantUnchgd: true, // no report line
		},
		{
			name: "foreign hook is left untouched", gitRepo: true, preHook: foreign,
			wantUnchgd: true, wantMsg: "isn't palbase's",
		},
		{
			name: "foreign hooksPath (husky) is not overridden", gitRepo: true,
			preHooksCfg: ".husky", wantV2: true, wantMsg: "core.hooksPath is .husky", wantCfg: ".husky",
		},
		{
			name: "no .git is a silent no-op", gitRepo: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.gitRepo {
				gitInit(t, dir)
			}
			if tc.preHooksCfg != "" {
				require.NoError(t, exec.Command("git", "-C", dir, "config", "core.hooksPath", tc.preHooksCfg).Run())
			}
			hookFile := filepath.Join(dir, "hooks", "pre-push")
			if tc.preHook != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(hookFile), 0o755))
				require.NoError(t, os.WriteFile(hookFile, []byte(tc.preHook), 0o755))
			}

			var out strings.Builder
			Ensure(dir, &out)

			if tc.wantMsg != "" {
				require.Contains(t, out.String(), tc.wantMsg)
			}
			if !tc.gitRepo {
				_, err := os.Stat(hookFile)
				require.True(t, os.IsNotExist(err), "no .git → hook must not be written")
				require.Empty(t, out.String(), "no .git → silent")
				return
			}

			body, err := os.ReadFile(hookFile)
			require.NoError(t, err)
			if tc.wantV2 {
				require.Equal(t, Body, string(body), "hook must be upgraded to v2")
				info, _ := os.Stat(hookFile)
				require.NotZero(t, info.Mode()&0o111, "hook must be executable")
			}
			if tc.wantUnchgd {
				require.Equal(t, tc.preHook, string(body), "hook must be left byte-identical")
			}
			if tc.wantCfg != "" {
				require.Equal(t, tc.wantCfg, hooksPath(t, dir))
			}
		})
	}
}

// TestEnsure_Idempotent verifies a second run after a fresh install is a no-op
// (no re-write, no extra output).
func TestEnsure_Idempotent(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	var first strings.Builder
	Ensure(dir, &first)
	require.Contains(t, first.String(), "installed hooks/pre-push")

	var second strings.Builder
	Ensure(dir, &second)
	require.Empty(t, second.String(), "second run must be a silent no-op")
}

// TestEnsure_UpgradesV1_Mutation is the M5 mutation lock: the v1→v2 overwrite is
// what heals the marker-less fleet. If isKnownV1 stops recognizing the v1 body
// (the mutation — remove the entry / return false), Ensure would treat it as a
// foreign hook and NOT upgrade it, and this assertion goes RED.
func TestEnsure_UpgradesV1_Mutation(t *testing.T) {
	require.True(t, isKnownV1(knownV1Bodies[0]), "v1 body must be recognized as ours — else Ensure won't upgrade it")

	dir := t.TempDir()
	gitInit(t, dir)
	hookFile := filepath.Join(dir, "hooks", "pre-push")
	require.NoError(t, os.MkdirAll(filepath.Dir(hookFile), 0o755))
	require.NoError(t, os.WriteFile(hookFile, []byte(knownV1Bodies[0]), 0o755))

	Ensure(dir, &strings.Builder{})

	body, err := os.ReadFile(hookFile)
	require.NoError(t, err)
	require.Equal(t, Body, string(body), "v1 hook must be overwritten with v2")
}

// A hook that carries OUR current marker but a DRIFTED body must be rewritten to
// Body. This is what keeps the orchestrator's scaffold-template copy of the hook
// from becoming a second, permanent source of truth: before this, `v >= Version`
// alone short-circuited and any divergent copy was pinned into a repo forever.
// MUTATION CHECK: drop `&& body == Body` from Ensure's no-op condition → RED.
func TestEnsure_RewritesDriftedCurrentVersionBody(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "hooks"), 0o755))
	// Same "# palbase-hook: v2" marker, different body (a stale template copy).
	drifted := "#!/bin/sh\n# palbase-hook: v2\n# an older shipped copy\nexit 0\n"
	hookPath := filepath.Join(dir, "hooks", "pre-push")
	require.NoError(t, os.WriteFile(hookPath, []byte(drifted), 0o755))

	Ensure(dir, io.Discard)

	got, err := os.ReadFile(hookPath)
	require.NoError(t, err)
	assert.Equal(t, Body, string(got), "a drifted copy of our own hook must be restored to Body")

	// And an identical copy is still left alone (no pointless rewrite churn).
	info, err := os.Stat(hookPath)
	require.NoError(t, err)
	Ensure(dir, io.Discard)
	info2, err := os.Stat(hookPath)
	require.NoError(t, err)
	assert.Equal(t, info.ModTime(), info2.ModTime(), "a byte-identical hook must be a no-op")
}
