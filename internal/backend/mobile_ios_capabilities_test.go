package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Apple Sign In: entitlements file -----------------------------------

func TestEnsureAppleSignInEntitlement_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "App.entitlements")
	changed, err := ensureAppleSignInEntitlement(path)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true for new file")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), appleSignInEntitlement) {
		t.Errorf("entitlement missing:\n%s", data)
	}
	// Idempotent: second run is a no-op.
	changed2, err := ensureAppleSignInEntitlement(path)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if changed2 {
		t.Error("second run should be no-op")
	}
}

func TestEnsureAppleSignInEntitlement_MergeIntoExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "App.entitlements")
	existing := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>keychain-access-groups</key>
	<array>
		<string>$(AppIdentifierPrefix)com.demo.app</string>
	</array>
</dict>
</plist>
`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := ensureAppleSignInEntitlement(path)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !changed {
		t.Fatal("expected merge to change the file")
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	// Both the pre-existing key and the new one must survive.
	if !strings.Contains(s, "keychain-access-groups") {
		t.Error("existing key lost during merge")
	}
	if !strings.Contains(s, appleSignInEntitlement) {
		t.Error("applesignin not added")
	}
	// Idempotent.
	changed2, _ := ensureAppleSignInEntitlement(path)
	if changed2 {
		t.Error("second run should be no-op")
	}
}

// --- Apple Sign In: CODE_SIGN_ENTITLEMENTS in BOTH configs ---------------

func TestEnsureCodeSignEntitlementsSetting_BothConfigs(t *testing.T) {
	// Two XCBuildConfiguration blocks (Debug + Release) for the app
	// target, plus one for a different target that must NOT be touched.
	const pbx = `// !$*UTF8*$!
{
	objects = {
/* Begin XCBuildConfiguration section */
		AAAA1111 /* Debug */ = {
			isa = XCBuildConfiguration;
			buildSettings = {
				PRODUCT_NAME = App;
			};
			name = Debug;
		};
		AAAA2222 /* Release */ = {
			isa = XCBuildConfiguration;
			buildSettings = {
				PRODUCT_NAME = App;
			};
			name = Release;
		};
		BBBB3333 /* Debug */ = {
			isa = XCBuildConfiguration;
			buildSettings = {
				PRODUCT_NAME = Tests;
			};
			name = Debug;
		};
/* End XCBuildConfiguration section */
	};
}
`
	out, changed := ensureCodeSignEntitlementsSetting(pbx, "App.entitlements", []string{"AAAA1111", "AAAA2222"})
	if !changed {
		t.Fatal("expected change")
	}
	// Exactly two injections — one per app config, not the Tests config.
	count := strings.Count(out, "CODE_SIGN_ENTITLEMENTS = App.entitlements;")
	if count != 2 {
		t.Fatalf("expected 2 entitlements settings (Debug+Release), got %d:\n%s", count, out)
	}
	// The Tests config must stay clean.
	testsBlock := out[strings.Index(out, "BBBB3333"):]
	if strings.Contains(testsBlock[:strings.Index(testsBlock, "name = Debug;")], "CODE_SIGN_ENTITLEMENTS") {
		t.Error("Tests config wrongly got the entitlements setting")
	}
	// Idempotent.
	out2, changed2 := ensureCodeSignEntitlementsSetting(out, "App.entitlements", []string{"AAAA1111", "AAAA2222"})
	if changed2 {
		t.Errorf("second run should be no-op:\n%s", out2)
	}
}

// --- Google URL scheme injection ----------------------------------------

func TestEnsureGoogleURLScheme_NoExistingURLTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Info.plist")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>App</string>
</dict>
</plist>
`
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	redirect := "com.googleusercontent.apps.123-abc:/oauthredirect"
	changed, err := ensureGoogleURLScheme(path, redirect)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !changed {
		t.Fatal("expected change")
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.Contains(s, "<key>CFBundleURLTypes</key>") {
		t.Error("CFBundleURLTypes not created")
	}
	if !strings.Contains(s, "<string>com.googleusercontent.apps.123-abc</string>") {
		t.Errorf("scheme not injected:\n%s", s)
	}
	if !strings.Contains(s, "<key>CFBundleName</key>") {
		t.Error("existing key lost")
	}
	// Idempotent.
	changed2, _ := ensureGoogleURLScheme(path, redirect)
	if changed2 {
		t.Error("second run should be no-op")
	}
}

func TestEnsureGoogleURLScheme_ExistingURLTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Info.plist")
	// Customer already has a URL type — we must append, not clobber.
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleURLTypes</key>
	<array>
		<dict>
			<key>CFBundleURLSchemes</key>
			<array>
				<string>myapp</string>
			</array>
		</dict>
	</array>
</dict>
</plist>
`
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := ensureGoogleURLScheme(path, "com.googleusercontent.apps.999:/oauthredirect")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !changed {
		t.Fatal("expected change")
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	// Customer's scheme survives.
	if !strings.Contains(s, "<string>myapp</string>") {
		t.Error("existing url scheme lost")
	}
	// Ours added.
	if !strings.Contains(s, "<string>com.googleusercontent.apps.999</string>") {
		t.Errorf("google scheme not added:\n%s", s)
	}
}

func TestPBXQuote(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"palbase.entitlements", "palbase.entitlements"}, // safe — unquoted
		{"My App.entitlements", `"My App.entitlements"`}, // space — quoted
		{"a/b-c_d.ext", "a/b-c_d.ext"},                   // all safe chars
		{`weird"name`, `"weird\"name"`},                  // embedded quote escaped
	}
	for _, tt := range tests {
		if got := pbxQuote(tt.in); got != tt.want {
			t.Errorf("pbxQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// When the target name has a space, the CODE_SIGN_ENTITLEMENTS value
// must be quoted in the pbxproj or the project fails to parse.
func TestEnsureCodeSignEntitlementsSetting_QuotesSpacedName(t *testing.T) {
	const pbx = `// !$*UTF8*$!
{
	objects = {
		AAAA1111 /* Debug */ = {
			isa = XCBuildConfiguration;
			buildSettings = {
				PRODUCT_NAME = App;
			};
			name = Debug;
		};
	};
}
`
	out, changed := ensureCodeSignEntitlementsSetting(pbx, "My App.entitlements", []string{"AAAA1111"})
	if !changed {
		t.Fatal("expected change")
	}
	if !strings.Contains(out, `CODE_SIGN_ENTITLEMENTS = "My App.entitlements";`) {
		t.Errorf("spaced entitlements path not quoted:\n%s", out)
	}
}

func TestEnsureGoogleURLScheme_MissingPlistIsNoOp(t *testing.T) {
	// GENERATE_INFOPLIST_FILE projects have no file on disk — must not
	// error, just skip (caller surfaces a manual-step hint).
	changed, err := ensureGoogleURLScheme(filepath.Join(t.TempDir(), "nope.plist"), "x:/y")
	if err != nil {
		t.Fatalf("missing plist should be no-op, got: %v", err)
	}
	if changed {
		t.Error("missing plist should report no change")
	}
}
