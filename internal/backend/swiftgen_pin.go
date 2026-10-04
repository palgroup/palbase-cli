package backend

// swiftgen_pin.go — WHICH palbackend-ios this app builds against, read from the
// checkout alone.
//
// The generator has to be the one released with the SDK the app links: the
// code it emits calls that SDK's API. The CLI used to learn the version by
// finding the checkout Xcode had resolved under DerivedData, which exists only
// on a Mac that has already built the app — so a Linux shell, or a fresh clone,
// could never generate (palbase-cli#9). The version is not a fact about
// DerivedData, though. SwiftPM decides it from two files the checkout carries —
// the requirement in the project and the pin in Package.resolved — and this
// file makes the same decision from the same two files.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// sdkRepository is the public distribution repo apps add as a package.
const sdkRepository = "https://github.com/palgroup/palbackend-ios"

// firstPrebuiltSwiftgen is the oldest release this CLI can generate with: the
// first whose generator reads the flat, one-environment config the link writes
// into every environment's directory. Older generators expect the environment
// MAP that layout replaced, so their output would be wrong rather than missing —
// which is why their releases carry no prebuilt generator.
var firstPrebuiltSwiftgen = sdkVersion{0, 59, 0}

// sdkVersion is a release version. Pre-release and build suffixes are not
// versions an app can pin here: SwiftPM skips them unless asked by name, and
// palbackend-ios publishes none.
type sdkVersion struct{ major, minor, patch int }

var strictVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

func parseSDKVersion(s string) (sdkVersion, bool) {
	m := strictVersion.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return sdkVersion{}, false
	}
	var v [3]int
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return sdkVersion{}, false
		}
		v[i] = n
	}
	return sdkVersion{v[0], v[1], v[2]}, true
}

func (v sdkVersion) String() string { return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) }

func (v sdkVersion) less(o sdkVersion) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

// sdkRequirement is the project's dependency rule, as the half-open range
// [lower, upper) SwiftPM resolves it to. A branch or revision rule has no range:
// it names a commit, and generators are published per release.
type sdkRequirement struct {
	rule         string // as written, for messages
	lower, upper sdkVersion
	commit       string // branch or revision; set → not a version rule
}

func (r sdkRequirement) allows(v sdkVersion) bool {
	return r.commit == "" && !v.less(r.lower) && v.less(r.upper)
}

func (r sdkRequirement) exact() bool {
	return r.commit == "" && r.upper == (sdkVersion{r.lower.major, r.lower.minor, r.lower.patch + 1})
}

func upToNextMajor(v sdkVersion) sdkRequirement {
	return sdkRequirement{rule: "up to next major from " + v.String(), lower: v, upper: sdkVersion{v.major + 1, 0, 0}}
}

func upToNextMinor(v sdkVersion) sdkRequirement {
	return sdkRequirement{rule: "up to next minor from " + v.String(), lower: v, upper: sdkVersion{v.major, v.minor + 1, 0}}
}

func exactly(v sdkVersion) sdkRequirement {
	return sdkRequirement{rule: "exactly " + v.String(), lower: v, upper: sdkVersion{v.major, v.minor, v.patch + 1}}
}

// isSDKRepository matches the public SDK's URL in any spelling a project may
// carry it: https or ssh, with or without `.git`, any case. The private SOURCE
// repo (palbackend-ios-src) is a different name and never matches — an app
// links that one by path, which localSwiftgenSources handles.
func isSDKRepository(location string) bool {
	s := strings.ToLower(strings.TrimSpace(location))
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	return s == "palbackend-ios"
}

