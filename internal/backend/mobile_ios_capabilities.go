package backend

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/palgroup/palbase-cli/internal/apps"
)

// Xcode capability wiring for `palbase mobile link ios`:
//
//   - Sign in with Apple: always wired (Apple sign-in is zero-config
//     on the SDK side — the id_token exchange is server-side, so any
//     project that turns the provider on in Studio can use
//     `pb.auth.signInWithApple()` with no per-app code). Writes a
//     <app>.entitlements file with the applesignin entitlement and
//     sets CODE_SIGN_ENTITLEMENTS in every build config.
//
//   - Google URL scheme: conditional on a Google client_id being
//     present (codegen fetched it from palauth's public
//     /auth/oauth/providers). Injects a CFBundleURLTypes entry with
//     the reversed-DNS scheme into Info.plist so
//     ASWebAuthenticationSession's redirect lands back in the app.
//
// Both are idempotent: re-running setup with the same config is a
// no-op. The plist + entitlements writers are pure string→string so
// they're unit-testable without touching Xcode or shelling out to
// PlistBuddy (which is macOS-only and untestable).

const appleSignInEntitlement = "com.apple.developer.applesignin"

// entitlementsTemplate is a minimal, valid entitlements plist with the
// Sign in with Apple capability. Written verbatim when no entitlements
// file exists yet; merged-into when one does (see ensureAppleSignInEntitlement).
const entitlementsTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>com.apple.developer.applesignin</key>
	<array>
		<string>Default</string>
	</array>
