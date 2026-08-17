package backend

// start_test.go — what `start` leaves behind, and where.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStartRefusesWithoutAStackToBootFrom: the local stack is a dev rail, not a
// download, so the refusal names the variable rather than inventing a fetch.
func TestStartRefusesWithoutAStackToBootFrom(t *testing.T) {
	t.Setenv(StackDirEnv, "")

	_, err := stackDirectory()
	if err == nil {
		t.Fatal("a machine with no stack directory was accepted")
	}
	if !strings.Contains(err.Error(), StackDirEnv) {
		t.Errorf("the refusal does not name the variable: %v", err)
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

	first, err := rememberPorts(envFile, 51234, 55432)
	if err != nil {
		t.Fatal(err)
	}
	if first.http != 51234 || first.pg != 55432 {
		t.Fatalf("first run chose %+v", first)
	}

	second, err := rememberPorts(envFile, 60000, 60001)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("a restart moved the stack from %+v to %+v", first, second)
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
	first, err := rememberPorts(envFile, 51234, 55432)
	if err != nil {
		t.Fatal(err)
	}

	// Bind it, which is what a running stack does.
	held, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", first.http))
	if err != nil {
		t.Skipf("could not bind %d to simulate a running stack: %v", first.http, err)
	}
	defer func() { _ = held.Close() }()

	second, err := rememberPorts(envFile, 60000, 60001)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("a start on a running stack moved it from %+v to %+v", first, second)
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
	back, err := readPorts(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if back != first {
		t.Errorf("the reader answers %+v while the writer answered %+v", back, first)
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
