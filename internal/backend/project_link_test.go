package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/config"

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
		case "/v1/management/auth/social-auth":
			w.Header().Set("Palbase-Auth-Contract", "1")
			_, _ = w.Write([]byte(`{"contract_revision":1,"credentials":[],"providers":{"google":{"enabled":false,"browser_clients":[],"native_clients":[]},"apple":{"enabled":false,"browser_clients":[],"native_clients":[]},"microsoft":{"enabled":false,"browser_clients":[],"native_clients":[]},"github":{"enabled":false,"browser_clients":[],"native_clients":[]}}}`))
			return
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

// readEnvConfig reads ONE environment's platform configuration.
//
// The file holds that environment's own fields now, not a map of every
// environment keyed by name: each environment owns a directory, so the name is
// the directory and a key inside the file would be a second copy of it. Tests
// that unmarshalled `appEnvironments` here were reading the shape this
// migration removed, and got an empty map without an error.
func readEnvConfig(t *testing.T, env, platform string) appEnvironment {
	t.Helper()
	raw, err := os.ReadFile(ConfigPath(env, platform))
	if err != nil {
		t.Fatalf("no config for %s/%s: %v", env, platform, err)
	}
	var cfg appEnvironment
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("%s/%s config did not parse: %v", env, platform, err)
	}
	return cfg
}