// resolveSDKVersion decides which release's generator to run, the way SwiftPM
// decides which release to build: the pin in Package.resolved while the
// requirement still allows it, otherwise the newest published release the
// requirement allows. The second case is a fresh resolve; it prints a warning,
// because a machine that resolves later may land on a newer release.
func resolveSDKVersion(projectRoot string, w io.Writer) (sdkVersion, error) {
	pin, pinFile, err := sdkPinIn(projectRoot)
	if err != nil {
		return sdkVersion{}, err
	}
	req, err := sdkRequirementIn(projectRoot)
	if err != nil {
		// A rule this CLI cannot read does not unsettle a version SwiftPM has
		// already settled: the pin is the outcome of resolving that very rule.
		if pin != nil && pin.commit == "" {
			return checkPrebuilt(pin.version, pinFile)
		}
		return sdkVersion{}, err
	}
	if req == nil && pin == nil {
		return sdkVersion{}, errNoSDKDependency(projectRoot)
	}
	if pin != nil && pin.commit != "" && (req == nil || req.commit != "") {
		return sdkVersion{}, fmt.Errorf("%s pins palbackend-ios to %s, not to a release — "+
			"the Swift generator ships with each palbackend-ios release, so depend on a version", pinFile, pin.commit)
	}
	if pin != nil && pin.commit == "" && (req == nil || req.allows(pin.version)) {
		return checkPrebuilt(pin.version, pinFile)
	}
	if req.commit != "" {
		return sdkVersion{}, fmt.Errorf("this project depends on palbackend-ios at %s, not on a release — "+
			"the Swift generator ships with each palbackend-ios release, so depend on a version", req.commit)
	}
	if req.exact() {
		return checkPrebuilt(req.lower, "the project's requirement")
	}

	tags, err := listSDKReleases()
	if err != nil {
		return sdkVersion{}, fmt.Errorf("no Package.resolved pins palbackend-ios here, and the releases %s allows "+
			"could not be listed: %w", req.rule, err)
	}
	var best *sdkVersion
	for i := range tags {
		if req.allows(tags[i]) && (best == nil || best.less(tags[i])) {
			best = &tags[i]
		}
	}
	if best == nil {
		return sdkVersion{}, fmt.Errorf("no published palbackend-ios release satisfies this project's requirement (%s)", req.rule)
	}
	switch {
	case pin != nil && pin.commit != "":
		fmt.Fprintf(w, "! %s pins palbackend-ios to %s, while the project's requirement (%s) asks for a release;\n", pinFile, pin.commit, req.rule)
	case pin != nil:
		fmt.Fprintf(w, "! %s pins palbackend-ios %s, which the project's requirement (%s) no longer allows;\n", pinFile, pin.version, req.rule)
	default:
		fmt.Fprintf(w, "! no Package.resolved pins palbackend-ios in this checkout;\n")
	}
	fmt.Fprintf(w, "  generating with %s, the newest release %s allows — the one a fresh resolve picks.\n"+
		"  Commit Package.resolved so every machine builds the SDK this client was generated for.\n", best, req.rule)
	return checkPrebuilt(*best, "the newest release the requirement allows")
}

func checkPrebuilt(v sdkVersion, from string) (sdkVersion, error) {
	if v.less(firstPrebuiltSwiftgen) {
		return sdkVersion{}, fmt.Errorf("this project builds palbackend-ios %s (%s), and this CLI generates for %s or later — "+
			"earlier generators read the environment map the per-environment layout replaced. "+
			"Update the package to %s or later, then link again", v, from, firstPrebuiltSwiftgen, firstPrebuiltSwiftgen)
	}
	return v, nil
}

// errNoSDKDependency says what was READ, not what the project is. This lookup
// sees the Xcode projects at the checkout root and the ones its workspaces
// name, and a Package.swift at the root; an SDK that arrives another way — only
// through a local package's own manifest, say — is real and simply out of its
// sight, so the message names the files rather than declaring the app unlinked.
func errNoSDKDependency(projectRoot string) error {
	var read []string
	for _, pbxproj := range projectFiles(projectRoot) {
		if rel, err := filepath.Rel(projectRoot, pbxproj); err == nil {
			read = append(read, rel)
		}
	}
	if isRegularFile(filepath.Join(projectRoot, "Package.swift")) {
		read = append(read, "Package.swift")
	}
	where := "no Xcode project or Package.swift was found at the checkout root"
	if len(read) > 0 {
		where = "none of " + strings.Join(read, ", ") + " depends on it, and no Package.resolved pins it"
	}
	msg := "cannot tell which palbackend-ios this app builds: " + where + " — add the package " + sdkRepository +
		" (product Palbe) to the app, or commit the Package.resolved that pins it, then link again"
	if refs := localPackageRefs(projectRoot); len(refs) > 0 {
		msg += fmt.Sprintf(" (its local packages %s hold no Sources/palbase-swiftgen on this machine)", strings.Join(refs, ", "))
	}
	return fmt.Errorf("%s", msg)
}

