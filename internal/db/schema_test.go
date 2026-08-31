package db

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadSchemaFindsEverySchemaFile: the local verbs read the DIRECTORY, and a
// project that declares two schemas has both in hand — sorted, because an order
// that came off readdir would make the plan flap between runs.
func TestReadSchemaFindsEverySchemaFile(t *testing.T) {
	scratchCheckout(t)
	writeSchemaFiles(t, map[string]string{
		"public.ts":  `export default defineSchema("public", { tables: [] });`,
		"billing.ts": `export default defineSchema("billing", { tables: [] });`,
	})

	public, others, err := readSchema()
	if err != nil {
		t.Fatalf("readSchema: %v", err)
	}
	if !strings.Contains(public, `defineSchema("public"`) {
		t.Errorf("the public declaration is not what travels: %q", public)
	}
	if len(others) != 1 || others[0] != "db/billing.ts" {
		t.Fatalf("the schemas that did NOT travel must be named, got %v", others)
	}
}

// TestReadSchemaRejectsTheLegacyLayout is NFR-002 on the surface a person
// actually types. A refusal that says "invalid schema" tells them nothing they
// can act on; this one names the file to write and where the move is written.
func TestReadSchemaRejectsTheLegacyLayout(t *testing.T) {
	scratchCheckout(t)
	writeSchemaFiles(t, map[string]string{
		"schema.ts": `export default defineSchema({ tables: {} });`,
	})

	_, _, err := readSchema()
	if err == nil {
		t.Fatal("the old single-file layout was accepted")
	}
	for _, want := range []string{"db/schema.ts", "db/public.ts", "/docs/backend/schema-migration"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// TestReadSchemaNamesThePublicFileWhenThereIsNoSchema — the missing-file
// refusal has to name db/public.ts, because that is the file to create.
//
// Pinned as the WHOLE SENTENCE, not a substring, because the published docs
// quote it verbatim (cli/database.md). A `Contains("db/public.ts")` would stay
// green through a reword and leave the documentation asserting a sentence this
// binary no longer says — which is the failure mode nobody notices, since both
// halves keep passing their own checks.
func TestReadSchemaNamesThePublicFileWhenThereIsNoSchema(t *testing.T) {
	scratchCheckout(t)

	_, _, err := readSchema()
	if err == nil {
		t.Fatal("a checkout with no db/ was accepted")
	}
	const documented = "no db/public.ts in this directory — `palbase db` reads the schema this project declares"
	if err.Error() != documented {
		t.Errorf("the refusal drifted from the sentence cli/database.md publishes:\n got: %s\nwant: %s", err, documented)
	}
}

// TestASingleSchemaProjectCarriesNoNote is the NEGATIVE CONTROL for the note
// below: the ordinary project must not be told that something did not travel.
func TestASingleSchemaProjectCarriesNoNote(t *testing.T) {
	scratchCheckout(t)
	writeSchemaFiles(t, map[string]string{
		"public.ts": `export default defineSchema("public", { tables: [] });`,
	})

	_, others, err := readSchema()
	if err != nil {
		t.Fatalf("readSchema: %v", err)
	}
	if len(others) != 0 {
		t.Fatalf("a single-schema project was told something stayed behind: %v", others)
	}
}

// writeSchemaFiles writes db/<name> for each entry.
func writeSchemaFiles(t *testing.T, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll("db", 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join("db", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestPlanSaysWhichSchemasDidNotTravel — the note is the whole reason the
// narrower scope is acceptable, so it has to REACH somebody. A helper nobody
// calls is not a feature, and this asserts the text on the stream a person
// reads, not the return value of the function that builds it.
//
// It also pins WHICH source went: the public declaration, never a sibling.
func TestPlanSaysWhichSchemasDidNotTravel(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"in_sync":true,"changes":[],"destructive":[]}`))
	}))
	defer srv.Close()

	scratchCheckout(t)
	runningStackAt(t, srv.URL)
	writeSchemaFiles(t, map[string]string{
		"public.ts":  `export default defineSchema("public", { tables: [] });`,
		"billing.ts": `export default defineSchema("billing", { tables: [] });`,
	})

	_, banner, err := run(t, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !strings.Contains(gotBody, `defineSchema("public"`) {
		t.Errorf("the PUBLIC declaration is not what travelled: %q", gotBody)
	}
	if strings.Contains(gotBody, "billing") {
		t.Errorf("a sibling schema rode along in a body the stack names `public`: %q", gotBody)
	}
	for _, want := range []string{"db/billing.ts", "db/public.ts", "palbase push"} {
		if !strings.Contains(banner, want) {
			t.Errorf("the note does not name %q: %q", want, banner)
		}
	}
}

// TestAnOrdinarySchemaIsNotAnnounced is the NEGATIVE CONTROL: the gate must
// only fire on its target. A note printed for every project is noise people
// learn to skip, and then it is not there on the day it matters.
func TestAnOrdinarySchemaIsNotAnnounced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"in_sync":true,"changes":[],"destructive":[]}`))
	}))
	defer srv.Close()

	scratchCheckout(t)
	runningStackAt(t, srv.URL)
	writeSchemaFiles(t, map[string]string{
		"public.ts": `export default defineSchema("public", { tables: [] });`,
	})

	if _, banner, err := run(t, "plan"); err != nil {
		t.Fatalf("plan: %v", err)
	} else if strings.Contains(banner, "did not travel") {
		t.Errorf("a single-schema project was told something stayed behind: %q", banner)
	}
}
