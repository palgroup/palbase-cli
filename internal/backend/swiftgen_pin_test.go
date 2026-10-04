package backend

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// resolvedJSON is a Package.resolved (format 3) pinning the SDK at version.
func resolvedJSON(version string) string {
	return `{
  "originHash" : "x",
  "pins" : [
    {
      "identity" : "client-sdk-swift",
      "kind" : "remoteSourceControl",
      "location" : "https://github.com/livekit/client-sdk-swift.git",
      "state" : { "revision" : "aaaa", "version" : "2.15.0" }
    },
    {
      "identity" : "palbackend-ios",
      "kind" : "remoteSourceControl",
      "location" : "https://github.com/palgroup/palbackend-ios",
      "state" : { "revision" : "bbbb", "version" : "` + version + `" }
    }
  ],
  "version" : 3
}`
}

// xcodeProject writes App.xcodeproj whose palbackend-ios reference carries the
// given requirement fields, the way Xcode writes them.
func xcodeProject(t *testing.T, root string, requirement string) {
	t.Helper()
	dir := filepath.Join(root, "App.xcodeproj")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "project.pbxproj"), []byte(`// !$*UTF8*$!
{
	objects = {
/* Begin XCRemoteSwiftPackageReference section */
		AA00000000000000000000AA /* XCRemoteSwiftPackageReference "client-sdk-swift" */ = {
			isa = XCRemoteSwiftPackageReference;
			repositoryURL = "https://github.com/livekit/client-sdk-swift.git";
			requirement = {
				kind = exactVersion;
				version = 2.15.0;
			};
		};
		BB00000000000000000000BB /* XCRemoteSwiftPackageReference "palbackend-ios" */ = {
			isa = XCRemoteSwiftPackageReference;
			repositoryURL = "https://github.com/palgroup/palbackend-ios.git";
			requirement = {
`+requirement+`
			};
		};
/* End XCRemoteSwiftPackageReference section */
	};
}
`), 0o644))
}

// embeddedResolved writes the Package.resolved Xcode keeps inside the project.
func embeddedResolved(t *testing.T, root, version string) {
	t.Helper()
	dir := filepath.Join(root, "App.xcodeproj", "project.xcworkspace", "xcshareddata", "swiftpm")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Package.resolved"), []byte(resolvedJSON(version)), 0o644))
}

// serveTags stands in for the public repo's ref advertisement — the same
// pkt-line bytes `git ls-remote` reads, including the NUL-separated capability
// list after the first ref and the peeled `^{}` lines of annotated tags.
func serveTags(t *testing.T, tags ...string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	line := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	var b strings.Builder
	b.WriteString(line("# service=git-upload-pack\n") + "0000")
	for i, tag := range tags {
		ref := "refs/tags/" + tag
		if i == 0 {
			b.WriteString(line("1111111111111111111111111111111111111111 " + ref + "\x00multi_ack thin-pack\n"))
		} else {
			b.WriteString(line("1111111111111111111111111111111111111111 " + ref + "\n"))
		}
		b.WriteString(line("2222222222222222222222222222222222222222 " + ref + "^{}\n"))
	}
	b.WriteString("0000")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		require.Equal(t, "git-upload-pack", r.URL.Query().Get("service"))
		_, _ = w.Write([]byte(b.String()))
	}))
	t.Cleanup(srv.Close)
	prev := sdkReleasesURL
	sdkReleasesURL = srv.URL + "/palbackend-ios.git/info/refs?service=git-upload-pack"
	t.Cleanup(func() { sdkReleasesURL = prev })
	return &hits
}

func TestPackageResolvedPinWins(t *testing.T) {
	root := t.TempDir()
	xcodeProject(t, root, "kind = upToNextMajorVersion;\n\t\t\t\tminimumVersion = 0.59.0;")
	embeddedResolved(t, root, "0.62.1")
	hits := serveTags(t, "v0.66.0")

	var out bytes.Buffer
	v, err := resolveSDKVersion(root, &out)
	require.NoError(t, err)
	require.Equal(t, "0.62.1", v.String(), "the pinned release, not the newest one")
	require.Empty(t, out.String(), "a pinned build is not worth a warning")
	require.Zero(t, hits.Load(), "a pin needs no network")
}

func TestTheIssueReproResolvesTheRequirementAgainstPublishedReleases(t *testing.T) {
	// palbase-cli#9: a fresh clone — an Xcode project, no Package.resolved.
	// SwiftPM's fresh resolve picks the newest release the requirement allows,
	// skipping pre-releases and the next major; so does this.
	root := t.TempDir()
	xcodeProject(t, root, "kind = upToNextMajorVersion;\n\t\t\t\tminimumVersion = 0.59.0;")
	serveTags(t, "v0.58.0", "v0.59.0", "v0.66.0", "v0.66.0-beta.1", "v0.67.0-rc.1", "v1.0.0", "v0.9.10")

	var out bytes.Buffer
	v, err := resolveSDKVersion(root, &out)
	require.NoError(t, err)
	require.Equal(t, "0.66.0", v.String())
	require.Contains(t, out.String(), "no Package.resolved pins palbackend-ios")
	require.Contains(t, out.String(), "Commit Package.resolved")
}

func TestAPinTheRequirementNoLongerAllowsIsResolvedAgain(t *testing.T) {
	root := t.TempDir()
	xcodeProject(t, root, "kind = upToNextMinorVersion;\n\t\t\t\tminimumVersion = 0.64.0;")
	embeddedResolved(t, root, "0.60.0")
	serveTags(t, "v0.60.0", "v0.64.0", "v0.64.3", "v0.65.0")

	var out bytes.Buffer
	v, err := resolveSDKVersion(root, &out)
	require.NoError(t, err)
	require.Equal(t, "0.64.3", v.String(), "up to next minor stops below 0.65.0")
	require.Contains(t, out.String(), "pins palbackend-ios 0.60.0, which the project's requirement")
}