</dict>
</plist>
`

// ensureAppleSignInEntitlement writes (or augments) the entitlements
// file at entitlementsPath so it declares the Sign in with Apple
// capability. Returns changed=false when the file already has the
// entitlement (idempotent).
//
// New file: write the template. Existing file WITHOUT the key: splice
// the applesignin block in before the closing </dict>. Existing file
// WITH the key: no-op.
func ensureAppleSignInEntitlement(entitlementsPath string) (changed bool, err error) {
	existing, readErr := os.ReadFile(entitlementsPath)
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return false, fmt.Errorf("read %s: %w", entitlementsPath, readErr)
		}
		// New file — write the whole template.
		if err := os.WriteFile(entitlementsPath, []byte(entitlementsTemplate), 0o644); err != nil {
			return false, fmt.Errorf("write %s: %w", entitlementsPath, err)
		}
		return true, nil
	}
	content := string(existing)
	if strings.Contains(content, appleSignInEntitlement) {
		return false, nil // already present
	}
	merged, ok := injectEntitlement(content)
	if !ok {
		// Couldn't find the closing </dict> — non-standard file, don't
		// risk corrupting it. Caller surfaces a warning.
		return false, fmt.Errorf("entitlements file %s has no <dict> to merge into", entitlementsPath)
	}
	if err := os.WriteFile(entitlementsPath, []byte(merged), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", entitlementsPath, err)
	}
	return true, nil
}

// injectEntitlement splices the applesignin block in before the last
// </dict> of an existing entitlements plist. Pure string op for
// testability. Returns (merged, true) on success; ("", false) when no
// </dict> marker is found.
func injectEntitlement(content string) (string, bool) {
	const marker = "</dict>"
	idx := strings.LastIndex(content, marker)
	if idx == -1 {
		return "", false
	}
	block := "\t<key>" + appleSignInEntitlement + "</key>\n" +
		"\t<array>\n" +
		"\t\t<string>Default</string>\n" +
		"\t</array>\n"
	return content[:idx] + block + content[idx:], true
}

// ensureCodeSignEntitlementsSetting sets CODE_SIGN_ENTITLEMENTS =
// <relPath>; in every XCBuildConfiguration block of the app target's
// config list. Returns the patched pbxproj + whether anything changed.
//
// Xcode builds with the entitlements only when this setting points at
// the file in the config being built — so it MUST be set in both Debug
// and Release (advisor trap #1: setting it in one config silently
// fails the other build path with a 1000 error on Apple sign-in).
//
// We scope to the app target's build configurations (not the project-
// level ones) so we don't accidentally apply the entitlement to other
// targets (tests, extensions) that don't want it.
func ensureCodeSignEntitlementsSetting(pbx, relPath string, appConfigIDs []string) (string, bool) {
	// Quote the value when it contains pbxproj-unsafe characters (spaces
	// etc.) — a target named "My App" yields "My App.entitlements" which
	// must be quoted or the project won't parse.
	relPath = pbxQuote(relPath)
	setting := "CODE_SIGN_ENTITLEMENTS = " + relPath + ";"
	if strings.Contains(pbx, setting) {
		// Already set somewhere — assume idempotent. (A project that
		// already had a DIFFERENT entitlements path is the customer's
		// own; we don't clobber it — see ensureAppleSignInEntitlement
		// which merges into whatever file is referenced.)
		return pbx, false
	}
	idSet := make(map[string]bool, len(appConfigIDs))
	for _, id := range appConfigIDs {
		idSet[id] = true
	}
	changed := false
	lines := strings.Split(pbx, "\n")
	out := make([]string, 0, len(lines)+len(appConfigIDs))
	// We only need to inject the setting on the first `buildSettings = {`
	// line that opens inside one of the target's XCBuildConfiguration
	// blocks. `armed` is set when we enter a target config block and
	// disarmed after we inject (one injection per config block).
	armed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Entering one of the app target's XCBuildConfiguration blocks?
		if strings.Contains(line, "= {") {
			fields := strings.Fields(trimmed)
			if len(fields) > 0 && idSet[fields[0]] {
				armed = true
			}
		}
		out = append(out, line)
		if armed && trimmed == "buildSettings = {" {
			out = append(out, indentOf(line)+"\tCODE_SIGN_ENTITLEMENTS = "+relPath+";")
			changed = true
			armed = false // injected for this config; wait for the next block
		}
	}
	return strings.Join(out, "\n"), changed
}

// appTargetConfigIDs returns the XCBuildConfiguration object IDs that
// belong to the given app target's buildConfigurationList. We need
// these to scope CODE_SIGN_ENTITLEMENTS to the app target only.
func appTargetConfigIDs(pbx string, target xcodeTarget) []string {
	block := objectBlock(pbx, target.id)
	listID := xcodeValue(block, "buildConfigurationList")
	// buildConfigurationList value is "<id> /* ... */" — take the id.
	listID = strings.Fields(strings.TrimSpace(listID))[0]
	if listID == "" {
		return nil
	}
	listBlock := objectBlock(pbx, listID)
	return xcodeListValue(listBlock, "buildConfigurations")
}

// --- Google URL scheme injection into Info.plist -----------------------

// ensureGoogleURLScheme injects a CFBundleURLTypes entry carrying the
// Google reversed-DNS scheme into the Info.plist at plistPath. The
// scheme is derived from the redirectURI (everything before the first
// `:`). Idempotent: if the scheme is already present, no-op.
//
// Best-effort: a missing Info.plist (GENERATE_INFOPLIST_FILE = YES
// projects have no file to patch) returns (false, nil) — the customer
// must add the URL type manually in that case, surfaced by the caller.
func ensureGoogleURLScheme(plistPath, redirectURI string) (changed bool, err error) {
	scheme := redirectURI
	if i := strings.Index(redirectURI, ":"); i != -1 {
		scheme = redirectURI[:i]
	}
	if scheme == "" {
		return false, nil
	}
	data, readErr := os.ReadFile(plistPath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return false, nil // generated-plist project; caller warns
		}
		return false, fmt.Errorf("read %s: %w", plistPath, readErr)
	}
	content := string(data)
	if strings.Contains(content, "<string>"+scheme+"</string>") {
		return false, nil // scheme already present
	}
	merged, ok := injectURLScheme(content, scheme)
	if !ok {
		return false, fmt.Errorf("Info.plist %s has no <dict> to merge CFBundleURLTypes into", plistPath)
	}
	if err := os.WriteFile(plistPath, []byte(merged), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", plistPath, err)
	}
	return true, nil
}

// injectURLScheme adds the scheme to the Info.plist's CFBundleURLTypes.
// Two cases:
//   - CFBundleURLTypes already exists: append a new url-type dict to
//     the array (we matched no existing scheme above, so it's genuinely
//     a new entry).
//   - CFBundleURLTypes doesn't exist: create the whole key + array
//     before the last </dict>.
//
// Pure string op; returns ("", false) when there's no </dict> to anchor.
func injectURLScheme(content, scheme string) (string, bool) {
	urlTypeDict := "" +
		"\t\t<dict>\n" +
		"\t\t\t<key>CFBundleURLName</key>\n" +
		"\t\t\t<string>com.palbase.oauth.google</string>\n" +
		"\t\t\t<key>CFBundleURLSchemes</key>\n" +
		"\t\t\t<array>\n" +
		"\t\t\t\t<string>" + scheme + "</string>\n" +
		"\t\t\t</array>\n" +
		"\t\t</dict>\n"

	if i := strings.Index(content, "<key>CFBundleURLTypes</key>"); i != -1 {
		// Find the <array> that opens right after the key and insert our
		// dict as the first child. Look for the next "<array>" after the key.
		arrIdx := strings.Index(content[i:], "<array>")
		if arrIdx == -1 {
			return "", false
		}
		insertAt := i + arrIdx + len("<array>")
		// Newline after <array>, then our dict.
		return content[:insertAt] + "\n" + urlTypeDict + content[insertAt+len("\n"):], true
	}

	// No CFBundleURLTypes — create the full key+array before last </dict>.
	const marker = "</dict>"
	idx := strings.LastIndex(content, marker)
	if idx == -1 {
		return "", false
	}
	block := "\t<key>CFBundleURLTypes</key>\n" +
		"\t<array>\n" +
		urlTypeDict +
		"\t</array>\n"
	return content[:idx] + block + content[idx:], true
}

// resolveInfoPlistPath finds the Info.plist for the app target by
// reading INFOPLIST_FILE from one of its build configs. Returns the
// absolute path; empty string when the project uses
// GENERATE_INFOPLIST_FILE = YES (no file on disk to patch).
func resolveInfoPlistPath(pbx, projectPath string, configIDs []string) string {
	srcroot := filepath.Dir(projectPath)
	for _, id := range configIDs {
		block := objectBlock(pbx, id)
		if block == "" {
			continue
		}
		v := strings.Trim(strings.TrimSpace(xcodeValue(block, "INFOPLIST_FILE")), `"`)
		if v == "" || v == "$(GENERATE_INFOPLIST_FILE)" {
			continue
		}
		// INFOPLIST_FILE is SRCROOT-relative; SRCROOT is the dir that
		// contains the .xcodeproj.
		return filepath.Join(srcroot, v)
	}
	return ""
}

