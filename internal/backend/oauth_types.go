package backend

// swiftOAuthConfig is the provider-availability map fetched from palauth's
// public `/auth/oauth/providers` endpoint (Apple enabled flag, Google
// client_id/redirect_uri). Strictly secret-free — palauth's public endpoint
// never returns secrets. It is mapped onto the per-platform config artifacts
// (palbase-config.json → Palbase-Info.plist, which this CLI emits by running the
// SDK's palbase-swiftgen — the SwiftPM plugin that used to, via swiftOAuthToApps,
// is gone) and embedded in web link's
// Palbase/palbase-config.json for palbe-gen. Nil means "fetch failed or no
// providers configured".
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
