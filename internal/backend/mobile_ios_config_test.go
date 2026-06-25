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

// parsePerEnvPlist parses the build-config-conditioned Palbase-Info.plist
// (a top-level <dict> whose <key>Debug</key>/<key>Release</key> each hold a
// nested config <dict>) back into env -> {key: value}. It walks the XML token
// stream so the nested-dict nesting is honoured exactly. Schema-agnostic so a
// dropped env or a missing key surfaces as an absent map entry — making the
// "both identifiers present" assertion mutation-evident.
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

// stubArtifactFetcher returns a pre-seeded apps.ConfigArtifact per env ref,
// standing in for the two real Studio apps.configArtifact queries the codegen
// fires (one for the dev binding, one for the production binding).
type stubArtifactFetcher struct {
	byEnv map[string]apps.ConfigArtifact
	calls []string
}

func (s *stubArtifactFetcher) configArtifact(_ context.Context, appID, envRef string) (apps.ConfigArtifact, error) {
	s.calls = append(s.calls, envRef)
	art := s.byEnv[envRef]
	art.AppID = appID
	return art, nil
}

// TestMobileIOSCodegen_EmitsPerEnvPlist pins the build-config-conditioned
// Palbase-Info.plist: given a dev binding (com.x.todo.dev) and a production
// binding (com.x.todo) for the SAME app, the codegen fetches BOTH env
// artifacts and writes ONE plist that carries BOTH identifiers keyed by build
// config — Debug→dev (com.x.todo.dev), Release→production (com.x.todo) — with
// the env_preset key present per env.
//
// Mutation-evident: if the codegen emitted only one env, the "both identifiers
// present" assertions (Debug.identifier AND Release.identifier) would fail.
func TestMobileIOSCodegen_EmitsPerEnvPlist(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")

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

	err := emitIOSPerEnvPlist(
		context.Background(), fetcher.configArtifact,
		"ios_todoapp", "todoappm8p6zm", "todoappprod", out,
	)
	require.NoError(t, err)

	// Both env artifacts must have been fetched (one per env).
	require.ElementsMatch(t, []string{"todoappm8p6zm", "todoappprod"}, fetcher.calls)

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	envs := parsePerEnvPlist(t, raw)

	// The NEW per-env file is written (not PalbaseGenerated.json).
	require.Equal(t, "Palbase-Info.plist", filepath.Base(out))

	dbg, ok := envs[apps.BuildConfigDebug]
	require.True(t, ok, "plist must carry a Debug env dict")
	rel, ok := envs[apps.BuildConfigRelease]
	require.True(t, ok, "plist must carry a Release env dict")

	// BOTH identifiers present, keyed by build config: Debug=dev, Release=prod.
	require.Equal(t, "com.x.todo.dev", dbg["identifier"], "Debug build must bind the DEV identifier")
	require.Equal(t, "com.x.todo", rel["identifier"], "Release build must bind the PRODUCTION identifier")

	// env_preset keys present per env (the new per-env format).
	require.Equal(t, "development", dbg["env_preset"])
	require.Equal(t, "production", rel["env_preset"])

	// app_id carried into both env dicts.
	require.Equal(t, "ios_todoapp", dbg["app_id"])
	require.Equal(t, "ios_todoapp", rel["app_id"])
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
	fetcher := &stubArtifactFetcher{byEnv: map[string]apps.ConfigArtifact{
		"todoappm8p6zm": {Identifier: "com.x.todo.dev", EnvPreset: "development", OAuth: devOAuth},
		"todoappprod":   {Identifier: "com.x.todo", EnvPreset: "production", OAuth: prodOAuth},
	}}

	err := emitIOSPerEnvPlist(
		context.Background(), fetcher.configArtifact,
		"ios_todoapp", "todoappm8p6zm", "todoappprod", out,
	)
	require.NoError(t, err)

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	s := string(raw)

	require.Contains(t, s, "<key>oauth</key>")
	require.Contains(t, s, "dev-client.apps.googleusercontent.com", "Debug env oauth client_id present")
	require.Contains(t, s, "prod-client.apps.googleusercontent.com", "Release env oauth client_id present")
	require.Contains(t, s, "<key>apple</key>", "prod apple provider present")
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
