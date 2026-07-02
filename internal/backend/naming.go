package backend

import (
	"strings"
)

// This file holds the OpenAPI→identifier naming + OAuth-config helpers shared by
// the TypeScript emitter (tsemit.go, `palbase web gen`). The iOS Swift emitter that
// used to live here moved to the palbase-swift-codegen SPM build-tool plugin;
// only the language-neutral helpers the TS path still calls remain.

// swiftOAuthConfig is the provider-availability map fetched from palauth's
// public `/auth/oauth/providers` endpoint (Apple enabled flag, Google
// client_id/redirect_uri). Strictly secret-free — palauth's public endpoint
// never returns secrets. It is mapped onto the per-env Palbase-Info.plist's
// `oauth` block (via swiftOAuthToApps) and embedded in palbe.gen.ts's runtime
// config. Nil means "fetch failed or no providers configured".
type swiftOAuthConfig struct {
	Apple  *swiftOAuthApple  `json:"apple,omitempty"`
	Google *swiftOAuthGoogle `json:"google,omitempty"`
}

type swiftOAuthApple struct {
	Enabled bool `json:"enabled"`
}

type swiftOAuthGoogle struct {
	Enabled     bool   `json:"enabled"`
	ClientID    string `json:"client_id"`
	RedirectURI string `json:"redirect_uri"`
}

// --- Naming -----------------------------------------------------------------

func opSegments(opID string) []string {
	parts := strings.Split(opID, ".")
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func typeNameOf(s string) string { return sanitize(s, true) }

// typePrefix builds the PascalCase concatenation of all op-id segments,
// used as the BASE for top-level <Prefix>Request / <Prefix>Response /
// <Prefix>Error type names. Example: "rooms.create" → "RoomsCreate".
// Dots between segments are dropped — the consumer wants flat top-level
// type names ("RoomsCreateRequest").
func typePrefix(opID string) string {
	segs := opSegments(opID)
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = typeNameOf(s)
	}
	return strings.Join(parts, "")
}

func sanitize(s string, firstUpper bool) string {
	var parts []string
	var cur strings.Builder
	for _, ch := range s {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			cur.WriteRune(ch)
		} else if cur.Len() > 0 {
			parts = append(parts, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	if len(parts) == 0 {
		if firstUpper {
			return "Op"
		}
		return "op"
	}
	var res strings.Builder
	for i, p := range parts {
		if i == 0 && !firstUpper {
			res.WriteString(strings.ToLower(p[:1]) + p[1:])
		} else {
			res.WriteString(strings.ToUpper(p[:1]) + p[1:])
		}
	}
	out := res.String()
	if out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return out
}

func indent(d int) string { return strings.Repeat("    ", d) }
