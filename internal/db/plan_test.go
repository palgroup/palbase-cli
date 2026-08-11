package db

import (
	"bytes"
	"strings"
	"testing"
)

// An in-sync schema is the answer this command gives most of the time, and it has to
// be unmistakable — a person who reads it stops looking.
func TestRenderPlan_SaysInSyncForAnEmptyPlan(t *testing.T) {
	var out bytes.Buffer
	renderPlan(&out, schemaPlan{SQL: "", FromFingerprint: strings.Repeat("a", 64), ToFingerprint: strings.Repeat("a", 64)})

	got := out.String()
	if !strings.Contains(got, "in sync") {
		t.Fatalf("an empty plan did not say so:\n%s", got)
	}
	if strings.Contains(got, "DROP") {
		t.Fatalf("an empty plan printed changes:\n%s", got)
	}
}

// The row count is the decision. "drops notes" and "drops notes, 431 rows" are not
// the same choice, and a plan that showed only the first would be approved by
// someone who never learned the second.
func TestRenderPlan_PutsTheRowCountNextToEveryDestructiveLine(t *testing.T) {
	var out bytes.Buffer
	renderPlan(&out, schemaPlan{
		SQL: `drop table "public"."notes";`,
		Destructive: &planDestructive{
			HasDataLoss: true,
			Drops:       []planDrop{{Kind: "table", Table: "notes", RowCount: 431}},
		},
		Findings: []planFinding{{Level: "error", Code: "destructive_drop", Message: "table notes is dropped, destroying 431 row(s)"}},
	})

	got := out.String()
	if !strings.Contains(got, "431") {
		t.Fatalf("the row count is missing:\n%s", got)
	}
	if !strings.Contains(got, "notes") {
		t.Fatalf("the table is missing:\n%s", got)
	}
	// The destructive summary must appear BEFORE the SQL: a reader who stops at the
	// first screen has to have seen it.
	if strings.Index(got, "431") > strings.Index(got, "drop table") {
		t.Fatalf("the destructive summary came after the SQL:\n%s", got)
	}
}

// A column drop destroys the values that are actually there, not one per row. Showing
// the table's row count for a column that is almost entirely NULL would overstate the
// loss, and an overstated warning is one people learn to skip.
func TestRenderPlan_CountsOnlyTheValuesAColumnDropDestroys(t *testing.T) {
	var out bytes.Buffer
	renderPlan(&out, schemaPlan{
		SQL: `alter table "public"."todos" drop column legacy;`,
		Destructive: &planDestructive{
			Drops: []planDrop{{Kind: "column", Table: "todos", Column: "legacy", RowCount: 100000, NonNull: 12}},
		},
	})

	got := out.String()
	if !strings.Contains(got, "12 row(s)") {
		t.Fatalf("the non-null count is missing:\n%s", got)
	}
	if strings.Contains(got, "100000") {
		t.Fatalf("the table's row count was shown instead of the values actually lost:\n%s", got)
	}
}

// The fingerprints are what make the plan verifiable at apply time, so they are shown
// — short enough to compare at a glance, but shown.
func TestRenderPlan_ShowsBothFingerprints(t *testing.T) {
	var out bytes.Buffer
	from, to := strings.Repeat("a", 64), strings.Repeat("b", 64)
	renderPlan(&out, schemaPlan{SQL: "alter table x add column y int;", FromFingerprint: from, ToFingerprint: to})

	got := out.String()
	if !strings.Contains(got, from[:12]) || !strings.Contains(got, to[:12]) {
		t.Fatalf("a fingerprint is missing:\n%s", got)
	}
}

// A plan carrying an error must say that it needs approval. Printing the same output
// for a routine change and for one that destroys data is how a destructive plan gets
// waved through.
func TestRenderPlan_SaysWhenAPlanNeedsApproval(t *testing.T) {
	var withError bytes.Buffer
	renderPlan(&withError, schemaPlan{
		SQL:      `drop table "public"."notes";`,
		Findings: []planFinding{{Level: "error", Code: "destructive_drop", Message: "table notes is dropped, destroying 431 row(s)"}},
	})
	if !strings.Contains(withError.String(), "approval") {
		t.Fatalf("a plan with an error did not say it needs approval:\n%s", withError.String())
	}

	var warningOnly bytes.Buffer
	renderPlan(&warningOnly, schemaPlan{
		SQL:      "alter table x add column y int;",
		Findings: []planFinding{{Level: "warning", Code: "empty_drop", Message: "column x.z is dropped; it holds no rows"}},
	})
	if strings.Contains(warningOnly.String(), "approval") {
		t.Fatalf("a warning-only plan was made to look like it needs approval:\n%s", warningOnly.String())
	}
}

