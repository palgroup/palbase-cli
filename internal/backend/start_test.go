package backend

// start_test.go — what `start` leaves behind, and where.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestStartNeedsNothingButTheBinary is the rule that replaced "the local stack
// is a dev rail, not a download".
//
// This asserted the opposite: with no PALBASE_STACK_DIR, `stackDirectory`
// refused and named the variable. That was defensible while the only people
// starting a stack had the palbase repository — and it stopped being true the
// day `palbase init` shipped on brew. A person scaffolds a project, types the
// command the same page prints two lines later, and is told to point an
// environment variable at a directory they have never heard of. The two commands
// are one sentence and only the first of them worked.
//
// So the binary carries the stack and writes it out. The variable still WINS
// when set, which the next test covers.
func TestStartNeedsNothingButTheBinary(t *testing.T) {
	t.Setenv(StackDirEnv, "")
	t.Setenv("HOME", t.TempDir())

	dir, err := stackDirectory("proofgroup")
	if err != nil {
		t.Fatalf("a machine with no stack directory was refused: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(dir, composeFile))
	if err != nil {
		t.Fatalf("no %s was written where the stack is brought up from: %v", composeFile, err)
	}
	if string(written) != string(stackCompose) {
		t.Error("what was written is not the document the binary carries")
	}
}

// The override exists for one person: somebody editing v2/deploy who wants their
// edit rather than the copy compiled into whichever CLI they have installed.
func TestAnExplicitStackDirectoryWins(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, composeFile), []byte("# an edited stack\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(StackDirEnv, repo)
	t.Setenv("HOME", t.TempDir())

	dir, err := stackDirectory("proofgroup")
	if err != nil {
		t.Fatalf("an explicit stack directory was refused: %v", err)
	}
	if dir != repo {
		t.Errorf("start used %s, not the directory it was pointed at (%s)", dir, repo)
	}
}

// And a variable pointed at the wrong place still refuses, naming what it looked
// for — silently falling back to the vendored copy would serve a stack the
// person believes they are editing.
func TestAStackDirectoryWithoutAComposeFileIsRefused(t *testing.T) {
	t.Setenv(StackDirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())

	_, err := stackDirectory("proofgroup")
	if err == nil {
		t.Fatal("a directory with no compose file was accepted")
	}
	if !strings.Contains(err.Error(), composeFile) {
		t.Errorf("the refusal does not name what it looked for: %v", err)
	}
}

// TestTheGroupComesFromTheLinkedProject: two checkouts of one project share a
// stack; two different projects never do. The group keys the compose project,
// the state directory and the registry, so getting it from the link rather than
// from the directory name is what makes "clone it again" not a second database.
func TestTheGroupComesFromTheLinkedProject(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()

	if got := groupName(dir); got != sanitiseGroup(filepath.Base(dir)) {
		t.Errorf("an unlinked checkout grouped as %q", got)
	}

	if err := WriteTarget(Target{Project: "TodoApp", Env: "main"}); err != nil {
		t.Fatal(err)
	}
	if got := groupName(dir); got != "todoapp" {
		t.Errorf("a linked checkout grouped as %q, want todoapp", got)
	}
}

// TestTheLocalPointerIsIgnoredButTheProjectFileIsNOT is the mistake this guards:
// `.palbase/` in a .gitignore takes project.json with it, and project.json is the
// one file a colleague cloning the repository needs.
func TestTheLocalPointerIsIgnoredButTheProjectFileIsNOT(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()

	if err := ignoreLocalTarget(dir); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), ".palbase/local.json") {
		t.Errorf(".gitignore does not carry the local pointer:\n%s", body)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == ".palbase" || strings.TrimSpace(line) == ".palbase/" {
			t.Error("the whole .palbase directory was ignored — project.json is committed on purpose")
		}
	}

	// Twice is once: a start that ran yesterday must not add a second line.
	if err := ignoreLocalTarget(dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if strings.Count(string(after), ".palbase/local.json") != 1 {
		t.Errorf("the entry was added twice:\n%s", after)
	}
}

// TestALocalStackWinsOverTheLinkedProject: FR-016's real consequence — after
// start, every verb acts locally without a flag, and after stop they all go back.
func TestALocalStackWinsOverTheLinkedProject(t *testing.T) {
	inScratchCheckout(t)
	if err := WriteTarget(Target{Project: "todoapp", Env: "production"}); err != nil {
		t.Fatal(err)
	}

	before, err := ReadTarget()
	if err != nil {
		t.Fatal(err)
	}
	if before.Describe() != "todoapp/production" {
		t.Fatalf("before start: %s", before.Describe())
	}

	if err := WriteLocalTarget(Target{URL: "http://127.0.0.1:51234"}); err != nil {
		t.Fatal(err)
	}
	during, err := ReadTarget()
	if err != nil {
		t.Fatal(err)
	}
	if !during.Local || during.Describe() != "http://127.0.0.1:51234 (local)" {
		t.Errorf("during start: %s (local=%v)", during.Describe(), during.Local)
	}

	if err := os.Remove(localPath()); err != nil {
		t.Fatal(err)
	}
	after, err := ReadTarget()
	if err != nil {
		t.Fatal(err)
	}
	if after.Describe() != "todoapp/production" {
		t.Errorf("after stop: %s", after.Describe())
	}
	// And the committed file never learned about the local address.
	raw, err := os.ReadFile(projectPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "127.0.0.1") {
		t.Errorf("the local address reached the committed file:\n%s", raw)
	}
}

// TestTheMachineRegisterFindsAStackFromAnotherDirectory is FR-061: an app
// checkout somewhere else on this machine has nothing to go on but the group's
// name.
func TestTheMachineRegisterFindsAStackFromAnotherDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got := LookupLocalStack("todoapp"); got != "" {
		t.Errorf("a stack was found before one started: %q", got)
	}
	if err := registerStack("todoapp", "http://127.0.0.1:51234", "palbase-todoapp", "/somewhere/backend"); err != nil {
		t.Fatal(err)
	}
	if got := LookupLocalStack("todoapp"); got != "http://127.0.0.1:51234" {
		t.Errorf("the register answered %q", got)
	}
	// The name is normalised on the way in AND on the way out, so an app
	// checkout that knows the project as "TodoApp" still finds it.
	if got := LookupLocalStack("TodoApp"); got != "http://127.0.0.1:51234" {
		t.Errorf("a differently-cased group answered %q", got)
	}
	if err := deregisterStack("todoapp"); err != nil {
		t.Fatal(err)
	}
	if got := LookupLocalStack("todoapp"); got != "" {
		t.Errorf("a stopped stack is still registered: %q", got)
	}
}

