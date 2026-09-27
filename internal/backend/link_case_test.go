package backend

// link_case_test.go — ONE ENVIRONMENT, ONE SPELLING, ON EVERY DISK (FR-012).
//
// On a case-insensitive disk — every Mac's — `Staging/` and `staging/` are one
// directory, and a build that looks its environment up by exact name (the
// 2.4 Gradle plugin does, so a Linux CI agrees with a Mac) finds only the
// spelling on disk. An environment renamed by case alone must come out of the
// link under the name the project gives it now.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// underEnvironments is one file's path inside the checkout, as a publish keys it.
func underEnvironments(env, file string) string {
	return filepath.Join(RootDir(), envSubdir, env, file)
}

// A DIRECTORY RENAMED BY CASE ALONE REACHES THE CHECKOUT. Publishing goes file
// by file, and on a case-insensitive disk `staging/openapi.json` is a path
// `Staging/openapi.json` already answers to: the publish took the renamed file
// for one that appeared during the link and refused everything. The checkout
// ends up spelled as the stage is, holding exactly the stage's files.
func TestAPublishCarriesADirectoryRenamedByCaseAlone(t *testing.T) {
	root := t.TempDir()
	envs := filepath.Join(root, RootDir(), envSubdir)
	require.NoError(t, os.MkdirAll(filepath.Join(envs, "Staging"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, underEnvironments("Staging", "openapi.json")), []byte("old contract"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, underEnvironments("Staging", "PalbaseGenerated.swift")), []byte("old client"), 0o644))
	before := map[string]artifactFile{
		underEnvironments("Staging", "openapi.json"):           {[]byte("old contract"), 0o644},
		underEnvironments("Staging", "PalbaseGenerated.swift"): {[]byte("old client"), 0o644},
	}
	after := map[string]artifactFile{
		underEnvironments("staging", "openapi.json"):        {[]byte("new contract"), 0o644},
		underEnvironments("staging", "android-config.json"): {[]byte("new config"), 0o600},
	}

	require.NoError(t, publishArtifacts(root, before, after))

	require.Equal(t, []string{"staging"}, entriesIn(t, envs))
	require.Equal(t, []string{"android-config.json", "openapi.json"}, entriesIn(t, filepath.Join(envs, "staging")))
	raw, err := os.ReadFile(filepath.Join(envs, "staging", "openapi.json"))
	require.NoError(t, err)
	require.Equal(t, "new contract", string(raw))
}

// AND A PUBLISH THAT IS REFUSED TAKES THE RENAME BACK: "previous artifacts
// were preserved" covers the directory's spelling too. Somebody edited a file
// while the link ran, so nothing is published.
func TestARefusedPublishLeavesTheDirectorySpelledAsItWas(t *testing.T) {
	root := t.TempDir()
	envs := filepath.Join(root, RootDir(), envSubdir)
	require.NoError(t, os.MkdirAll(filepath.Join(envs, "Staging"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, underEnvironments("Staging", "openapi.json")), []byte("edited meanwhile"), 0o644))
	before := map[string]artifactFile{underEnvironments("Staging", "openapi.json"): {[]byte("old contract"), 0o644}}
	after := map[string]artifactFile{underEnvironments("staging", "openapi.json"): {[]byte("new contract"), 0o644}}

	require.ErrorContains(t, publishArtifacts(root, before, after), "changed during link")

	require.Equal(t, []string{"Staging"}, entriesIn(t, envs))
	raw, err := os.ReadFile(filepath.Join(root, underEnvironments("Staging", "openapi.json")))
	require.NoError(t, err)
	require.Equal(t, "edited meanwhile", string(raw))
}

// caseInsensitiveDisk answers whether dir's disk takes `a` and `A` for one name.
func caseInsensitiveDisk(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "caseprobe")
	require.NoError(t, os.Mkdir(probe, 0o755))
	defer func() { require.NoError(t, os.Remove(probe)) }()
	_, err := os.Stat(filepath.Join(dir, "CASEPROBE"))
	return err == nil
}

