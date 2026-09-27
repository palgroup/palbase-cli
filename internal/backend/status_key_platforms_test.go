package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// EVERY PLATFORM WITH A CONFIG ON DISK IS COMPARED (FR-018). Key drift read the
// iOS configs alone, so in an Android or web checkout it found nothing, printed
// nothing, and `--json` answered "unchecked" about a key that was stale.

// seedKeys writes one platform's config per environment, each carrying its key.
func seedKeys(t *testing.T, platform, url string, keys map[string]string) {
	t.Helper()
	for env, key := range keys {
		require.NoError(t, os.MkdirAll(EnvDir(env), 0o755))
		blob, err := json.MarshalIndent(appEnvironment{AppID: projectAppID, BaseURL: url, APIKey: key}, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(ConfigPath(env, platform), blob, 0o644))
	}
}

// stackRunningHere is a stack `palbase start` runs for this checkout, handing
// out key; status compares the `local` environment against it.
func stackRunningHere(t *testing.T, key string) string {
	t.Helper()
	srv := stackServing(t, key, nil)
	require.NoError(t, WriteLocalTarget(Target{URL: srv.URL, Local: true}))
	require.NoError(t, StoreCredential(srv.URL, Credentials{Value: "k", Kind: KindKey}))
	return srv.URL
}

func appKeyOf(t *testing.T) any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(runStatus(t, true)), &doc))
	return doc["app_key"]
}

func TestKeyDriftReadsAnAndroidOnlyCheckout(t *testing.T) {
	inScratchCheckout(t)
	url := stackRunningHere(t, "pb_project_cLOCALNOW")
	seedKeys(t, "android", url, map[string]string{"local": "pb_project_cLOCALOLD"})

	assert.Contains(t, runStatus(t, false),
		"app key:      STALE (android) — local ships a key this project no longer hands out.\n"+
			"              Run `palbase link` to refresh it, then rebuild the app.\n")
	assert.Equal(t, "stale", appKeyOf(t))
}

// ONE STALE PLATFORM IS NOT HIDDEN BY THE OTHERS: each line names the platforms
// it speaks for, so "current" can no longer be read as "every config I ship".
func TestKeyDriftNamesEachPlatformItCompared(t *testing.T) {
	inScratchCheckout(t)
	url := stackRunningHere(t, "pb_project_cLOCALNOW")
	seedKeys(t, "ios", url, map[string]string{"local": "pb_project_cLOCALNOW"})
	seedKeys(t, "android", url, map[string]string{"local": "pb_project_cLOCALOLD"})
	seedKeys(t, webPlatform, url, map[string]string{"local": "pb_project_cLOCALNOW"})

	out := runStatus(t, false)
	assert.Contains(t, out, "app key:      STALE (android) — local ships a key this project no longer hands out.\n")
	assert.Contains(t, out, "app key:      current (ios, web)\n")
	assert.Equal(t, "stale", appKeyOf(t))
}

// A PLATFORM THAT DOES NOT CARRY THE ENVIRONMENT IS NAMED, not passed over:
// iOS being current says nothing about an Android config that is not there.
func TestKeyDriftNamesThePlatformThatLacksTheEnvironment(t *testing.T) {
	inScratchCheckout(t)
	url := stackRunningHere(t, "pb_project_cLOCALNOW")
	seedKeys(t, "ios", url, map[string]string{"local": "pb_project_cLOCALNOW"})
	seedKeys(t, "android", url, map[string]string{"main": "pb_project_cMAIN"})

	out := runStatus(t, false)
	assert.Contains(t, out, "app key:      unchecked (android) — local has no committed config here; `palbase link` writes it\n")
	assert.Contains(t, out, "app key:      current (ios)\n")
	assert.Equal(t, "unchecked", appKeyOf(t), "a platform that could not be compared is not current")
}

// A CONFIG THAT CARRIES NO KEY IS NAMED TOO — what an older CLI wrote for a
// stack that was down. Silence beside "current (ios)" read as "every key this
// checkout ships is fine" while the Android build shipped none.
func TestKeyDriftNamesAPlatformWhoseConfigCarriesNoKey(t *testing.T) {
	inScratchCheckout(t)
	url := stackRunningHere(t, "pb_project_cLOCALNOW")
	seedKeys(t, "ios", url, map[string]string{"local": "pb_project_cLOCALNOW"})
	seedKeys(t, "android", url, map[string]string{"local": ""})

	out := runStatus(t, false)
	assert.Contains(t, out, "app key:      unchecked (android) — local ships no key here; `palbase link` writes it\n")
	assert.Contains(t, out, "app key:      current (ios)\n")
	assert.Equal(t, "unchecked", appKeyOf(t), "a platform that ships no key is not current")
}

// A CONFIG THAT CANNOT BE READ IS NAMED WITH THE READ ERROR, never passed over:
// a platform nobody could compare is not current, whatever the others say.
func TestKeyDriftNamesAPlatformWhoseConfigCannotBeRead(t *testing.T) {
	inScratchCheckout(t)
	url := stackRunningHere(t, "pb_project_cLOCALNOW")
	seedKeys(t, "ios", url, map[string]string{"local": "pb_project_cLOCALNOW"})
	require.NoError(t, os.WriteFile(ConfigPath("local", "android"), []byte("not json"), 0o644))

	out := runStatus(t, false)
	assert.Contains(t, out, "app key:      unchecked (android) — a config here could not be read (read "+
		ConfigPath("local", "android")+": invalid character")
	assert.Contains(t, out, "app key:      current (ios)\n")
	assert.Equal(t, "unchecked", appKeyOf(t), "a platform whose config could not be read is not current")
}

// ONE READ ERROR IS SAID ONCE: when the environments directory itself cannot be
// listed, every platform fails the same way, and one line names them all.
func TestKeyDriftSaysOnceWhyNoConfigCouldBeRead(t *testing.T) {
	inScratchCheckout(t)
	stackRunningHere(t, "pb_project_cLOCALNOW")
	root := filepath.Dir(filepath.FromSlash(EnvDir("any")))
	require.NoError(t, os.MkdirAll(filepath.Dir(root), 0o755))
	require.NoError(t, os.WriteFile(root, []byte("not a directory"), 0o644))

	out := runStatus(t, false)
	assert.Contains(t, out, "app key:      unchecked (ios, macos, android, web) — a config here could not be read (")
	assert.Equal(t, 1, strings.Count(out, "app key:"), out)
	assert.Equal(t, "unchecked", appKeyOf(t))
}
