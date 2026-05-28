package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Xcode capability wiring for `palbase mobile setup ios`:
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

// resolveInfoPlistPath finds the Info.plist for the app target by
// reading INFOPLIST_FILE from one of its build configs. Returns the
// path relative to the project dir's parent (SRCROOT). Empty string
// when the project uses GENERATE_INFOPLIST_FILE (no file on disk).
func resolveInfoPlistPath(pbx, projectDir string, configIDs []string) string {
	for _, id := range configIDs {
		block := objectBlock(pbx, id)
		if block == "" {
			continue
		}
		v := xcodeValue(block, "INFOPLIST_FILE")
		v = strings.Trim(strings.TrimSpace(v), `"`)
		if v == "" {
			continue
		}
		// INFOPLIST_FILE is SRCROOT-relative; SRCROOT is the directory
		// that contains the .xcodeproj.
		srcroot := filepath.Dir(projectDir)
		return filepath.Join(srcroot, v)
	}
	return ""
}
