package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/palgroup/palbase-cli/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// What `palbase link <url>` must get right.
//
// It used to be handed both of the stack's keys — through --env-file, or pasted
// — and the dangerous half of that is gone with them: the SECRET key is no
// longer part of linking at all. What remains is the publishable one, which the
// stack serves itself, and which is written into a file that ships inside an
// app. So the assertions here are: ask the stack rather than the operator, put
// the publishable key in the app, and never put anything else there.

// inScratchCheckout runs the test inside an empty directory with its own HOME,
// the way a person runs `link` from a fresh clone — and so one test's session
// never reaches another's.
func inScratchCheckout(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
}

// stackServing answers as a project: the public document, the AUTHENTICATED key
// route, the contract, and whatever else a test wires up.
//
// The key route refuses without a bearer, exactly as the real one does — a
// harness that handed it out unconditionally would let a test pass that a real
// project fails.
func stackServing(t *testing.T, anonKey string, extra http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case wellKnownPath:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"hosting":"project","sdk_version":"18.0.0"}`))
			return
		case "/v1/management/keys":
			// EITHER credential, because the real gate takes either: a person's
			// token in Authorization, the operator's key in `apikey`. A harness
			// that took only one would fail a caller the project accepts.
			if r.Header.Get("Authorization") == "" && r.Header.Get("apikey") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"publishable":"` + anonKey + `"}`))
			return
		case "/v1/management/openapi":
			if r.Header.Get("Authorization") == "" && r.Header.Get("apikey") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"openapi":"3.2.0","paths":{}}`))
			return
		}
		if extra != nil {
			extra(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// linkedAs gives this scratch checkout a credential for one target, the way
// `palbase start` or `palbase login` would.
func linkedAs(t *testing.T, url, token string) {
	t.Helper()
	t.Setenv(AccessTokenEnv, "")
	if err := StoreCredential(url, Credentials{Value: token, Kind: KindPerson}); err != nil {
		t.Fatal(err)
	}
}

func TestTheProjectSaysWhatItIsAndNotWhatOpensIt(t *testing.T) {
	// The public document says WHAT answered. It used to hand out the
	// publishable key as well, which meant knowing an address was enough to hold
	// a working client credential — so the key moved behind an authenticated
	// route and this document kept the part that is safe to tell a stranger.
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)

	described, err := describeStack(context.Background(), srv.URL, false)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if described.Hosting != "project" {
		t.Errorf("hosting came back as %q", described.Hosting)
	}
}

func TestSomethingThatIsNotAStackIsSaidToBeOne(t *testing.T) {
	// A mistyped address, or a web server that is not this product. Saying so by
	// name beats the errors that follow from carrying on: a 404 while fetching a
	// contract reads like "nothing is deployed", which is a different problem
	// with a different fix.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := describeStack(context.Background(), srv.URL, false)
	if err == nil {
		t.Fatal("a server that is not a stack was accepted as one")
	}
	if !strings.Contains(err.Error(), "does not look like a Palbase stack") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

func TestAProjectWithNoPublishableKeyIsRefused(t *testing.T) {
	// Its clients could not authenticate at all, so writing that config would
	// produce an app that builds and fails at its first request.
	inScratchCheckout(t)
	srv := stackServing(t, "", nil)
	linkedAs(t, srv.URL, "a-credential")

	_, err := projectPublishableKey(context.Background(), Target{URL: srv.URL})
	if err == nil {
		t.Fatal("a project with no publishable key was linked")
	}
	if !strings.Contains(err.Error(), "publishable key") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

func TestTheAppConfigCarriesThePUBLISHABLEKey(t *testing.T) {
	inScratchCheckout(t)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	opts := linkOpts{url: srv.URL, platforms: []string{"ios"}}
	if err := runLink(context.Background(), opts, &strings.Builder{}); err != nil {
		t.Fatalf("link: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json"))
	if err != nil {
		t.Fatalf("no slot file: %v", err)
	}
	var slot appEnvironments
	if err := json.Unmarshal(raw, &slot); err != nil {
		t.Fatal(err)
	}
	entry, ok := slot.Environments[slot.Default]
	if !ok {
		t.Fatalf("the slot has no %q environment: %v", slot.Default, slot.names())
	}
	// THE assertion. This file is committed and ships inside the app, so the key
	// in it must be the one that is safe to ship.
	if entry.APIKey != "pb_project_cPUBLISHABLE" {
		t.Errorf("the app's config carries %q", entry.APIKey)
	}
	if entry.BaseURL != srv.URL {
		t.Errorf("base_url is %q", entry.BaseURL)
	}
	if entry.AppID != projectAppID {
		t.Errorf("the app slot is %q, want %q", entry.AppID, projectAppID)
	}
	// And the file carries NO second copy of the project's identity. It used to
	// write one beside the key, and on 2026-08-16 the two disagreed — "project"
	// in the field, "project" inside the key — so the web generator refused the
	// config outright and the iOS realtime client joined a channel nobody
	// published to. The key is the identity; a copy is only a way to be wrong.
	if strings.Contains(string(raw), "environment_ref") {
		t.Errorf("the app's config still carries a second copy of the project identity:\n%s", raw)
	}
}

func TestLinkingWithoutACredentialWritesNOTHING(t *testing.T) {
	// This is the reverse of what it used to do, and the reversal is the point.
	// Both halves of a link now come from the project over authenticated routes
	// — the key as well as the contract — so a run with no credential has
	// nothing true to write, and writing "most of it" would leave a checkout
	// that looks linked and cannot work.
	inScratchCheckout(t)
	t.Setenv(AccessTokenEnv, "")
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)

	var out strings.Builder
	err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &out)
	if err == nil {
		t.Fatal("linking succeeded with no credential")
	}
	if _, statErr := os.Stat(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json")); statErr == nil {
		t.Error("a half-linked checkout was written")
	}
	// And the refusal names both ways to fix it.
	for _, want := range []string{"palbase start", "palbase login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestTheContractIsFetchedWithTheSessionAndNoKey(t *testing.T) {
	// THE rule of the management surface: an apikey WINS over a bearer token, so
	// a request carrying both is a request whose person is invisible — the stack
	// mints the identity from the key and answers 403. A client that helpfully
	// attaches the publishable key here would be refused for a reason nobody
	// could see in their own code.
	var sawKey, sawBearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("apikey")
		sawBearer = r.Header.Get("Authorization")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"openapi":"3.2.0","paths":{}}`))
	}))
	defer srv.Close()

	body, err := fetchStackSpec(context.Background(),
		Target{URL: srv.URL}, Credentials{Value: "the-session-token", Kind: KindPerson})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if sawKey != "" {
		t.Errorf("the contract was fetched with an apikey (%q) — the person becomes invisible", sawKey)
	}
	if sawBearer != "Bearer the-session-token" {
		t.Errorf("authorization was %q", sawBearer)
	}
	if !strings.Contains(string(body), "openapi") {
		t.Errorf("body came back as %q", body)
	}
}