// TestTheStackKeepsItsPortAcrossRestarts: a URL written into local.json, a
// generated client and an iOS build all point at a number. Picking a fresh one
// on every start would break each of them silently.
func TestTheStackKeepsItsPortAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("PALBASE_ANON_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := rememberPort(envFile, 51234)
	if err != nil {
		t.Fatal(err)
	}
	if first != 51234 {
		t.Fatalf("first run chose %d", first)
	}

	second, err := rememberPort(envFile, 60000)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("a restart moved the stack from %d to %d", first, second)
	}
}

// TestBootValuesStayOutOfTheCheckout is FR-062: they are secrets, they are
// per-machine, and a .env in a repository is a .env somebody commits.
func TestBootValuesStayOutOfTheCheckout(t *testing.T) {
	inScratchCheckout(t)
	checkout, _ := os.Getwd()
	home := os.Getenv("HOME")

	state, err := stackStateDir("todoapp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(state, home) {
		t.Errorf("the state directory is %s, outside %s", state, home)
	}
	if strings.HasPrefix(state, checkout) {
		t.Errorf("the boot values landed inside the checkout: %s", state)
	}
	info, err := os.Stat(state)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("the directory holding this stack's keys is %o", perm)
	}
}

// TestASecretChangedHereIsKept is FR-020, and the case it protects is ordinary:
// somebody points SENTRY_DSN at a throwaway project so their dev traffic stops
// paging the team. A start that overwrote that every morning is a tool people
// learn to work around.
func TestASecretChangedHereIsKept(t *testing.T) {
	const pulledValue = "https://key@sentry.io/production"

	local := Target{URL: "http://127.0.0.1:1", Local: true}
	cred := Credentials{Value: "k", Kind: KindKey}

	// Never pulled before → not a local change, so it is pulled.
	if changedLocally(context.Background(), local, cred, "SENTRY_DSN", "") {
		t.Error("a name that was never pulled counted as changed")
	}

	// Pulled before, and the stack still holds exactly that → overwrite.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(pulledValue))
	}))
	defer srv.Close()
	unchanged := Target{URL: srv.URL, Local: true}
	if changedLocally(context.Background(), unchanged, cred, "SENTRY_DSN", hashOf(pulledValue)) {
		t.Error("an untouched secret counted as changed")
	}

	// Pulled before, and the stack now holds something else → keep.
	edited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("https://key@sentry.io/my-throwaway"))
	}))
	defer edited.Close()
	if !changedLocally(context.Background(), Target{URL: edited.URL, Local: true}, cred, "SENTRY_DSN", hashOf(pulledValue)) {
		t.Error("a locally edited secret would have been overwritten")
	}
}

