package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// A ROUTE LITERAL THE SERVER DOES NOT SERVE IS A COMMAND THAT CANNOT WORK.
//
// This gate was designed in Artım 1 and DELIBERATELY HELD BACK: on the day it
// was written it would have been born red, because `selection/resolve.go:192`
// still called `GET /api/v2/projects` — a route the v2 cloud does not serve. A
// gate that is red on the day it lands is a gate somebody turns off, so it waited
// for the work that makes it green (FR-015, done in T010). Its number, 008, was
// left blank in Artım 1's plan so the wait stayed visible.
//
// WHAT IT MEASURES, precisely: every `/v1/cloud/...` string literal in this
// CLI's production code must match a route the cloud controllers declare. The
// cloud plane is the one whose routes are readable from source here; the tenant
// plane (`/v1/management/...`) and Studio (`/api/v2/...`) are served elsewhere
// and are NAMED as unmeasured rather than silently skipped — a gate that quietly
// ignores two thirds of its subject reports a silence it did not earn.
func TestEveryCloudRouteLiteralIsServed(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	// cmd/palbase → sdk/cli → sdk → palbase
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	controllers := filepath.Join(repoRoot, "..", "..", "v2-cloud", "platform", "server", "controllers")

	served, err := servedCloudRoutes(controllers)
	if err != nil {
		// The cloud tree is not beside this checkout — every CI runner clones the
		// CLI alone. Its neighbour TestTheVendoredComposeMatchesTheRepository
		// skips for the same reason: this pin is only measurable where both trees
		// are, which is the machine where the routes are actually edited.
		t.Skipf("the palbase repository is not beside this checkout: %v", err)
	}
	if len(served) == 0 {
		t.Fatalf("read %s but found no routes — the extraction is wrong, not the code", controllers)
	}

	literals, unmeasured, err := cloudRouteLiterals(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	var missing []string
	for _, lit := range literals {
		if !servedBy(served, lit.path) {
			missing = append(missing, lit.where+" calls "+lit.path)
		}
	}
	if len(missing) > 0 {
		t.Errorf("these routes are called but not served by %s:\n%s\n\n"+
			"a literal nobody serves is a command that cannot work, and it fails at the "+
			"user rather than here", filepath.Base(controllers), strings.Join(missing, "\n"))
	}

	// SAY WHAT WAS NOT MEASURED. Artım 1's N-2 lesson: a gate that cannot read
	// something must name it, because silence reads like a pass.
	if len(unmeasured) > 0 {
		t.Logf("not measured by this gate (%d literal(s) on planes served elsewhere):\n%s",
			len(unmeasured), strings.Join(unmeasured, "\n"))
	}
}

// routeLiteral is one call site's path.
type routeLiteral struct{ where, path string }

var (
	controllerBase = regexp.MustCompile(`@Controller\("([^"]+)"`)
	// The closing paren is NOT part of the match: a decorator can carry options —
	// `@Get("/config", { auth: false })` — and demanding `")` right after the
	// path silently dropped every route that has any. Measured: /me and /config
	// were reported as unserved while sitting in cloud.controller.ts.
	routeMethod = regexp.MustCompile(`@(?:Get|Post|Put|Delete|Patch)\("([^"]*)"`)
	pathParam   = regexp.MustCompile(`\{[^}]+\}`)
)

// servedCloudRoutes reads the cloud controllers and returns their routes as
// regexps, with `{param}` standing for one segment.
func servedCloudRoutes(dir string) ([]servedRoute, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []servedRoute
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") || strings.HasSuffix(e.Name(), ".test.ts") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		text := string(body)
		base := ""
		if m := controllerBase.FindStringSubmatch(text); m != nil {
			base = m[1]
		}
		// TWO PLANES, BOTH DECLARED IN THIS TREE. The gate read `/v1/cloud`
		// only and named `/api/v2` as unmeasured — but `cli.controller.ts`
		// declares that one right beside it, so "served elsewhere" was true of
		// the tenant plane and merely unexamined here.
		if !strings.HasPrefix(base, "/v1/cloud") && !strings.HasPrefix(base, "/api/v2") {
			continue
		}
		for _, m := range routeMethod.FindAllStringSubmatch(text, -1) {
			full := strings.TrimRight(base+m[1], "/")
			// QUOTE THE LITERAL PARTS, THEN JOIN WITH THE PARAMETER PATTERN.
			//
			// This used to QuoteMeta the whole path and then try to undo the
			// escaping around `{param}`. It could not: QuoteMeta turns `{` into
			// `\{`, the parameter regex then matched starting AT the brace and
			// left the backslash behind, so the compiled pattern contained
			// `\[^/]+` — a literal `[`. Every parameterised route therefore
			// matched nothing, and the gate reported seven live routes as
			// unserved the moment it was pointed at a plane that has any.
			parts := pathParam.Split(full, -1)
			for i := range parts {
				parts[i] = regexp.QuoteMeta(parts[i])
			}
			out = append(out, servedRoute{
				pattern: regexp.MustCompile(`^` + strings.Join(parts, `[^/]+`) + `$`),
				raw:     full,
			})
		}
	}
	return out, nil
}

