package configcode

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
	"sync"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

// comboServer is a stateful mock backing BOTH the flags and notifications
// push surfaces, so PushAll can be exercised end-to-end across multiple
// modules. It counts SETs per module to assert the exact mutation set and
// can be told to fail one module's create to drive the partial-fail path.
type comboServer struct {
	mu sync.Mutex

	flags     map[string]systemFlagRow
	providers map[string]notifProviderRow // key channel/provider

	flagPuts        int
	providerCreates int
	providerDeletes int

	// failNotifyCreate makes notifications.providers.create return 500,
	// driving the PushAll partial-fail branch (apply phase, not validate).
	failNotifyCreate bool
}

func newComboServer() *comboServer {
	return &comboServer{flags: map[string]systemFlagRow{}, providers: map[string]notifProviderRow{}}
}

func (cs *comboServer) client(t *testing.T) *studio.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(cs.handle(t)))
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) { return "tok", nil })
}

func (cs *comboServer) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/userFlags.system.list":
			cs.mu.Lock()
			rows := make([]systemFlagRow, 0, len(cs.flags))
			for _, row := range cs.flags {
				rows = append(rows, row)
			}
			cs.mu.Unlock()
			trpcOK(w, rows)

		case "/api/trpc/userFlags.system.put":
			var env struct {
				JSON flagsPutInput `json:"json"`
			}
			decode(t, r, &env)
			valRaw, err := json.Marshal(env.JSON.Value)
			require.NoError(t, err)
			cs.mu.Lock()
			cs.flagPuts++
			cs.flags[env.JSON.Key] = systemFlagRow{
				Key: env.JSON.Key, ValueType: env.JSON.ValueType,
				Value: json.RawMessage(valRaw), Description: env.JSON.Description,
			}
			cs.mu.Unlock()
			trpcOK(w, map[string]any{"ok": true})

		case "/api/trpc/notifications.providers.list":
			cs.mu.Lock()
			rows := make([]notifProviderRow, 0, len(cs.providers))
			for _, row := range cs.providers {
				rows = append(rows, row)
			}
			cs.mu.Unlock()
			trpcOK(w, rows)

		case "/api/trpc/notifications.providers.create":
			cs.mu.Lock()
			fail := cs.failNotifyCreate
			cs.mu.Unlock()
			if fail {
				http.Error(w, `{"error":{"json":{"message":"provider create failed"}}}`, http.StatusInternalServerError)
				return
			}
			var env struct {
				JSON createProviderInput `json:"json"`
			}
			decode(t, r, &env)
			cs.mu.Lock()
			cs.providerCreates++
			cs.providers[env.JSON.Channel+"/"+env.JSON.Provider] = notifProviderRow{
				ID: "p_new", Channel: env.JSON.Channel, Provider: env.JSON.Provider,
				Configured: true, IsActive: true, Priority: env.JSON.Priority,
			}
			cs.mu.Unlock()
			trpcOK(w, map[string]any{
				"id": "p_new", "channel": env.JSON.Channel, "provider": env.JSON.Provider,
				"configured": true, "isActive": true, "priority": env.JSON.Priority, "metadata": nil,
				"createdAt": "t", "updatedAt": "t",
			})

		case "/api/trpc/notifications.providers.delete":
			var env struct {
				JSON deleteProviderInput `json:"json"`
			}
			decode(t, r, &env)
			cs.mu.Lock()
			cs.providerDeletes++
			for k, row := range cs.providers {
				if row.ID == env.JSON.ID {
					delete(cs.providers, k)
					break
				}
			}
			cs.mu.Unlock()
			trpcOK(w, map[string]any{"ok": true})

		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}
}

func decode(t *testing.T, r *http.Request, v any) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, v))
}