// indentOf returns the leading whitespace of a line.
func indentOf(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// pbxQuote wraps a pbxproj value in double quotes when it contains
// characters outside the unquoted-string grammar (letters, digits,
// _ . / - are safe; spaces and most punctuation are not). Mirrors how
// Xcode serialises such values.
func pbxQuote(s string) string {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '.' || r == '/' || r == '-' {
			continue
		}
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// wireGoogleURLSchemeFromGenerated injects the Google redirect-URI
// CFBundleURLTypes entry into the app target's Info.plist. No-op (nil)
// when the project has no Google provider, or uses a generated
// Info.plist (nothing on disk to patch — surfaced to the caller).
//
// CONFIG-CUTOVER: the runtime config moved to the per-env Palbase-Info.plist,
// which `palbase apps config` / codegen `--app` emits next to the .swift
// output. The Google redirect-URI is sourced from that plist's
// `oauth.google.redirect_uri` (it used to come from the now-retired
// PalbaseGenerated.json). On the bare-link path (no `--app` run yet) there's
// no plist on disk, so this no-ops (returns nil) until codegen `--app`
// produces the plist.
//
// SOURCE = Palbase-Info.plist's oauth.google (next to the generated swift);
// TARGET = the app target's own Info.plist (CFBundleURLTypes). The two are
// kept distinct: we READ the redirect-URI from the SOURCE and INJECT the
// scheme into the TARGET.
func wireGoogleURLSchemeFromGenerated(projectPath, targetName, generatedSwiftPath string, w io.Writer) error {
	// SOURCE: the per-env Palbase-Info.plist emitted next to the .swift output.
	srcPlistPath := filepath.Join(filepath.Dir(generatedSwiftPath), "Palbase-Info.plist")
	redirectURI, err := googleRedirectURIFromPlist(srcPlistPath)
	if err != nil {
		return fmt.Errorf("read Google config from %s: %w", filepath.Base(srcPlistPath), err)
	}
	if redirectURI == "" {
		return nil // no Palbase-Info.plist yet, or no Google provider — nothing to wire
	}

	pbxPath := filepath.Join(projectPath, "project.pbxproj")
	pbxData, err := os.ReadFile(pbxPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", pbxPath, err)
	}
	pbx := string(pbxData)
	targets := parseXcodeTargets(pbx)
	target, err := chooseXcodeTarget(targets, targetName)
	if err != nil {
		return err
	}
	configIDs := appTargetConfigIDs(pbx, target)
	// TARGET: the app's own Info.plist (NOT the SOURCE Palbase-Info.plist).
	plistPath := resolveInfoPlistPath(pbx, projectPath, configIDs)
	if plistPath == "" {
		return fmt.Errorf("target %q uses a generated Info.plist — add the Google URL scheme (%s) manually", target.name, redirectURI)
	}
	changed, err := ensureGoogleURLScheme(plistPath, redirectURI)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(w, "✓ wired Google URL scheme into %s\n", filepath.Base(plistPath))
	}
	return nil
}

// googleRedirectURIFromPlist reads the Google OAuth redirect-URI from the
// per-env Palbase-Info.plist's `oauth.google.redirect_uri`. The plist carries
// one config dict per build config (Debug=dev env, Release=production); the
// FIRST `google` dict's redirect_uri is returned — the Debug (dev) env's,
// matching the single redirect_uri the retired PalbaseGenerated.json carried.
//
// Returns ("", nil) — a graceful no-op — when the plist is absent (no `apps
// config` / codegen `--app` run yet) or carries no oauth.google block (Google
// not configured), mirroring how the JSON path degraded by omitting the block.
func googleRedirectURIFromPlist(plistPath string) (string, error) {
	data, err := os.ReadFile(plistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // no plist yet (bare-link path) — skip
		}
		return "", fmt.Errorf("read %s: %w", plistPath, err)
	}
	content := string(data)
	// Scope to the first `google` dict so we don't pick up an unrelated
	// `redirect_uri` from elsewhere in the plist. The emitter (apps package)
	// writes `<key>google</key>` immediately before the provider's dict.
	gi := strings.Index(content, "<key>google</key>")
	if gi == -1 {
		return "", nil // no Google provider configured — nothing to wire
	}
	ri := strings.Index(content[gi:], "<key>redirect_uri</key>")
	if ri == -1 {
		return "", nil // google block without a redirect_uri — nothing to wire
	}
	rest := content[gi+ri:]
	open := strings.Index(rest, "<string>")
	if open == -1 {
		return "", nil
	}
	rest = rest[open+len("<string>"):]
	end := strings.Index(rest, "</string>")
	if end == -1 {
		return "", nil
	}
	return plistUnescape(rest[:end]), nil
}

