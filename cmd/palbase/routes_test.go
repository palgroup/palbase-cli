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
func servedCloudRoutes(dir string) ([]*regexp.Regexp, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*regexp.Regexp
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
		if !strings.HasPrefix(base, "/v1/cloud") {
			continue
		}
		for _, m := range routeMethod.FindAllStringSubmatch(text, -1) {
			full := strings.TrimRight(base+m[1], "/")
			quoted := regexp.QuoteMeta(full)
			// A path parameter matches exactly one segment.
			quoted = pathParam.ReplaceAllStringFunc(quoted, func(string) string { return `[^/]+` })
			quoted = strings.ReplaceAll(quoted, `\{`, `{`)
			quoted = strings.ReplaceAll(quoted, `\}`, `}`)
			quoted = pathParam.ReplaceAllString(quoted, `[^/]+`)
			out = append(out, regexp.MustCompile(`^`+quoted+`$`))
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
			case strings.HasPrefix(value, "/v1/cloud/"):
				found = append(found, routeLiteral{where: where, path: strings.TrimRight(value, "/")})
			case strings.HasPrefix(value, "/v1/") || strings.HasPrefix(value, "/api/v2/"):
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
func servedBy(served []*regexp.Regexp, literal string) bool {
	for _, re := range served {
		if re.MatchString(literal) {
			return true
		}
		// `"/v1/cloud/projects/" + ref` reaches the AST as the static prefix.
		if strings.HasSuffix(literal, "/") || !strings.Contains(literal[1:], "/") {
			continue
		}
		if re.MatchString(literal + "/x") {
			return true
		}
	}
	return false
}
