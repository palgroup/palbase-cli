package backend

import (
	"bytes"
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/apps"
)

// parsePerEnvPlist parses the bundle-id-keyed Palbase-Info.plist (a top-level
// <dict> whose keys are bundle ids, each holding a nested config <dict>) back
// into bundleID -> {key: value}. It walks the XML token stream so the
// nested-dict nesting is honoured exactly. Schema-agnostic so a dropped env or a
// missing key surfaces as an absent map entry — making the "both bundle ids
// present" assertion mutation-evident (the OLD build-config-keyed output keyed
// by Debug/Release, so a bundle-id-key assertion fails against it).
func parsePerEnvPlist(t *testing.T, b []byte) map[string]map[string]string {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(b))
	out := map[string]map[string]string{}

	depth := 0           // 0 before plist; 1 inside top <dict>; 2 inside an env <dict>
	var pendingEnv string // top-level key awaiting its env <dict>
	var pendingKey string // inner key awaiting its <string>
	var curEnv map[string]string

	readChars := func() string {
		var s string
		for {
			tok, err := dec.Token()
			require.NoError(t, err)
			if cd, ok := tok.(xml.CharData); ok {
				s += string(cd)
				continue
			}
			// EndElement of the <key>/<string> we were reading.
			return s
		}
	}

	for {
		tok, err := dec.Token()
		if err != nil {
			break // io.EOF
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "dict":
				depth++
				if depth == 2 {
					curEnv = map[string]string{}
					out[pendingEnv] = curEnv
				}
			case "key":
				val := readChars()
				if depth == 1 {
					pendingEnv = val
				} else if depth == 2 {
					pendingKey = val
				}
			case "string":
				val := readChars()
				if depth == 2 && curEnv != nil {
					curEnv[pendingKey] = val
				}
			}
		case xml.EndElement:
			if el.Name.Local == "dict" {
				depth--
			}
		}
	}
	return out
}

// stubArtifactFetcher returns a pre-seeded apps.ConfigArtifact per BARE env ref,
// standing in for the real Studio apps.configArtifact queries the codegen fires
// (one per registered binding).
type stubArtifactFetcher struct {
	byEnv map[string]apps.ConfigArtifact
	calls []string
}

func (s *stubArtifactFetcher) configArtifact(_ context.Context, appID, envRef string) (apps.ConfigArtifact, error) {
	s.calls = append(s.calls, envRef)
	art := s.byEnv[envRef]
	art.AppID = appID
	art.ProjectRef = envRef
	return art, nil
}

// stubBindingLister returns a fixed binding list, standing in for the real
// Studio apps.listBindings query (each binding carries a bare project_ref, its
// registered identifier/bundle id, and its preset).
type stubBindingLister struct {
	bindings []AppBinding
	calls    []string
}

func (s *stubBindingLister) listBindings(_ context.Context, appID string) ([]AppBinding, error) {
	s.calls = append(s.calls, appID)
	return s.bindings, nil
}