// cloudRouteLiterals collects `/v1/cloud/...` string literals from production
// Go code, with AST rather than a regex: Artım 1 measured what a text scan does
// to raw strings, and it miscounted.
func cloudRouteLiterals(root string) (found []routeLiteral, unmeasured []string, err error) {
	fset := token.NewFileSet()
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/selectiontest/") {
			return nil
		}
		parsed, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, isLit := n.(*ast.BasicLit)
			if !isLit || lit.Kind != token.STRING {
				return true
			}
			value, uerr := strconv.Unquote(lit.Value)
			if uerr != nil || !strings.HasPrefix(value, "/") {
				return true
			}
			rel, _ := filepath.Rel(root, path)
			where := rel + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line)
			switch {
			case strings.HasPrefix(value, "/v1/cloud/"), strings.HasPrefix(value, "/api/v2/"):
				found = append(found, routeLiteral{where: where, path: strings.TrimRight(value, "/")})
			case strings.HasPrefix(value, "/v1/"):
				unmeasured = append(unmeasured, "  "+where+" "+value)
			}
			return true
		})
		return nil
	})
	return found, unmeasured, err
}

// servedBy reports whether any declared route matches this literal. A literal
// built by concatenation ends at the last static segment, so a prefix match
// against a declared route counts: the CLI appends the id at runtime.
func servedBy(served []servedRoute, literal string) bool {
	for _, r := range served {
		if r.pattern.MatchString(literal) {
			return true
		}
		// A CONCATENATED LITERAL ENDS AT ITS LAST STATIC SEGMENT.
		//
		// `"/api/v2/apps/" + id + "/bindings"` reaches the AST as `/api/v2/apps`
		// — the id and everything after it are appended at run time. Appending
		// ONE segment and re-matching only covered routes whose parameter was
		// last, so `/apps/{appId}/bindings` and `/environments/{ref}/openapi`
		// were reported unserved while being served all along.
		//
		// The honest test is the declared route's own text: a literal that is a
		// path-prefix of it, at a segment boundary, is the static head of a call
		// to it.
		if strings.HasPrefix(r.raw, strings.TrimRight(literal, "/")+"/") {
			return true
		}
	}
	return false
}

// servedRoute is one declared route: the pattern a full path must match, and the
// text it was declared with — which is what a concatenated literal is a head of.
type servedRoute struct {
	pattern *regexp.Regexp
	raw     string
}

// THE PATTERN BUILDER IS MEASURED DIRECTLY, because the corpus cannot measure it.
//
// A parameterised route used to compile to a pattern that matched NOTHING:
// QuoteMeta turned `{` into `\{`, the parameter regex matched starting at the
// brace and left the backslash behind, so `/api/v2/projects/{projectId}` became
// `^/api/v2/projects/\[^/]+$` — a literal `[`. Every such route was reported
// unserved the moment this gate was pointed at a plane that has any.
//
// It is asserted here rather than through the corpus because the prefix rule
// (a concatenated literal is the static head of a declared route) MASKS the bug:
// with both in place, `/api/v2/projects` is served either way. Breaking the
// builder and re-running the gate stays green, so the gate alone cannot tell
// anybody the builder is broken.
func TestARouteWithAParameterCompilesToAPatternThatMatches(t *testing.T) {
	dir := t.TempDir()
	body := `@Controller("/api/v2", { auth: { required: true } })
export class Fake {
  @Get("/projects/{projectId}") one() {}
  @Get("/projects/{projectId}/environments/{ref}/deployments") two() {}
  @Get("/health") three() {}
}
`
	if err := os.WriteFile(filepath.Join(dir, "fake.controller.ts"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	served, err := servedCloudRoutes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(served) != 3 {
		t.Fatalf("read %d routes, want 3 — the extraction is wrong", len(served))
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/api/v2/projects/proj_123", true},
		{"/api/v2/projects/proj_123/environments/main/deployments", true},
		{"/api/v2/health", true},
		// One segment is one segment: a parameter must not swallow a slash.
		{"/api/v2/projects/proj_123/environments", false},
		{"/api/v2/projects", false},
	} {
		got := false
		for _, r := range served {
			if r.pattern.MatchString(tc.path) {
				got = true
				break
			}
		}
		if got != tc.want {
			t.Errorf("%s matched=%v, want %v — patterns: %v", tc.path, got, tc.want, served)
		}
	}
}