func TestTheAppConfigCarriesThePUBLISHABLEKey(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	opts := linkOpts{url: srv.URL, platforms: []string{"ios"}}
	if err := runLink(context.Background(), opts, &strings.Builder{}); err != nil {
		t.Fatalf("link: %v", err)
	}

	entry := readEnvConfig(t, "main", "ios")
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
	onDisk, err := os.ReadFile(ConfigPath("main", "ios"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), "environment_ref") {
		t.Errorf("the app's config still carries a second copy of the project identity:\n%s", onDisk)
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
	if _, statErr := os.Stat(ConfigPath("main", "ios")); statErr == nil {
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
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
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

	// EVERY ENVIRONMENT HAS ITS OWN DIRECTORY AND ITS OWN FLAT CONFIG.
	//
	// This used to read ONE file carrying a map of them all, plus a
	// `default_environment`. The map is gone: the checkout answers "which
	// environments do I carry" by what is on disk.
	for _, env := range []string{"main", localEnvName} {
		raw, err := os.ReadFile(ConfigPath(env, "ios"))
		if err != nil {
			all, _ := filepath.Glob("palbase/environments/*")
			t.Fatalf("no config for %s: %v\nLINK:\n%s\nDISK: %v", env, err, out.String(), all)
		}

		var cfg appEnvironment
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("%s: %v", env, err)
		}
		if strings.Contains(string(raw), "default_environment") ||
			strings.Contains(string(raw), `"environments"`) {
			t.Errorf("%s config still carries the retired map:\n%s", env, raw)
		}
		want := srv.URL
		if env == localEnvName {
			want = local.URL
		}
		if cfg.BaseURL != want {
			t.Errorf("%s points at %q, want %s", env, cfg.BaseURL, want)
		}
	}

	// The local stack's key rides along, as the environment every checkout gets
	// for free.
	localRaw, err := os.ReadFile(ConfigPath(localEnvName, "ios"))
	if err != nil {
		t.Fatal(err)
	}
	var localCfg appEnvironment
	if err := json.Unmarshal(localRaw, &localCfg); err != nil {
		t.Fatal(err)
	}
	if localCfg.APIKey != "pb_local_cLOCALKEY" {
		t.Errorf("the local environment carries %q", localCfg.APIKey)
	}

	// AND NOTHING WAS WRITTEN INTO THE APP'S BUILD SYSTEM. The xcconfigs this
	// test used to demand are the mechanism the CLI could never finish wiring.
	// The retired root cannot be probed by NAME on macOS: the filesystem is
	// case-insensitive, so `Palbase` and `palbase` are the same directory and
	// `os.Stat("Palbase")` succeeds on the new one. Measured at design time and
	// it caught this assertion. What IS distinguishable is what used to live
	// inside it.
	for _, retired := range []string{
		filepath.Join(dir, "Palbase", "Config"),
		filepath.Join(dir, "Palbase", "Generated"),
	} {
		if _, err := os.Stat(retired); !os.IsNotExist(err) {
			t.Errorf("the retired layout was created: %s", retired)
		}
	}
	if !strings.Contains(out.String(), "EXCLUDED_SOURCE_FILE_NAMES") {
		t.Errorf("link did not print the selection snippet:\n%s", out.String())
	}
}

func TestAStoppedLocalStackStillGetsAnEntry(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
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

	// THE LOCAL ENVIRONMENT HAS ITS OWN DIRECTORY, and it must exist even when
	// the stack is down — an app whose Local configuration disappears with a
	// stopped container stops compiling for a reason nobody connects to it.
	entry := readEnvConfig(t, localEnvName, "ios")
	if entry.APIKey != "" {
		t.Errorf("a key was invented for a stack that did not answer: %q", entry.APIKey)
	}
	if !strings.Contains(out.String(), "palbase start") {
		t.Errorf("the output does not say how to fill it in:\n%s", out.String())
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
// EMEKLİ: `TestAnExplicitInfoPlistIsToldWhatItNeeds` ve
// `TestTheOldFlatClientIsRemoved`.
//
// Birincisi `reportInfoPlistRequirement`i ölçüyordu — CLI'ın müşterinin
// Info.plist'ine anahtar eklemesini İSTEDİĞİ satırı. O talimat silindi: CLI
// artık uygulamanın build sistemine ne yazar ne de ondan bir şey ister, ve
// bunun YOKLUĞU `TestLinkLayout`ta ölçülüyor.
//
// İkincisi `Palbase/Generated/PalbaseGenerated.swift` düz istemcisinin
// silindiğini ölçüyordu — ortam başına klasöre geçişin artığı. O yol artık
// hiç yazılmıyor; bugünkü karşılığı bayat ORTAM dizinlerinin toplanması ve
// onu `TestOrphanedEnvironmentFolderIsRemovedButForeignFilesAreNot` ölçüyor,
// üstelik "bizim yazmadığımız dosya silinmez" kuralıyla birlikte.

func TestLink_ResolvesABareRefToItsAddress(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())

	cmd := newLinkCmd(Resolvers{
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "v2.palbase.studio"} },
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	// A REAL WEB CHECKOUT, because `--platform web` in a directory with no
	// package.json is now refused before any network — the web wiring has a
	// prerequisite and saying so early beats a half-done link. This test is
	// about the ADDRESS, so the fixture is the cheapest thing that IS an app.
	writeFile(t, "package.json", `{"name":"app"}`)
	writeFile(t, "index.html", "<!doctype html>")
	cmd.SetArgs([]string{"na1m7lt2m", "--platform", "web"})
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
// requireWebGenerator stops a web-link test when the published `palbe-gen`
// cannot read what THIS CLI writes.
//
// KNOWN DEBT, NAMED RATHER THAN HIDDEN. `@palbase/web` 10.0.1 moved the
// generator onto the per-environment layout `layout.go` already declares —
// `palbase/environments/<env>/{openapi.json, roles.json, web-config.json}` —
// and this CLI still writes the pair it is replacing. The reader shipped before
// the writer, which is the outage this repository has a rule about, and closing
// it is the `cli-dizin-duzeni` migration's whole job, not a hotfix's.
//
// IT RETIRES ITSELF. The moment the CLI writes that layout, `runLink` succeeds
// here, this function returns, and the assertions below run again unchanged —
// so nothing has to remember to delete it. Only the generator's own refusal is
// tolerated: any other failure is still a failure.
func requireWebGenerator(t *testing.T, err error, out string) {
	t.Helper()
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "palbe-gen") {
		t.Skipf("the published web generator does not read this CLI's layout yet "+
			"(cli-dizin-duzeni): %v\n%s", err, out)
	}
	t.Fatalf("link: %v\n%s", err, out)
}

func TestLinkingForWebWritesTheWebGeneratorsInputs(t *testing.T) {
	inScratchCheckout(t)
	// A web link needs a web project — AFTER the chdir, or the files land
	// wherever the suite happened to be. This fixture used to be a bare temp
	// dir, which is not a web checkout, and only got through because the
	// prerequisite was checked from inside the wiring, after the artifacts were
	// already on disk.
	seedWebCheckout(t)
	t.Setenv("HOME", t.TempDir())

	const anon = "pb_project_cI1Gf8cAvKPylFE4E4jWVF5FKCT2KmaU0"
	srv := stackServing(t, anon, nil)
	linkedAs(t, srv.URL, "a-credential")

	var out strings.Builder
	requireWebGenerator(t, runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"web"}}, &out), out.String())
	dir, _ := os.Getwd()

	raw, err := os.ReadFile(ConfigPath("main", webPlatform))
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
	if _, err := os.Stat(SpecPath("main")); err != nil {
		t.Errorf("palbe-gen's contract input is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Palbase", "Config", "Main.xcconfig")); err == nil {
		t.Error("a web link wrote an Xcode build configuration")
	}
	if _, err := os.Stat(filepath.Join(dir, "palbase", "environments")); err == nil {
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
	// A web link needs a web project — AFTER the chdir, or the files land
	// wherever the suite happened to be. This fixture used to be a bare temp
	// dir, which is not a web checkout, and only got through because the
	// prerequisite was checked from inside the wiring, after the artifacts were
	// already on disk.
	seedWebCheckout(t)

	const anon = "pb_project_cI1Gf8cAvKPylFE4E4jWVF5FKCT2KmaU0"
	srv := stackServing(t, anon, nil)
	linkedAs(t, srv.URL, "a-credential")

	// A config from the cloud path, carrying both a field this path cannot
	// produce and the removed one.
	if err := os.MkdirAll(EnvDir("main"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath("main", webPlatform), []byte(
		`{"app_id":"app_real","base_url":"https://old.example","api_key":"pb_old_c01234567890123456789",`+
			`"environment_ref":"deadenv","kind":"production"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	requireWebGenerator(t, runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"web"}}, &out), out.String())

	raw, err := os.ReadFile(ConfigPath("main", webPlatform))
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
	// Metadata from another backend must not be carried into this one.
	if _, exists := cfg["kind"]; exists {
		t.Error("kind from the previous backend survived")
	}
	if got, _ := cfg["app_id"].(string); got != projectAppID {
		t.Errorf("app_id is %q, want this link's app identity", got)
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
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
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
		case "/v1/management/auth/social-auth":
			w.Header().Set("Palbase-Auth-Contract", "1")
			_, _ = w.Write([]byte(`{"contract_revision":1,"credentials":[],"providers":{"google":{"enabled":false,"browser_clients":[],"native_clients":[]},"apple":{"enabled":false,"browser_clients":[],"native_clients":[]},"microsoft":{"enabled":false,"browser_clients":[],"native_clients":[]},"github":{"enabled":false,"browser_clients":[],"native_clients":[]}}}`))
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
	entry := readEnvConfig(t, "main", "ios")
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
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	// stackServing'in kendi cevabi zaten sealed_root ICERMIYOR — kok bildirmeyen
	// yiginin ta kendisi.
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &strings.Builder{}); err != nil {
		t.Fatalf("kok bildirmeyen bir yigin link'i basarisiz yapti: %v", err)
	}
	if got := readEnvConfig(t, "main", "ios").SealedRoot; got != "" {
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
	if _, err := os.Stat(filepath.Join(dir, "palbase", "project.json")); !os.IsNotExist(err) {
		t.Error("project.json survived the unlink — the checkout is still linked")
	}

	// AN UNLINKED CHECKOUT IS NOT AN ERROR, it is already where you asked to be.
	if err := runUnlink(&out); err != nil {
		t.Errorf("unlinking an unlinked checkout was an error: %v", err)
	}
}

// THE WEB WIRING IS REACHED FROM THE COMMAND, not just from its own tests.
//
// This is the failure the whole restoration exists to prevent: `palbase web
// link` did eight setup steps, the command was retired, and the steps became
// 1.208 lines nothing could call — while their own tests went on passing. A
// suite that only drives wireWebProject directly would have stayed green
// through exactly that.
//
// So this runs the PRODUCTION path — runLink, the function the command's RunE
// calls — in a real web checkout, and asserts on what only the wiring produces:
// the package.json scripts that regenerate the client on the next build.
func TestLinkWiresAWebCheckoutEndToEnd(t *testing.T) {
	inScratchCheckout(t)
	installStubCodegen(t, "// generated client")

	const anon = "pb_project_cI1Gf8cAvKPylFE4E4jWVF5FKCT2KmaU0"
	srv := stackServing(t, anon, nil)
	linkedAs(t, srv.URL, "a-credential")

	// A web checkout: package.json beside an entry file. Detection would find
	// this on its own; the platform is named so the test says what it means.
	if err := os.WriteFile("package.json", []byte(`{"name":"app","scripts":{"dev":"next dev"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"web"}}, &out); err != nil {
		t.Fatalf("link: %v\n%s", err, out.String())
	}

	pkg, err := os.ReadFile("package.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"predev", "prebuild"} {
		if !strings.Contains(string(pkg), `"`+hook+`"`) {
			t.Errorf("package.json carries no %s script after `link` — the web wiring did not run:\n%s\n\n%s",
				hook, pkg, out.String())
		}
	}
	// And the existing script survived byte-for-byte: the patcher splices, it
	// does not round-trip somebody's file through a JSON encoder.
	if !strings.Contains(string(pkg), `"dev":"next dev"`) {
		t.Errorf("the project's own script did not survive the patch:\n%s", pkg)
	}

	// AND NO IGNORE FILE IS INVENTED. A curated checkout keeps its own rules;
	// one that has none is left alone, because this CLI has nothing to ignore:
	// everything it writes here is committed. `link` only writes a `.gitignore`
	// where it also scaffolds the project.
	if ignore, err := os.ReadFile(".gitignore"); err == nil {
		if strings.Contains(strings.ToLower(string(ignore)), "palbase") {
			t.Errorf(".gitignore still ignores a palbase path:\n%s", ignore)
		}
	}
}

// writeFile writes one file in the current (scratch) checkout.
//
// AND REFUSES TO WRITE ANYWHERE ELSE. Called before the chdir, it seeded
// `package.json` and `index.html` into `internal/backend/` itself — the package
// source tree — where they survived the run and made `hasWeb()` answer true for
// this repository. A leftover like that is inherited by every later test on the
// machine and by the next build's binary, and CI never sees it because a clean
// runner has none. The guard is cheaper than the hunt.
func writeFile(t *testing.T, name, body string) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cwd, os.TempDir()) && !strings.HasPrefix(cwd, "/private"+os.TempDir()) {
		t.Fatalf("writeFile would write %s into %s, which is not a scratch directory — "+
			"call inScratchCheckout(t) or t.Chdir(t.TempDir()) first", name, cwd)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedWebCheckout makes the current directory the thing a web link acts on.
func seedWebCheckout(t *testing.T) {
	t.Helper()
	writeFile(t, "package.json", `{"name":"app"}`)
	writeFile(t, "index.html", "<!doctype html>")
}

// A REFUSAL THAT ARRIVES AFTER THE WRITES IS A HALF-DONE LINK.
//
// `--platform web` in a directory with no package.json used to be judged from
// inside the web wiring, which runs LAST: the contract and the config were
// already on disk, and the command then reported an error. The reader was left
// with a checkout that was partly linked and a message saying it had failed.
//
// The prerequisite is now checked beside `--platform`'s own validation, before
// the network and before the first byte. This asserts both halves — it refuses,
// and it left nothing behind.
func TestAnUnsupportedPlatformIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()

	const anon = "pb_project_cI1Gf8cAvKPylFE4E4jWVF5FKCT2KmaU0"
	srv := stackServing(t, anon, nil)

	var out strings.Builder
	err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"web"}}, &out)
	if err == nil {
		t.Fatalf("a web link in a directory with no package.json was accepted:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "package.json") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}

	// NOTHING ON DISK. These are what the write path produces, in order; any of
	// them existing means the refusal came too late.
	for _, left := range []string{
		ConfigPath("main", webPlatform),
		SpecPath("main"),
		filepath.Join(dir, ".palbase", "project.json"),
		filepath.Join(dir, ".gitignore"),
	} {
		if _, statErr := os.Stat(left); statErr == nil {
			t.Errorf("%s was written before the refusal — the link is half done", left)
		}
	}
}