// comboProject lays out a temp projectDir with config/flags.toml +
// config/notifications.toml and a state.json whose baselines match the
// current server (so there's no conflict). Returns the dir.
func comboProject(t *testing.T, cs *comboServer, c *studio.Client, flagRows []systemFlagRow, providerEntries []notifProviderEntry) string {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, ConfigDir)
	require.NoError(t, os.MkdirAll(configPath, 0o755))

	flagsBody, err := serializeFlags(flagRows)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(configPath, "flags.toml"), flagsBody, 0o644))

	var nbuf bytes.Buffer
	require.NoError(t, toml.NewEncoder(&nbuf).Encode(notificationsDoc{Notifications: notificationsTable{Providers: providerEntries}}))
	require.NoError(t, os.WriteFile(filepath.Join(configPath, "notifications.toml"), nbuf.Bytes(), 0o644))

	// Baselines = current server hashes (no conflict).
	st := newState()
	st.Modules["flags"] = ModuleState{Hash: serverHashOf(t, flagsSerializer{}, c)}
	st.Modules["notifications"] = ModuleState{Hash: serverHashOf(t, notificationsSerializer{}, c)}
	require.NoError(t, writeState(filepath.Join(dir, StateFile), st))
	return dir
}

func serverHashOf(t *testing.T, s ModuleSerializer, c *studio.Client) string {
	t.Helper()
	body, err := s.Pull(context.Background(), "myproj", c)
	require.NoError(t, err)
	return hashContent(body)
}

// TestPushAll_HappyPath: two push-capable modules (flags + notifications),
// each with one pending change, apply in one batch. flags creates a new
// flag (1 put); notifications creates a new provider (1 create).
func TestPushAll_HappyPath(t *testing.T) {
	t.Setenv("NOTIFY_EMAIL_SENDGRID_CREDENTIALS", `{"api_key":"SG"}`)
	cs := newComboServer() // empty server (no flags, no providers)
	c := cs.client(t)

	dir := comboProject(t, cs, c,
		[]systemFlagRow{row("brand_new", "string", `"hi"`, "", "")},
		[]notifProviderEntry{{
			Channel: "email", Provider: "sendgrid",
			Credentials: SecretRef("NOTIFY_EMAIL_SENDGRID_CREDENTIALS"), Priority: 0,
		}},
	)

	result, err := PushAll(context.Background(), dir, "myproj", c)
	require.NoError(t, err)
	require.Empty(t, result.FailedModule)
	require.Empty(t, result.Skipped)

	// Applied in registry (Name-sorted) order: flags before notifications.
	require.Len(t, result.Applied, 2)
	require.Equal(t, "flags", result.Applied[0].Module)
	require.Equal(t, "notifications", result.Applied[1].Module)
	require.Equal(t, 1, cs.flagPuts)
	require.Equal(t, 1, cs.providerCreates)
}

// TestPushAll_Idempotent: pushing a file that already matches the server
// makes ZERO mutations across all modules.
func TestPushAll_Idempotent(t *testing.T) {
	cs := newComboServer()
	cs.flags["existing"] = systemFlagRow{Key: "existing", ValueType: "bool", Value: json.RawMessage("true")}
	cs.providers["email/sendgrid"] = notifProviderRow{
		ID: "p1", Channel: "email", Provider: "sendgrid", Configured: true, IsActive: true, Priority: 0,
	}
	c := cs.client(t)

	dir := comboProject(t, cs, c,
		[]systemFlagRow{row("existing", "bool", "true", "", "")},
		[]notifProviderEntry{{
			Channel: "email", Provider: "sendgrid",
			Credentials: SecretRef("NOTIFY_EMAIL_SENDGRID_CREDENTIALS"), Priority: 0,
		}},
	)

	result, err := PushAll(context.Background(), dir, "myproj", c)
	require.NoError(t, err)
	require.Equal(t, 0, cs.flagPuts, "unchanged flags → no put")
	require.Equal(t, 0, cs.providerCreates, "existing provider → no create")
	require.Equal(t, 0, cs.providerDeletes, "provider present locally → no delete")
	// notifications reports the existing provider as Ignored (can't update in place).
	require.Len(t, result.Applied, 2)
}