// seedUnder commits one file into an environment directory under root.
func seedUnder(t *testing.T, root, env, file, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, RootDir(), envSubdir, env), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, underEnvironments(env, file)), []byte(body), 0o644))
}

// AN ENVIRONMENT IN ANOTHER LETTER CASE IS NOT A LOST ONE. The sweep compared
// names exactly, so `Staging/` read as an environment the project no longer
// has once the project spelled it `staging` — and went, files and all. It is
// renamed instead, with everything it holds.
func TestTheSweepRenamesADirectoryWhoseNameDiffersOnlyInCase(t *testing.T) {
	root := t.TempDir()
	seedUnder(t, root, "Staging", "openapi.json", "staging's contract")

	var out strings.Builder
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main", "staging"}, selectedLeftover, &out))

	require.Equal(t, []string{"staging"}, entriesIn(t, filepath.Join(root, RootDir(), envSubdir)))
	raw, err := os.ReadFile(filepath.Join(root, underEnvironments("staging", "openapi.json")))
	require.NoError(t, err)
	require.Equal(t, "staging's contract", string(raw))
	require.Equal(t, "renamed palbase/environments/Staging to staging — the project spells that environment "+
		"staging now, and a build finds its directory by the exact name\n", out.String())
}

// AND IN ANOTHER UNICODE FORM, which APFS ignores the same way: `café` written
// with two code points is the directory the project now spells with one. It is
// renamed, never swept — on a Mac this link has just written the new
// spelling's files into it, as it does for a case-only rename. (A
// case-sensitive APFS volume, which still ignores normalisation, keeps the old
// bytes through the rename; the one directory answers to the listed name on
// every disk, and that is what is measured.)
func TestTheSweepRenamesADirectoryWhoseNameDiffersOnlyInUnicodeForm(t *testing.T) {
	const composed, decomposed = "caf\u00e9", "cafe\u0301"
	root := t.TempDir()
	seedUnder(t, root, decomposed, "openapi.json", "its contract")

	var out strings.Builder
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main", composed}, selectedLeftover, &out))

	require.Len(t, entriesIn(t, filepath.Join(root, RootDir(), envSubdir)), 1)
	raw, err := os.ReadFile(filepath.Join(root, underEnvironments(composed, "openapi.json")))
	require.NoError(t, err)
	require.Equal(t, "its contract", string(raw))
	require.Equal(t, "renamed palbase/environments/\""+decomposed+"\" to \""+composed+"\" — the project spells that environment \""+
		composed+"\" now, and a build finds its directory by the exact name\n", out.String())
}

// TWO LISTED NAMES THAT SHARE ONE DIRECTORY (FR-003) say nothing about which
// spelling it should have: both were skipped, and skipped is not gone.
func TestTheSweepLeavesADirectoryTwoListedNamesShare(t *testing.T) {
	root := t.TempDir()
	seedUnder(t, root, "STAGING", "openapi.json", "one of the twins")

	var out strings.Builder
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main", "Staging", "staging"}, selectedLeftover, &out))

	require.Equal(t, []string{"STAGING"}, entriesIn(t, filepath.Join(root, RootDir(), envSubdir)))
	require.Empty(t, out.String())
}

// `local/` IS NEVER MADE OUT OF SOMETHING ELSE. `Local/` is what a cloud
// environment of that name left behind; when this machine's stack was not
// written this run, nothing says the directory holds it — so it is swept like
// any environment the project no longer has, never renamed into local/.
func TestTheSweepNeverRenamesALeftoverIntoLocal(t *testing.T) {
	root := t.TempDir()
	seedUnder(t, root, "Local", "android-config.json", `{"base_url":"https://cloud.example"}`)

	var out strings.Builder
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main"}, selectedLeftover, &out))

	require.Empty(t, entriesIn(t, filepath.Join(root, RootDir(), envSubdir)))
	require.Equal(t, "removed palbase/environments/Local (the project no longer has that environment)\n", out.String())
}