// TestThePullRecordHoldsNoValues: the record exists to detect a local edit, and
// a file of values beside the vault would be the dotenv this design removed
// under a different name.
func TestThePullRecordHoldsNoValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const value = "sk-live-DO-NOT-LEAK"

	if err := writePulled("todoapp", map[string]string{"STRIPE_KEY": hashOf(value)}); err != nil {
		t.Fatal(err)
	}
	path, err := pulledPath("todoapp")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), value) {
		t.Errorf("the pull record carries the value:\n%s", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the pull record is %o", perm)
	}

	back, err := readPulled("todoapp")
	if err != nil {
		t.Fatal(err)
	}
	if back["STRIPE_KEY"] != hashOf(value) {
		t.Errorf("the record did not survive the round trip: %v", back)
	}
}

// TestTheImageCheckAsksForTheTAGCOMPOSEUSES is a bug this test exists because
// of: the check looked for `palbase-runtime` while the compose file resolves
// `${PALBASE_RUNTIME_IMAGE:-palbase-runtime-dev}`, and it PASSED anyway, because
// another stack on the machine had built the right one. On a clean machine it
// would have waved the start through into a compose pull error for an image no
// registry has.
//
// The `-dev` suffix is not decoration: the shipped stack builds its runtime
// under the plain name, so a dev image written over that tag leaves the
// production stack running a dev image the next time it is recreated.
func TestTheImageCheckAsksForTheTAGCOMPOSEUSES(t *testing.T) {
	compose, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "v2", "deploy", composeFile))
	if err != nil {
		t.Skipf("the dev compose file is not beside this checkout: %v", err)
	}

	for _, want := range stackImages {
		// The variable and its default, exactly as the compose file spells them.
		spelling := "${" + want.env + ":-" + want.fallback + "}"
		if !strings.Contains(string(compose), spelling) {
			t.Errorf("the compose file does not resolve %s — this check would look for an image it never pulls", spelling)
		}
		if want.build == "" {
			t.Errorf("%s has no build command, so its refusal cannot say how to fix it", want.env)
		}
	}
}

