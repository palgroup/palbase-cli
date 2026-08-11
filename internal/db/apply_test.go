package db

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Consent has to name what is at stake. A plan that destroys rows and a plan that
// adds a nullable column must not read the same at the moment someone types y —
// that is where the decision actually happens.
func TestConfirm_NamesTheRowsAtStake(t *testing.T) {
	destructive := schemaPlan{
		SQL:      `drop table "public"."recipes";`,
		Findings: []planFinding{{Level: "error", Code: "destructive_drop", Message: "table recipes is dropped, destroying 7 row(s)"}},
		Destructive: &planDestructive{
			HasDataLoss: true,
			Drops:       []planDrop{{Kind: "table", Table: "recipes", RowCount: 7}},
		},
	}

	var out bytes.Buffer
	if _, err := confirm(strings.NewReader("n\n"), &out, destructive); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !strings.Contains(out.String(), "7 row(s)") {
		t.Fatalf("the prompt does not say what is destroyed:\n%s", out.String())
	}
}

// A column drop destroys the values that are there, not one per row.
func TestConfirm_CountsOnlyTheValuesAColumnDropDestroys(t *testing.T) {
	plan := schemaPlan{
		SQL:      `alter table "public"."todos" drop column legacy;`,
		Findings: []planFinding{{Level: "error", Code: "destructive_drop", Message: "12 rows"}},
		Destructive: &planDestructive{
			Drops: []planDrop{{Kind: "column", Table: "todos", Column: "legacy", RowCount: 100000, NonNull: 12}},
		},
	}

	var out bytes.Buffer
	if _, err := confirm(strings.NewReader("n\n"), &out, plan); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !strings.Contains(out.String(), "12 row(s)") {
		t.Fatalf("the prompt does not use the non-null count:\n%s", out.String())
	}
	if strings.Contains(out.String(), "100000") {
		t.Fatalf("the prompt overstates the loss with the table's row count:\n%s", out.String())
	}
}

// Anything that is not an explicit yes is a no — including an empty line, a closed
// stdin, and the word "no". A prompt that defaulted to yes would apply a plan to a
// database because someone pressed return.
func TestConfirm_TreatsAnythingButYesAsNo(t *testing.T) {
	plan := schemaPlan{SQL: "alter table x add column y int;"}

	for _, answer := range []string{"", "\n", "n\n", "no\n", "N\n", "maybe\n", "yes please\n"} {
		var out bytes.Buffer
		ok, err := confirm(strings.NewReader(answer), &out, plan)
		if err != nil {
			t.Fatalf("confirm(%q): %v", answer, err)
		}
		if ok {
			t.Errorf("answer %q was taken as consent", answer)
		}
	}

	for _, answer := range []string{"y\n", "Y\n", "yes\n", "  y  \n"} {
		var out bytes.Buffer
		ok, err := confirm(strings.NewReader(answer), &out, plan)
		if err != nil {
			t.Fatalf("confirm(%q): %v", answer, err)
		}
		if !ok {
			t.Errorf("answer %q was not taken as consent", answer)
		}
	}
}

// The plan id names this apply in the environment's history, so two applies must
// never share one — the ledger refuses a duplicate and the second would fail.
func TestNewPlanID_IsRecognisableAndCarriesTheTarget(t *testing.T) {
	id := newPlanID(strings.Repeat("a", 64))

	if !strings.HasPrefix(id, "pl_") {
		t.Fatalf("plan id %q is not recognisable as one", id)
	}
	if !strings.Contains(id, "aaaaaaaa") {
		t.Fatalf("plan id %q does not carry the target fingerprint — a history entry could not be recognised without opening it", id)
	}
	// A short or absent fingerprint must not panic or produce a bare prefix.
	if short := newPlanID("abc"); !strings.HasPrefix(short, "pl_") || len(short) < 10 {
		t.Fatalf("a short fingerprint produced %q", short)
	}
}

// The CLI decodes the server's plan into its own struct and then marshals THAT back
// as the apply's input. Every field the struct does not know is silently dropped in
// between — which is how a rename could be planned, shown, and then not applied.
// This holds the round trip against the server's own wire shape.
func TestSchemaPlan_RoundTripsRenamesBackToApply(t *testing.T) {
	// The server's wire shape, verbatim from internal/schema.SchemaPlan's json tags.
	fromServer := `{"sql":"","renames":[{"table":"todos","from":"notes","to":"remarks"}],` +
		`"fromFingerprint":"aa","toFingerprint":"bb","findings":[],"destructive":null}`

	var plan schemaPlan
	if err := json.Unmarshal([]byte(fromServer), &plan); err != nil {
		t.Fatalf("decode the server's plan: %v", err)
	}
	if len(plan.Renames) != 1 || plan.Renames[0].To != "remarks" || plan.Renames[0].From != "notes" {
		t.Fatalf("the rename did not survive decoding: %+v", plan.Renames)
	}
	if plan.empty() {
		t.Fatal("a plan that renames a column read as empty — apply would never run")
	}

	backToServer, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if !strings.Contains(string(backToServer), `"renames":[{"table":"todos","from":"notes","to":"remarks"}]`) {
		t.Fatalf("the rename was dropped on the way to apply:\n%s", backToServer)
	}
}