// ON A CASE-SENSITIVE DISK BOTH SPELLINGS CAN EXIST — a Linux CI's, after a
// link wrote `staging/` beside the old `Staging/`. The exact one is the
// project's; the other goes, and the line says where its environment is now.
// (A Mac cannot hold the two, so there is nothing to measure there.)
func TestTheSweepRemovesTheOldSpellingBesideTheNewOne(t *testing.T) {
	root := t.TempDir()
	if caseInsensitiveDisk(t, root) {
		t.Skip("this disk takes Staging and staging for one name — there is no second directory to sweep")
	}
	seedUnder(t, root, "Staging", "openapi.json", "old")
	seedUnder(t, root, "staging", "openapi.json", "written this run")

	var out strings.Builder
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main", "staging"}, selectedLeftover, &out))

	require.Equal(t, []string{"staging"}, entriesIn(t, filepath.Join(root, RootDir(), envSubdir)))
	require.Equal(t, "removed palbase/environments/Staging (the project spells that environment staging now)\n", out.String())
}

// A LINK NEVER DELETES WHAT IT JUST WROTE. The project renamed `Staging` to
// `staging`; the link wrote staging's files, and on a Mac they landed in the
// directory that already existed, `Staging/`. The sweep then read `Staging/`
// as an environment the project no longer has and deleted it — measured for
// Apple (verification of 2026-09-25, A3): the link removed what it had just
// written. Whatever the disk, the checkout ends up with `staging/`, holding
// staging's configuration.
func TestALinkKeepsAnEnvironmentRenamedByCaseAlone(t *testing.T) {
	for _, c := range sweepCheckouts {
		t.Run(c.platform, func(t *testing.T) {
			inScratchCheckout(t)
			c.seed(t)
			seedEnvironment(t, "Staging", c.platform)
			main := stackServing(t, linkKeyMain, nil)
			staging := stackServing(t, linkKeyStaging, nil)
			routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
			o := linkOpts{
				url:       main.URL,
				platforms: []string{c.platform},
				linkedEnv: "main",
				product:   Product{ID: "prd_a", Name: "todoapp"},
				environments: []Environment{
					{Name: "main", Ref: "mainref000", Status: "Running"},
					{Name: "staging", Ref: "stagref000", Status: "Running"},
				},
			}

			var out strings.Builder
			require.NoError(t, runLink(context.Background(), o, &out), out.String())

			assert.Equal(t, []string{"main", "staging"}, entriesIn(t, filepath.Join(RootDir(), envSubdir)), out.String())
			assert.Equal(t, staging.URL, readEnvConfig(t, "staging", c.platform).BaseURL)

			// AND NO LINE SAYS IT WAS TAKEN (T014 report, concern 1: the link
			// printed "wrote …/staging/…" and then "removed …/Staging"). The
			// contract under staging/ is the one this run fetched, not the seeded
			// one. Where the disk takes both spellings for one name, Staging/ IS
			// the directory this run wrote into, so no "removed" line may appear
			// at all — it is renamed. Where it does not, the link wrote a new
			// staging/ beside the old Staging/, and the one removal is the old
			// spelling, said with where its environment is now.
			contract, err := os.ReadFile(SpecPath("staging"))
			require.NoError(t, err, out.String())
			assert.Contains(t, string(contract), `"3.2.0"`, out.String())
			var removed []string
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.HasPrefix(line, "removed ") {
					removed = append(removed, line)
				}
			}
			if caseInsensitiveDisk(t, t.TempDir()) {
				assert.Empty(t, removed, out.String())
				assert.Contains(t, out.String(), "renamed palbase/environments/Staging to staging — the project spells that "+
					"environment staging now, and a build finds its directory by the exact name\n")
			} else {
				assert.Equal(t, []string{"removed palbase/environments/Staging (the project spells that environment staging now)"},
					removed, out.String())
			}
		})
	}
}