func TestAnExpiredSessionIsReportedAsNotSignedIn(t *testing.T) {
	// So `link --email` can sign in and retry. A stack rebuilt since the last
	// link leaves a token behind that verifies as nothing, and that is the case
	// most in need of a fresh sign-in — treating "a token exists" as "signed in"
	// is what made it the one case that could not recover.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := fetchStackSpec(context.Background(), Target{URL: srv.URL}, Credentials{Value: "stale", Kind: KindPerson})
	if !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("an expired session came back as %v", err)
	}
}

func TestNothingDeployedYetIsAStateNotAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"spec_unavailable"}`))
	}))
	defer srv.Close()

	_, err := fetchStackSpec(context.Background(), Target{URL: srv.URL}, Credentials{Value: "token", Kind: KindPerson})
	if err == nil {
		t.Fatal("an empty stack was treated as a contract")
	}
	// A stack that is up with nothing pushed to it is the normal first state, so
	// the message says what to do next rather than what went wrong.
	if !strings.Contains(err.Error(), "push a backend") {
		t.Errorf("the message does not say what to do: %v", err)
	}
}

// TestTheSlotCarriesEveryEnvironment is FR-050 and FR-055 together: an app that
// holds only the environment somebody linked last is an app whose address
// depends on when it was built.
func TestTheSlotCarriesEveryEnvironment(t *testing.T) {
	inScratchCheckout(t)
	t.Setenv("HOME", t.TempDir())

	// A stack running on this machine, registered the way `palbase start` does.
	local := stackServing(t, "pb_local_cLOCALKEY", nil)
	dir, _ := os.Getwd()
	group := sanitiseGroup(filepath.Base(dir))
	if err := registerStack(group, local.URL, "palbase-"+group, dir); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential(local.URL, Credentials{Value: "local-key", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}

	// …and the project this checkout is being linked to.
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	var out strings.Builder
	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &out); err != nil {
		t.Fatalf("link: %v\n%s", err, out.String())
	}

	raw, err := os.ReadFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var slot appEnvironments
	if err := json.Unmarshal(raw, &slot); err != nil {
		t.Fatal(err)
	}
	if slot.Default != "main" {
		t.Errorf("the default environment is %q", slot.Default)
	}
	if got := slot.Environments["main"].BaseURL; got != srv.URL {
		t.Errorf("main points at %q", got)
	}
	if got := slot.Environments[localEnvName].BaseURL; got != local.URL {
		t.Errorf("the local environment points at %q, want %s", got, local.URL)
	}
	if got := slot.Environments[localEnvName].APIKey; got != "pb_local_cLOCALKEY" {
		t.Errorf("the local environment carries %q", got)
	}

	// One build configuration per environment, each excluding the others' client.
	for _, name := range []string{"Main.xcconfig", "Local.xcconfig"} {
		body, err := os.ReadFile(filepath.Join(dir, "Palbase", "Config", name))
		if err != nil {
			t.Fatalf("no %s: %v", name, err)
		}
		if !strings.Contains(string(body), "PALBASE_ENV = ") {
			t.Errorf("%s does not name an environment:\n%s", name, body)
		}
		if !strings.Contains(string(body), "EXCLUDED_SOURCE_FILE_NAMES") {
			t.Errorf("%s does not exclude the other environments' client:\n%s", name, body)
		}
	}
	mainCfg, _ := os.ReadFile(filepath.Join(dir, "Palbase", "Config", "Main.xcconfig"))
	if !strings.Contains(string(mainCfg), "Generated/local/*") {
		t.Errorf("the main configuration would compile the local client too:\n%s", mainCfg)
	}
}

// TestAStoppedLocalStackStillGetsAnEntry is FR-057: a build configuration that
// disappears because a container was stopped is a configuration whose absence
// nobody connects to the container.
func TestAStoppedLocalStackStillGetsAnEntry(t *testing.T) {
	inScratchCheckout(t)
	t.Setenv("HOME", t.TempDir())

	dir, _ := os.Getwd()
	group := sanitiseGroup(filepath.Base(dir))
	// Registered, but nothing is listening there.
	if err := registerStack(group, "http://127.0.0.1:1", "palbase-"+group, dir); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential("http://127.0.0.1:1", Credentials{Value: "k", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}

	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	var out strings.Builder
	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &out); err != nil {
		t.Fatalf("link: %v\n%s", err, out.String())
	}

	raw, _ := os.ReadFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json"))
	var slot appEnvironments
	if err := json.Unmarshal(raw, &slot); err != nil {
		t.Fatal(err)
	}
	entry, ok := slot.Environments[localEnvName]
	if !ok {
		t.Fatal("the local environment was left out because the stack was down")
	}
	if entry.APIKey != "" {
		t.Errorf("a key was invented for a stack that did not answer: %q", entry.APIKey)
	}
	if !strings.Contains(out.String(), "palbase start") {
		t.Errorf("the output does not say how to fill it in:\n%s", out.String())
	}
}

// TestAnExplicitInfoPlistIsToldWhatItNeeds is the silent failure this reports:
// Xcode merges INFOPLIST_KEY_* only into a plist it GENERATES, so a target with
// an explicit Info.plist gets PALBASE_ENV computed and thrown away. Measured on
// a real simulator — a build in the Local configuration signed up against the
// MAIN environment's address while every build setting still read `local`.
func TestAnExplicitInfoPlistIsToldWhatItNeeds(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	envs := appEnvironments{
		Default:      "main",
		Environments: map[string]appEnvironment{"main": {}, "local": {}},
	}

	// A target whose plist Xcode generates: nothing to say.
	var quiet strings.Builder
	reportInfoPlistRequirement(dir, envs, &quiet)
	if quiet.Len() != 0 {
		t.Errorf("a generated-plist target was told to edit a file it does not have:\n%s", quiet.String())
	}

	// An explicit one, without the key.
	if err := os.MkdirAll(filepath.Join(dir, "MyApp"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(dir, "MyApp", "Info.plist")
	if err := os.WriteFile(plist, []byte(`<?xml version="1.0"?><plist><dict>
  <key>CFBundleName</key><string>MyApp</string>
