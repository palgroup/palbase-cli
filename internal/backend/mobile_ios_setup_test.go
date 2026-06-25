package backend

import (
	"strings"
	"testing"
)

// richPBX is a minimal-but-realistic Xcode project fixture: it carries an
// objectVersion line (deliberately 60, pre-sync-folder, to prove the bump
// to 77), a PBXProject with a mainGroup, a root PBXGroup, a
// PBXNativeTarget with a Sources build phase, and the Sources phase object.
// The synchronized-folder wiring needs all of these to splice into.
const richPBX = `// !$*UTF8*$!
{
	archiveVersion = 1;
	classes = {
	};
	objectVersion = 60;
	objects = {
/* Begin PBXBuildFile section */
/* End PBXBuildFile section */
/* Begin PBXFileReference section */
		AAAAAAAAAAAAAAAAAAAAAAAA /* App.app */ = {isa = PBXFileReference; explicitFileType = wrapper.application; path = App.app; sourceTree = BUILT_PRODUCTS_DIR; };
/* End PBXFileReference section */
/* Begin PBXFrameworksBuildPhase section */
		666666666666666666666666 /* Frameworks */ = {
			isa = PBXFrameworksBuildPhase;
			buildActionMask = 2147483647;
			files = (
			);
			runOnlyForDeploymentPostprocessing = 0;
		};
/* End PBXFrameworksBuildPhase section */
/* Begin PBXGroup section */
		777777777777777777777777 /* App */ = {
			isa = PBXGroup;
			children = (
			);
			path = App;
			sourceTree = "<group>";
		};
		888888888888888888888888 /* Products */ = {
			isa = PBXGroup;
			children = (
				AAAAAAAAAAAAAAAAAAAAAAAA /* App.app */,
			);
			name = Products;
			sourceTree = "<group>";
		};
		999999999999999999999999 = {
			isa = PBXGroup;
			children = (
				777777777777777777777777 /* App */,
				888888888888888888888888 /* Products */,
			);
			sourceTree = "<group>";
		};
/* End PBXGroup section */
/* Begin PBXNativeTarget section */
		111111111111111111111111 /* App */ = {
			isa = PBXNativeTarget;
			buildConfigurationList = 222222222222222222222222 /* Build configuration list for PBXNativeTarget "App" */;
			buildPhases = (
				333333333333333333333333 /* Sources */,
				666666666666666666666666 /* Frameworks */,
				555555555555555555555555 /* Resources */,
			);
			dependencies = (
			);
			name = App;
			productName = App;
			productReference = AAAAAAAAAAAAAAAAAAAAAAAA /* App.app */;
			productType = "com.apple.product-type.application";
		};
/* End PBXNativeTarget section */
/* Begin PBXProject section */
		444444444444444444444444 /* Project object */ = {
			isa = PBXProject;
			buildConfigurationList = BBBBBBBBBBBBBBBBBBBBBBBB /* Build configuration list for PBXProject "App" */;
			mainGroup = 999999999999999999999999;
			productRefGroup = 888888888888888888888888 /* Products */;
			targets = (
				111111111111111111111111 /* App */,
			);
		};
/* End PBXProject section */
/* Begin PBXResourcesBuildPhase section */
		555555555555555555555555 /* Resources */ = {
			isa = PBXResourcesBuildPhase;
			buildActionMask = 2147483647;
			files = (
			);
			runOnlyForDeploymentPostprocessing = 0;
		};
/* End PBXResourcesBuildPhase section */
/* Begin PBXSourcesBuildPhase section */
		333333333333333333333333 /* Sources */ = {
			isa = PBXSourcesBuildPhase;
			buildActionMask = 2147483647;
			files = (
			);
			runOnlyForDeploymentPostprocessing = 0;
		};
/* End PBXSourcesBuildPhase section */
	};
	rootObject = 444444444444444444444444 /* Project object */;
}
`

