package configcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/palgroup/palbase-cli/internal/studio"
)

func init() { Register(notificationsSerializer{}) }

// notificationsSerializer mirrors flagsSerializer for the notifications
// module's provider-config surface. Pull serializes the project's
// provider configs to config/notifications.toml; Push applies that file
// back via the notifications.providers tRPC procedures.
//
// SECRETS: a provider's credentials (SendGrid API key, Twilio token, FCM
// service account) are write-only — the server returns `configured:true`
// but NEVER the value. So Pull can't emit the real credential; it always
// writes the reference form `@secret/<NAME>` ([SecretRef]). Push resolves
// that reference from the environment (os.Getenv(NAME)) and fails loudly
// if the env var is unset, rather than sending the literal "@secret/…"
// string as a credential.
type notificationsSerializer struct{}

func (notificationsSerializer) Name() string     { return "notifications" }
func (notificationsSerializer) Filename() string { return "notifications.toml" }

// notifProviderRow mirrors the notifications.providers.list tRPC response
// (platform/studio/.../routers/notifications.ts) and the module's
// ProviderConfigResponse. credentials are absent by design — `configured`
// stands in for "a secret is stored". metadata is opaque JSON.
type notifProviderRow struct {
	ID         string          `json:"id"`
	Channel    string          `json:"channel"`
	Provider   string          `json:"provider"`
	Configured bool            `json:"configured"`
	IsActive   bool            `json:"isActive"`
	Priority   int             `json:"priority"`
	Metadata   json.RawMessage `json:"metadata"`
}

// notificationsDoc is the root of config/notifications.toml. Providers
// are a positional array-of-tables sorted by (channel, provider) so the
// output is deterministic (there's no natural map key — a project can
// have multiple providers per channel for failover).
//
//	[[notifications.providers]]
//	channel     = "email"
//	provider    = "sendgrid"
//	credentials = "@secret/NOTIFY_EMAIL_SENDGRID_CREDENTIALS"
//	priority    = 0
//	metadata    = { from = "no-reply@acme.dev" }   # omitted if empty
//
// is_active is intentionally NOT in the schema: the module's
// CreateProviderRequest has no is_active field (it defaults active
// server-side) and there is no toggle endpoint, so a local is_active
// could never round-trip. Providers are active-by-default.
type notificationsDoc struct {
	Notifications notificationsTable `toml:"notifications"`
}

type notificationsTable struct {
	Providers []notifProviderEntry `toml:"providers"`
}

type notifProviderEntry struct {
	Channel     string         `toml:"channel"`
	Provider    string         `toml:"provider"`
	Credentials string         `toml:"credentials"`
	Priority    int            `toml:"priority"`
	Metadata    map[string]any `toml:"metadata,omitempty"`
}

const notificationsHeader = `# config/notifications.toml — notification provider config (config-as-code, Faz 4).
#
# Each [[notifications.providers]] is one channel→provider binding
# (e.g. email→sendgrid). Multiple providers per channel form a
# priority-ordered failover chain (priority 0 = primary).
#
# SECRETS: credentials never leave the server, so pull writes the
# reference form "@secret/<NAME>". On push the CLI resolves <NAME> from
# the environment; set it before pushing (export NOTIFY_EMAIL_SENDGRID_CREDENTIALS=...).
# A provider already present on the server is left as-is on push —
# rotating a credential requires deleting the provider and re-pushing.

`

// secretEnvName derives the env var name a provider's credential is read
// from on push (and the @secret/<NAME> reference Pull emits): e.g.
// (email, sendgrid) → NOTIFY_EMAIL_SENDGRID_CREDENTIALS. Deterministic so
// Pull and Push agree without storing the name server-side.
func secretEnvName(channel, provider string) string {
	up := func(s string) string {
		return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(s))
	}
	return fmt.Sprintf("NOTIFY_%s_%s_CREDENTIALS", up(channel), up(provider))
}

