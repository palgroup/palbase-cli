package notifications

import (
	"bytes"
	"context"
	"encoding/json"
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
func chdirTemp(t *testing.T) {
	t.Helper()
	orig, err := os.Getwd()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// capturedMutation is one env.set call the fake Studio server recorded.
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
func run(t *testing.T, studioFn func() *studio.Client, args ...string) (string, error) {
	t.Helper()
	t.Helper()
	cmd := Cmd(Resolvers{Studio: studioFn, Selection: selectiontest.Selected(t)})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func readConfigFile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	return string(data)
}

// --- providers --------------------------------------------------------------

func TestProviders_ListsCatalog(t *testing.T) {
	t.Chdir(t.TempDir())
	chdirTemp(t)
	out, err := run(t, nil, "providers")
	require.NoError(t, err)
	// Every catalog provider appears, grouped by channel.
	for _, name := range sortedProviderNames() {
		assert.Contains(t, out, name)
	}
	assert.Contains(t, out, "push:")
	assert.Contains(t, out, "email:")
	assert.Contains(t, out, "sms:")
	assert.Contains(t, out, "available")
}

func TestProviders_MarksConfigured(t *testing.T) {
	t.Chdir(t.TempDir())
	chdirTemp(t)
	// Seed a config with apns enabled.
	require.NoError(t, writeConfig(notificationsConfig{
		"apns": {enabled: true, fields: map[string]string{"teamId": "T", "keyId": "K", "bundleId": "com.x"}},
	}))
	out, err := run(t, nil, "providers")
	require.NoError(t, err)
	assert.Contains(t, out, "configured (enabled)")
}

// --- add --------------------------------------------------------------------

func TestAddApns_UploadsReservedSecretAndWritesConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	chdirTemp(t)
	// Write a .p8 file to feed --p8-file.
	p8 := filepath.Join(t.TempDir(), "AuthKey.p8")
	require.NoError(t, os.WriteFile(p8, []byte("-----BEGIN PRIVATE KEY-----\nXXX\n-----END PRIVATE KEY-----"), 0o600))

	fs := newFakeStudio(t)
	out, err := run(t, fs.client,
		"add", "apns",
		"--team-id", "TEAM12345",
		"--key-id", "KEY1234567",
		"--bundle-id", "com.acme.app",
		"--p8-file", p8,
	)
	require.NoError(t, err, out)

	// 1. The secret was uploaded to the DERIVED reserved key, marked secret.
	calls := fs.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "env.set", calls[0].Path)
	assert.Equal(t, "PB_NOTIFICATIONS_APNS_P8", calls[0].Key)
	assert.True(t, calls[0].Sec)
	assert.Contains(t, calls[0].Value, "BEGIN PRIVATE KEY")

	// 2. config/notifications.ts has the apns entry with non-secret fields only.
	file := readConfigFile(t)
	assert.Contains(t, file, "defineNotifications")
	assert.Contains(t, file, "push: {")
	assert.Contains(t, file, "apns: { enabled: true")
	assert.Contains(t, file, `teamId: "TEAM12345"`)
	assert.Contains(t, file, `bundleId: "com.acme.app"`)
	// The secret NEVER lands in the config file.
	assert.NotContains(t, file, "BEGIN PRIVATE KEY")
	assert.NotContains(t, file, "p8")
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

func TestAddFcm_ServiceAccountFile(t *testing.T) {
	t.Chdir(t.TempDir())
	chdirTemp(t)
	sa := filepath.Join(t.TempDir(), "sa.json")
	require.NoError(t, os.WriteFile(sa, []byte(`{"type":"service_account","project_id":"p"}`), 0o600))
	fs := newFakeStudio(t)
	out, err := run(t, fs.client, "add", "fcm", "--service-account-file", sa)
	require.NoError(t, err, out)
	calls := fs.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "PB_NOTIFICATIONS_FCM_SERVICE_ACCOUNT", calls[0].Key)
	// fcm has no non-secret fields — entry is just enabled.
	file := readConfigFile(t)
	assert.Contains(t, file, "fcm: { enabled: true }")
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

// --- remove -----------------------------------------------------------------

func TestRemove_DropsFromConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	chdirTemp(t)
	require.NoError(t, writeConfig(notificationsConfig{
		"apns": {enabled: true, fields: map[string]string{"teamId": "T", "keyId": "K", "bundleId": "com.x"}},
	}))
	out, err := run(t, nil, "remove", "apns")
	require.NoError(t, err)
	assert.Contains(t, out, "removed provider")
	assert.NotContains(t, readConfigFile(t), "apns: {")
}

func TestRemove_NotDeclared(t *testing.T) {
	t.Chdir(t.TempDir())
	chdirTemp(t)
	require.NoError(t, writeConfig(notificationsConfig{}))
	_, err := run(t, nil, "remove", "apns")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no provider")
}

// --- config round-trip ------------------------------------------------------

func TestConfig_RoundTrips(t *testing.T) {
	t.Chdir(t.TempDir())
	chdirTemp(t)
	in := notificationsConfig{
		"apns":     {enabled: true, fields: map[string]string{"teamId": "T1", "keyId": "K1", "bundleId": "com.acme", "isProduction": "false"}},
		"sendgrid": {enabled: true, fields: map[string]string{"fromDomain": "m.acme.com"}},
		"smtp":     {enabled: false, fields: map[string]string{"host": "smtp.x", "port": "587", "fromEmail": "a@x.com"}},
	}
	require.NoError(t, writeConfig(in))
	got, err := readConfig()
	require.NoError(t, err)

	assert.True(t, got["apns"].enabled)
	assert.Equal(t, "T1", got["apns"].fields["teamId"])
	assert.Equal(t, "false", got["apns"].fields["isProduction"])
	assert.Equal(t, "m.acme.com", got["sendgrid"].fields["fromDomain"])
	assert.False(t, got["smtp"].enabled)
	assert.Equal(t, "587", got["smtp"].fields["port"])
}

func TestReservedSecretKey_Derivation(t *testing.T) {
	t.Chdir(t.TempDir())
	cases := map[string]string{
		"apns/p8":              "PB_NOTIFICATIONS_APNS_P8",
		"fcm/serviceAccount":   "PB_NOTIFICATIONS_FCM_SERVICE_ACCOUNT",
		"twilio/authToken":     "PB_NOTIFICATIONS_TWILIO_AUTH_TOKEN",
		"ses/secretAccessKey":  "PB_NOTIFICATIONS_SES_SECRET_ACCESS_KEY",
		"acs/connectionString": "PB_NOTIFICATIONS_ACS_CONNECTION_STRING",
	}
	for in, want := range cases {
		parts := strings.SplitN(in, "/", 2)
		assert.Equal(t, want, reservedSecretKey(parts[0], parts[1]), in)
	}
}

// writeTemp writes content to a temp file and returns its path.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}
