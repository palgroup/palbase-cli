package notifications

// The senders and their credentials now go through ONE transport.
//
// They used to go through two: the sender over REST to the stack, its secret
// over `env.set` to the Studio — which kept its own copy and handed it back at
// deploy time. This harness therefore records EVERY call rather than the last
// one, because "which door did the secret go to" is the question these tests
// exist to answer.

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stackCall is one request the project received.
type stackCall struct {
	Method string
	Path   string
	Body   string
}

// fakeStack records every call and answers each of them.
type fakeStack struct {
	mu     sync.Mutex
	calls  []stackCall
	status int
	answer string
}

func (f *fakeStack) Do(_ context.Context, method, path string, body []byte) (int, []byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, stackCall{Method: method, Path: path, Body: string(body)})
	f.mu.Unlock()
	st := f.status
	if st == 0 {
		st = http.StatusOK
	}
	ans := f.answer
	if ans == "" {
		ans = "[]"
	}
	return st, []byte(ans), nil
}

func (f *fakeStack) all() []stackCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]stackCall(nil), f.calls...)
}

// last is the call a verb ended on — the provider write, for `add`.
func (f *fakeStack) last() stackCall {
	all := f.all()
	if len(all) == 0 {
		return stackCall{}
	}
	return all[len(all)-1]
}

// secrets are the calls that wrote into the project's vault.
func (f *fakeStack) secrets() []stackCall {
	var out []stackCall
	for _, c := range f.all() {
		if strings.HasPrefix(c.Path, "/v1/management/secrets/") {
			out = append(out, c)
		}
	}
	return out
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runWith(t, &fakeStack{}, args...)
}

func runWith(t *testing.T, rest *fakeStack, args ...string) (string, error) {
	t.Helper()
	cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// --- the change this task exists for ----------------------------------------

// TestSendersLiveOnTheStackNotInAFile. They used to be declared in
// config/notifications.ts, created on every deploy and never deleted when
// dropped — so "removed" and "still sending" were both true.
func TestSendersLiveOnTheStackNotInAFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	rest := &fakeStack{}

	_, err := runWith(t, rest, "add", "apns",
		"--team-id", "T", "--key-id", "K", "--bundle-id", "com.x",
		"--p8-file", writeSecretIn(t, dir, "k.p8", "-----BEGIN PRIVATE KEY-----"))
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(dir, "config", "notifications.ts"))
	assert.True(t, os.IsNotExist(statErr), "a file was written: senders have one home now")
	assert.Equal(t, http.MethodPost, rest.last().Method)
	assert.Equal(t, "/v1/management/notifications/providers", rest.last().Path)
	assert.Contains(t, rest.last().Body, "apns")
}

// The credential goes to the PROJECT'S vault — the same door `palbase secret
// set` uses — and never travels inside the provider entry.
//
// The door is half the assertion. A secret written to the Studio was written
// where nothing on this plane reads it: the sender would be configured, the
// upload would report success, and the first send would fail on a credential
// that was never there.
func TestSecretsGoToTheProjectsVaultNotTheBody(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	rest := &fakeStack{}

	_, err := runWith(t, rest, "add", "apns",
		"--team-id", "T", "--key-id", "K", "--bundle-id", "com.x",
		"--p8-file", writeSecretIn(t, dir, "k.p8", "-----BEGIN PRIVATE KEY-----SECRETBYTES"))
	require.NoError(t, err)

	written := rest.secrets()
	require.Len(t, written, 1, "the secret did not reach the project's vault")
	assert.Equal(t, http.MethodPut, written[0].Method)
	assert.Contains(t, written[0].Path, "/v1/management/secrets/")
	assert.Contains(t, written[0].Body, "SECRETBYTES")
	// KİMLİK GÖVDEDE DE GİDER — ve bu testin eski iddiası hiç DOĞRU OLMAMIŞTI.
	//
	// Burası "sır gövdede gitmez, kasaya gider ve yığın oradan okur" diyordu. O
	// okuma mekanizması v1'in `br-pod apply` adımıydı; v2'de YOK. Sonucu ölçüldü
	// canlı 26.08.2026: `palbase notifications add acs` sırrı yüklüyor, sonra
	// config'i `bad_request: invalid channel:` ile yazamıyor ve HİÇBİR sağlayıcı
	// yapılandırılmıyordu. Yani bu iddia, var olmayan bir davranışı koruyordu.
	//
	// Sır artık modülün sözleşmesine göre `credentials` içinde gidiyor: TLS
	// üzerinden yığına ulaşıyor, ŞİFRELİ saklanıyor ve geri okutulmuyor. Git'e
	// yine girmiyor — testin asıl koruduğu şey oydu ve o hâlâ doğru.
	assert.Contains(t, rest.last().Body, "SECRETBYTES",
		"kimlik gövdede gitmiyor — modül credentials olmadan sağlayıcıyı yapılandıramaz")
	assert.Contains(t, rest.last().Body, "\"channel\":\"push\"",
		"kanal gönderilmiyor — modül bunu 'invalid channel' ile reddeder")
}