// sdkPin is what Package.resolved records for the SDK.
type sdkPin struct {
	version sdkVersion
	commit  string // branch or revision, when the pin is not a release
}

// resolvedFiles lists the Package.resolved files SwiftPM would read, the
// workspace's first: Xcode resolves a workspace's packages into the workspace,
// and the project's own embedded workspace only when it is opened alone.
func resolvedFiles(projectRoot string) []string {
	var files []string
	for _, pattern := range []string{
		filepath.Join(projectRoot, "*.xcworkspace", "xcshareddata", "swiftpm", "Package.resolved"),
		filepath.Join(projectRoot, "*.xcodeproj", "project.xcworkspace", "xcshareddata", "swiftpm", "Package.resolved"),
		filepath.Join(projectRoot, "Package.resolved"),
	} {
		matches, _ := filepath.Glob(pattern)
		files = append(files, matches...)
	}
	return files
}

func sdkPinIn(projectRoot string) (*sdkPin, string, error) {
	for _, file := range resolvedFiles(projectRoot) {
		blob, err := os.ReadFile(file)
		if err != nil {
			return nil, "", err
		}
		// Versions 2 and 3 of the format. Version 1 predates Xcode 13 and
		// cannot build an iOS 18 app, so it is never in a checkout this SDK
		// supports.
		var resolved struct {
			Pins []struct {
				Location string `json:"location"`
				State    struct {
					Version  string `json:"version"`
					Branch   string `json:"branch"`
					Revision string `json:"revision"`
				} `json:"state"`
			} `json:"pins"`
		}
		if err := json.Unmarshal(blob, &resolved); err != nil {
			return nil, "", fmt.Errorf("read %s: %w", file, err)
		}
		rel, _ := filepath.Rel(projectRoot, file)
		for _, p := range resolved.Pins {
			if !isSDKRepository(p.Location) {
				continue
			}
			if v, ok := parseSDKVersion(p.State.Version); ok {
				return &sdkPin{version: v}, rel, nil
			}
			commit := "branch " + p.State.Branch
			if p.State.Branch == "" {
				commit = "revision " + p.State.Revision
			}
			return &sdkPin{commit: commit}, rel, nil
		}
	}
	return nil, "", nil
}

// sdkRequirementIn reads the project's dependency rule: an Xcode project's
// XCRemoteSwiftPackageReference, or a Swift package's `.package(url:)`.
func sdkRequirementIn(projectRoot string) (*sdkRequirement, error) {
	for _, pbxproj := range projectFiles(projectRoot) {
		blob, err := os.ReadFile(pbxproj)
		if err != nil {
			return nil, err
		}
		for _, m := range remotePackageReference.FindAllStringSubmatch(string(blob), -1) {
			if isSDKRepository(m[1]) {
				return pbxprojRequirement(m[2])
			}
		}
	}
	if blob, err := os.ReadFile(filepath.Join(projectRoot, "Package.swift")); err == nil {
		return manifestRequirement(string(blob))
	}
	return nil, nil
}

// projectFiles lists the project.pbxproj files that describe this app: every
// Xcode project at the checkout root, and every project a root WORKSPACE names.
// The second half is the common layout where the workspace sits at the root and
// the project in a subdirectory (App/App.xcodeproj) — a checkout hasAppleProject
// already accepts, and one whose only requirement lives below the root.
func projectFiles(projectRoot string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(project string) {
		pbxproj := filepath.Join(project, "project.pbxproj")
		if !seen[pbxproj] && isRegularFile(pbxproj) {
			seen[pbxproj] = true
			out = append(out, pbxproj)
		}
	}
	projects, _ := filepath.Glob(filepath.Join(projectRoot, "*.xcodeproj"))
	for _, project := range projects {
		add(project)
	}
	workspaces, _ := filepath.Glob(filepath.Join(projectRoot, "*.xcworkspace", "contents.xcworkspacedata"))
	for _, data := range workspaces {
		blob, err := os.ReadFile(data)
		if err != nil {
			continue
		}
		for _, m := range workspaceProjectRef.FindAllStringSubmatch(string(blob), -1) {
			add(filepath.Join(projectRoot, filepath.FromSlash(m[1])))
		}
	}
	return out
}