// modernPBX models a real Xcode 16 project: the app target's source folder
// "palbase" is ITSELF a PBXFileSystemSynchronizedRootGroup (path = palbase),
// listed in the target's fileSystemSynchronizedGroups, and parented under the
// main group. objectVersion is already 77. This is the shape that produces
// the double-folder bug with the old code (a stray root "Palbase/Generated"
// reference on top of the app folder auto-surfacing "palbase/Generated").
const modernPBX = `// !$*UTF8*$!
{
	archiveVersion = 1;
	classes = {
	};
	objectVersion = 77;
	objects = {
/* Begin PBXFileReference section */
		AAAAAAAAAAAAAAAAAAAAAAAA /* palbase.app */ = {isa = PBXFileReference; explicitFileType = wrapper.application; path = palbase.app; sourceTree = BUILT_PRODUCTS_DIR; };
/* End PBXFileReference section */
/* Begin PBXFileSystemSynchronizedRootGroup section */
		CCCCCCCCCCCCCCCCCCCCCCCC /* palbase */ = {
			isa = PBXFileSystemSynchronizedRootGroup;
			path = palbase;
			sourceTree = "<group>";
		};
/* End PBXFileSystemSynchronizedRootGroup section */
/* Begin PBXGroup section */
		888888888888888888888888 /* Products */ = {
			isa = PBXGroup;
			children = (
				AAAAAAAAAAAAAAAAAAAAAAAA /* palbase.app */,
			);
			name = Products;
			sourceTree = "<group>";
		};
		999999999999999999999999 = {
			isa = PBXGroup;
			children = (
				CCCCCCCCCCCCCCCCCCCCCCCC /* palbase */,
				888888888888888888888888 /* Products */,
			);
			sourceTree = "<group>";
		};
/* End PBXGroup section */
/* Begin PBXNativeTarget section */
		111111111111111111111111 /* palbase */ = {
			isa = PBXNativeTarget;
			buildConfigurationList = 222222222222222222222222 /* Build configuration list for PBXNativeTarget "palbase" */;
			buildPhases = (
				333333333333333333333333 /* Sources */,
			);
			dependencies = (
			);
			fileSystemSynchronizedGroups = (
				CCCCCCCCCCCCCCCCCCCCCCCC /* palbase */,
			);
			name = palbase;
			productName = palbase;
			productReference = AAAAAAAAAAAAAAAAAAAAAAAA /* palbase.app */;
			productType = "com.apple.product-type.application";
		};
/* End PBXNativeTarget section */
/* Begin PBXProject section */
		444444444444444444444444 /* Project object */ = {
			isa = PBXProject;
			mainGroup = 999999999999999999999999;
			productRefGroup = 888888888888888888888888 /* Products */;
			targets = (
				111111111111111111111111 /* palbase */,
			);
		};
/* End PBXProject section */
/* Begin PBXSourcesBuildPhase section */
		333333333333333333333333 /* Sources */ = {
			isa = PBXSourcesBuildPhase;
			buildActionMask = 2147483647;
			files = (
			);
			runOnlyForDeploymentPostprocessing = 0;
		};
/* End PBXSourcesBuildPhase section */
	};
	rootObject = 444444444444444444444444 /* Project object */;
}
`

// TestPatchXcodeProjectModernSyncedTarget: on a modern Xcode 16 project whose
// app target folder is itself a synchronized folder, codegen output lives at
// <appFolder>/Generated and the app's own synced folder surfaces it — so we
// add NO stray root-level synchronized folder, NO root child ref, and only
// wire the build phase. This is the fix for the double-appearance bug.
func TestPatchXcodeProjectModernSyncedTarget(t *testing.T) {
	out, target, changed, err := patchXcodeProject(modernPBX, "")
	if err != nil {
		t.Fatalf("patchXcodeProject: %v", err)
	}
	if !changed {
		t.Fatal("expected project to change (build phase added)")
	}
	if target != "palbase" {
		t.Fatalf("target = %q, want palbase", target)
	}

	syncID := xcodeObjectID("palbase-ios-sync-folder")

	// PRESENT: the build phase, with outputPaths INSIDE the app folder
	// (lowercase "palbase/Generated", matching the app folder's casing — not
	// a case-colliding capital "Palbase/Generated").
	for _, must := range []string{
		"name = \"Palbase Codegen iOS\";",
		"$(SRCROOT)/palbase/Generated/PalbaseGenerated.swift",
		"alwaysOutOfDate = 1;",
		"palbase mobile codegen ios",
		// The phase must export PALBASE_IOS_GENERATED_DIR from Xcode's declared
		// output path so codegen writes inside the sandbox-permitted dir (the
		// fix for "mkdir Generated: operation not permitted" under user-script
		// sandboxing, where runtime .xcodeproj discovery is blocked).
		"PALBASE_IOS_GENERATED_DIR=",
		"SCRIPT_OUTPUT_FILE_0",
	} {
		if !strings.Contains(out, must) {
			t.Fatalf("patched project missing %q:\n%s", must, out)
		}
	}

	// ABSENT: we must NOT add our own synchronized folder — the app target's
	// existing synced folder already covers palbase/Generated. No stray root
	// reference, no capital "Palbase/Generated", no extra synced group.
	for _, gone := range []string{
		syncID,                      // our deterministic sync-folder id never appears
		"path = Palbase/Generated;", // the case-colliding capital path
		"path = palbase/Generated;", // we don't add OUR OWN synced folder either
		// CONFIG-CUTOVER: codegen no longer emits PalbaseGenerated.json, so the
		// build phase must NOT declare it as an output (Xcode would expect a file
		// that's never produced). Mutation-evident: re-add it to outputPaths → RED.
		"PalbaseGenerated.json",
	} {
		if strings.Contains(out, gone) {
			t.Fatalf("patched project must NOT add %q (app folder already covers it):\n%s", gone, out)
		}
	}

	// The target's fileSystemSynchronizedGroups must still hold ONLY the app
	// folder (CCC...), never our Generated id.
	targetBlock := objectBlock(out, "111111111111111111111111")
	if strings.Contains(targetBlock, syncID) {
		t.Fatalf("our sync id leaked into the target's fileSystemSynchronizedGroups:\n%s", targetBlock)
	}

	// Idempotency.
	out2, _, changed2, err := patchXcodeProject(out, "palbase")
	if err != nil {
		t.Fatalf("second patch: %v", err)
	}
	if changed2 {
		t.Fatalf("second patch should be idempotent:\n%s", out2)
	}
}