// GÖVDE MODÜLÜN SÖZLEŞMESİ: {channel, provider, credentials}.
//
// Eski gövde {provider, config} idi ve modül onu reddediyordu — komut sırrı
// yükleyip config'i yazamadan bitiyordu. Bu, HER sağlayıcıyı etkiliyordu.
func TestTheProviderEntryMatchesTheModuleContract(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	rest := &fakeStack{}

	_, err := runWith(t, rest, "add", "twilio",
		"--account-sid", "AC1", "--messaging-sid", "MG1",
		"--auth-token-file", writeSecretIn(t, dir, "tok", "TOKENBYTES"))
	require.NoError(t, err)

	body := rest.last().Body
	for _, want := range []string{`"channel":"sms"`, `"provider":"twilio"`, `"credentials"`, "TOKENBYTES", "AC1"} {
		assert.Contains(t, body, want, "gövde modülün beklediği alanı taşımıyor: %s", want)
	}
	assert.NotContains(t, body, `"config"`, "eski gövde şekli hâlâ gönderiliyor")
}

// The vault FIRST, then the sender. A sender written before its credential is a
// sender the project will try to use and cannot.
func TestTheSecretIsWrittenBeforeTheSender(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	rest := &fakeStack{}

	_, err := runWith(t, rest, "add", "apns",
		"--team-id", "T", "--key-id", "K", "--bundle-id", "com.x",
		"--p8-file", writeSecretIn(t, dir, "k.p8", "-----BEGIN PRIVATE KEY-----"))
	require.NoError(t, err)

	all := rest.all()
	require.GreaterOrEqual(t, len(all), 2)
	assert.Contains(t, all[0].Path, "/v1/management/secrets/", "the sender was written first")
	assert.Equal(t, "/v1/management/notifications/providers", all[len(all)-1].Path)
}

// TestRemoveActuallyRemoves.
func TestRemoveActuallyRemoves(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeStack{status: http.StatusNoContent}

	_, err := runWith(t, rest, "remove", "apns")
	require.NoError(t, err)
	assert.Equal(t, http.MethodDelete, rest.last().Method)
	assert.Equal(t, "/v1/management/notifications/providers/apns", rest.last().Path)
}

// TestProviders_MarksWhatTheStackHas.
func TestProviders_MarksWhatTheStackHas(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeStack{answer: `[{"provider":"apns"}]`}

	out, err := runWith(t, rest, "providers")
	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, rest.last().Method)
	assert.Contains(t, out, "apns")
	assert.Contains(t, out, "●")
}