</dict></plist>`), 0o644); err != nil {
		t.Fatal(err)
	}
	// …and a dependency's copy, which is not the app's business.
	if err := os.MkdirAll(filepath.Join(dir, "Pods", "SomeLib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Pods", "SomeLib", "Info.plist"), []byte(`<plist/>`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	reportInfoPlistRequirement(dir, envs, &out)
	got := out.String()
	if !strings.Contains(got, "MyApp/Info.plist") {
		t.Errorf("the app's plist was not named:\n%s", got)
	}
	if strings.Contains(got, "Pods") {
		t.Errorf("a dependency's plist was named:\n%s", got)
	}
	if !strings.Contains(got, "<key>PALBASE_ENV</key>") || !strings.Contains(got, "$(PALBASE_ENV)") {
		t.Errorf("the exact line to add is missing:\n%s", got)
	}
	if !strings.Contains(got, `"main"`) {
		t.Errorf("it does not say which environment every build would reach instead:\n%s", got)
	}

	// With the key present, silence.
	if err := os.WriteFile(plist, []byte(`<?xml version="1.0"?><plist><dict>
  <key>PALBASE_ENV</key><string>$(PALBASE_ENV)</string>
</dict></plist>`), 0o644); err != nil {
		t.Fatal(err)
	}
	var after strings.Builder
	reportInfoPlistRequirement(dir, envs, &after)
	if after.Len() != 0 {
		t.Errorf("a plist that already carries the key was told to add it:\n%s", after.String())
	}
}

// TestTheOldFlatClientIsRemoved is the defect that stopped the real app from
// building at all: one environment used to mean one
// Palbase/Generated/PalbaseGenerated.swift, and writing the per-environment ones
// beside it left both in the target. Xcode 16 compiles every file under a
// synchronized folder, so the build died with "Multiple commands produce
// PalbaseGenerated.stringsdata" — measured on the real todoapp app, which built
// again the moment the old file was deleted by hand.
func TestTheOldFlatClientIsRemoved(t *testing.T) {
	inScratchCheckout(t)
	t.Setenv("HOME", t.TempDir())
	dir, _ := os.Getwd()

	legacy := filepath.Join(dir, generatedDir, "PalbaseGenerated.swift")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("// the one-environment client"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	var out strings.Builder
	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &out); err != nil {
		t.Fatalf("link: %v\n%s", err, out.String())
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("the one-environment client survived the link (%v) — the app would not compile", err)
	}
	if !strings.Contains(out.String(), "one client per environment") {
		t.Errorf("the removal was silent:\n%s", out.String())
	}
}

// BİR REF, BU BULUTUN BİLDİĞİ BİR ADRESTİR — ve yardım metni bunu hep vaat etti.
//
// `palbase link` documents "palbase link <project> — a project in the cloud", and
// the code refused anything without a scheme, sending people to `palbase ios
// link` — which links an iOS APP and is no use at all to a backend checkout. So a
// backend had no way to reach a cloud project by name, which is exactly the step
// between `login` and `secret set`. The only thing missing was the suffix, and
// the configured cloud carries it.
func TestLink_ResolvesABareRefToItsAddress(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())

	cmd := newLinkCmd(Resolvers{
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "v2.palbase.studio"} },
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"na1m7lt2m"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true

	err := cmd.Execute()
	// It cannot finish here — there is no stack at that address in a unit test —
	// but the ERROR proves the ref became the address instead of being refused
	// for having no scheme.
	require.Error(t, err)
	require.Contains(t, err.Error(), "na1m7lt2m.v2.palbase.studio")
	require.NotContains(t, err.Error(), "has no scheme")
}