// TestPatchXcodeProjectHealsV036DoubleFolder: a project linked by v0.3.35/36
// carries a STRAY root-level synchronized folder (our deterministic id, path
// Palbase/Generated, in the main group + the target's fileSystemSynchronizedGroups).
// Re-linking must STRIP it (object + main-group child ref + target entry) so
// the double appearance heals to the single natural copy.
func TestPatchXcodeProjectHealsV036DoubleFolder(t *testing.T) {
	syncID := xcodeObjectID("palbase-ios-sync-folder")

	// Inject the v0.3.36 stray into the modern fixture: the synced-folder
	// object, its main-group child ref, and its target entry.
	strayObject := "/* Begin PBXFileSystemSynchronizedRootGroup section */\n" +
		"\t\t" + syncID + " /* Generated */ = {\n" +
		"\t\t\tisa = PBXFileSystemSynchronizedRootGroup;\n" +
		"\t\t\tpath = Palbase/Generated;\n" +
		"\t\t\tsourceTree = \"<group>\";\n" +
		"\t\t};\n" +
		"/* Begin PBXFileSystemSynchronizedRootGroup section */"
	seeded := strings.Replace(modernPBX,
		"/* Begin PBXFileSystemSynchronizedRootGroup section */", strayObject, 1)
	// add the stray to the main group children
	seeded = strings.Replace(seeded,
		"\t\t\t\tCCCCCCCCCCCCCCCCCCCCCCCC /* palbase */,",
		"\t\t\t\tCCCCCCCCCCCCCCCCCCCCCCCC /* palbase */,\n\t\t\t\t"+syncID+" /* Generated */,", 1)
	// add the stray to the target's fileSystemSynchronizedGroups
	seeded = strings.Replace(seeded,
		"\t\t\t\tCCCCCCCCCCCCCCCCCCCCCCCC /* palbase */,\n\t\t\t);\n\t\t\tname = palbase;",
		"\t\t\t\tCCCCCCCCCCCCCCCCCCCCCCCC /* palbase */,\n\t\t\t\t"+syncID+" /* Generated */,\n\t\t\t);\n\t\t\tname = palbase;", 1)

	if !strings.Contains(seeded, syncID) {
		t.Fatalf("test setup failed: stray not seeded:\n%s", seeded)
	}

	out, _, _, err := patchXcodeProject(seeded, "")
	if err != nil {
		t.Fatalf("patchXcodeProject: %v", err)
	}

	// The stray sync-folder object must be gone.
	if objectBlock(out, syncID) != "" {
		t.Fatalf("stray sync-folder object %s was not removed:\n%s", syncID, out)
	}
	// Its id must not remain anywhere (no dangling child ref / target entry).
	if strings.Contains(out, syncID) {
		t.Fatalf("stray sync-folder id %s still referenced after heal:\n%s", syncID, out)
	}
	if strings.Contains(out, "path = Palbase/Generated;") {
		t.Fatalf("stray capital Palbase/Generated path survived:\n%s", out)
	}
}

