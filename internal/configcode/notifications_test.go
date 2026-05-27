package configcode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

// nrow builds a notifProviderRow from literals so table cases read close
// to the wire shape.
func nrow(id, channel, provider string, priority int, metadata string) notifProviderRow {
	r := notifProviderRow{ID: id, Channel: channel, Provider: provider, Configured: true, IsActive: true, Priority: priority}
	if metadata != "" {
		r.Metadata = json.RawMessage(metadata)
	}
	return r
}

// TestSerializeProviders_Golden asserts exact TOML — the determinism gate.
// Input is intentionally out of (channel, provider) order; output must sort,
// and credentials must be the @secret reference (never the real value, which
// the server doesn't return anyway).
func TestSerializeProviders_Golden(t *testing.T) {
	rows := []notifProviderRow{
		nrow("p_2", "email", "sendgrid", 0, `{"from":"no-reply@acme.dev"}`),
		nrow("p_1", "email", "ses", 1, ""),
		nrow("p_3", "push", "fcm", 0, ""),
	}

	got, err := serializeProviders(rows)
	require.NoError(t, err)

	const want = notificationsHeader + `[notifications]

  [[notifications.providers]]
    channel = "email"
    provider = "sendgrid"
    credentials = "@secret/NOTIFY_EMAIL_SENDGRID_CREDENTIALS"
    priority = 0
    [notifications.providers.metadata]
      from = "no-reply@acme.dev"

  [[notifications.providers]]
    channel = "email"
    provider = "ses"
    credentials = "@secret/NOTIFY_EMAIL_SES_CREDENTIALS"
    priority = 1

  [[notifications.providers]]
    channel = "push"
    provider = "fcm"
    credentials = "@secret/NOTIFY_PUSH_FCM_CREDENTIALS"
    priority = 0
`
	require.Equal(t, want, string(got))
}

// TestSerializeProviders_Empty: a project with no providers yields a valid
// header-only document (no bare [notifications] table).
func TestSerializeProviders_Empty(t *testing.T) {
	got, err := serializeProviders(nil)
	require.NoError(t, err)
	// Header-only: exactly the header, no encoded providers table appended.
	require.Equal(t, notificationsHeader, string(got))
}

// secretEnvName is deterministic and uppercases + sanitizes channel/provider.
func TestSecretEnvName(t *testing.T) {
	require.Equal(t, "NOTIFY_EMAIL_SENDGRID_CREDENTIALS", secretEnvName("email", "sendgrid"))
	require.Equal(t, "NOTIFY_SMS_TWILIO_CREDENTIALS", secretEnvName("sms", "twilio"))
	// hyphens/dots in a provider name are sanitized to underscores.
	require.Equal(t, "NOTIFY_PUSH_APNS_HTTP2_CREDENTIALS", secretEnvName("push", "apns-http2"))
}

// --- Push tests -----------------------------------------------------

// notifyServer is a stateful mock of Studio's notifications.providers
// surface: list (GET), create (POST), delete (POST). It tracks create /
// delete call counts so tests can assert the exact mutation set
// (idempotency + full-sync gates).
type notifyServer struct {
	mu          sync.Mutex
	providers   map[string]notifProviderRow // key: channel/provider
	createCalls int
	deleteCalls int
	nextID      int
}

func newNotifyServer(seed ...notifProviderRow) *notifyServer {
	ns := &notifyServer{providers: map[string]notifProviderRow{}}
	for _, r := range seed {
		ns.providers[r.Channel+"/"+r.Provider] = r
	}
	return ns
}

func (ns *notifyServer) client(t *testing.T) *studio.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(ns.handle(t)))
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) { return "test-token", nil })
}

func (ns *notifyServer) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/trpc/notifications.providers.list" && r.Method == http.MethodGet:
			ns.mu.Lock()
			rows := make([]notifProviderRow, 0, len(ns.providers))
			for _, row := range ns.providers {
				rows = append(rows, row)
			}
			ns.mu.Unlock()
			trpcOK(w, rows)

		case r.URL.Path == "/api/trpc/notifications.providers.create" && r.Method == http.MethodPost:
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var env struct {
				JSON createProviderInput `json:"json"`
			}
			require.NoError(t, json.Unmarshal(body, &env))
			in := env.JSON
			require.NotEmpty(t, in.Channel)
			require.NotEmpty(t, in.Provider)
			// credentials must be resolved JSON, never the @secret placeholder.
			require.True(t, json.Valid(in.Credentials), "credentials must be valid JSON")
			require.NotContains(t, string(in.Credentials), "@secret/", "placeholder must be resolved before POST")
			ns.mu.Lock()
			ns.createCalls++
			ns.nextID++
			row := notifProviderRow{
				ID:         "p_new",
				Channel:    in.Channel,
				Provider:   in.Provider,
				Configured: true,
				IsActive:   true,
				Priority:   in.Priority,
			}
			ns.providers[in.Channel+"/"+in.Provider] = row
			ns.mu.Unlock()
			trpcOK(w, map[string]any{
				"id": row.ID, "channel": row.Channel, "provider": row.Provider,
				"configured": true, "isActive": true, "priority": row.Priority, "metadata": nil,
				"createdAt": "2026-05-27T00:00:00Z", "updatedAt": "2026-05-27T00:00:00Z",
			})

		case r.URL.Path == "/api/trpc/notifications.providers.delete" && r.Method == http.MethodPost:
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var env struct {
				JSON deleteProviderInput `json:"json"`
			}
			require.NoError(t, json.Unmarshal(body, &env))
			require.NotEmpty(t, env.JSON.ID)
			ns.mu.Lock()
			ns.deleteCalls++
			for k, row := range ns.providers {
				if row.ID == env.JSON.ID {
					delete(ns.providers, k)
					break
				}
			}
			ns.mu.Unlock()
			trpcOK(w, map[string]any{"ok": true})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}
}

