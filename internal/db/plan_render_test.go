package db

// plan_render_test.go — the plan says what the push will say, before the push.

import (
	"bytes"
	"strings"
	"testing"
)

// FR-014: a plan that would be refused says so, where the developer is standing.
//
// `db plan` used to describe the drop and stop there. The rule that refuses it
// lives on push, so it was learned after `apply` had already passed locally and
// the code had already run — the one moment the fix is most expensive. The
// stack now sends the verdict with the plan; this is the render that shows it,
// the way out included, because a refusal without one is just a wall.
func TestRenderPlanShowsTheRefusalThePushWouldGive(t *testing.T) {
	var out bytes.Buffer
	renderPlan(&out, schemaPlan{
		Changes: []string{"drop column todos.note"},
		Destructive: []destructiveChange{{
			Kind: "column", Table: "todos", Column: "note", Rows: 12, NonNull: 9,
		}},
		Incompatible: []string{
			"todos.note would be dropped while the running release still declares it — " +
				"mark it `.ignored()` and deploy once, then drop it",
		},
	})
	got := out.String()

	if !strings.Contains(got, "push") {
		t.Errorf("the plan never says the push would refuse this:\n%s", got)
	}
	for _, want := range []string{"todos.note", "ignored"} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal does not name %q — the way out is the only part that helps:\n%s", want, got)
		}
	}
}

// And silence when there is nothing to refuse: a heading with no lines under it
// reads as a warning about a change the reader cannot find.
func TestRenderPlanSaysNothingAboutRefusalsWhenThereAreNone(t *testing.T) {
	var out bytes.Buffer
	renderPlan(&out, schemaPlan{
		Changes:     []string{"add column todos.note"},
		Destructive: []destructiveChange{},
	})
	got := out.String()

	for _, absent := range []string{"push", "refus", "reddedil"} {
		if strings.Contains(strings.ToLower(got), absent) {
			t.Errorf("a plan with nothing to refuse mentions %q:\n%s", absent, got)
		}
	}
}