// workspaceProjectRef matches a workspace's `<FileRef location = "group:…xcodeproj">`.
// `group:` paths are relative to the workspace's own directory, which is the
// checkout root for a workspace found there.
var workspaceProjectRef = regexp.MustCompile(`location\s*=\s*"group:([^"]+\.xcodeproj)"`)

// remotePackageReference matches one XCRemoteSwiftPackageReference object.
// Xcode writes `isa` first and every other key in alphabetical order, so the
// URL always precedes the requirement block.
var remotePackageReference = regexp.MustCompile(
	`(?s)isa = XCRemoteSwiftPackageReference;\s*repositoryURL = "?([^";\n]+)"?;\s*requirement = \{(.*?)\};`)

var pbxprojField = regexp.MustCompile(`(\w+) = "?([^";\n]*)"?;`)

func pbxprojRequirement(block string) (*sdkRequirement, error) {
	fields := map[string]string{}
	for _, m := range pbxprojField.FindAllStringSubmatch(block, -1) {
		fields[m[1]] = m[2]
	}
	version := func(key string) (sdkVersion, error) {
		v, ok := parseSDKVersion(fields[key])
		if !ok {
			return sdkVersion{}, fmt.Errorf("the project's palbackend-ios requirement has an unreadable %s %q", key, fields[key])
		}
		return v, nil
	}
	var r sdkRequirement
	switch kind := fields["kind"]; kind {
	case "upToNextMajorVersion", "upToNextMinorVersion":
		v, err := version("minimumVersion")
		if err != nil {
			return nil, err
		}
		r = upToNextMajor(v)
		if kind == "upToNextMinorVersion" {
			r = upToNextMinor(v)
		}
	case "exactVersion":
		v, err := version("version")
		if err != nil {
			return nil, err
		}
		r = exactly(v)
	case "versionRange":
		lo, err := version("minimumVersion")
		if err != nil {
			return nil, err
		}
		hi, err := version("maximumVersion")
		if err != nil {
			return nil, err
		}
		r = sdkRequirement{rule: lo.String() + "..<" + hi.String(), lower: lo, upper: hi}
	case "branch":
		r = sdkRequirement{rule: "branch " + fields["branch"], commit: "branch " + fields["branch"]}
	case "revision":
		r = sdkRequirement{rule: "revision " + fields["revision"], commit: "revision " + fields["revision"]}
	default:
		return nil, fmt.Errorf("the project's palbackend-ios requirement has an unknown kind %q", kind)
	}
	return &r, nil
}

// manifestURLDependency finds `.package(url: "<url>"` in a Package.swift; the
// rule is read from what follows it, up to the call's closing parenthesis.
var manifestURLDependency = regexp.MustCompile(`\.package\(\s*(?:name:\s*"[^"]*"\s*,\s*)?url:\s*"([^"]+)"`)

var (
	quotedString = regexp.MustCompile(`"(?:[^"\\\n]|\\.)*"`)
	placeholder  = regexp.MustCompile("\"\x00\\d+\"")
)