// TestMobileIOSCodegen_EmitsBundleKeyedPlist pins the bundle-id-keyed
// Palbase-Info.plist: given a dev binding (com.x.todo.dev) and a production
// binding (com.x.todo) for the SAME app, the codegen LISTS the bindings, fetches
// BOTH env artifacts by their BARE project_ref, and writes ONE plist that
// carries BOTH identifiers AS TOP-LEVEL KEYS (the bundle ids) — with the
// env_preset key present per env.
//
// Mutation-evident: the OLD build-config-keyed emitter keyed by Debug/Release,
// so the "envs keyed by com.x.todo.dev AND com.x.todo" assertions would FAIL;
// likewise if the codegen emitted only one env.
func TestMobileIOSCodegen_EmitsBundleKeyedPlist(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")

	lister := &stubBindingLister{bindings: []AppBinding{
		{ProjectRef: "todoappm8p6zm", Identifier: "com.x.todo.dev", EnvPreset: "development"},
		{ProjectRef: "todoappprod", Identifier: "com.x.todo", EnvPreset: "production"},
	}}
	fetcher := &stubArtifactFetcher{byEnv: map[string]apps.ConfigArtifact{
		"todoappm8p6zm": { // dev env
			Identifier: "com.x.todo.dev",
			EnvPreset:  "development",
			BaseURL:    "https://todoappm8p6zm.dev.palbase.studio",
			APIKey:     "pb_todoappm8p6zm_c_dev",
		},
		"todoappprod": { // production env
			Identifier: "com.x.todo",
			EnvPreset:  "production",
			BaseURL:    "https://todoappprod.palbase.studio",
			APIKey:     "pb_todoappprod_c_prod",
		},
	}}

	var w bytes.Buffer
	err := emitIOSBundleKeyedPlist(
		context.Background(), lister.listBindings, fetcher.configArtifact,
		"ios_todoapp", out, &w,
	)
	require.NoError(t, err)

	// The bindings were listed once, and BOTH bindings' BARE refs were fetched.
	require.Equal(t, []string{"ios_todoapp"}, lister.calls)
	require.ElementsMatch(t, []string{"todoappm8p6zm", "todoappprod"}, fetcher.calls)

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	envs := parsePerEnvPlist(t, raw)

	// The NEW per-env file is written (not PalbaseGenerated.json).
	require.Equal(t, "Palbase-Info.plist", filepath.Base(out))

	// Top-level keys are the BUNDLE IDS — not Debug/Release.
	require.NotContains(t, envs, "Debug", "the plist must NOT use the retired build-config key")
	require.NotContains(t, envs, "Release", "the plist must NOT use the retired build-config key")
	dev, ok := envs["com.x.todo.dev"]
	require.True(t, ok, "plist must carry the dev env dict keyed by its bundle id")
	prod, ok := envs["com.x.todo"]
	require.True(t, ok, "plist must carry the prod env dict keyed by its bundle id")

	// Each dict's own identifier must equal its bundle-id key.
	require.Equal(t, "com.x.todo.dev", dev["identifier"])
	require.Equal(t, "com.x.todo", prod["identifier"])

	// env_preset keys present per env.
	require.Equal(t, "development", dev["env_preset"])
	require.Equal(t, "production", prod["env_preset"])

	// app_id carried into both env dicts.
	require.Equal(t, "ios_todoapp", dev["app_id"])
	require.Equal(t, "ios_todoapp", prod["app_id"])
}

// TestMobileIOSCodegen_PlistCarriesOAuth pins that the codegen `--app` path
// embeds the per-env `oauth` block in the Palbase-Info.plist — the field the
// legacy PalbaseGenerated.json carried. A fetcher returning an artifact with
// OAuth set must surface that block in the emitted plist, making the plist a
// true SUPERSET of the JSON's config role (closes the OAuth regression).
//
// Mutation-evident: drop apps.writeIOSOAuthDict's emit and the client_id
// assertion below fails.
func TestMobileIOSCodegen_PlistCarriesOAuth(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")

	devOAuth := &apps.OAuthConfig{Google: &apps.OAuthGoogle{
		Enabled: true, ClientID: "dev-client.apps.googleusercontent.com",
		RedirectURI: "com.googleusercontent.apps.dev-client:/oauthredirect",
	}}
	prodOAuth := &apps.OAuthConfig{
		Apple: &apps.OAuthApple{Enabled: true},
		Google: &apps.OAuthGoogle{
			Enabled: true, ClientID: "prod-client.apps.googleusercontent.com",
			RedirectURI: "com.googleusercontent.apps.prod-client:/oauthredirect",
		},
	}
	lister := &stubBindingLister{bindings: []AppBinding{
		{ProjectRef: "todoappm8p6zm", Identifier: "com.x.todo.dev", EnvPreset: "development"},
		{ProjectRef: "todoappprod", Identifier: "com.x.todo", EnvPreset: "production"},
	}}
	fetcher := &stubArtifactFetcher{byEnv: map[string]apps.ConfigArtifact{
		"todoappm8p6zm": {Identifier: "com.x.todo.dev", EnvPreset: "development", OAuth: devOAuth},
		"todoappprod":   {Identifier: "com.x.todo", EnvPreset: "production", OAuth: prodOAuth},
	}}

	var w bytes.Buffer
	err := emitIOSBundleKeyedPlist(
		context.Background(), lister.listBindings, fetcher.configArtifact,
		"ios_todoapp", out, &w,
	)
	require.NoError(t, err)

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	s := string(raw)

	require.Contains(t, s, "<key>oauth</key>")
	require.Contains(t, s, "dev-client.apps.googleusercontent.com", "dev bundle oauth client_id present")
	require.Contains(t, s, "prod-client.apps.googleusercontent.com", "prod bundle oauth client_id present")
	require.Contains(t, s, "<key>apple</key>", "prod apple provider present")
}