// TestAStartOnARunningStackKeepsItsPort is the defect this shape exists for: the
// port is occupied precisely when OUR OWN stack is up, so a "is it free" check
// moved the stack on every restart-while-running — and every xcconfig, app
// config and generated client written by an earlier `link` still named the old
// number.
func TestAStartOnARunningStackKeepsItsPort(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("PALBASE_ANON_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := rememberPort(envFile, 51234)
	if err != nil {
		t.Fatal(err)
	}

	// Bind it, which is what a running stack does.
	held, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", first))
	if err != nil {
		t.Skipf("could not bind %d to simulate a running stack: %v", first, err)
	}
	defer func() { _ = held.Close() }()

	second, err := rememberPort(envFile, 60000)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("a start on a running stack moved it from %d to %d", first, second)
	}

	// And ONE answer lives in the file: the writer and the reader must not
	// disagree, which is what a second PALBASE_HTTP_PORT line produced.
	body, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), "PALBASE_HTTP_PORT="); n != 1 {
		t.Errorf("the env file sets PALBASE_HTTP_PORT %d times:\n%s", n, body)
	}
	back, err := readPort(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if back != first {
		t.Errorf("the reader answers %d while the writer answered %d", back, first)
	}
}

// TestTheStoredCredentialBeatsTheAmbientOne: the store is keyed by ADDRESS and
// written deliberately by `palbase start` or `palbase login`; the environment
// variable is exported once and applies to everything. An agent in a container
// with a Dashboard token exported used to run `palbase start`, have the right key
// written, and then have every call carry the PAT instead — with the refusal
// advising `palbase start`, which they had just run.
func TestTheStoredCredentialBeatsTheAmbientOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(AccessTokenEnv, "a-dashboard-token")

	const local = "http://127.0.0.1:51234"
	if err := StoreCredential(local, Credentials{Value: "the-stacks-own-key", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}

	cred, source, err := Credential(local)
	if err != nil {
		t.Fatal(err)
	}
	if cred.Value != "the-stacks-own-key" || cred.Kind != KindKey || source != SourceStore {
		t.Errorf("resolved %q (%s) from %s", cred.Value, cred.Kind, source)
	}

	// …and the ambient one still answers for an address nobody stored.
	cloud, source, err := Credential("https://todoapp.palbase.studio")
	if err != nil {
		t.Fatal(err)
	}
	if cloud.Value != "a-dashboard-token" || cloud.Kind != KindPerson || source != SourceEnv {
		t.Errorf("the headless path broke: %q (%s) from %s", cloud.Value, cloud.Kind, source)
	}
}

