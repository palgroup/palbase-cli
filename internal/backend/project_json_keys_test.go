package backend

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// palbase/project.json CARRIES NO ENVIRONMENT REFERENCE (FR-013).
//
// The file is committed. An environment written into it is how a colleague
// pulls your branch and pushes to your staging — the reason Target.Project's own
// comment gives for never storing one there. A test that looked for a
// forbidden name ("env", "environment") would miss the next spelling; this one
// counts the keys the file may carry BY NAME, so a field added to Target
// tomorrow, for whatever reason, is refused until somebody decides it belongs in
// a committed file.

func TestProjectJSONCarriesOnlyItsNamedKeys(t *testing.T) {
	inScratchCheckout(t)
	// Every field set, so every key Target can serialise is measured.
	require.NoError(t, WriteTarget(Target{
		URL:      "https://shop.example",
		Project:  "prj_1",
		Name:     "shop",
		Insecure: true,
		SelfHost: true,
		OAuth:    map[string]OAuthSelection{"ios": {ApplicationKey: "app", Variant: "prod"}},
	}))
	raw, err := os.ReadFile(projectPath())
	require.NoError(t, err)
	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &keys))
	require.NotEmpty(t, keys)
	for key := range keys {
		require.Containsf(t, projectJSONKeys, key, "project.json carries %q, a key nobody allowed into a committed file", key)
	}
}

func TestProjectJSONRefusesAnEnvironmentKey(t *testing.T) {
	for _, blob := range []string{
		`{"project":"prj_1","env":"staging"}`,
		`{"project":"prj_1","environment":"staging"}`,
		`{"project":"prj_1","envRef":"staging"}`,
	} {
		err := checkProjectJSONKeys([]byte(blob))
		require.Error(t, err, blob)
		require.ErrorContains(t, err, "not a key a committed project record may hold")
	}
	// NEGATİF KONTROL: izinli anahtarlar geçer.
	require.NoError(t, checkProjectJSONKeys([]byte(`{"project":"prj_1","name":"shop","url":"https://shop.example"}`)))
}

// THE WRITER IS WHAT IS GATED, NOT ONLY THE CHECK (review-T014).
//
// Every field Target has today is an allowed key, so no real Target reaches the
// refusal — and deleting the call from WriteTarget left the whole package green.
// The allowlist is narrowed here instead (by one key, so the map order of the
// others cannot decide which key is named), and the one production writer has
// to refuse on its own, before it writes anything.
func TestProjectJSONWriterRefusesAKeyTheAllowlistDoesNotName(t *testing.T) {
	inScratchCheckout(t)
	prev := projectJSONKeys
	var narrowed []string
	for _, key := range prev {
		if key != "name" {
			narrowed = append(narrowed, key)
		}
	}
	require.Len(t, narrowed, len(prev)-1, "the allowlist no longer names \"name\" — pick another key to narrow")
	projectJSONKeys = narrowed
	t.Cleanup(func() { projectJSONKeys = prev })
	_, dirBefore := os.Stat(RootDir())

	err := WriteTarget(Target{URL: "https://shop.example", Project: "prj_1", Name: "shop"})
	require.ErrorContains(t, err, `"name"`)
	require.ErrorContains(t, err, "not a key a committed project record may hold")
	_, statErr := os.Stat(projectPath())
	require.ErrorIs(t, statErr, os.ErrNotExist, "a refused record was written anyway")
	if os.IsNotExist(dirBefore) {
		_, statErr = os.Stat(RootDir())
		require.ErrorIs(t, statErr, os.ErrNotExist, "the refusal came after the directory was created")
	}
}