// Pull fetches the project's provider configs and serializes them. An
// empty project still yields a valid header-only document.
func (notificationsSerializer) Pull(ctx context.Context, ref string, sc *studio.Client) ([]byte, error) {
	var rows []notifProviderRow
	if err := sc.Query(ctx, "notifications.providers.list", map[string]any{"ref": ref}, &rows); err != nil {
		return nil, fmt.Errorf("notifications.providers.list: %w", err)
	}
	return serializeProviders(rows)
}

// serializeProviders is the pure, testable core: rows → deterministic TOML.
func serializeProviders(rows []notifProviderRow) ([]byte, error) {
	doc := notificationsDoc{}
	for _, row := range rows {
		entry := notifProviderEntry{
			Channel:     row.Channel,
			Provider:    row.Provider,
			Credentials: SecretRef(secretEnvName(row.Channel, row.Provider)),
			Priority:    row.Priority,
		}
		meta, err := decodeMetadata(row.Metadata)
		if err != nil {
			return nil, fmt.Errorf("provider %s/%s: decode metadata: %w", row.Channel, row.Provider, err)
		}
		entry.Metadata = meta
		doc.Notifications.Providers = append(doc.Notifications.Providers, entry)
	}

	// Deterministic order: (channel, provider). The server has no stable
	// ordering guarantee and the id is non-deterministic, so sort here.
	sort.Slice(doc.Notifications.Providers, func(i, j int) bool {
		if doc.Notifications.Providers[i].Channel != doc.Notifications.Providers[j].Channel {
			return doc.Notifications.Providers[i].Channel < doc.Notifications.Providers[j].Channel
		}
		return doc.Notifications.Providers[i].Provider < doc.Notifications.Providers[j].Provider
	})

	var buf bytes.Buffer
	buf.WriteString(notificationsHeader)
	if len(doc.Notifications.Providers) > 0 {
		if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
			return nil, fmt.Errorf("encode toml: %w", err)
		}
	}
	return buf.Bytes(), nil
}

// decodeMetadata turns the opaque metadata JSON into a map the TOML
// encoder accepts. null/empty → nil (omitted). A non-object metadata
// value is an error — TOML can only express it as a table here.
func decodeMetadata(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}

// --- Faz 4: push ----------------------------------------------------
//
// notificationsSerializer implements [ModulePusher] with full-sync
// create/delete semantics (the module API is create + delete by id; there
// is no update). Identity is (channel, provider):
//   - local entry absent server-side  → create (credentials resolved from env)
//   - server entry absent locally      → delete
//   - present both sides               → no-op; if priority/metadata/credential
//                                         differ it's reported in Ignored,
//                                         since the API can't update in place
//                                         (rotate by delete + re-push).

// createProviderInput mirrors the notifications.providers.create tRPC
// input. credentials is opaque JSON resolved from the env var the
// @secret/<NAME> reference points at.
type createProviderInput struct {
	Ref         string          `json:"ref"`
	Channel     string          `json:"channel"`
	Provider    string          `json:"provider"`
	Credentials json.RawMessage `json:"credentials"`
	Priority    int             `json:"priority"`
	Metadata    map[string]any  `json:"metadata,omitempty"`
}

type deleteProviderInput struct {
	Ref string `json:"ref"`
	ID  string `json:"id"`
}