// TestALocalStackAnswersForItselfWithNoCopy is the question this design owes an
// answer to: why would `palbase start` write a credential at all?
//
// It should not. The key already exists once, in the group's state directory,
// written by the stack's own --init-env. Copying it into the credential store
// made a second copy of a secret this design otherwise refuses to duplicate —
// and a copy has to be kept in step: `stop` left it behind, and `--reset` gave
// the stack a new key while the copy went on claiming the old one.
func TestALocalStackAnswersForItselfWithNoCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(AccessTokenEnv, "")

	const url = "http://127.0.0.1:51234"
	state, err := stackStateDir("todoapp")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, ".env"),
		[]byte("PALBASE_SERVICE_ROLE_KEY=pb_project_sTHEKEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := registerStack("todoapp", url, "palbase-todoapp", "/somewhere"); err != nil {
		t.Fatal(err)
	}

	cred, source, err := Credential(url)
	if err != nil {
		t.Fatal(err)
	}
	if cred.Value != "pb_project_sTHEKEY" || cred.Kind != KindKey {
		t.Errorf("resolved %q (%s)", cred.Value, cred.Kind)
	}
	if source != SourceLocalStack {
		t.Errorf("resolved from %s, want the stack itself", source)
	}

	// And NOTHING was copied: the credential store does not exist.
	if _, err := os.Stat(filepath.Join(home, ".palbase", "credentials.json")); !os.IsNotExist(err) {
		t.Errorf("a copy of the key was written to the credential store (%v)", err)
	}

	// A NEW key — what `--reset` produces — is picked up on the next call,
	// which a copy could not do.
	if err := os.WriteFile(filepath.Join(state, ".env"),
		[]byte("PALBASE_SERVICE_ROLE_KEY=pb_project_sROTATED\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, _, err := Credential(url)
	if err != nil {
		t.Fatal(err)
	}
	if again.Value != "pb_project_sROTATED" {
		t.Errorf("a rotated key was not picked up: %q", again.Value)
	}

	// An address no stack here serves falls through to the ordinary refusal.
	if _, _, err := Credential("https://todoapp.palbase.studio"); err == nil {
		t.Error("a cloud address resolved from the local register")
	}
}

// YEREL YIĞIN DA KENDİ ADRESİNİ BİLMEK ZORUNDA.
//
// Bir `@Upload` handler'ı cevabına `getPublicUrl` ile nesne URL'i koyuyor ve o
// URL cevabın gövdesinden ÇIKIYOR. Yığının içindeki adres (`http://palsvc:8080`)
// dışarıdan çözülmez; göreli bir yol da çözülmez — ölçüldü canlı 24.08.2026,
// iOS istemcisi `/v1/files/...` alınca NSURLErrorUnsupportedURL verdi.
//
// Bu adresi bilen tek yer BURASI: portu `palbase start` seçiyor.
func TestTheLocalStackIsToldTheAddressItServesOn(t *testing.T) {
	env := composeEnv("/tmp/proj", "", 54321)

	want := "PALBASE_PUBLIC_ORIGIN=http://127.0.0.1:54321"
	if !slices.Contains(env, want) {
		t.Fatalf("compose ortamında %q yok:\n%s", want, strings.Join(env, "\n"))
	}
}

// `--lan` ile bağlanan yığın, TELEFONUN ulaştığı adresi ilan eder.
//
// Loopback ilan etseydi aynı wifi'daki telefon dönen nesne URL'ini KENDİ
// makinesinde arardı: istek 200 döner, resim yüklenmez, hiçbir yerde hata olmaz.
func TestALanBoundStackAdvertisesTheAddressThePhoneUses(t *testing.T) {
	env := composeEnv("/tmp/proj", "192.168.1.40", 54321)

	want := "PALBASE_PUBLIC_ORIGIN=http://192.168.1.40:54321"
	if !slices.Contains(env, want) {
		t.Fatalf("compose ortamında %q yok:\n%s", want, strings.Join(env, "\n"))
	}
}

// Zinciri OLMAYAN bir .env, ürünün kendi üreticisiyle zinciri kazanmalı.
//
// Ölçülen arıza: ensureBootValues, .env varsa hemen dönüyordu, dolayısıyla mühürleme
// zinciri eklenmeden önce (v2 e0246a4, 2026-08-27) yaratılmış bir yığın onu ASLA
// kazanmıyordu — o makinede mühürlemek zorunda olan her istemci (iOS SDK her /auth/*
// gövdesini mühürler ve açıkta göndermeyi reddeder) giriş yapamamaya devam ediyordu.
func TestSealingChainStateCountsWhatIsThere(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")

	if err := os.WriteFile(env, []byte("PALBASE_ANON_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := sealingChainState(env); err != nil || n != 0 {
		t.Fatalf("zincirsiz .env %d dondu (hata %v); 0 olmaliydi", n, err)
	}

	if err := os.WriteFile(env, []byte(
		"PALBASE_ANON_KEY=x\nPALBASE_SEALED_SIGNING_SEED=a\nPALBASE_SEALED_BINDING=b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := sealingChainState(env); err != nil || n != 2 {
		t.Fatalf("yarim zincir %d dondu (hata %v); 2 olmaliydi", n, err)
	}

	if err := os.WriteFile(env, []byte(
		"PALBASE_SEALED_SIGNING_SEED=a\nPALBASE_SEALED_BINDING=b\nPALBASE_SEALED_ROOT=c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := sealingChainState(env); err != nil || n != 3 {
		t.Fatalf("tam zincir %d dondu (hata %v); 3 olmaliydi", n, err)
	}

	// Dosya yoksa: sifir, hata degil — ensureBootValues bunu "yeni yigin" olarak okur.
	if n, err := sealingChainState(filepath.Join(dir, "yok.env")); err != nil || n != 0 {
		t.Fatalf("olmayan dosya %d dondu (hata %v); (0, nil) olmaliydi", n, err)
	}
}

// FR-005: yarim zincir yazilmaz. verify.sh'in kanitli kurali — "half a chain is not a
// chain": eslesmeyen bir SEED'in yanina ikinci bir BINDING eklemek, cogu .env okuyucusu
// icin son-deger-kazanir demektir, yani baska bir kilikta uzerine yazma.
func TestPartialSealingChainIsRefusedByName(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("PALBASE_SEALED_SIGNING_SEED=a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(env)

	err := migrateSealingChain(context.Background(), env, io.Discard)
	if err == nil {
		t.Fatal("yarim zincir kabul edildi; reddedilmeliydi")
	}
	if !strings.Contains(err.Error(), "1 of 3") {
		t.Fatalf("hata kac degisken bulundugunu soylemiyor: %v", err)
	}
	after, _ := os.ReadFile(env)
	if string(before) != string(after) {
		t.Fatalf(".env yarim zincir halindeyken DEGISTIRILDI:\nonce: %s\nsonra: %s", before, after)
	}
}

// FR-006: tam zincir varsa dokunulmaz — yeniden mint etmek, o zincirle muhurlenmis
// her seyi oksuz birakirdi.
func TestCompleteSealingChainIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	body := "PALBASE_SEALED_SIGNING_SEED=a\nPALBASE_SEALED_BINDING=b\nPALBASE_SEALED_ROOT=c\n"
	if err := os.WriteFile(env, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateSealingChain(context.Background(), env, io.Discard); err != nil {
		t.Fatalf("tam zincir hata verdi: %v", err)
	}
	after, _ := os.ReadFile(env)
	if string(after) != body {
		t.Fatalf("tam zincir degistirildi:\nonce: %s\nsonra: %s", body, after)
	}
}

// KABLOLAMA: ensureBootValues yargiyi GERCEKTEN cagiriyor mu.
//
// Ustteki uc test yardimcilari olcuyor; bu, onlarin uretim yoluna bagli oldugunu
// olcuyor. Onemi su: erken donus geri gelirse ("`.env` varsa hicbir sey yapma")
// yardimcilar hala yesil kalir ve kusur sessizce geri doner. Yarim zincir secildi
// cunku karar docker'a hic ulasmadan veriliyor — test bir konteyner baslatmaz.
func TestEnsureBootValuesJudgesAnExistingEnv(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("PALBASE_SEALED_BINDING=b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ensureBootValues(context.Background(), env, io.Discard)
	if err == nil {
		t.Fatal("mevcut .env icin ensureBootValues hicbir sey yapmadi; yarim zinciri reddetmeliydi")
	}
	if !strings.Contains(err.Error(), "half a chain is not a chain") {
		t.Fatalf("ensureBootValues yargilamiyor: %v", err)
	}
}

// KABLOLAMA, ikinci yarisi: goc yapildiginda cagirana SOYLENMELI.
//
// Ucdan uca olcum bunu ortaya cikardi: zincir .env'e yazildi ama konteynerin
// ortaminda yoktu (docker exec printenv | grep -c PALBASE_SEALED -> 0), cunku
// compose zaten calisan konteynerleri yeniden yaratmadan yeni --env-file'i
// okumuyor. Yigin hala "sealed_unconfigured" diyordu ve iOS giris yapamiyordu —
// yani "clients that must seal can sign in now" mesaji o durumda YALANDI.
//
// Bu test o sinyali sabitler; start onu gorup --force-recreate ekliyor.
func TestEnsureBootValuesReportsThatItMigrated(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")

	// Zinciri TAM olan bir .env: goc yok, sinyal de yok.
	full := "PALBASE_SEALED_SIGNING_SEED=a\nPALBASE_SEALED_BINDING=b\nPALBASE_SEALED_ROOT=c\n"
	if err := os.WriteFile(env, []byte(full), 0o600); err != nil {
		t.Fatal(err)
	}
	migrated, err := ensureBootValues(context.Background(), env, io.Discard)
	if err != nil {
		t.Fatalf("tam zincir hata verdi: %v", err)
	}
	if migrated {
		t.Fatal("hicbir sey degismediigi halde goc bildirildi — stack bosuna yeniden yaratilirdi")
	}
}

// .env son satiri newline ile bitmiyorsa zincir eklemesi onu BOZMAMALI.
//
// Bu, yayinlanmis kodda bulundu: dosya `STACK_ROOT_KEY=xyz` ile bitiyorsa append
// ilk eklenen satiri ona YAPISTIRIYORDU —
//
//	STACK_ROOT_KEY=xyzPALBASE_SEALED_SIGNING_SEED=a
//
// — ve bu IKI degiskeni birden yok ediyor: yiginin kendi kok anahtari cope
// donuyor, imzalama tohumu da hic gorunmuyor. POSIX metin dosyalari newline ile
// biter, ama bir .env'i ne yazdiysa o yazmistir; "genelde" bir dosyaya karsi
// append edilecek bir ozellik degil.
//
// Docker'a gitmeyen bir yol seciliyor: mint adimi bir imaj ister, ama bozulma
// EKLEME adiminda oluyor. O yuzden zincir ONCEDEN tam yaziliyor ve fonksiyonun
// dokunmadigi dogrulaniyor; bozulmanin kendisi asagidaki dogrudan olcumle
// sabitleniyor.
func TestSealingChainAppendSurvivesAMissingTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("PALBASE_ANON_KEY=abc\nSTACK_ROOT_KEY=xyz"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := appendSealingChain(env, "PALBASE_SEALED_SIGNING_SEED=a\nPALBASE_SEALED_BINDING=b\nPALBASE_SEALED_ROOT=c\n"); err != nil {
		t.Fatalf("ekleme hata verdi: %v", err)
	}

	body, _ := os.ReadFile(env)
	for _, want := range []string{"PALBASE_ANON_KEY=abc", "STACK_ROOT_KEY=xyz", "PALBASE_SEALED_SIGNING_SEED=a"} {
		found := false
		for _, line := range strings.Split(string(body), "\n") {
			if strings.TrimSpace(line) == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q kendi satirinda degil — dosya:\n%s", want, body)
		}
	}
	if n, _ := sealingChainState(env); n != 3 {
		t.Errorf("ekleme sonrasi zincir %d/3 gorunuyor", n)
	}
}

// `palbase push` bir lokal yığında REDDEDİLİYOR ve doğru sebeple: dev runtime
// mount ettiği DİZİNİ servis ediyor, deploy pointer'ını hiç takip etmiyor, yani
// bir push hiçbir şeyin yüklemeyeceği bir sürümü aktive ederdi. Reddin metni de
// yerine ne yapılacağını söylüyor. AMA bu ancak DENEYİNCE öğreniliyordu: start
// biterken "kaynak canlı servis edilir" diyor, şema için ne yapılacağını
// söylemiyordu — bir kiracı push'u deneyip reddi okuyarak öğrendi.
func TestStartBannerSaysHowToApplyTheSchema(t *testing.T) {
	lines := startBanner("http://127.0.0.1:63638")

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "db apply") {
		t.Errorf("banner şema için ne yapılacağını söylemiyor:\n%s", joined)
	}
	if !strings.Contains(joined, "http://127.0.0.1:63638") {
		t.Errorf("banner adresi kaybetti:\n%s", joined)
	}
	// Var olan iki cümle korunur — bu bir ekleme, yeniden yazım değil.
	if !strings.Contains(joined, "no build, no deploy") {
		t.Errorf("banner 'no build, no deploy' cümlesini kaybetti:\n%s", joined)
	}
	if !strings.Contains(joined, "palbase stop") {
		t.Errorf("banner 'palbase stop' cümlesini kaybetti:\n%s", joined)
	}
}

// YEREL YIĞININ İMAJLARI ÇEKİRDEĞİN TEK KAYNAĞIYLA AYNI SÜRÜMDE OLMALI.
//
// CANLIDA ÖLÇÜLDÜ (2026-09-01): `palbase db plan` ve `db apply` bu deponun
// KENDİ backend'inde düşüyordu —
//
//	db/schema.ts did not evaluate: Expected ";" but found ":" (422)
//
// — ve o dizinde `db/schema.ts` diye bir dosya YOK; `db/public.ts` var. Mesajı
// üreten palsvc'ydi: koşan binary `db/schema.ts` dizesini 11 kez taşıyor,
// `db/public.ts`'i SIFIR kez. Yani yerel yığın DSL göçünden ÖNCEKİ bir raile
// bakıyordu ve geliştirici yerel şemasını hiç ilerletemiyordu.
//
// TUZAK, PİNİN ESKİ OLMASI DEĞİL — NUMARASININ BÜYÜK OLMASI: `0.39.0`
// 29.08.2026'da yayımlandı, `0.36.5` ise 31.08'de. Sıralamada büyük görünen
// etiket terk edilmiş bir seriden geliyordu, ve "daha yeni" sanıldı.
//
// Bu yüzden test sürümleri KARŞILAŞTIRMIYOR, EŞİTLİK istiyor ve otoriteyi
// deponun tek çekirdek sürüm beyanından okuyor. `version.env`'in kendi başlığı
// zaten bunu söylüyor: "iki yerde ayrı ayrı yazılsaydı biri güncellenip diğeri
// unutulduğunda aynı ortamda İKİ FARKLI çekirdek koşardı ve bunu hiçbir hata
// mesajı söylemezdi." `start.go` tam olarak o ikinci yazandı.
//
// Ve dev = prod aynası olduğu için bu bir tercih değil: geliştiricinin yığını
// bulutun koştuğu çekirdekten geri kalırsa, yerelde geçen şey yayında düşer.
func TestStackImagesTrackTheCoreVersion(t *testing.T) {
	const authority = "../../../../v2-cloud/bootstrap/images/version.env"
	raw, err := os.ReadFile(authority)
	if err != nil {
		// Otorite palbase deposunda yaşıyor ve bu CLI kendi başına da klonlanır
		// (her CI runner'ı öyle). Komşusu `TestTheVendoredComposeMatchesTheRepository`
		// aynı sebeple ATLIYOR: pin YALNIZ iki ağacın yan yana olduğu yerde —
		// yani pinin gerçekten düzenlendiği geliştirici makinesinde — ölçülebilir.
		// Okunamayan bir girdi karşısında DÜŞMEK, ölçemediği şeyi kırmızı
		// göstermek olurdu.
		t.Skipf("palbase deposu bu checkout'un yanında değil: %v", err)
	}
	var core string
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "V2_VERSION="); ok {
			core = strings.TrimSpace(rest)
			break
		}
	}
	if core == "" {
		t.Fatalf("%s içinde V2_VERSION yok — bu dosya artık otorite değilse test de yanlış yerde", authority)
	}

	for _, img := range stackImages {
		tag := img.fallback[strings.LastIndex(img.fallback, ":")+1:]
		if tag != core {
			t.Errorf("%s varsayılanı %q sürümünü pinliyor, çekirdek ise %q — "+
				"yerel yığın buluttan farklı bir çekirdek koşar (imaj: %s)",
				img.env, tag, core, img.fallback)
		}
	}
}