// plistUnescape reverses the XML metacharacter escaping the apps-package
// emitter applies (plistEscape), so a redirect_uri containing &amp;/&lt;/&gt;
// round-trips to its literal form before we derive the URL scheme.
func plistUnescape(s string) string {
	return plistXMLUnescaper.Replace(s)
}

var plistXMLUnescaper = strings.NewReplacer(
	"&amp;", "&",
	"&lt;", "<",
	"&gt;", ">",
)

// --- Per-env Palbase-Info.plist (build-config-conditioned) ----------------

// configArtifactFetch fetches the per-(app × env) config artifact for one env.
// It abstracts the Studio apps.configArtifact query so the codegen emit is
// unit-testable without a live tRPC server (tests inject a stub that returns
// the dev / production artifacts directly).
type configArtifactFetch func(ctx context.Context, appID, envRef string) (apps.ConfigArtifact, error)

// emitIOSPerEnvPlist resolves the app's DEV binding (devEnvRef) and PRODUCTION
// binding (prodEnvRef) — two apps.configArtifact fetches, one per env — and
// writes ONE build-config-conditioned Palbase-Info.plist that carries BOTH
// envs' config: the Debug build reads the dev env's values, the Release build
// reads the production env's (spec §2.5 "Debug→dev env, Release→production").
//
// This REPLACES the old single-env PalbaseGenerated.json emit on the codegen
// path: ONE plist file now carries both envs, and the app picks by build config
// at runtime. The actual plist serialization is reused from the apps package
// (apps.EmitIOSPlistPerEnv) so the file format never drifts from `palbase apps
// config`.
func emitIOSPerEnvPlist(ctx context.Context, fetch configArtifactFetch, appID, devEnvRef, prodEnvRef, outPath string) error {
	devArt, err := fetch(ctx, appID, devEnvRef)
	if err != nil {
		return fmt.Errorf("fetch dev (%s) config artifact: %w", devEnvRef, err)
	}
	prodArt, err := fetch(ctx, appID, prodEnvRef)
	if err != nil {
		return fmt.Errorf("fetch production (%s) config artifact: %w", prodEnvRef, err)
	}
	if dir := filepath.Dir(outPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	return apps.EmitIOSPlistPerEnv(devArt, prodArt, outPath)
}

// studioConfigArtifactFetch is the production configArtifactFetch the codegen
// command supplies: it runs the apps.configArtifact tRPC query for the
// (app × env) pair (the SAME query `palbase apps config` uses). The env's
// project ref is passed as projectRef; the server resolves the endpoint_ref +
// mints/looks up the env-main key.
//
// The tRPC apps.configArtifact result does NOT carry OAuth, so this wrapper
// ALSO fetches palauth's public `/auth/oauth/providers` (the SAME source the
// legacy PalbaseGenerated.json path uses) against the artifact's base_url +
// api_key and merges the result into ConfigArtifact.OAuth — making the per-env
// plist a true superset of the JSON's config role (closes the OAuth regression
// the config cutover would otherwise open). The fetch is best-effort: a blip
// leaves OAuth nil and the plist omits the block, exactly as the JSON path
// degrades.
func studioConfigArtifactFetch(q interface {
	Query(ctx context.Context, path string, input any, out any) error
}) configArtifactFetch {
	return func(ctx context.Context, appID, envRef string) (apps.ConfigArtifact, error) {
		var art apps.ConfigArtifact
		if err := q.Query(ctx, "apps.configArtifact", map[string]any{
			"appId":      appID,
			"projectRef": envRef,
		}, &art); err != nil {
			return apps.ConfigArtifact{}, err
		}
		oauth, _ := fetchOAuthProviders(ctx, art.BaseURL, art.APIKey)
		art.OAuth = swiftOAuthToApps(oauth)
		return art, nil
	}
}

// swiftOAuthToApps maps the backend package's swiftOAuthConfig (the shape
// fetchOAuthProviders returns, also embedded in PalbaseGenerated.json) onto
// the apps package's OAuthConfig (embedded in the per-env plist). The two
// shapes are field-identical by design so the iOS SDK decodes the plist's
// `oauth` block the same way it decodes the JSON's. Nil in → nil out.
func swiftOAuthToApps(in *swiftOAuthConfig) *apps.OAuthConfig {
	if in == nil {
		return nil
	}
	out := &apps.OAuthConfig{}
	if in.Apple != nil {
		out.Apple = &apps.OAuthApple{Enabled: in.Apple.Enabled}
	}
	if in.Google != nil {
		out.Google = &apps.OAuthGoogle{
			Enabled:     in.Google.Enabled,
			ClientID:    in.Google.ClientID,
			RedirectURI: in.Google.RedirectURI,
		}
	}
	if out.Apple == nil && out.Google == nil {
		return nil
	}
	return out
}

// AppsOAuthFetcher adapts the backend package's fetchOAuthProviders (the
// owner of the palauth `/auth/oauth/providers` fetch + the JSON oauth shape)
// into an apps.OAuthFetcher, so `palbase apps config` can embed the same
// `oauth` block the codegen plist + the legacy JSON carry — without the apps
// package importing backend (which would cycle). Wired in main. Best-effort:
// a fetch error degrades to nil (the config writes without `oauth`).
func AppsOAuthFetcher() apps.OAuthFetcher {
	return func(ctx context.Context, baseURL, apiKey string) *apps.OAuthConfig {
		oauth, _ := fetchOAuthProviders(ctx, baseURL, apiKey)
		return swiftOAuthToApps(oauth)
	}
}
