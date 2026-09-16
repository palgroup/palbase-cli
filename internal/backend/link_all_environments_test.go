package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	linkKeyMain    = "pb_project_cI1Gf8cAvKPylFE4E4jWVF5FKCT2KmaU0"
	linkKeyStaging = "pb_project_cS2Hg9dBwLQzmGF5F5kXWG6GLDU3LnbV1"
	linkKeyCanary  = "pb_project_cC3Ji0eCxMRanHG6G6lYXH7HMEV4MocW2"
)

// threeEnvironmentLink is a project with three environments: two that answer
// every read, and `staging`, whose social read fails.
func threeEnvironmentLink(t *testing.T) linkOpts {
	t.Helper()
	main := stackServing(t, linkKeyMain, nil)
	staging, _ := envServer(t, linkKeyStaging, envServerOpts{}) // its social read answers 404
	canary := stackServing(t, linkKeyCanary, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL, "canaref000": canary.URL})
	return linkOpts{
		url:       main.URL,
		platforms: []string{"web"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
			{Name: "canary", Ref: "canaref000", Status: "Running"},
		},
	}
}

// ONE LINK, EVERY ENVIRONMENT — and a failed read is not an empty answer.
// Writing a keyless entry over a committed one would blank the key an app
// already ships (the merge takes the key from the new entry); the environment
// that could not be read keeps every byte it had.
func TestOneLinkWritesEveryReadableEnvironmentAndKeepsAFailedOnesFiles(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	o := threeEnvironmentLink(t)
	before := []byte(`{"app_id":"project","base_url":"https://old","api_key":"` + linkKeyStaging + `"}` + "\n")
	require.NoError(t, os.MkdirAll(EnvDir("staging"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath("staging", webPlatform), before, 0o600))

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.FileExists(t, ConfigPath("main", webPlatform))
	require.FileExists(t, ConfigPath("canary", webPlatform), "one link did not write every readable environment")
	after, err := os.ReadFile(ConfigPath("staging", webPlatform))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a failed environment's committed config was rewritten")
	assert.Contains(t, out.String(), "staging could not be read")
}

func TestAnEnvironmentThatCannotBeReadGetsNoDirectory(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	o := threeEnvironmentLink(t)

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.FileExists(t, ConfigPath("canary", webPlatform))
	_, err := os.Stat(EnvDir("staging"))
	assert.True(t, os.IsNotExist(err), "a directory was created for an environment that could not be read")
}

// iosClientServer answers every read of an environment, and its social
// configuration names one enabled iOS client — the same client the social
// link tests use. A checkout whose Xcode project carries no bundle identifier
// cannot say that client is its own, so this environment's iOS read fails,
// while its macOS read has no candidate and succeeds.
func iosClientServer(t *testing.T, key string) *httptest.Server {
	t.Helper()
	providers := map[string]any{}
	for _, name := range []string{"google", "apple", "microsoft", "github"} {
		providers[name] = map[string]any{"enabled": false, "browser_clients": []any{}, "native_clients": []any{}}
	}
	providers["google"] = map[string]any{"enabled": true, "browser_clients": []any{}, "native_clients": []any{map[string]any{"key": "google-ios", "enabled": true, "application_key": "consumer", "platform": "ios", "variant": "release", "bundle_id": "com.example.app", "ios_client_id": "IOS.apps.googleusercontent.com", "redirect_uri": "com.googleusercontent.apps.IOS:/oauthredirect"}}}
	admin, err := json.Marshal(map[string]any{"contract_revision": 1, "credentials": []any{}, "providers": providers})
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/management/auth/social-auth":
			w.Header().Set("Palbase-Auth-Contract", "1")
			_, _ = w.Write(admin)
		case wellKnownPath:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"hosting":"project","sdk_version":"18.0.0"}`))
		case "/v1/management/keys":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"publishable":"` + key + `"}`))
		case "/v1/management/openapi":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"openapi":"3.2.0","paths":{}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// FR-070 ACROSS PLATFORMS: an environment that fails for ONE platform is
// written for NONE. A macOS config written for staging beside a missing iOS one
// would be half an environment. Measured by mutation: with the union removed,
// every other test in this package stayed green.
func TestAnEnvironmentThatFailsForOnePlatformIsWrittenForNone(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	main := stackServing(t, linkKeyMain, nil)
	staging := iosClientServer(t, linkKeyStaging)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"ios", "macos"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.FileExists(t, ConfigPath("main", "ios"))
	require.FileExists(t, ConfigPath("main", "macos"))
	require.Contains(t, out.String(), "staging could not be read", "staging's iOS read did not fail — this test measures nothing")
	_, err := os.Stat(EnvDir("staging"))
	assert.True(t, os.IsNotExist(err), "an environment that failed for iOS was written for macOS")
}