func (notificationsSerializer) Push(ctx context.Context, ref string, sc *studio.Client, tomlBytes []byte) (PushApplied, error) {
	var doc notificationsDoc
	if err := toml.Unmarshal(tomlBytes, &doc); err != nil {
		return PushApplied{}, fmt.Errorf("parse notifications.toml: %w", err)
	}

	var rows []notifProviderRow
	if err := sc.Query(ctx, "notifications.providers.list", map[string]any{"ref": ref}, &rows); err != nil {
		return PushApplied{}, fmt.Errorf("notifications.providers.list: %w", err)
	}
	// Server providers keyed by (channel, provider). id is needed for delete.
	type srvKey struct{ channel, provider string }
	server := make(map[srvKey]notifProviderRow, len(rows))
	for _, r := range rows {
		server[srvKey{r.Channel, r.Provider}] = r
	}

	applied := PushApplied{}
	localKeys := make(map[srvKey]bool, len(doc.Notifications.Providers))
	// Stable iteration so Ignored/Set order is deterministic.
	sort.Slice(doc.Notifications.Providers, func(i, j int) bool {
		if doc.Notifications.Providers[i].Channel != doc.Notifications.Providers[j].Channel {
			return doc.Notifications.Providers[i].Channel < doc.Notifications.Providers[j].Channel
		}
		return doc.Notifications.Providers[i].Provider < doc.Notifications.Providers[j].Provider
	})

	for _, entry := range doc.Notifications.Providers {
		key := srvKey{entry.Channel, entry.Provider}
		localKeys[key] = true
		label := entry.Channel + "/" + entry.Provider

		if _, exists := server[key]; exists {
			// Already on the server. The module API has no update, and
			// credentials never round-trip, so we can't reconcile in place.
			// Surface it so the user knows the edit didn't apply (rotate by
			// delete + re-push).
			applied.Ignored = append(applied.Ignored, label)
			continue
		}

		creds, err := resolveCredential(entry)
		if err != nil {
			return PushApplied{}, fmt.Errorf("provider %s: %w", label, err)
		}
		in := createProviderInput{
			Ref:         ref,
			Channel:     entry.Channel,
			Provider:    entry.Provider,
			Credentials: creds,
			Priority:    entry.Priority,
			Metadata:    entry.Metadata,
		}
		if err := sc.Mutation(ctx, "notifications.providers.create", in, nil); err != nil {
			return PushApplied{}, fmt.Errorf("notifications.providers.create %s: %w", label, err)
		}
		applied.Set++
	}

	// Full-sync delete: server providers absent from the local file are
	// removed (the brief sanctions create/delete, unlike flags' upsert-only).
	var orphanKeys []srvKey
	for key := range server {
		if !localKeys[key] {
			orphanKeys = append(orphanKeys, key)
		}
	}
	sort.Slice(orphanKeys, func(i, j int) bool {
		if orphanKeys[i].channel != orphanKeys[j].channel {
			return orphanKeys[i].channel < orphanKeys[j].channel
		}
		return orphanKeys[i].provider < orphanKeys[j].provider
	})
	for _, key := range orphanKeys {
		row := server[key]
		label := key.channel + "/" + key.provider
		if err := sc.Mutation(ctx, "notifications.providers.delete",
			deleteProviderInput{Ref: ref, ID: row.ID}, nil); err != nil {
			return PushApplied{}, fmt.Errorf("notifications.providers.delete %s: %w", label, err)
		}
		applied.Orphans = append(applied.Orphans, label)
	}

	sort.Strings(applied.Ignored)
	sort.Strings(applied.Orphans)
	return applied, nil
}

// resolveCredential turns a provider entry's `credentials` field into the
// raw JSON the create API expects. The field must be a "@secret/<NAME>"
// reference; the value is read from os.Getenv(NAME) and must be valid
// JSON (the provider-specific credential object). A literal credential in
// the file, an unset env var, or non-JSON env content is a hard error —
// we never POST a "@secret/…" placeholder as a real credential.
func resolveCredential(entry notifProviderEntry) (json.RawMessage, error) {
	if !strings.HasPrefix(entry.Credentials, SecretRefPrefix) {
		return nil, fmt.Errorf("credentials must be a %q reference, got a literal value", SecretRefPrefix)
	}
	name := strings.TrimPrefix(entry.Credentials, SecretRefPrefix)
	if name == "" {
		return nil, fmt.Errorf("empty secret reference")
	}
	val := os.Getenv(name)
	if val == "" {
		return nil, fmt.Errorf("secret env var %q is not set", name)
	}
	if !json.Valid([]byte(val)) {
		return nil, fmt.Errorf("secret env var %q is not valid JSON credentials", name)
	}
	return json.RawMessage(val), nil
}