// TestPushAll_PreValidateConflictAbortsBatch: if ONE module's server state
// drifted from its baseline, the whole batch aborts in phase 1 with NO SET
// to any module.
func TestPushAll_PreValidateConflictAbortsBatch(t *testing.T) {
	t.Setenv("NOTIFY_EMAIL_SENDGRID_CREDENTIALS", `{"api_key":"SG"}`)
	cs := newComboServer()
	c := cs.client(t)

	dir := comboProject(t, cs, c,
		[]systemFlagRow{row("brand_new", "string", `"hi"`, "", "")},
		[]notifProviderEntry{{
			Channel: "email", Provider: "sendgrid",
			Credentials: SecretRef("NOTIFY_EMAIL_SENDGRID_CREDENTIALS"), Priority: 0,
		}},
	)

	// Drift the flags server out-of-band AFTER the baseline was recorded:
	// now state.json's flags hash no longer matches the server.
	cs.mu.Lock()
	cs.flags["sneaky"] = systemFlagRow{Key: "sneaky", ValueType: "bool", Value: json.RawMessage("true")}
	cs.mu.Unlock()

	result, err := PushAll(context.Background(), dir, "myproj", c)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrStateConflict))
	require.Equal(t, "flags", result.FailedModule)
	// No SET to ANY module — abort happened in the pre-validate phase.
	require.Equal(t, 0, cs.flagPuts)
	require.Equal(t, 0, cs.providerCreates)
	require.Empty(t, result.Applied)
}

// TestPushAll_PartialFailLeavesPriorApplied: flags applies, then
// notifications.create fails → flags stays applied, the batch stops, and
// the result names the failed module.
func TestPushAll_PartialFailLeavesPriorApplied(t *testing.T) {
	t.Setenv("NOTIFY_EMAIL_SENDGRID_CREDENTIALS", `{"api_key":"SG"}`)
	cs := newComboServer()
	cs.failNotifyCreate = true // notifications apply will 500
	c := cs.client(t)

	dir := comboProject(t, cs, c,
		[]systemFlagRow{row("brand_new", "string", `"hi"`, "", "")},
		[]notifProviderEntry{{
			Channel: "email", Provider: "sendgrid",
			Credentials: SecretRef("NOTIFY_EMAIL_SENDGRID_CREDENTIALS"), Priority: 0,
		}},
	)

	result, err := PushAll(context.Background(), dir, "myproj", c)
	require.Error(t, err)
	require.Equal(t, "notifications", result.FailedModule)
	// flags (ordered first) applied; notifications failed.
	require.Len(t, result.Applied, 1)
	require.Equal(t, "flags", result.Applied[0].Module)
	require.Equal(t, 1, cs.flagPuts, "flags applied before the notifications failure")

	// state.json persisted flags' new baseline (it really changed the server).
	st := loadStateForTest(t, dir)
	require.Equal(t, serverHashOf(t, flagsSerializer{}, c), st.Modules["flags"].Hash)
}

// TestPushAll_SkipsModuleWithoutLocalFile: a push-capable module with no
// config/<file> is skipped, not failed.
func TestPushAll_SkipsModuleWithoutLocalFile(t *testing.T) {
	cs := newComboServer()
	c := cs.client(t)

	// Only flags.toml on disk; notifications.toml absent.
	dir := t.TempDir()
	configPath := filepath.Join(dir, ConfigDir)
	require.NoError(t, os.MkdirAll(configPath, 0o755))
	flagsBody, err := serializeFlags(nil)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(configPath, "flags.toml"), flagsBody, 0o644))
	st := newState()
	st.Modules["flags"] = ModuleState{Hash: serverHashOf(t, flagsSerializer{}, c)}
	require.NoError(t, writeState(filepath.Join(dir, StateFile), st))

	result, err := PushAll(context.Background(), dir, "myproj", c)
	require.NoError(t, err)
	require.Contains(t, result.Skipped, "notifications")
}