// TestMobileIOSCodegen_SkipsBindingWithoutIdentifier pins that a binding with an
// empty identifier (the env has not registered a bundle id yet) is SKIPPED with
// a warning, while the configured binding is still emitted. The unconfigured
// binding's artifact is NEVER fetched.
func TestMobileIOSCodegen_SkipsBindingWithoutIdentifier(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")

	lister := &stubBindingLister{bindings: []AppBinding{
		{ProjectRef: "todoappprod", Identifier: "com.x.todo", EnvPreset: "production"},
		{ProjectRef: "todoappstg", Identifier: "", EnvPreset: "staging"}, // unconfigured
	}}
	fetcher := &stubArtifactFetcher{byEnv: map[string]apps.ConfigArtifact{
		"todoappprod": {Identifier: "com.x.todo", EnvPreset: "production", BaseURL: "https://todoappprod.palbase.studio", APIKey: "pb_prod"},
	}}

	var w bytes.Buffer
	err := emitIOSBundleKeyedPlist(
		context.Background(), lister.listBindings, fetcher.configArtifact,
		"ios_todoapp", out, &w,
	)
	require.NoError(t, err)

	// Only the configured binding's artifact was fetched — the unconfigured one
	// was skipped before any fetch.
	require.Equal(t, []string{"todoappprod"}, fetcher.calls)
	require.Contains(t, w.String(), "skipping env todoappstg", "an unconfigured binding must surface a skip warning")

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	envs := parsePerEnvPlist(t, raw)
	require.Len(t, envs, 1, "only the configured bundle id may be emitted")
	require.Contains(t, envs, "com.x.todo")
}

// TestMobileIOSCodegen_ErrorsWhenNoIdentifier pins that when NO binding carries
// an identifier (every env is unconfigured), the emit ERRORS and writes nothing.
func TestMobileIOSCodegen_ErrorsWhenNoIdentifier(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")

	lister := &stubBindingLister{bindings: []AppBinding{
		{ProjectRef: "todoappprod", Identifier: "", EnvPreset: "production"},
		{ProjectRef: "todoappstg", Identifier: "", EnvPreset: "staging"},
	}}
	fetcher := &stubArtifactFetcher{byEnv: map[string]apps.ConfigArtifact{}}

	var w bytes.Buffer
	err := emitIOSBundleKeyedPlist(
		context.Background(), lister.listBindings, fetcher.configArtifact,
		"ios_todoapp", out, &w,
	)
	require.Error(t, err, "an app with no registered bundle id must error (nothing to write)")
	require.Empty(t, fetcher.calls, "no artifact may be fetched when every binding is unconfigured")
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr), "no partial plist may exist after a refused emit")
}

// TestSwiftOAuthToApps maps the backend fetchOAuthProviders shape onto the
// apps plist shape field-for-field (the bridge that lets the codegen path
// reuse the JSON path's `/auth/oauth/providers` fetch).
func TestSwiftOAuthToApps(t *testing.T) {
	require.Nil(t, swiftOAuthToApps(nil))
	require.Nil(t, swiftOAuthToApps(&swiftOAuthConfig{}), "no providers collapses to nil")

	in := &swiftOAuthConfig{
		Apple: &swiftOAuthApple{Enabled: true},
		Google: &swiftOAuthGoogle{
			Enabled: true, ClientID: "x.apps.googleusercontent.com",
			RedirectURI: "com.googleusercontent.apps.x:/oauthredirect",
		},
	}
	got := swiftOAuthToApps(in)
	require.NotNil(t, got)
	require.True(t, got.Apple.Enabled)
	require.Equal(t, "x.apps.googleusercontent.com", got.Google.ClientID)
	require.Equal(t, "com.googleusercontent.apps.x:/oauthredirect", got.Google.RedirectURI)
}