// TestPatchXcodeProjectClassicTarget: a classic (non-synced) project has no
// app synced folder, so we wire our OWN synchronized folder at
// <appFolder>/Generated, parent it UNDER the target's group (not the project
// root), bump objectVersion to 77, and register it on the target.
func TestPatchXcodeProjectClassicTarget(t *testing.T) {
	out, target, changed, err := patchXcodeProject(richPBX, "")
	if err != nil {
		t.Fatalf("patchXcodeProject: %v", err)
	}
	if !changed {
		t.Fatal("expected project to change")
	}
	if target != "App" {
		t.Fatalf("target = %q, want App", target)
	}
	syncID := xcodeObjectID("palbase-ios-sync-folder")

	for _, must := range []string{
		"objectVersion = 77;", // bumped 60 → 77 for synced folder support
		"isa = PBXFileSystemSynchronizedRootGroup;",
		"path = App/Generated;", // inside the app folder, not root "Palbase/Generated"
		"fileSystemSynchronizedGroups = (",
		syncID + " /* Generated */,",
		"$(SRCROOT)/App/Generated/PalbaseGenerated.swift",
		"alwaysOutOfDate = 1;",
		"palbase mobile codegen ios",
	} {
		if !strings.Contains(out, must) {
			t.Fatalf("classic patch missing %q:\n%s", must, out)
		}
	}

	// The Generated folder is parented under the TARGET's group (App, id 777),
	// NOT the project root group (999). That keeps it nested in the app.
	appGroup := objectBlock(out, "777777777777777777777777")
	if !strings.Contains(appGroup, syncID) {
		t.Fatalf("Generated not parented under the App group:\n%s", appGroup)
	}
	rootGroup := objectBlock(out, "999999999999999999999999")
	if strings.Contains(rootGroup, syncID) {
		t.Fatalf("Generated must NOT be parented under the project root group:\n%s", rootGroup)
	}

	// Idempotency.
	out2, _, changed2, err := patchXcodeProject(out, "App")
	if err != nil {
		t.Fatalf("second patch: %v", err)
	}
	if changed2 {
		t.Fatalf("second patch should be idempotent:\n%s", out2)
	}
}

// TestDetectAppSourceFolder covers the synced vs classic detection + casing.
func TestDetectAppSourceFolder(t *testing.T) {
	t.Run("synced target returns folder path + synced=true", func(t *testing.T) {
		targets := parseXcodeTargets(modernPBX)
		tgt, err := chooseXcodeTarget(targets, "")
		if err != nil {
			t.Fatal(err)
		}
		folder, synced := detectAppSourceFolder(modernPBX, tgt)
		if !synced {
			t.Fatal("expected synced=true for the modern fixture")
		}
		if folder != "palbase" {
			t.Fatalf("folder = %q, want palbase (exact casing)", folder)
		}
		if got := iosGeneratedDirFor(folder); got != "palbase/Generated" {
			t.Fatalf("genDir = %q, want palbase/Generated", got)
		}
	})

	t.Run("classic target falls back to target name + synced=false", func(t *testing.T) {
		targets := parseXcodeTargets(richPBX)
		tgt, err := chooseXcodeTarget(targets, "")
		if err != nil {
			t.Fatal(err)
		}
		folder, synced := detectAppSourceFolder(richPBX, tgt)
		if synced {
			t.Fatal("expected synced=false for the classic fixture")
		}
		if folder != "App" {
			t.Fatalf("folder = %q, want App", folder)
		}
	})
}

// TestEnsureObjectVersionBumpsOnlyWhenLower locks the bump policy: a project
// below the synced-folder floor is rewritten to 77; one already at (or above)
// 77 is left untouched (no spurious diff → preserves idempotency).
func TestEnsureObjectVersionBumpsOnlyWhenLower(t *testing.T) {
	t.Run("below 77 bumps to 77", func(t *testing.T) {
		in := "\tarchiveVersion = 1;\n\tobjectVersion = 60;\n\tobjects = {\n"
		out, changed := ensureObjectVersion(in, 77)
		if !changed {
			t.Fatal("expected objectVersion 60 → 77 to report changed")
		}
		if !strings.Contains(out, "objectVersion = 77;") {
			t.Fatalf("objectVersion not bumped:\n%s", out)
		}
		if strings.Contains(out, "objectVersion = 60;") {
			t.Fatalf("old objectVersion still present:\n%s", out)
		}
		// archiveVersion must be left alone.
		if !strings.Contains(out, "archiveVersion = 1;") {
			t.Fatalf("archiveVersion must not be touched:\n%s", out)
		}
	})

	t.Run("already 77 unchanged", func(t *testing.T) {
		in := "\tobjectVersion = 77;\n"
		out, changed := ensureObjectVersion(in, 77)
		if changed {
			t.Fatalf("objectVersion already 77 must not change:\n%s", out)
		}
		if out != in {
			t.Fatalf("body mutated despite no bump:\n%s", out)
		}
	})

	t.Run("above 77 unchanged", func(t *testing.T) {
		in := "\tobjectVersion = 80;\n"
		out, changed := ensureObjectVersion(in, 77)
		if changed {
			t.Fatalf("objectVersion 80 must not be downgraded:\n%s", out)
		}
		if !strings.Contains(out, "objectVersion = 80;") {
			t.Fatalf("objectVersion 80 must survive:\n%s", out)
		}
	})
}

