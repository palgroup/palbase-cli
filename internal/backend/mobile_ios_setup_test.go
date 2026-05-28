package backend

import (
	"strings"
	"testing"
)

func TestPatchXcodeProjectAddsPalbaseGeneratedFileAndPhase(t *testing.T) {
	const pbx = `// !$*UTF8*$!
{
	objects = {
/* Begin PBXBuildFile section */
/* End PBXBuildFile section */
/* Begin PBXFileReference section */
/* End PBXFileReference section */
/* Begin PBXNativeTarget section */
		111111111111111111111111 /* App */ = {
			isa = PBXNativeTarget;
			buildConfigurationList = 222222222222222222222222 /* Build configuration list for PBXNativeTarget "App" */;
			buildPhases = (
				333333333333333333333333 /* Sources */,
			);
			name = App;
			productName = App;
			productType = "com.apple.product-type.application";
		};
/* End PBXNativeTarget section */
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
	rootObject = 444444444444444444444444;
}
`

	out, target, changed, err := patchXcodeProject(pbx, "")
	if err != nil {
		t.Fatalf("patchXcodeProject: %v", err)
	}
	if !changed {
		t.Fatal("expected project to change")
	}
	if target != "App" {
		t.Fatalf("target = %q, want App", target)
	}
	for _, must := range []string{
		"PalbaseGenerated.swift in Sources",
		"path = .palbase/generated/ios/PalbaseGenerated.swift;",
		"name = \"Palbase Codegen iOS\";",
		"palbase mobile codegen ios",
		"$(SRCROOT)/.palbase/generated/ios/PalbaseGenerated.swift",
	} {
		if !strings.Contains(out, must) {
			t.Fatalf("patched project missing %q:\n%s", must, out)
		}
	}

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
