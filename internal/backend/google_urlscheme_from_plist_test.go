package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A per-env Palbase-Info.plist (as emitted by `apps config` / codegen `--app`)
// carrying oauth.google for both build configs. The Debug dict is the dev env
// the bare-link app wires its URL scheme from (Debug=dev, Release=production).
const perEnvPlistWithGoogle = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Debug</key>
	<dict>
		<key>app_id</key>
		<string>app_1</string>
		<key>identifier</key>
		<string>com.x.todo.dev</string>
		<key>env_preset</key>
		<string>development</string>
		<key>base_url</key>
		<string>https://dev.example.com</string>
		<key>api_key</key>
		<string>pb_dev</string>
		<key>oauth</key>
		<dict>
			<key>google</key>
			<dict>
				<key>enabled</key>
				<true/>
				<key>client_id</key>
				<string>123-abc.apps.googleusercontent.com</string>
				<key>redirect_uri</key>
				<string>com.googleusercontent.apps.123-abc:/oauthredirect</string>
			</dict>
		</dict>
	</dict>
	<key>Release</key>
	<dict>
		<key>app_id</key>
		<string>app_1</string>
		<key>identifier</key>
		<string>com.x.todo</string>
		<key>env_preset</key>
		<string>production</string>
		<key>base_url</key>
		<string>https://prod.example.com</string>
		<key>api_key</key>
		<string>pb_prod</string>
		<key>oauth</key>
		<dict>
			<key>google</key>
			<dict>
				<key>enabled</key>
				<true/>
				<key>client_id</key>
				<string>999-rel.apps.googleusercontent.com</string>
				<key>redirect_uri</key>
				<string>com.googleusercontent.apps.999-rel:/oauthredirect</string>
			</dict>
		</dict>
	</dict>
</dict>
</plist>
`

// Same plist shape but with NO oauth block (Google not configured) — the
// graceful no-op case the JSON path used to handle by omitting the block.
const perEnvPlistNoGoogle = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Debug</key>
	<dict>
		<key>app_id</key>
		<string>app_1</string>
		<key>identifier</key>
		<string>com.x.todo.dev</string>
		<key>env_preset</key>
		<string>development</string>
		<key>base_url</key>
		<string>https://dev.example.com</string>
		<key>api_key</key>
		<string>pb_dev</string>
	</dict>
</dict>
</plist>
`

// --- googleRedirectURIFromPlist: the new SOURCE (Palbase-Info.plist) ----------

