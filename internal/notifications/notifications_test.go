package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// chdirTemp moves into a fresh temp dir so each test gets its own
// config/notifications.ts. Restores cwd on cleanup.
type capturedMutation struct {
	Path  string
	Key   string
	Value string
	Sec   bool
}

// fakeStudio is an httptest.Server that records tRPC env.set mutations and a
// studio.Client pointed at it.
type fakeStudio struct {
	srv  *httptest.Server
	mu   sync.Mutex
	muts []capturedMutation
}

func newFakeStudio(t *testing.T) *fakeStudio {
	t.Helper()
	f := &fakeStudio{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// tRPC mutation path: /api/trpc/env.set, body {"json":{ref,key,value,isSecret}}
		var body struct {
			JSON struct {
				Ref      string `json:"ref"`
				Key      string `json:"key"`
				Value    string `json:"value"`
				IsSecret bool   `json:"isSecret"`
			} `json:"json"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.muts = append(f.muts, capturedMutation{
			Path:  strings.TrimPrefix(r.URL.Path, "/api/trpc/"),
			Key:   body.JSON.Key,
			Value: body.JSON.Value,
			Sec:   body.JSON.IsSecret,
		})
		f.mu.Unlock()
		// tRPC success envelope.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"data":{"json":{"key":"` + body.JSON.Key + `","isSecret":true}}}}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeStudio) client() *studio.Client {
	return studio.New(
		f.srv.URL,
		func(context.Context) (string, error) { return "test-token", nil },
		func(context.Context, string, string, string) (string, error) { return "test-proof", nil },
	)
}

func (f *fakeStudio) calls() []capturedMutation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]capturedMutation(nil), f.muts...)
}

// run drives the notifications command tree with args, capturing stdout+stderr.
// fakeREST records what a verb asked the stack for.
type fakeREST struct {
	method, path, body string
	status             int
	answer             string
}

func (f *fakeREST) Do(_ context.Context, method, path string, body []byte) (int, []byte, error) {
	f.method, f.path, f.body = method, path, string(body)
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

func run(t *testing.T, studioFn func() *studio.Client, args ...string) (string, error) {
	t.Helper()
	return runWith(t, &fakeREST{}, studioFn, args...)
}

func runWith(t *testing.T, rest *fakeREST, studioFn func() *studio.Client, args ...string) (string, error) {
	t.Helper()
	cmd := Cmd(Resolvers{
		REST:      func(*cobra.Command) (REST, error) { return rest, nil },
		Studio:    studioFn,
		Selection: selectiontest.Selected(t),
	})
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
	f := newFakeStudio(t)
	rest := &fakeREST{}

	_, err := runWith(t, rest, f.client, "add", "apns",
		"--team-id", "T", "--key-id", "K", "--bundle-id", "com.x",
		"--p8-file", writeSecretIn(t, dir, "k.p8", "-----BEGIN PRIVATE KEY-----"))
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(dir, "config", "notifications.ts"))
	assert.True(t, os.IsNotExist(statErr), "a file was written: senders have one home now")
	assert.Equal(t, http.MethodPost, rest.method)
	assert.Equal(t, "/v1/management/notifications/providers", rest.path)
	assert.Contains(t, rest.body, "apns")
}

// TestSecretsStillGoToTheVaultNotTheBody keeps the property that mattered before
// and still does: a credential never travels in the provider entry.
func TestSecretsStillGoToTheVaultNotTheBody(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	f := newFakeStudio(t)
	rest := &fakeREST{}

	_, err := runWith(t, rest, f.client, "add", "apns",
		"--team-id", "T", "--key-id", "K", "--bundle-id", "com.x",
		"--p8-file", writeSecretIn(t, dir, "k.p8", "-----BEGIN PRIVATE KEY-----SECRETBYTES"))
	require.NoError(t, err)

	assert.NotEmpty(t, f.calls(), "the secret never reached the vault")
	assert.NotContains(t, rest.body, "SECRETBYTES",
		"a credential travelled in the provider entry: they go to the vault and the stack reads them there")
}

// TestRemoveActuallyRemoves.
func TestRemoveActuallyRemoves(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{status: http.StatusNoContent}

	_, err := runWith(t, rest, nil, "remove", "apns")
	require.NoError(t, err)
	assert.Equal(t, http.MethodDelete, rest.method)
	assert.Equal(t, "/v1/management/notifications/providers/apns", rest.path)
}

// TestProviders_MarksWhatTheStackHas.
func TestProviders_MarksWhatTheStackHas(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{answer: `[{"provider":"apns"}]`}

	out, err := runWith(t, rest, nil, "providers")
	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, rest.method)
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

func chdirTemp(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

func TestAddApns_MissingRequiredField(t *testing.T) {
	t.Chdir(t.TempDir())
	chdirTemp(t)
	p8 := filepath.Join(t.TempDir(), "k.p8")
	require.NoError(t, os.WriteFile(p8, []byte("key"), 0o600))
	fs := newFakeStudio(t)
	// Missing --bundle-id.
	_, err := run(t, fs.client, "add", "apns", "--team-id", "T", "--key-id", "K", "--p8-file", p8)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--bundle-id")
	// No secret uploaded when validation fails before the upload step.
	assert.Empty(t, fs.calls())
}

func TestAddApns_MissingSecretFile(t *testing.T) {
	t.Chdir(t.TempDir())
	chdirTemp(t)
	fs := newFakeStudio(t)
	// No --p8-file and stdin is not a TTY in tests → hard error.
	_, err := run(t, fs.client, "add", "apns", "--team-id", "T", "--key-id", "K", "--bundle-id", "com.x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--p8-file")
	assert.Empty(t, fs.calls())
}

func TestAddTwilio_RequiresOneOfFromOrMessaging(t *testing.T) {
	t.Chdir(t.TempDir())
	chdirTemp(t)
	fs := newFakeStudio(t)
	_, err := run(t, fs.client, "add", "twilio", "--account-sid", "AC1", "--auth-token-file", writeTemp(t, "tok"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--from-number")
	assert.Empty(t, fs.calls())
}

func TestAddUnknownProvider(t *testing.T) {
	t.Chdir(t.TempDir())
	chdirTemp(t)
	_, err := run(t, nil, "add", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}