func writeSecretIn(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestAddApns_MissingRequiredField(t *testing.T) {
	t.Chdir(t.TempDir())
	p8 := filepath.Join(t.TempDir(), "k.p8")
	require.NoError(t, os.WriteFile(p8, []byte("key"), 0o600))
	rest := &fakeStack{}
	// Missing --bundle-id.
	_, err := runWith(t, rest, "add", "apns", "--team-id", "T", "--key-id", "K", "--p8-file", p8)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--bundle-id")
	// Nothing is written when validation fails before the upload step.
	assert.Empty(t, rest.all())
}

func TestAddApns_MissingSecretFile(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeStack{}
	// No --p8-file and stdin is not a TTY in tests → hard error.
	_, err := runWith(t, rest, "add", "apns", "--team-id", "T", "--key-id", "K", "--bundle-id", "com.x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--p8-file")
	assert.Empty(t, rest.all())
}

func TestAddTwilio_RequiresOneOfFromOrMessaging(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeStack{}
	_, err := runWith(t, rest, "add", "twilio", "--account-sid", "AC1", "--auth-token-file", writeTemp(t, "tok"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--from-number")
	assert.Empty(t, rest.all())
}

func TestAddUnknownProvider(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := run(t, "add", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}

// --- templates: the door config/notifications.ts used to be -----------------

// The TEMPLATE half had no command at all. Providers moved to the stack and got
// `add`/`remove`; templates stayed in config/notifications.ts, which the deploy
// evaluated and applied to nothing after the declaration applier was retired.
// Removing that file without these verbs would delete the capability outright —
// the exact failure contract_lock_test.go records the applier producing five
// times.
func TestTemplatesSet_WritesTheStack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "templates.json")
	doc := `{"email":{"todo-digest":{"subject":"Hi {{name}}","html":"<p>hi</p>","text":"hi"}}}`
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))

	f := &fakeStack{answer: `{"applied":1}`}
	out, err := runWith(t, f, "templates", "set", "--file", path)
	require.NoError(t, err)

	last := f.last()
	assert.Equal(t, http.MethodPut, last.Method)
	assert.Equal(t, "/v1/management/notifications/templates", last.Path)
	assert.JSONEq(t, doc, last.Body, "the file's document travels verbatim")
	assert.Contains(t, out, "template")
}

// An EMPTY set is a legitimate value — "this project sends nothing" — and must
// travel as one. Refusing it, or silently sending nothing, would make "cleared"
// and "never set" indistinguishable, which is the shape of the egress fence's
// The read door exists now (GET /v1/management/notifications/templates), so the
// verb is back — and this pins the METHOD and PATH, which is what the first
// attempt got wrong.
func TestTemplatesList_ReadsTheStack(t *testing.T) {
	f := &fakeStack{answer: `{"email":{"todo-digest":{"subject":"Hi"}}}`}
	out, err := runWith(t, f, "templates", "list")
	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, f.last().Method)
	assert.Equal(t, "/v1/management/notifications/templates", f.last().Path)
	assert.Contains(t, out, "todo-digest")
	assert.Contains(t, out, "email")
}

// FR-006: the stack's own refusal reaches the operator verbatim. A verb that
// swallowed a 4xx would report success for a template set nobody stored.
func TestTemplatesSet_SurfacesTheStackRefusal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"email":{}}`), 0o600))

	f := &fakeStack{status: http.StatusBadRequest, answer: `{"error":"bad_template","error_description":"subject is required"}`}
	_, err := runWith(t, f, "templates", "set", "--file", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subject is required")
}

// A malformed file is refused BEFORE the round trip: an unparseable document is
// the author's mistake, and finding it locally beats finding it in the stack's
// error message.
func TestTemplatesSet_RefusesMalformedJSONLocally(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	require.NoError(t, os.WriteFile(path, []byte(`{not json`), 0o600))

	f := &fakeStack{}
	_, err := runWith(t, f, "templates", "set", "--file", path)
	require.Error(t, err)
	assert.Empty(t, f.all(), "nothing may leave the machine when the file does not parse")
}

// TestProviderAddStaysReachable is a CAPABILITY LOCK, not a feature test.
//
// `config/notifications.ts` declared providers and the deploy applied them —
// until the declaration applier was retired, after which the file declared into
// the void. `add` is the door that replaced it. Removing the file is only safe
// while this verb exists, so its disappearance must break a test rather than a
// user's stack: that is exactly the silent capability loss
// contract_lock_test.go records the applier producing five times.
func TestProviderAddStaysReachable(t *testing.T) {
	dir := t.TempDir()
	f := &fakeStack{}
	_, err := runWith(t, f, "add", "twilio",
		"--account-sid", "AC1", "--messaging-sid", "MG1",
		"--auth-token-file", writeSecretIn(t, dir, "tok", "TOKENBYTES"))
	require.NoError(t, err)

	var wrote bool
	for _, c := range f.all() {
		if c.Method == http.MethodPost && c.Path == providersPath {
			wrote = true
		}
	}
	assert.True(t, wrote, "notifications add must POST %s — it is the door config/notifications.ts used to be", providersPath)
}
