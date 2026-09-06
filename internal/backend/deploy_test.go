package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ── the deploy paths ────────────────────────────────────────────────────────

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

// THE FIXTURE-SHIPPING TESTS ARE GONE WITH THE BEHAVIOUR THEY LOCKED.
//
// `runPush` used to PUT `config/test-users.ts` at the stack before the artifact,
// and five tests pinned that ordering (a release is graded against fixtures, so
// they had to land first — measured 2026-08-24, when nothing shipped them and a
// fresh environment refused every push forever).
//
// The file is gone (2026-08-29) and the templates are written where they live:
// `palbase test-user templates set --file <path>`, locked by
// TestTemplatesSet_SendsTheTemplatesKey in internal/testuser. The push no longer
// has a fixture step, so there is no ordering left to pin here.

// THE GITHUB PROVIDER BRANCH IS GONE (T011).
//
// `repository_provider: github` routed push to `git push`, clone to `git clone`
// and pull to `git pull`, on the premise that a project's code lived in a repo
// the CLI drove. The v2 cloud addresses a project by its ref and takes an
// ARTIFACT; nothing mints a github-provider project any more, so the branch was
// a road to a country that closed.
//
// A branch nobody can reach is not free: it doubles the shapes every deploy verb
// has to be read against.
func TestDeployVerbsHaveOneProviderPath(t *testing.T) {
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	grep := exec.Command("grep", "-rn", "ProviderGitHub", "--include=*.go", root)
	found, _ := grep.Output()
	var live []string
	for _, line := range strings.Split(strings.TrimSpace(string(found)), "\n") {
		if line == "" || strings.Contains(line, "_test.go") {
			continue
		}
		live = append(live, line)
	}
	if len(live) > 0 {
		t.Errorf("the github provider branch survives in production code:\n%s", strings.Join(live, "\n"))
	}
}

// THE GITHUB-PROVIDER TESTS WENT WITH THE BRANCH (T011).
//
// They measured real behaviour: push exec'd `git push` and never uploaded, an
// unmapped branch was refused by name, a git failure propagated. All correct,
// and all about a provider nothing mints any more — the v2 cloud addresses a
// project by its ref and takes an ARTIFACT.
//
// What replaced them measures the absence:
// TestDeployVerbsHaveOneProviderPath greps production code for ProviderGitHub,
// because a branch can be cut from one verb and survive in another.

// TestRunPull_RoutesByProvider went with the routing (T011): there is one
// provider now, so there is no route to take.