// TestEnsureSyncedFolderGroup_UnrelatedSyncFolderDoesNotFalseSkip guards the
// idempotency check against a split-condition false positive: a project that
// already has an UNRELATED PBXFileSystemSynchronizedRootGroup (e.g. its own
// source folder) AND some other object whose path is Palbase/Generated must
// still get OUR sync group emitted. A guard that just checks "isa present" &&
// "path present" globally would falsely skip, leaving the target's
// fileSystemSynchronizedGroups pointing at an object that was never created
// (a dangling "reference to unknown object" that breaks the project).
func TestEnsureSyncedFolderGroup_UnrelatedSyncFolderDoesNotFalseSkip(t *testing.T) {
	syncID := xcodeObjectID("palbase-ios-sync-folder")
	// A pbxproj that already contains an unrelated sync folder AND a stray
	// object at path = Palbase/Generated, but NOT our deterministic groupID.
	pbx := "/* Begin PBXFileSystemSynchronizedRootGroup section */\n" +
		"\t\tDEADBEEFDEADBEEFDEADBEEF /* Sources */ = {\n" +
		"\t\t\tisa = PBXFileSystemSynchronizedRootGroup;\n" +
		"\t\t\tpath = Sources;\n" +
		"\t\t\tsourceTree = \"<group>\";\n" +
		"\t\t};\n" +
		"\t\tCAFEBABECAFEBABECAFEBABE /* something */ = {isa = PBXFileReference; path = Palbase/Generated; sourceTree = \"<group>\"; };\n" +
		"/* End PBXFileSystemSynchronizedRootGroup section */\n"

	out, did := ensureSyncedFolderGroup(pbx, syncID, "Palbase/Generated")
	if !did {
		t.Fatal("expected our sync group to be emitted despite unrelated sync folder + stray path")
	}
	block := objectBlock(out, syncID)
	if block == "" {
		t.Fatalf("our sync group %s was not emitted:\n%s", syncID, out)
	}
	if !strings.Contains(block, "isa = PBXFileSystemSynchronizedRootGroup;") ||
		!strings.Contains(block, "path = Palbase/Generated;") {
		t.Fatalf("our sync group %s is malformed:\n%s", syncID, block)
	}

	// And it IS idempotent once ours exists: a second call is a no-op.
	out2, did2 := ensureSyncedFolderGroup(out, syncID, "Palbase/Generated")
	if did2 {
		t.Fatalf("second call must be a no-op once our group exists:\n%s", out2)
	}
}

// resolveIOSGeneratedDir must honour PALBASE_IOS_GENERATED_DIR. The Xcode build
// phase sets it (from $SCRIPT_OUTPUT_FILE_0) so codegen writes inside the
// sandbox-permitted output directory instead of attempting runtime .xcodeproj
// discovery — which the user-script sandbox blocks, causing the bare-"Generated"
// fallback and "mkdir Generated: operation not permitted".
func TestResolveIOSGeneratedDir_EnvOverrideWins(t *testing.T) {
	want := "/build/sandbox/palbase/Generated"
	t.Setenv("PALBASE_IOS_GENERATED_DIR", want)
	if got := resolveIOSGeneratedDir(); got != want {
		t.Fatalf("resolveIOSGeneratedDir() = %q, want env override %q", got, want)
	}
	// Whitespace is trimmed so a stray newline from $(dirname …) can't break it.
	t.Setenv("PALBASE_IOS_GENERATED_DIR", "  "+want+"\n")
	if got := resolveIOSGeneratedDir(); got != want {
		t.Fatalf("resolveIOSGeneratedDir() with padded env = %q, want %q", got, want)
	}
}