// Ve ref'e BENZEMEYEN bir şey hâlâ reddedilir: her şeyi adrese çevirmek, yazım
// hatasını "o adrese ulaşamadım" diye raporlamak olurdu.
func TestLink_RefusesSomethingThatIsNeitherAddressNorRef(t *testing.T) {
	t.Chdir(t.TempDir())
	cmd := newLinkCmd(Resolvers{
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "v2.palbase.studio"} },
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"Not A Ref"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true

	require.ErrorContains(t, cmd.Execute(), "neither a stack address nor an environment ref")
}

// TestLinkingForWebWritesTheWebGeneratorsInputs: `--platform web` must produce
// what @palbase/web's `palbe-gen` actually reads, and nothing Xcode-shaped.
//
// ÖLÇÜLDÜ 25.08.2026 (palaicloud): bir web checkout'unda `palbase web link`,
// bağlı hedefi görünce bu doğrudan yola düşüyor (platform_link_target.go) ve bu
// yol PLATFORMDAN BAĞIMSIZ olarak Apple adımlarını koşuyordu —
// `Palbase/Config/Main.xcconfig` yazdı, `Palbase/palbase-config.json`'ı ortam
// HARİTASI şemasıyla ezdi (palbe-gen düz `{app_id, base_url, api_key}` okur) ve
// `Palbase/openapi.json`'ı hiç yazmadı. palbe.gen.ts üretilemedi ve bunu söyleyen
// bir hata da yoktu: her adım başarıyla döndü.
func TestLinkingForWebWritesTheWebGeneratorsInputs(t *testing.T) {
	inScratchCheckout(t)
	t.Setenv("HOME", t.TempDir())

	const anon = "pb_project_cI1Gf8cAvKPylFE4E4jWVF5FKCT2KmaU0"
	srv := stackServing(t, anon, nil)
	linkedAs(t, srv.URL, "a-credential")

	var out strings.Builder
	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"web"}}, &out); err != nil {
		t.Fatalf("link: %v\n%s", err, out.String())
	}
	dir, _ := os.Getwd()

	raw, err := os.ReadFile(filepath.Join(webArtifactsDir, "palbase-config.json"))
	if err != nil {
		t.Fatalf("no web config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, isNativeShape := cfg["environments"]; isNativeShape {
		t.Errorf("the web config carries the native per-environment map:\n%s", raw)
	}
	if got, _ := cfg["api_key"].(string); got != anon {
		t.Errorf("api_key is %q, want the publishable key", got)
	}
	if got, _ := cfg["base_url"].(string); got != srv.URL {
		t.Errorf("base_url is %q, want %s", got, srv.URL)
	}
	if got, _ := cfg["app_id"].(string); got == "" {
		t.Error("app_id is empty — palbe-gen refuses the file")
	}
	if _, err := os.Stat(filepath.Join(webArtifactsDir, "openapi.json")); err != nil {
		t.Errorf("palbe-gen's contract input is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Palbase", "Config", "Main.xcconfig")); err == nil {
		t.Error("a web link wrote an Xcode build configuration")
	}
	if _, err := os.Stat(filepath.Join(dir, generatedDir)); err == nil {
		t.Error("a web link produced the Swift client directory")
	}
}

