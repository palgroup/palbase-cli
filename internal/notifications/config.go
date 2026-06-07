package notifications

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// config.go — read/generate config/notifications.ts.
//
// The CLI owns config/notifications.ts entirely (same contract as
// config/storage.ts): writeConfig regenerates the whole file from the provider
// set (deterministic, grouped by channel, providers sorted by name) and
// readConfig parses that generated shape back. The generated file is valid
// TypeScript importing defineNotifications from @palbase/backend, so it
// type-checks and the deploy's br-pod evals it the same as a hand-written one.
//
// SECRETS ARE NEVER IN THIS FILE — only non-secret fields + each provider's
// `enabled` flag. The cert/key material lives in the reserved encrypted env var
// (PB_NOTIFICATIONS_<PROVIDER>_<FIELD>), uploaded by `add` and resolved at deploy.

// configPath is the project-relative path the deploy's br-pod evals.
const configPath = "config/notifications.ts"

// providerEntry is the CLI's in-memory model of one declared provider: its
// enabled flag + its non-secret fields keyed by camelCase name. Values are the
// raw author strings (port stays a string here; it renders unquoted by catalog).
type providerEntry struct {
	enabled bool
	fields  map[string]string
}

// notificationsConfig is the full parsed config: provider name → entry. The
// channel is derived from the catalog, so the in-memory model is flat.
type notificationsConfig map[string]providerEntry

// providerEntryRE matches one `apns: { enabled: true, ... }` entry inside a
// channel object. The provider key is a bare identifier; the body is the
// (non-nested) object literal. Provider bodies never nest braces, so a
// non-greedy [^{}]* body is sufficient (no full TS parser).
var providerEntryRE = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*:\s*\{([^{}]*)\}`)

// lineCommentRE strips `// ...` comments before parsing so the generated header
// comment (which references the entry shape) is never mistaken for a real entry.
var lineCommentRE = regexp.MustCompile(`(?m)//[^\n]*`)

// readConfig parses config/notifications.ts into a provider map. A missing file
// is NOT an error ("no providers yet"). A present-but-unparseable file IS an
// error so we never clobber a hand-authored config we don't understand.
func readConfig() (notificationsConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return notificationsConfig{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	return parseConfig(string(data))
}

// parseConfig extracts provider entries from config/notifications.ts source.
// Only KNOWN providers (in the catalog) are kept — an unrecognized key is
// ignored so a stray nested identifier (none expected) can't poison the map.
func parseConfig(src string) (notificationsConfig, error) {
	if !strings.Contains(src, "defineNotifications") {
		return nil, fmt.Errorf("%s does not look like a defineNotifications() config (no defineNotifications call found) — refusing to overwrite; remove or fix it manually", configPath)
	}
	src = lineCommentRE.ReplaceAllString(src, "")
	cfg := notificationsConfig{}
	for _, m := range providerEntryRE.FindAllStringSubmatch(src, -1) {
		name := m[1]
		spec := specByName(name)
		if spec == nil {
			continue // not a provider key (e.g. a channel wrapper has no [^{}] body match)
		}
		entry, err := parseProviderBody(spec, m[2])
		if err != nil {
			return nil, err
		}
		cfg[name] = entry
	}
	return cfg, nil
}

var (
	enabledFieldRE = regexp.MustCompile(`\benabled\s*:\s*(true|false)\b`)
	boolFieldRE    = regexp.MustCompile(`:\s*(true|false)\b`)
)

// parseProviderBody reads enabled + the non-secret fields out of one provider
// `{ ... }` body, per the provider's catalog field list.
func parseProviderBody(spec *providerSpec, body string) (providerEntry, error) {
	entry := providerEntry{fields: map[string]string{}}
	if m := enabledFieldRE.FindStringSubmatch(body); m != nil {
		entry.enabled = m[1] == "true"
	}
	for _, f := range spec.fields {
		switch {
		case f.isInt:
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(f.name) + `\s*:\s*(\d+)\b`)
			if m := re.FindStringSubmatch(body); m != nil {
				entry.fields[f.name] = m[1]
			}
		case f.isBool:
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(f.name) + `\s*` + boolFieldRE.String())
			if m := re.FindStringSubmatch(body); m != nil {
				entry.fields[f.name] = m[1]
			}
		default:
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(f.name) + `\s*:\s*"([^"]*)"`)
			if m := re.FindStringSubmatch(body); m != nil {
				entry.fields[f.name] = m[1]
			}
		}
	}
	return entry, nil
}

// writeConfig regenerates config/notifications.ts from the provider set and
// writes it (creating config/ if needed).
func writeConfig(cfg notificationsConfig) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(configPath), err)
	}
	if err := os.WriteFile(configPath, []byte(generateConfig(cfg)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}
	return nil
}

// generateConfig renders the full config/notifications.ts source for a provider
// set. Deterministic: channels in push/email/sms order, providers sorted by name
// within a channel, fields in catalog order. A channel with no providers is
// omitted. An empty set still emits a valid `defineNotifications({})`.
func generateConfig(cfg notificationsConfig) string {
	// Group declared providers by channel (catalog order within channel).
	byChannel := map[string][]string{}
	for name := range cfg {
		if spec := specByName(name); spec != nil {
			byChannel[spec.channel] = append(byChannel[spec.channel], name)
		}
	}
	for ch := range byChannel {
		sort.Strings(byChannel[ch])
	}

	var b strings.Builder
	b.WriteString("// Generated + maintained by `palbase notifications`. Edit via the CLI,\n")
	b.WriteString("// or by hand keeping each provider's `{ enabled, ... }` shape. Config-as-code:\n")
	b.WriteString("// commit this file and `git push` — the deploy configures the providers.\n")
	b.WriteString("// SECRETS (cert/key/api-key) are NOT here — they live in reserved encrypted\n")
	b.WriteString("// env vars (PB_NOTIFICATIONS_*), set by `palbase notifications add`.\n")
	b.WriteString("import { defineNotifications } from \"@palbase/backend\";\n\n")
	b.WriteString("export default defineNotifications({\n")
	for _, ch := range []string{"push", "email", "sms"} {
		names := byChannel[ch]
		if len(names) == 0 {
			continue
		}
		b.WriteString("  ")
		b.WriteString(ch)
		b.WriteString(": {\n")
		for _, name := range names {
			b.WriteString("    ")
			b.WriteString(name)
			b.WriteString(": ")
			b.WriteString(renderProvider(specByName(name), cfg[name]))
			b.WriteString(",\n")
		}
		b.WriteString("  },\n")
	}
	b.WriteString("});\n")
	return b.String()
}

// renderProvider renders one provider's `{ enabled, ...fields }` literal. Fields
// are emitted in catalog order; only present fields are written. enabled is
// always first.
func renderProvider(spec *providerSpec, entry providerEntry) string {
	parts := []string{fmt.Sprintf("enabled: %t", entry.enabled)}
	if spec != nil {
		for _, f := range spec.fields {
			v, ok := entry.fields[f.name]
			if !ok || v == "" {
				continue
			}
			switch {
			case f.isInt:
				parts = append(parts, fmt.Sprintf("%s: %s", f.name, v))
			case f.isBool:
				parts = append(parts, fmt.Sprintf("%s: %s", f.name, v))
			default:
				parts = append(parts, fmt.Sprintf("%s: %s", f.name, strconv.Quote(v)))
			}
		}
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}