// TestGoogleRedirectURIFromPlist_Present pins that the Debug env's
// oauth.google.redirect_uri is extracted from the Palbase-Info.plist — the
// SOURCE that replaced the retired PalbaseGenerated.json. The scheme injected
// into the app's Info.plist (everything before the first ':') derives from it.
func TestGoogleRedirectURIFromPlist_Present(t *testing.T) {
	dir := t.TempDir()
	plist := filepath.Join(dir, "Palbase-Info.plist")
	if err := os.WriteFile(plist, []byte(perEnvPlistWithGoogle), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := googleRedirectURIFromPlist(plist)
	if err != nil {
		t.Fatalf("googleRedirectURIFromPlist: %v", err)
	}
	// Debug (dev) env's redirect_uri — first env dict wins, matching the
	// single redirect_uri the JSON path used to carry.
	want := "com.googleusercontent.apps.123-abc:/oauthredirect"
	if got != want {
		t.Fatalf("redirect_uri = %q, want %q", got, want)
	}
}

// TestGoogleRedirectURIFromPlist_NoGoogle pins the graceful no-op: a plist
// without an oauth.google block yields an empty redirect_uri (no scheme to
// inject) — the same degrade the JSON path had when it omitted the block.
func TestGoogleRedirectURIFromPlist_NoGoogle(t *testing.T) {
	dir := t.TempDir()
	plist := filepath.Join(dir, "Palbase-Info.plist")
	if err := os.WriteFile(plist, []byte(perEnvPlistNoGoogle), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := googleRedirectURIFromPlist(plist)
	if err != nil {
		t.Fatalf("googleRedirectURIFromPlist: %v", err)
	}
	if got != "" {
		t.Fatalf("redirect_uri = %q, want empty (no google configured)", got)
	}
}

// TestGoogleRedirectURIFromPlist_MissingFile pins that a missing plist (no
// `apps config` / codegen `--app` run yet — the bare-link path) is a graceful
// no-op (empty, no error), exactly as the JSON read returned nil when absent.
func TestGoogleRedirectURIFromPlist_MissingFile(t *testing.T) {
	got, err := googleRedirectURIFromPlist(filepath.Join(t.TempDir(), "nope.plist"))
	if err != nil {
		t.Fatalf("missing plist should be no-op, got err: %v", err)
	}
	if got != "" {
		t.Fatalf("redirect_uri = %q, want empty for missing plist", got)
	}
}

// --- wireGoogleURLSchemeFromPlist: full flow (SOURCE plist → TARGET Info.plist)

// pbxWithInfoPlist is a minimal but complete pbxproj whose App target points
// INFOPLIST_FILE at App/Info.plist in both Debug and Release configs, so
// resolveInfoPlistPath finds the TARGET Info.plist to inject the scheme into.
const pbxWithInfoPlist = `// !$*UTF8*$!
{
	archiveVersion = 1;
	objectVersion = 60;
	objects = {
		AAAAAAAAAAAAAAAAAAAAAAAA /* App.app */ = {isa = PBXFileReference; explicitFileType = wrapper.application; path = App.app; sourceTree = BUILT_PRODUCTS_DIR; };
		111111111111111111111111 /* App */ = {
			isa = PBXNativeTarget;
			buildConfigurationList = 222222222222222222222222 /* Build configuration list for PBXNativeTarget "App" */;
			name = App;
			productName = App;
			productReference = AAAAAAAAAAAAAAAAAAAAAAAA /* App.app */;
			productType = "com.apple.product-type.application";
		};
		222222222222222222222222 /* Build configuration list for PBXNativeTarget "App" */ = {
			isa = XCConfigurationList;
			buildConfigurations = (
				D1D1D1D1D1D1D1D1D1D1D1D1 /* Debug */,
				R1R1R1R1R1R1R1R1R1R1R1R1 /* Release */,
			);
			defaultConfigurationIsVisible = 0;
			defaultConfigurationName = Release;
		};
		D1D1D1D1D1D1D1D1D1D1D1D1 /* Debug */ = {
			isa = XCBuildConfiguration;
			buildSettings = {
				INFOPLIST_FILE = App/Info.plist;
				PRODUCT_BUNDLE_IDENTIFIER = com.x.todo;
			};
			name = Debug;
		};
		R1R1R1R1R1R1R1R1R1R1R1R1 /* Release */ = {
			isa = XCBuildConfiguration;
			buildSettings = {
				INFOPLIST_FILE = App/Info.plist;
				PRODUCT_BUNDLE_IDENTIFIER = com.x.todo;
			};
			name = Release;
		};
		444444444444444444444444 /* Project object */ = {
			isa = PBXProject;
			targets = (
				111111111111111111111111 /* App */,
			);
		};
	};
	rootObject = 444444444444444444444444 /* Project object */;
}
`

const minimalInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>App</string>
</dict>
</plist>
`

// TestWireGoogleURLSchemeFromPlist_Injects pins the full fix: given a
// Palbase-Info.plist (SOURCE) carrying oauth.google.redirect_uri, the wiring
// injects the reversed-DNS scheme into the app target's Info.plist (TARGET).
// SOURCE = Palbase-Info.plist, TARGET = App/Info.plist — kept distinct.
func TestWireGoogleURLSchemeFromPlist_Injects(t *testing.T) {
	root := t.TempDir()

	// SOURCE: per-env Palbase-Info.plist lives next to the generated swift.
	genDir := filepath.Join(root, "Generated")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}
	swift := filepath.Join(genDir, "PalbaseGenerated.swift")
	if err := os.WriteFile(filepath.Join(genDir, "Palbase-Info.plist"), []byte(perEnvPlistWithGoogle), 0o644); err != nil {
		t.Fatal(err)
	}

	// TARGET: the app project + its Info.plist.
	projDir := filepath.Join(root, "App.xcodeproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "project.pbxproj"), []byte(pbxWithInfoPlist), 0o644); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(root, "App")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetPlist := filepath.Join(appDir, "Info.plist")
	if err := os.WriteFile(targetPlist, []byte(minimalInfoPlist), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := wireGoogleURLSchemeFromGenerated(projDir, "App", swift, &buf); err != nil {
		t.Fatalf("wireGoogleURLSchemeFromGenerated: %v", err)
	}

	got, err := os.ReadFile(targetPlist)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	// Scheme = redirect_uri up to the first ':' — derived from the SOURCE plist.
	wantScheme := "<string>com.googleusercontent.apps.123-abc</string>"
	if !strings.Contains(s, wantScheme) {
		t.Fatalf("TARGET Info.plist missing injected scheme %q:\n%s", wantScheme, s)
	}
	if !strings.Contains(s, "<key>CFBundleURLTypes</key>") {
		t.Fatalf("TARGET Info.plist missing CFBundleURLTypes:\n%s", s)
	}
}

// TestWireGoogleURLSchemeFromPlist_NoGoogleNoOp pins the mutation-evident
// graceful no-op: a Palbase-Info.plist WITHOUT oauth.google leaves the TARGET
// Info.plist untouched (no CFBundleURLTypes injected).
func TestWireGoogleURLSchemeFromPlist_NoGoogleNoOp(t *testing.T) {
	root := t.TempDir()

	genDir := filepath.Join(root, "Generated")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}
	swift := filepath.Join(genDir, "PalbaseGenerated.swift")
	if err := os.WriteFile(filepath.Join(genDir, "Palbase-Info.plist"), []byte(perEnvPlistNoGoogle), 0o644); err != nil {
		t.Fatal(err)
	}

	projDir := filepath.Join(root, "App.xcodeproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "project.pbxproj"), []byte(pbxWithInfoPlist), 0o644); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(root, "App")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetPlist := filepath.Join(appDir, "Info.plist")
	if err := os.WriteFile(targetPlist, []byte(minimalInfoPlist), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := wireGoogleURLSchemeFromGenerated(projDir, "App", swift, &buf); err != nil {
		t.Fatalf("wireGoogleURLSchemeFromGenerated: %v", err)
	}

	got, err := os.ReadFile(targetPlist)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "CFBundleURLTypes") {
		t.Fatalf("no-google plist must NOT inject a URL scheme:\n%s", string(got))
	}
}

// TestWireGoogleURLSchemeFromPlist_MissingPlistNoOp pins the bare-link path:
// no Palbase-Info.plist on disk yet → graceful no-op (the JSON-retirement
// behavior the fix preserves until codegen `--app` produces the plist).
func TestWireGoogleURLSchemeFromPlist_MissingPlistNoOp(t *testing.T) {
	root := t.TempDir()
	genDir := filepath.Join(root, "Generated")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}
	swift := filepath.Join(genDir, "PalbaseGenerated.swift")
	// No Palbase-Info.plist written next to swift.

	projDir := filepath.Join(root, "App.xcodeproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "project.pbxproj"), []byte(pbxWithInfoPlist), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := wireGoogleURLSchemeFromGenerated(projDir, "App", swift, &buf); err != nil {
		t.Fatalf("missing plist should be a graceful no-op, got: %v", err)
	}
}