var manifestRules = []struct {
	pattern *regexp.Regexp
	build   func(m []string) (sdkRequirement, bool)
}{
	{regexp.MustCompile(`^\s*,\s*(?:from:|\.upToNextMajor\(\s*from:)\s*"([^"]+)"`), func(m []string) (sdkRequirement, bool) {
		v, ok := parseSDKVersion(m[1])
		return upToNextMajor(v), ok
	}},
	{regexp.MustCompile(`^\s*,\s*\.upToNextMinor\(\s*from:\s*"([^"]+)"`), func(m []string) (sdkRequirement, bool) {
		v, ok := parseSDKVersion(m[1])
		return upToNextMinor(v), ok
	}},
	{regexp.MustCompile(`^\s*,\s*(?:exact:|\.exact\()\s*"([^"]+)"`), func(m []string) (sdkRequirement, bool) {
		v, ok := parseSDKVersion(m[1])
		return exactly(v), ok
	}},
	{regexp.MustCompile(`^\s*,\s*"([^"]+)"\s*\.\.<\s*"([^"]+)"`), func(m []string) (sdkRequirement, bool) {
		lo, ok1 := parseSDKVersion(m[1])
		hi, ok2 := parseSDKVersion(m[2])
		return sdkRequirement{rule: lo.String() + "..<" + hi.String(), lower: lo, upper: hi}, ok1 && ok2
	}},
	{regexp.MustCompile(`^\s*,\s*"([^"]+)"\s*\.\.\.\s*"([^"]+)"`), func(m []string) (sdkRequirement, bool) {
		lo, ok1 := parseSDKVersion(m[1])
		hi, ok2 := parseSDKVersion(m[2])
		return sdkRequirement{rule: lo.String() + "..." + hi.String(), lower: lo,
			upper: sdkVersion{hi.major, hi.minor, hi.patch + 1}}, ok1 && ok2
	}},
	{regexp.MustCompile(`^\s*,\s*(branch|revision):\s*"([^"]+)"`), func(m []string) (sdkRequirement, bool) {
		return sdkRequirement{rule: m[1] + " " + m[2], commit: m[1] + " " + m[2]}, true
	}},
}

// swiftComment matches a `//` line comment or a `/* */` block. A dependency
// someone commented out is not a dependency; read as one, it would name a
// version the app does not build.
var swiftComment = regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*`)

func manifestRequirement(manifest string) (*sdkRequirement, error) {
	// URLs contain `//`, so they are lifted out before comments are cut and
	// put back after.
	var urls []string
	manifest = quotedString.ReplaceAllStringFunc(manifest, func(s string) string {
		urls = append(urls, s)
		return fmt.Sprintf("\"\x00%d\"", len(urls)-1)
	})
	manifest = swiftComment.ReplaceAllString(manifest, "")
	manifest = placeholder.ReplaceAllStringFunc(manifest, func(s string) string {
		var i int
		_, _ = fmt.Sscanf(s, "\"\x00%d\"", &i)
		return urls[i]
	})
	for _, loc := range manifestURLDependency.FindAllStringSubmatchIndex(manifest, -1) {
		if !isSDKRepository(manifest[loc[2]:loc[3]]) {
			continue
		}
		rest := manifest[loc[1]:]
		for _, rule := range manifestRules {
			if m := rule.pattern.FindStringSubmatch(rest); m != nil {
				if r, ok := rule.build(m); ok {
					return &r, nil
				}
			}
		}
		end := strings.IndexByte(rest, '\n')
		if end < 0 {
			end = len(rest)
		}
		return nil, fmt.Errorf("this project's Package.swift depends on palbackend-ios with a rule this CLI cannot read: %q", strings.TrimSpace(rest[:end]))
	}
	return nil, nil
}

// sdkReleasesURL lists the public repo's refs over git's smart HTTP protocol —
// what `git ls-remote` reads — so a fresh resolve needs neither git nor a GitHub
// API token, and is not subject to the API's anonymous rate limit. A var only so
// tests can serve their own tag list.
var sdkReleasesURL = sdkRepository + ".git/info/refs?service=git-upload-pack"

// A ref name ends at the line's newline, or — on the first ref of the
// advertisement — at the NUL that precedes the server's capability list.
var releaseTag = regexp.MustCompile(`refs/tags/(v?\d+\.\d+\.\d+)(?:\^\{\})?[\x00\n]`)

func listSDKReleases() ([]sdkVersion, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(sdkReleasesURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", sdkReleasesURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	seen := map[sdkVersion]bool{}
	var out []sdkVersion
	for _, m := range releaseTag.FindAllStringSubmatch(string(body), -1) {
		if v, ok := parseSDKVersion(m[1]); ok && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, nil
}