// hasError is what the approval line branches on, and errors do not always come first.
func TestSchemaPlan_HasError(t *testing.T) {
	if (schemaPlan{}).hasError() {
		t.Error("an empty plan reported an error")
	}
	if (schemaPlan{Findings: []planFinding{{Level: "warning"}}}).hasError() {
		t.Error("a warning was read as an error")
	}
	if !(schemaPlan{Findings: []planFinding{{Level: "warning"}, {Level: "error"}}}).hasError() {
		t.Error("an error behind a warning was missed")
	}
}

// --detailed-exitcode is what CI branches on: 0 means nothing to do, 2 means there is.
// Collapsing either into 1 would make a pipeline treat "there are changes" as a crash.
func TestChangesError_CarriesTheDetailedExitCode(t *testing.T) {
	var err error = changesError{}
	coded, ok := err.(interface{ ExitCode() int })
	if !ok {
		t.Fatal("changesError does not carry an exit code; main() would report it as a failure")
	}
	if coded.ExitCode() != 2 {
		t.Fatalf("exit code is %d, want 2", coded.ExitCode())
	}
	// It must print nothing: an empty line above a meaningful status reads as a crash.
	if err.Error() != "" {
		t.Fatalf("changesError carries a message %q", err.Error())
	}
}

// Whitespace-only SQL is not a change. Treating it as one would make
// --detailed-exitcode report 2 for a schema that is in sync, and a pipeline would
// stop on nothing.
func TestSchemaPlan_EmptyIgnoresWhitespace(t *testing.T) {
	if !(schemaPlan{SQL: "\n  \n"}).empty() {
		t.Fatal("whitespace-only SQL was read as a change")
	}
	if (schemaPlan{SQL: "alter table x add column y int;"}).empty() {
		t.Fatal("a real change was read as empty")
	}
}

// A column drop that destroys NOTHING must not be rendered as destroying the whole
// table. This is the case the earlier test missed — it used a non-zero non-null
// count, so the conditional fallback never showed itself. Live on todoapp the server
// correctly said "all 168 rows are already NULL, so no data is lost" while the line
// above it read "⚠ 168 row(s)".
func TestRenderPlan_DoesNotOverstateAColumnDropThatLosesNothing(t *testing.T) {
	var out bytes.Buffer
	renderPlan(&out, schemaPlan{
		SQL: `alter table "public"."todos" drop column "uat_applied";`,
		Destructive: &planDestructive{
			Drops: []planDrop{{Kind: "column", Table: "todos", Column: "uat_applied", RowCount: 168, NonNull: 0}},
		},
		Findings: []planFinding{{Level: "warning", Code: "empty_drop", Message: "column todos.uat_applied is dropped; all 168 rows are already NULL, so no data is lost"}},
	})

	got := out.String()
	if strings.Contains(got, "168 row(s)") {
		t.Fatalf("a drop that loses nothing was rendered as destroying 168 rows:\n%s", got)
	}
	if !strings.Contains(got, "uat_applied") {
		t.Fatalf("the dropped column is missing:\n%s", got)
	}
}

// A rename-only plan has no SQL in it — the planner aligned both sides by name so
// the diff had nothing to say. Read as "empty", it prints "schema in sync" over a
// real change the user asked for. Measured live on todoapp, 2026-08-11.
func TestRenderPlan_ShowsARenameThatCarriesNoSQL(t *testing.T) {
	var out bytes.Buffer
	renderPlan(&out, schemaPlan{
		Renames: []planRename{{Table: "todos", From: "notes", To: "remarks"}},
	})

	got := out.String()
	if strings.Contains(got, "schema in sync") {
		t.Fatalf("a rename was reported as no change:\n%s", got)
	}
	if !strings.Contains(got, "todos.remarks") || !strings.Contains(got, "RENAME FROM notes") {
		t.Fatalf("the rename is not shown:\n%s", got)
	}
}

// The rename has to be visible next to the drops, because it is the reason a column
// disappearing from one name is not data loss.
func TestRenderPlan_ShowsRenamesBeforeTheRestOfThePlan(t *testing.T) {
	var out bytes.Buffer
	renderPlan(&out, schemaPlan{
		SQL:     `alter table "public"."todos" add column "done" boolean;`,
		Renames: []planRename{{Table: "todos", From: "notes", To: "remarks"}},
	})

	got := out.String()
	rename := strings.Index(got, "RENAME FROM")
	sql := strings.Index(got, "add column")
	if rename == -1 || sql == -1 {
		t.Fatalf("a line is missing (rename=%d sql=%d):\n%s", rename, sql, got)
	}
	if rename > sql {
		t.Errorf("the rename printed after the SQL it explains:\n%s", got)
	}
}

// An unchanged schema still has to read as unchanged.
func TestRenderPlan_StillReportsAnEmptyPlanAsInSync(t *testing.T) {
	var out bytes.Buffer
	renderPlan(&out, schemaPlan{})
	if !strings.Contains(out.String(), "schema in sync") {
		t.Fatalf("an empty plan was not reported as in sync:\n%s", out.String())
	}
}
