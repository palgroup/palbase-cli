package backend

// credentials_test.go — one resolver, three sources, and a file two processes
// can write at once.

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestTheEnvironmentWinsAndTouchesNoFile is the AI-in-a-container case: a token
// in the environment, no browser, no home directory to depend on.
func TestTheEnvironmentWinsAndTouchesNoFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(AccessTokenEnv, "pat-from-the-environment")

	cred, source, err := Credential("https://todoapp.palbase.studio")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cred.Value != "pat-from-the-environment" || source != SourceEnv {
		t.Fatalf("resolved %q from %s", cred.Value, source)
	}
	// A Dashboard-issued token is a PERSON's, and the header it goes in follows
	// from that rather than from the shape of the string.
	if cred.Kind != KindPerson {
		t.Errorf("the environment produced a %q credential", cred.Kind)
	}
	if _, err := os.Stat(filepath.Join(home, ".palbase", "credentials.json")); !os.IsNotExist(err) {
		t.Error("resolving from the environment created a credentials file")
	}
}

// TestTheStoreIsKeyedByURL: one machine, several projects, and a local stack
// whose credential `palbase start` writes for every checkout to find.
func TestTheStoreIsKeyedByURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(AccessTokenEnv, "")

	if err := StoreCredential("http://localhost:54321", Credentials{Value: "local-key", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential("https://todoapp.palbase.studio", Credentials{Value: "cloud-session", Kind: KindPerson}); err != nil {
		t.Fatal(err)
	}

	for url, want := range map[string]string{
		"http://localhost:54321":         "local-key",
		"https://todoapp.palbase.studio": "cloud-session",
	} {
		cred, source, err := Credential(url)
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		if cred.Value != want || source != SourceStore {
			t.Errorf("%s resolved %q from %s, want %q from the store", url, cred.Value, source, want)
		}
	}

	info, err := os.Stat(filepath.Join(home, ".palbase", "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the credentials file is %v — it is the ability to deploy to every project in it", info.Mode().Perm())
	}
}

// TestTheRefusalNamesBothWaysIn: a person with no credential is one sentence
// away from having one, and the sentence has to say which one applies.
func TestTheRefusalNamesBothWaysIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(AccessTokenEnv, "")

	_, _, err := Credential("http://localhost:54321")
	if err == nil {
		t.Fatal("an unknown target resolved a credential")
	}
	for _, want := range []string{"palbase start", "palbase login", AccessTokenEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// TestConcurrentWritesDoNotLoseACredential is the shared-machine case: two
// agents in two panes, or a `start` finishing while a `link` writes. A
// read-modify-write without a lock loses whichever finished first, and the
// symptom is being signed out of a project you just linked.
func TestConcurrentWritesDoNotLoseACredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(AccessTokenEnv, "")

	const writers = 16
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			url := "http://localhost:" + string(rune('a'+i))
			if err := StoreCredential(url, Credentials{Value: "token-" + string(rune('a'+i)), Kind: KindPerson}); err != nil {
				t.Errorf("write %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < writers; i++ {
		url := "http://localhost:" + string(rune('a'+i))
		cred, _, err := Credential(url)
		if err != nil {
			t.Fatalf("%s was lost: %v", url, err)
		}
		if cred.Value != "token-"+string(rune('a'+i)) {
			t.Errorf("%s resolved %q", url, cred.Value)
		}
	}
}

// TestForgettingOneLeavesTheOthers — `palbase logout` is per target.
func TestForgettingOneLeavesTheOthers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(AccessTokenEnv, "")

	if err := StoreCredential("http://localhost:54321", Credentials{Value: "local", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential("https://cloud.example", Credentials{Value: "cloud", Kind: KindPerson}); err != nil {
		t.Fatal(err)
	}
	if err := ForgetCredential("http://localhost:54321"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Credential("http://localhost:54321"); err == nil {
		t.Error("the forgotten credential still resolves")
	}
	if _, _, err := Credential("https://cloud.example"); err != nil {
		t.Errorf("forgetting one target removed another: %v", err)
	}
}

// TestEachCredentialGoesInTheHeaderItsProjectAccepts is the reason a credential
// carries a kind at all. The management gate reads a PERSON from Authorization
// and a KEY from `apikey`, and neither works in the other's place: a secret key
// sent as a Bearer gets "this stack did not issue that token" — true, and
// useless, because the caller was holding a credential the project accepts and
// presenting it the one way it does not.
func TestEachCredentialGoesInTheHeaderItsProjectAccepts(t *testing.T) {
	for _, c := range []struct {
		kind   Kind
		header string
		want   string
		absent string
	}{
		{KindPerson, "Authorization", "Bearer a-session", "apikey"},
		{KindKey, "apikey", "pb_project_sSECRET", "Authorization"},
	} {
		req, err := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
		if err != nil {
			t.Fatal(err)
		}
		value := strings.TrimPrefix(c.want, "Bearer ")
		Credentials{Value: value, Kind: c.kind}.Apply(req)

		if got := req.Header.Get(c.header); got != c.want {
			t.Errorf("%s credential set %s=%q, want %q", c.kind, c.header, got, c.want)
		}
		if got := req.Header.Get(c.absent); got != "" {
			t.Errorf("%s credential also set %s=%q — the value travels twice", c.kind, c.absent, got)
		}
	}
}

// BİR PALBASE ANAHTARI, ORTAM DEĞİŞKENİNDEN GELSE BİLE BİR ANAHTARDIR.
//
// Çözümleyicinin yorumu "there is no headless key" diyordu ve v2-cloud'da bu
// artık DOĞRU DEĞİL: bulut projesinin yönetim yüzeyini açan şey o projenin
// KENDİ `service_role` anahtarıdır ve hedefe-göreli her komut onunla konuşur.
//
// ÖLÇÜLDÜ (2026-08-21, canlı): aynı anahtar `curl -H "apikey: …"` ile
// `https://<ref>.v2.palbase.studio/v1/management/whoami` uçunu 200 ile açıyor;
// CLI ise onu `Bearer` olarak yolladığı için 401 alıyordu. Kimlik doğruydu,
// SUNULUŞU yanlıştı.
func TestAPalbaseKeyFromTheEnvironmentIsAKeyNotABearer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(AccessTokenEnv, "pb_juvuev3mm_sABCDEFGHIJKLMNOPQRSTUV")

	cred, source, err := Credential("https://juvuev3mm.v2.palbase.studio")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if source != SourceEnv {
		t.Fatalf("source = %q, environment beklenirdi", source)
	}
	if cred.Kind != KindKey {
		t.Fatalf("kind = %q — `pb_…` bir Palbase anahtarıdır ve `apikey` başlığında gider; "+
			"Bearer olarak yollamak 401 demek", cred.Kind)
	}

	// VE SUNULUŞU DOĞRU OLMALI: kind'ın tek işi bu.
	req, _ := http.NewRequest("GET", "https://juvuev3mm.v2.palbase.studio/v1/management/whoami", nil)
	cred.Apply(req)
	if got := req.Header.Get("apikey"); got != "pb_juvuev3mm_sABCDEFGHIJKLMNOPQRSTUV" {
		t.Fatalf("apikey başlığı %q — anahtar oraya konmalı", got)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("anahtar Authorization'a da konmuş — iki başlık iki kimlik demektir")
	}
}

// Dashboard token'ı DEĞİŞMEDEN kalır: bu değişiklik bir ayrım ekliyor, bir
// davranışı değiştirmiyor.
func TestANonKeyTokenIsStillABearer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(AccessTokenEnv, "eyJhbGciOiJFUzI1NiIsInR5cCI6IkpXVCJ9.abc.def")

	cred, _, err := Credential("https://app.example/project")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if cred.Kind != KindPerson {
		t.Fatalf("kind = %q — Dashboard token'ı bir KİŞİnin kimliğidir ve Bearer'da gider", cred.Kind)
	}
}

// BİR DÜZLEM OTURUMU, TENANT'IN KİMLİĞİ DEĞİLDİR.
//
// `PALBASE_ACCESS_TOKEN` is the documented headless credential for the CONTROL
// PLANE — "set it and every command resolves it" — so it holds a plane session
// far more often than a project key. A tenant's management surface takes that
// PROJECT's service key and nothing else, so handing it the session earns a 401
// from the project the person just created.
//
// Measured 2026-08-24: `palbase link https://<ref>.v2.palbase.studio` answered
// "did not accept this credential (401)" while the cloud could have brokered the
// right key on the very next line. The value's KIND already separates the two.
func TestAPlaneSessionDefersToTheCloudForAProjectAddress(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(AccessTokenEnv, "session-not-a-project-key")

	prev := CloudKeyFetcher
	CloudKeyFetcher = func(string) (string, error) { return "pb_project_sfromtheplane", nil }
	t.Cleanup(func() { CloudKeyFetcher = prev })

	cred, source, err := Credential("https://abc1234m.v2.palbase.studio")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cred.Value != "pb_project_sfromtheplane" {
		t.Fatalf("resolved %q from %s — the session was presented to the tenant", cred.Value, source)
	}
	if cred.Kind != KindKey {
		t.Errorf("a project key must travel as a key, got %q", cred.Kind)
	}
}

// AND A PROJECT KEY IN THE ENVIRONMENT STILL WINS. An agent holding the
// project's own secret key has nowhere else to put it, and asking the cloud
// instead would ignore what it was given.
func TestAProjectKeyInTheEnvironmentIsNotOverridden(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(AccessTokenEnv, "pb_project_sfromtheenvironment")

	prev := CloudKeyFetcher
	CloudKeyFetcher = func(string) (string, error) { return "pb_project_sfromtheplane", nil }
	t.Cleanup(func() { CloudKeyFetcher = prev })

	cred, _, err := Credential("https://abc1234m.v2.palbase.studio")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cred.Value != "pb_project_sfromtheenvironment" {
		t.Fatalf("the environment's own project key was overridden: %q", cred.Value)
	}
}

// AND AN ADDRESS THE CLOUD CANNOT ANSWER FOR KEEPS THE SESSION. A stack somebody
// hosts themselves is not this cloud's to speak for, so the environment stays
// the answer rather than becoming "no credential".
func TestASessionSurvivesForAnAddressTheCloudDoesNotOwn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(AccessTokenEnv, "session-not-a-project-key")

	prev := CloudKeyFetcher
	CloudKeyFetcher = func(string) (string, error) { return "", errors.New("not a project on this cloud") }
	t.Cleanup(func() { CloudKeyFetcher = prev })

	cred, source, err := Credential("https://stack.example.com")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cred.Value != "session-not-a-project-key" || source != SourceEnv {
		t.Fatalf("resolved %q from %s", cred.Value, source)
	}
}
