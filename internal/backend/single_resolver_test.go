package backend

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ONE RESOLVER, AND THE ALLOWLIST SHRINKS.
//
// Every verb that acts on an environment goes through `ResolveFor`. `ReadTarget`
// survives for the few places that legitimately want only the committed record,
// and this gate names them — because an exception nobody counts is an exception
// that grows.
//
// THE LIST IS A DEBT, SO IT IS WRITTEN AS ONE. `stack_spec.go` leaves it in
// T013 and `deploy.go` in T014; a gate whose exception list never shrinks is a
// gate that has stopped measuring.
func TestReadTargetHasOnlyItsDeclaredCallers(t *testing.T) {
	allowed := map[string]bool{
		// The resolver itself: it reads the committed record and asks whether a
		// local stack is up. Nothing below it can do that for it.
		"internal/backend/environments.go": true,
		// The banner's project-only line, and the refusal that carries the fix.
		"internal/backend/banner.go": true,
		// `link` remembers the previous address so a re-link need not retype it.
		"internal/backend/project_link.go": true,
		// The committed record's own reader lives here.
		"internal/backend/target.go": true,
		// `doctor` ASKS BOTH QUESTIONS ON PURPOSE. It prints what the committed
		// file says AND where a verb would act, on two lines, because a
		// diagnostic that collapses them hides the one people arrive with:
		// "the file says todoapp, so why did my push refuse?"
		"cmd/palbase/doctor.go": true,
		// SHRINKING: these two move to ResolveFor in T013 and T014.
		"internal/backend/stack_spec.go": true,
		"internal/backend/deploy.go":     true,
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if !strings.Contains(string(body), "ReadTarget()") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if !allowed[rel] {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("these read the committed target directly instead of resolving an environment:\n  %s\n\n"+
			"a verb that reads ReadTarget acts on whatever the file says and cannot honour --env; "+
			"use backend.ResolveFor(cmd)", strings.Join(offenders, "\n  "))
	}
}
