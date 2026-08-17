package db

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// scratchCheckout puts the test in an empty directory with an empty HOME, so the
// credential store and the .palbase files are the test's own.
func scratchCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv(backend.AccessTokenEnv, "")
	return dir
}

// runningStackAt writes the two files `palbase start` writes: the local target,
// and a credential for it.
func runningStackAt(t *testing.T, url string) {
	t.Helper()
	if err := os.MkdirAll(".palbase", 0o755); err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(backend.Target{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(".palbase", "local.json"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := backend.StoreCredential(url, "a-credential"); err != nil {
		t.Fatal(err)
	}
}

func writeSchema(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll("db", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("db", "schema.ts"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// run executes one db verb the way the binary does, and returns stdout.
func run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := Cmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// TestEveryVerbRefusesWithoutARunningStack is FR-026 — and the refusal has to
// carry BOTH ways out, because the person who hit it wanted one of two different
// things. Naming only `palbase start` would send somebody who meant production
// to a local database and let them watch it succeed.
func TestEveryVerbRefusesWithoutARunningStack(t *testing.T) {
	for _, verb := range [][]string{{"plan"}, {"apply"}, {"query", "select 1"}} {
		t.Run(verb[0], func(t *testing.T) {
			scratchCheckout(t)
			writeSchema(t, "export default {}")

			_, _, err := run(t, verb...)
			if err == nil {
				t.Fatalf("`db %s` ran with no stack in front of it", verb[0])
			}
			for _, want := range []string{"palbase start", "palbase push"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not offer %q: %v", want, err)
				}
			}
		})
	}
}

// TestALinkedCloudProjectIsNotATarget is FR-025 from the other side: a checkout
// linked to a cloud environment has a perfectly good target, and `db` still
// refuses — because applying a schema to production through the group meant for
// experiments is exactly the accident this design removes.
func TestALinkedCloudProjectIsNotATarget(t *testing.T) {
	scratchCheckout(t)
	writeSchema(t, "export default {}")
	if err := backend.WriteTarget(backend.Target{Project: "todoapp", Env: "production"}); err != nil {
		t.Fatal(err)
	}

	_, _, err := run(t, "apply")
	if err == nil {
		t.Fatal("`db apply` acted on a cloud environment")
	}
	if !strings.Contains(err.Error(), "palbase push") {
		t.Errorf("the refusal does not name the verb that DOES reach it: %v", err)
	}
}

// TestPlanGoesToTheStackInFrontOfYou: the request lands on the local address,
// carries the credential, and ships db/schema.ts verbatim.
func TestPlanGoesToTheStackInFrontOfYou(t *testing.T) {
	var seen atomic.Int32
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"in_sync":false,"changes":["create table notes"],"destructive":[]}`))
	}))
	defer srv.Close()

	scratchCheckout(t)
	runningStackAt(t, srv.URL)
	writeSchema(t, "export default defineSchema({ tables: {} });")

	out, banner, err := run(t, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if seen.Load() != 1 {
		t.Fatalf("the stack saw %d requests", seen.Load())
	}
	if gotPath != "/v1/management/schema/plan" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer a-credential" {
		t.Errorf("authorization = %q", gotAuth)
	}
	if !strings.Contains(gotBody, "defineSchema") {
		t.Errorf("the schema source did not travel: %q", gotBody)
	}
	if !strings.Contains(out, "create table notes") {
		t.Errorf("the plan was not shown:\n%s", out)
	}
	if !strings.Contains(banner, srv.URL) {
		t.Errorf("the banner does not name where it acted: %q", banner)
	}
}

// TestApplyRefusesDestructiveChangesWithTheirCounts: the 409 body IS the plan, so
// the person deciding sees "41908 values" rather than a sentence saying a number
// exists somewhere.
func TestApplyRefusesDestructiveChangesWithTheirCounts(t *testing.T) {
	var approveSeen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		approveSeen = r.URL.Query().Get("approve")
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"in_sync":false,"changes":[],"destructive":[{"kind":"column","table":"notes","column":"body","rows":50000,"non_null":41908}]}`))
	}))
	defer srv.Close()

	scratchCheckout(t)
	runningStackAt(t, srv.URL)
	writeSchema(t, "export default defineSchema({ tables: {} });")

	out, _, err := run(t, "apply")
	if err == nil {
		t.Fatal("a destructive apply was not refused")
	}
	if approveSeen != "" {
		t.Errorf("apply asked for approval nobody gave: approve=%q", approveSeen)
	}
	if !strings.Contains(out, "41908") || !strings.Contains(out, "notes.body") {
		t.Errorf("the refusal does not say what would be lost:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--approve") {
		t.Errorf("the refusal does not name the way through: %v", err)
	}
}

// TestApproveIsCarriedToTheStack: the flag is not advice, it is the query
// parameter the stack gates on.
func TestApproveIsCarriedToTheStack(t *testing.T) {
	var approveSeen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		approveSeen = r.URL.Query().Get("approve")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"changed":true,"summary":["drop column notes.body"]}`))
	}))
	defer srv.Close()

	scratchCheckout(t)
	runningStackAt(t, srv.URL)
	writeSchema(t, "export default defineSchema({ tables: {} });")

	out, _, err := run(t, "apply", "--approve")
	if err != nil {
		t.Fatalf("apply --approve: %v", err)
	}
	if approveSeen != "true" {
		t.Errorf("approve=%q reached the stack", approveSeen)
	}
	if !strings.Contains(out, "drop column notes.body") {
		t.Errorf("what changed was not reported:\n%s", out)
	}
}

// TestQueryPrintsTheRows is FR-027, including the two things a naive renderer
// gets wrong: NULL must not look like an empty string, and a truncated result
// must say so.
func TestQueryPrintsTheRows(t *testing.T) {
	var gotSQL struct {
		SQL   string `json:"sql"`
		Limit int    `json:"limit"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotSQL)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"columns":["id","title","done"],"rows":[[1,"write it",null]],"row_count":1,"truncated":true,"duration_ms":3}`))
	}))
	defer srv.Close()

	scratchCheckout(t)
	runningStackAt(t, srv.URL)

	out, _, err := run(t, "query", "select * from todos")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if gotSQL.SQL != "select * from todos" {
		t.Errorf("statement = %q", gotSQL.SQL)
	}
	for _, want := range []string{"id", "title", "done", "write it", "NULL", "1 row(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "there are more") {
		t.Errorf("a truncated result did not say so:\n%s", out)
	}
	// An integer must not come back as 1e+00 — a value nobody can paste into a
	// WHERE clause is a value the console did not really show.
	if strings.Contains(out, "e+") {
		t.Errorf("a number was rendered in exponent form:\n%s", out)
	}
}

// TestResetIsNotHere: the local database is thrown away by `palbase start
// --reset`, which takes the whole stack with it. A second reset that empties one
// schema of a disposable database is a verb whose only use is to be confused
// with the other one.
func TestResetIsNotHere(t *testing.T) {
	for _, c := range Cmd().Commands() {
		if c.Name() == "reset" || c.Name() == "types" {
			t.Errorf("`db %s` is back", c.Name())
		}
	}
}
