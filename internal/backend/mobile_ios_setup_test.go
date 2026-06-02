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

func TestPatchXcodeProjectWiresSynchronizedFolder(t *testing.T) {
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

	// The deterministic sync-folder id (xcodeObjectID seed) the wiring uses.
	syncID := xcodeObjectID("palbase-ios-sync-folder")

	for _, must := range []string{
		// objectVersion bumped 60 → 77 (synced folders need >= 70; we emit 77).
		"objectVersion = 77;",
		// The synchronized FOLDER reference object + its section.
		"/* Begin PBXFileSystemSynchronizedRootGroup section */",
		"isa = PBXFileSystemSynchronizedRootGroup;",
		"path = Palbase/Generated;",
		// Attached to the target as a synchronized group.
		"fileSystemSynchronizedGroups = (",
		syncID + " /* Generated */,",
		// Shows in the navigator: parented under the project's main group.
		"777777777777777777777777 /* App */,", // root group children intact
		// The codegen build phase survives, with the NEW visible output path.
		"name = \"Palbase Codegen iOS\";",
		"$(SRCROOT)/Palbase/Generated/PalbaseGenerated.swift",
		"$(SRCROOT)/Palbase/Generated/PalbaseGenerated.json",
		// Build phase prepended to the target so it runs BEFORE compile.
		"palbase mobile codegen ios",
	} {
		if !strings.Contains(out, must) {
			t.Fatalf("patched project missing %q:\n%s", must, out)
		}
	}

	// The sync-folder id must appear in the main (root) group's children so
	// Xcode shows the folder in the navigator.
	rootBlock := objectBlock(out, "999999999999999999999999")
	if !strings.Contains(rootBlock, syncID) {
		t.Fatalf("sync folder id %s not parented under main group:\n%s", syncID, rootBlock)
	}

	// The fileSystemSynchronizedGroups array must live on the native target
	// object (sibling of buildPhases), not somewhere stray.
	targetBlock := objectBlock(out, "111111111111111111111111")
	if !strings.Contains(targetBlock, "fileSystemSynchronizedGroups = (") {
		t.Fatalf("fileSystemSynchronizedGroups not on native target:\n%s", targetBlock)
	}
	if !strings.Contains(targetBlock, syncID) {
		t.Fatalf("sync folder id %s not in target's fileSystemSynchronizedGroups:\n%s", syncID, targetBlock)
	}

	// ABSENCE: the old per-file plumbing must NOT be emitted — a .swift
	// referenced both via a PBXBuildFile AND auto-synced would double-compile.
	for _, gone := range []string{
		"PalbaseGenerated.swift in Sources",
		"PalbaseGenerated.json in Resources",
		"path = .palbase/generated/ios/PalbaseGenerated.swift;",
		"path = .palbase/generated/ios/PalbaseGenerated.json;",
		"$(SRCROOT)/.palbase/generated/ios/PalbaseGenerated.swift",
	} {
		if strings.Contains(out, gone) {
			t.Fatalf("patched project still contains old per-file artifact %q:\n%s", gone, out)
		}
	}

	// Idempotency: re-patching the already-wired project is a no-op.
	out2, target2, changed2, err := patchXcodeProject(out, "App")
	if err != nil {
		t.Fatalf("second patchXcodeProject: %v", err)
	}
	if changed2 {
		t.Fatalf("second patch should be idempotent:\n%s", out2)
	}
	if target2 != "App" {
		t.Fatalf("second target = %q, want App", target2)
	}
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