// FR-016 THROUGH THE LINK: the sweep is handed every environment the project
// lists, not only the ones this run described. Measured by mutation: a caller
// that passed what it had read left every test green while the sweep deleted
// a Failed environment's committed files.
//
// THE FILE IS THE MEASURE, NOT THE DIRECTORY. A link runs in a stage and
// publishes file by file, so a swept environment reaches the checkout as an
// EMPTY directory — and a DirExists assertion passed on exactly that.
func TestAnAppleLinkKeepsTheDirectoryOfAnEnvironmentItDidNotRead(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	root, err := os.Getwd()
	require.NoError(t, err)
	staging := seedGeneratedEnvironment(t, root, "staging")
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"ios"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Failed"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.FileExists(t, ConfigPath("main", "ios"))
	assert.FileExists(t, filepath.Join(staging, "PalbaseGenerated.swift"),
		"an Apple link deleted the committed files of an environment the project still lists")
}

// FR-070 FOR THE APPLE GENERATOR: an environment dropped this run keeps its
// committed contract, roles, config, client and plist byte for byte. The
// generator is handed the environments that survived, not the ones described.
func TestADroppedEnvironmentKeepsItsGeneratedAppleFiles(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	main := stackServing(t, linkKeyMain, nil)
	staging := iosClientServer(t, linkKeyStaging)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	committed := map[string]string{
		SpecPath("staging"):             `{"openapi":"3.1.0"}`,
		ConfigPath("staging", "ios"):    `{"app_id":"project"}`,
		GeneratedPath("staging", "ios"): "// generated before",
		PlistPath("staging"):            "old plist",
	}
	for path, body := range committed {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"ios"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.Contains(t, out.String(), "staging could not be read", "staging's iOS read did not fail — this test measures nothing")
	for path, body := range committed {
		got, err := os.ReadFile(path)
		require.NoError(t, err, path)
		assert.Equal(t, body, string(got), "%s was rewritten for an environment this link dropped", path)
	}
}

// A CHECKOUT WITH NO CLIENT READS ONE ENVIRONMENT. Describing the others waited
// — the whole readiness budget for one that sleeps — for files nothing here
// writes; which environment is Failed is still said, without asking it (FR-015).
func TestABackendCheckoutAsksOnlyTheEnvironmentItReadsFrom(t *testing.T) {
	inScratchCheckout(t)
	prevWait, prevEvery := stackReadyWait, stackReadyRetryEvery
	stackReadyWait, stackReadyRetryEvery = 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { stackReadyWait, stackReadyRetryEvery = prevWait, prevEvery })
	main := stackServing(t, linkKeyMain, nil)
	staging, stagingHits := envServer(t, linkKeyStaging, envServerOpts{notReady: true})
	broken, brokenHits := envServer(t, linkKeyCanary, envServerOpts{})
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL, "brokref000": broken.URL})
	o := linkOpts{
		url:       main.URL,
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Archived"},
			{Name: "broken", Ref: "brokref000", Status: "Failed"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	assert.Zero(t, stagingHits.Load(), "a checkout with no client asked an environment it writes nothing for")
	assert.Zero(t, brokenHits.Load(), "a Failed environment was asked")
	assert.Contains(t, out.String(), "broken is Failed")
	_, err := os.Stat(EnvDir("staging"))
	assert.True(t, os.IsNotExist(err))
}

// THE SWEEP'S DIRECTORY LEAVES THE CHECKOUT (FR-017, X-1). The link runs in a
// stage and publishes file by file: the swept environment's files went, and its
// directory stayed behind, empty, under the line that said it was removed.
func TestASweptEnvironmentLeavesNoDirectoryInTheCheckout(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	root, err := os.Getwd()
	require.NoError(t, err)
	staging := seedGeneratedEnvironment(t, root, "staging")
	o := linkOpts{
		url:          main.URL,
		platforms:    []string{"ios"},
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.Contains(t, out.String(), "the project no longer has that environment", "the sweep did not run — this test measures nothing")
	assert.NoDirExists(t, staging, "the checkout kept an empty directory for an environment the link said it removed")
}