func notifPushFile(t *testing.T, entries ...notifProviderEntry) []byte {
	t.Helper()
	var buf strings.Builder
	require.NoError(t, toml.NewEncoder(&buf).Encode(notificationsDoc{Notifications: notificationsTable{Providers: entries}}))
	return []byte(buf.String())
}

// TestNotificationsPush_CreatesMissing: a local provider absent server-side
// is created, with its credential resolved from the env var.
func TestNotificationsPush_CreatesMissing(t *testing.T) {
	t.Setenv("NOTIFY_EMAIL_SENDGRID_CREDENTIALS", `{"api_key":"SG.test"}`)
	srv := newNotifyServer() // empty project
	sc := srv.client(t)

	file := notifPushFile(t, notifProviderEntry{
		Channel:     "email",
		Provider:    "sendgrid",
		Credentials: SecretRef("NOTIFY_EMAIL_SENDGRID_CREDENTIALS"),
		Priority:    0,
	})

	applied, err := notificationsSerializer{}.Push(context.Background(), "abc123", sc, file)
	require.NoError(t, err)
	require.Equal(t, 1, applied.Set)
	require.Empty(t, applied.Orphans)
	require.Empty(t, applied.Ignored)
	require.Equal(t, 1, srv.createCalls)
	require.Equal(t, 0, srv.deleteCalls)
}

// TestNotificationsPush_DeletesOrphan: a server provider absent locally is
// deleted (full-sync, unlike flags' upsert-only).
func TestNotificationsPush_DeletesOrphan(t *testing.T) {
	srv := newNotifyServer(nrow("p_old", "sms", "twilio", 0, ""))
	sc := srv.client(t)

	// Empty local file → the server's twilio provider is an orphan.
	applied, err := notificationsSerializer{}.Push(context.Background(), "abc123", sc, notifPushFile(t))
	require.NoError(t, err)
	require.Equal(t, 0, applied.Set)
	require.Equal(t, []string{"sms/twilio"}, applied.Orphans)
	require.Equal(t, 1, srv.deleteCalls)
}

// TestNotificationsPush_IgnoresExisting: a provider present on both sides is
// a no-op (credentials can't round-trip; the API has no update) and is
// reported in Ignored so the user knows the edit didn't apply.
func TestNotificationsPush_IgnoresExisting(t *testing.T) {
	t.Setenv("NOTIFY_EMAIL_SENDGRID_CREDENTIALS", `{"api_key":"SG.test"}`)
	srv := newNotifyServer(nrow("p_sg", "email", "sendgrid", 0, ""))
	sc := srv.client(t)

	file := notifPushFile(t, notifProviderEntry{
		Channel:     "email",
		Provider:    "sendgrid",
		Credentials: SecretRef("NOTIFY_EMAIL_SENDGRID_CREDENTIALS"),
		Priority:    5, // changed priority — still can't update in place
	})

	applied, err := notificationsSerializer{}.Push(context.Background(), "abc123", sc, file)
	require.NoError(t, err)
	require.Equal(t, 0, applied.Set)
	require.Equal(t, []string{"email/sendgrid"}, applied.Ignored)
	require.Empty(t, applied.Orphans)
	require.Equal(t, 0, srv.createCalls)
	require.Equal(t, 0, srv.deleteCalls)
}

// TestNotificationsPush_MissingSecretFails: a @secret reference whose env var
// is unset is a hard error — we never POST the literal placeholder.
func TestNotificationsPush_MissingSecretFails(t *testing.T) {
	srv := newNotifyServer()
	sc := srv.client(t)

	file := notifPushFile(t, notifProviderEntry{
		Channel:     "email",
		Provider:    "sendgrid",
		Credentials: SecretRef("NOTIFY_EMAIL_SENDGRID_CREDENTIALS"), // env not set
		Priority:    0,
	})

	_, err := notificationsSerializer{}.Push(context.Background(), "abc123", sc, file)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not set")
	require.Equal(t, 0, srv.createCalls)
}

// TestNotificationsPush_RejectsLiteralCredential: a literal (non-@secret)
// credential in the file is rejected — secrets must come from the env.
func TestNotificationsPush_RejectsLiteralCredential(t *testing.T) {
	srv := newNotifyServer()
	sc := srv.client(t)

	file := notifPushFile(t, notifProviderEntry{
		Channel:     "email",
		Provider:    "sendgrid",
		Credentials: `{"api_key":"SG.inline"}`, // literal, not @secret/
		Priority:    0,
	})

	_, err := notificationsSerializer{}.Push(context.Background(), "abc123", sc, file)
	require.Error(t, err)
	require.Contains(t, err.Error(), "@secret/")
	require.Equal(t, 0, srv.createCalls)
}

// notificationsSerializer is registered so Pull/Push command layers see it.
func TestNotificationsSerializer_Registered(t *testing.T) {
	found := false
	for _, s := range Serializers() {
		if s.Name() == "notifications" {
			found = true
			require.Equal(t, "notifications.toml", s.Filename())
			_, isPusher := s.(ModulePusher)
			require.True(t, isPusher, "notifications must implement ModulePusher")
		}
	}
	require.True(t, found, "notifications serializer not registered")
}