func TestRequirementKinds(t *testing.T) {
	cases := []struct {
		name, requirement, want string
	}{
		{"exact", "kind = exactVersion;\n\t\t\t\tversion = 0.63.1;", "0.63.1"},
		{"range", "kind = versionRange;\n\t\t\t\tmaximumVersion = 0.64.0;\n\t\t\t\tminimumVersion = 0.60.0;", "0.63.1"},
		{"next major", "kind = upToNextMajorVersion;\n\t\t\t\tminimumVersion = 0.60.0;", "0.66.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			xcodeProject(t, root, c.requirement)
			hits := serveTags(t, "v0.59.0", "v0.63.1", "v0.64.0", "v0.66.0")
			v, err := resolveSDKVersion(root, &bytes.Buffer{})
			require.NoError(t, err)
			require.Equal(t, c.want, v.String())
			if c.name == "exact" {
				require.Zero(t, hits.Load(), "an exact requirement is already the answer")
			}
		})
	}
}

func TestAPackageManifestRequirementIsReadToo(t *testing.T) {
	cases := map[string]string{
		`.package(url: "https://github.com/palgroup/palbackend-ios", from: "0.60.0")`:                     "0.66.0",
		`.package(url: "https://github.com/palgroup/palbackend-ios.git", .upToNextMinor(from: "0.63.0"))`: "0.63.1",
		`.package(url: "git@github.com:palgroup/palbackend-ios.git", exact: "0.64.0")`:                    "0.64.0",
		`.package(url: "https://github.com/palgroup/palbackend-ios", "0.59.0"..<"0.64.0")`:                "0.63.1",
		`.package(name: "Palbe", url: "https://github.com/palgroup/palbackend-ios", "0.59.0"..."0.64.0")`: "0.64.0",
	}
	for dep, want := range cases {
		t.Run(want+" "+dep, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "Package.swift"), []byte(`// swift-tools-version:5.9
import PackageDescription
let package = Package(name: "App", dependencies: [
    .package(url: "https://github.com/livekit/client-sdk-swift.git", from: "2.15.0"),
    `+dep+`,
])
`), 0o644))
			serveTags(t, "v0.59.0", "v0.63.1", "v0.64.0", "v0.66.0")
			v, err := resolveSDKVersion(root, &bytes.Buffer{})
			require.NoError(t, err)
			require.Equal(t, want, v.String())
		})
	}
}

func TestVersionsThatCannotBeGeneratedForAreRefusedByName(t *testing.T) {
	t.Run("branch requirement", func(t *testing.T) {
		root := t.TempDir()
		xcodeProject(t, root, "branch = main;\n\t\t\t\tkind = branch;")
		_, err := resolveSDKVersion(root, &bytes.Buffer{})
		require.ErrorContains(t, err, "at branch main, not on a release")
	})
	t.Run("revision pin", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "Package.resolved"), []byte(`{"pins":[{"identity":"palbackend-ios",
			"location":"https://github.com/palgroup/palbackend-ios","state":{"revision":"abc123"}}],"version":2}`), 0o644))
		_, err := resolveSDKVersion(root, &bytes.Buffer{})
		require.ErrorContains(t, err, "pins palbackend-ios to revision abc123, not to a release")
	})
	t.Run("older than the per-environment layout", func(t *testing.T) {
		root := t.TempDir()
		xcodeProject(t, root, "kind = exactVersion;\n\t\t\t\tversion = 0.58.0;")
		_, err := resolveSDKVersion(root, &bytes.Buffer{})
		require.ErrorContains(t, err, "builds palbackend-ios 0.58.0")
		require.ErrorContains(t, err, "Update the package to 0.59.0 or later")
	})
	t.Run("no release satisfies the requirement", func(t *testing.T) {
		root := t.TempDir()
		xcodeProject(t, root, "kind = upToNextMajorVersion;\n\t\t\t\tminimumVersion = 2.0.0;")
		serveTags(t, "v0.66.0")
		_, err := resolveSDKVersion(root, &bytes.Buffer{})
		require.ErrorContains(t, err, "no published palbackend-ios release satisfies")
	})
	t.Run("no SDK dependency", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "App.xcodeproj"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "App.xcodeproj", "project.pbxproj"),
			[]byte("isa = XCLocalSwiftPackageReference;\n\t\t\trelativePath = ../palbackend-ios-src;\n"), 0o644))
		_, err := resolveSDKVersion(root, &bytes.Buffer{})
		require.ErrorContains(t, err, "does not depend on palbackend-ios")
		require.ErrorContains(t, err, "../palbackend-ios-src", "a local reference that is missing here is named")
	})
}

func TestIsSDKRepository(t *testing.T) {
	for _, url := range []string{
		"https://github.com/palgroup/palbackend-ios",
		"https://github.com/palgroup/palbackend-ios.git",
		"https://github.com/PalGroup/PalBackend-iOS/",
		"git@github.com:palgroup/palbackend-ios.git",
	} {
		require.True(t, isSDKRepository(url), url)
	}
	for _, url := range []string{
		"https://github.com/palgroup/palbackend-ios-src",
		"https://github.com/palgroup/palbackend-android",
		"https://github.com/livekit/client-sdk-swift.git",
	} {
		require.False(t, isSDKRepository(url), url)
	}
}