// TestTheWebConfigDoesNotResurrectARemovedField.
//
// `environment_ref` was taken OUT of this contract on purpose: identity comes
// from the key and from nowhere else, and a copy that must equal its original is
// not a second fact but a second chance to be wrong (palbe/src/gen/generate.ts).
// The cloud writer already drops it — it rewrites the document whole.
//
// The direct writer merges, so that everything it cannot produce (`kind`, the
// OAuth block) survives — and merging carried the removed field forward too.
// ÖLÇÜLDÜ 25.08.2026, palai-cloud: after a re-link onto a NEW project the file
// still read `"environment_ref": "palaicloudm"`, naming an environment that no
// longer exists. Preserving what a writer cannot produce is not the same as
// preserving what the contract deleted.
func TestTheWebConfigDoesNotResurrectARemovedField(t *testing.T) {
	inScratchCheckout(t)

	const anon = "pb_project_cI1Gf8cAvKPylFE4E4jWVF5FKCT2KmaU0"
	srv := stackServing(t, anon, nil)
	linkedAs(t, srv.URL, "a-credential")

	// A config from the cloud path, carrying both a field this path cannot
	// produce and the removed one.
	if err := os.MkdirAll(webArtifactsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webArtifactsDir, "palbase-config.json"), []byte(
		`{"app_id":"app_real","base_url":"https://old.example","api_key":"pb_old_c01234567890123456789",`+
			`"environment_ref":"deadenv","kind":"production"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"web"}}, &out); err != nil {
		t.Fatalf("link: %v\n%s", err, out.String())
	}

	raw, err := os.ReadFile(filepath.Join(webArtifactsDir, "palbase-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, still := cfg["environment_ref"]; still {
		t.Errorf("the removed field survived the re-link, naming an environment nothing points at:\n%s", raw)
	}
	// …while what this writer genuinely cannot produce is still preserved.
	if got, _ := cfg["kind"].(string); got != "production" {
		t.Errorf("kind is %q — the writer deleted what it cannot produce", got)
	}
	if got, _ := cfg["app_id"].(string); got != "app_real" {
		t.Errorf("app_id is %q, want the real registration preserved", got)
	}
	if got, _ := cfg["api_key"].(string); got != anon {
		t.Errorf("api_key is %q — the run produced a value and it must win", got)
	}
}

// FR-001: link, yiginin muhur kokunu uygulamanin yapilandirmasina TASIR.
//
// SDK bu alani OKUYOR ve kimse YAZMIYORDU (3d2043c bunu duzeltti ama testsiz indi).
// Alan olmadan self-host eden bir uygulama, yiginin muhurlu /auth/* cevabini
// dogrulayacak koke sahip olmuyor ve ilk kayitta soyle oluyor:
//
//	"This request must be encrypted and the encryption key is unavailable."
//
// — ne sebebi ne cozumu soyleyen bir mesaj. Bu test o regresyonu sabitler.
func TestTheAppConfigCarriesTheStacksSealingRoot(t *testing.T) {
	inScratchCheckout(t)
	const root = "MSpMCEuCo76fF82x5Sa9d+9h8RRzNLC3/JiTe0WOvhI="
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/management/keys" {
			if r.Header.Get("Authorization") == "" && r.Header.Get("apikey") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"publishable":"pb_project_cPUBLISHABLE","sealed_root":"` + root + `"}`))
			return
		}
		switch r.URL.Path {
		case wellKnownPath:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"hosting":"project","sdk_version":"18.0.0"}`))
		case "/v1/management/openapi":
			if r.Header.Get("Authorization") == "" && r.Header.Get("apikey") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"openapi":"3.2.0","paths":{}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	linkedAs(t, srv.URL, "a-credential")

	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &strings.Builder{}); err != nil {
		t.Fatalf("link: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json"))
	if err != nil {
		t.Fatalf("no slot file: %v", err)
	}
	var slot appEnvironments
	if err := json.Unmarshal(raw, &slot); err != nil {
		t.Fatal(err)
	}
	entry := slot.Environments[slot.Default]
	if entry.SealedRoot != root {
		t.Errorf("the app's config carries sealed_root %q; the stack said %q", entry.SealedRoot, root)
	}
}

// FR-002: kok bildirmeyen bir yigin, link'i BASARISIZ SAYDIRMAZ ve alan hic yazilmaz.
//
// Zinciri olmayan bir yigin muhurlemiyor demektir, bu da operatorun istedigi link'i
// reddetmek icin sebep degil. Olculdu: bu makinedeki `palbase start` yiginin .env'i
// sifir muhurleme degiskeni tasiyor — yani bu dal kurgusal degil, sahadaki durum.
func TestAStackWithNoSealingRootStillLinks(t *testing.T) {
	inScratchCheckout(t)
	// stackServing'in kendi cevabi zaten sealed_root ICERMIYOR — kok bildirmeyen
	// yiginin ta kendisi.
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &strings.Builder{}); err != nil {
		t.Fatalf("kok bildirmeyen bir yigin link'i basarisiz yapti: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json"))
	if err != nil {
		t.Fatalf("no slot file: %v", err)
	}
	var slot appEnvironments
	if err := json.Unmarshal(raw, &slot); err != nil {
		t.Fatal(err)
	}
	if got := slot.Environments[slot.Default].SealedRoot; got != "" {
		t.Errorf("kok bildirmeyen yigin icin sealed_root yazildi: %q", got)
	}
}

// A CHECKOUT ALREADY SAYS WHAT IT IS — the flag was asking the reader to repeat it.
//
// `--platform` defaulted to `ios`, so `palbase link` in a web-only checkout
// wrote Apple artifacts and nothing else, silently. The material to answer the
// question was already here (hasApple, hasWeb, detectAndroidApplicationID); it
// was simply never asked.
func TestDetectPlatformsReadsTheCheckout(t *testing.T) {
	t.Run("an Apple checkout", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "App.xcodeproj"), 0o755); err != nil {
			t.Fatal(err)
		}
		got := detectPlatforms(dir)
		if !slices.Contains(got, "ios") {
			t.Errorf("detectPlatforms = %v, want ios in it", got)
		}
	})

	t.Run("a web checkout", func(t *testing.T) {
		dir := t.TempDir()
		for _, f := range []string{"package.json", "index.html"} {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got := detectPlatforms(dir)
		if !slices.Contains(got, webPlatform) {
			t.Errorf("detectPlatforms = %v, want web in it", got)
		}
		if slices.Contains(got, "ios") {
			t.Errorf("detectPlatforms found ios in a web-only checkout: %v", got)
		}
	})

	t.Run("an Android checkout", func(t *testing.T) {
		dir := t.TempDir()
		app := filepath.Join(dir, "app")
		if err := os.MkdirAll(app, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "android {\n  defaultConfig {\n    applicationId = \"com.example.app\"\n  }\n}\n"
		if err := os.WriteFile(filepath.Join(app, "build.gradle.kts"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		got := detectPlatforms(dir)
		if !slices.Contains(got, "android") {
			t.Errorf("detectPlatforms = %v, want android in it", got)
		}
	})

	// NOTHING FOUND IS AN ANSWER, not an empty guess. The caller has to be able
	// to tell "this is a web app" from "I could not tell", because the second
	// one deserves a sentence and the first one does not.
	t.Run("a checkout with none of them", func(t *testing.T) {
		if got := detectPlatforms(t.TempDir()); len(got) != 0 {
			t.Errorf("detectPlatforms invented %v for an empty directory", got)
		}
	})
}

// AN UNKNOWN PLATFORM IS NAMED, not accepted and quietly ignored.
//
// `--platform bogus` used to sail through: the loop simply never matched it, so
// the run wrote nothing for that entry and said nothing about it. A flag that
// accepts anything teaches the reader that their typo worked.
func TestPlatformFlagRefusesWhatItCannotBuild(t *testing.T) {
	if err := validatePlatforms([]string{"ios", "web"}); err != nil {
		t.Fatalf("valid platforms were refused: %v", err)
	}
	err := validatePlatforms([]string{"ios", "bogus"})
	if err == nil {
		t.Fatal("`bogus` was accepted as a platform")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bogus") {
		t.Errorf("the refusal does not name the offending value: %s", msg)
	}
	for _, valid := range []string{"ios", "macos", "android", "web"} {
		if !strings.Contains(msg, valid) {
			t.Errorf("the refusal does not list %q as a choice: %s", valid, msg)
		}
	}
}

// UNLINK REMOVES THE BOND, and the bond is the committed project file.
//
// The old `web unlink` deleted the SELECTION instead — a file that is going away
// entirely — and told the reader to re-link with `palbase web link`, a command
// that is going away too. What makes a checkout linked is .palbase/project.json;
// that is what unlink has to remove.
func TestUnlinkRemovesTheProjectFile(t *testing.T) {
	dir := seedProject(t, Target{URL: "https://app1prod.palbase.studio", Project: "proj_1"})

	var out bytes.Buffer
	if err := runUnlink(&out); err != nil {
		t.Fatalf("unlink failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, nativeArtifactsDir, "project.json")); !os.IsNotExist(err) {
		t.Error("project.json survived the unlink — the checkout is still linked")
	}

	// AN UNLINKED CHECKOUT IS NOT AN ERROR, it is already where you asked to be.
	if err := runUnlink(&out); err != nil {
		t.Errorf("unlinking an unlinked checkout was an error: %v", err)
	}
}
