package db

import (
	"bytes"
	"encoding/json"
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

	sources, err := readSchema()
	if err != nil {
		t.Fatalf("readSchema: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("both schemas must be in hand, got %d", len(sources))
	}
	// Sorted by file name — readdir order would make the plan flap between runs.
	if sources[0].Name != "billing" || sources[1].Name != "public" {
		t.Fatalf("the sources are not sorted by file name: %v", sources)
	}
	if !strings.Contains(sources[1].Source, `defineSchema("public"`) ||
		!strings.Contains(sources[0].Source, `defineSchema("billing"`) {
		t.Errorf("each source must carry its OWN text: %v", sources)
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

	_, err := readSchema()
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

	_, err := readSchema()
	if err == nil {
		t.Fatal("a checkout with no db/ was accepted")
	}
	const documented = "no db/public.ts in this directory — `palbase db` reads the schema this project declares"
	if err.Error() != documented {
		t.Errorf("the refusal drifted from the sentence cli/database.md publishes:\n got: %s\nwant: %s", err, documented)
	}
}

// TestASingleSchemaProjectIsStillASet is the NEGATIVE CONTROL for the envelope:
// the ordinary one-file project takes the SAME road as a three-file one.
//
// A shape that special-cases the common case has two code paths, and the rare
// one is the one nobody runs — so it is the one that is broken.
func TestASingleSchemaProjectIsStillASet(t *testing.T) {
	scratchCheckout(t)
	writeSchemaFiles(t, map[string]string{
		"public.ts": `export default defineSchema("public", { tables: [] });`,
	})

	sources, err := readSchema()
	if err != nil {
		t.Fatalf("readSchema: %v", err)
	}
	if len(sources) != 1 || sources[0].Name != "public" {
		t.Fatalf("a single-schema project must still arrive as a set: %v", sources)
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

// TestPlanSeesEverySchema — `db plan` and `db apply` carry the WHOLE directory.
//
// They used to send one body, the public declaration, and print a note naming
// the siblings that stayed behind. A note is not a plan: a project whose
// billing schema drifted got told "in sync" by the verb whose entire job is to
// say what would change. The endpoint now takes every source the directory
// declares, so the answer covers the project rather than a part of it.
func TestPlanSeesEverySchema(t *testing.T) {
	var gotBody, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody, gotType = buf.String(), r.Header.Get("content-type")
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

	if _, _, err := run(t, "plan"); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !strings.HasPrefix(gotType, "application/json") {
		t.Errorf("the sources travel as JSON, got content-type %q", gotType)
	}
	var sent struct {
		Sources []struct{ Name, Source string } `json:"sources"`
	}
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("the body is not the sources envelope: %v — %q", err, gotBody)
	}
	// Sorted by name, because an order that came off readdir would make the
	// plan flap between runs on the same unchanged project.
	if len(sent.Sources) != 2 ||
		sent.Sources[0].Name != "billing" || sent.Sources[1].Name != "public" {
		t.Fatalf("both schemas must travel, sorted: %+v", sent.Sources)
	}
	if !strings.Contains(sent.Sources[0].Source, `defineSchema("billing"`) {
		t.Errorf("the billing source is not its own text: %q", sent.Sources[0].Source)
	}
	if !strings.Contains(sent.Sources[1].Source, `defineSchema("public"`) {
		t.Errorf("the public source is not its own text: %q", sent.Sources[1].Source)
	}
}

// TestApplySeesEverySchema — the same, on the verb that WRITES. Diverging here
// is the worse half: plan would report on the project and apply would change
// only part of it.
func TestApplySeesEverySchema(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"changed":false,"summary":[]}`))
	}))
	defer srv.Close()

	scratchCheckout(t)
	runningStackAt(t, srv.URL)
	writeSchemaFiles(t, map[string]string{
		"public.ts": `export default defineSchema("public", { tables: [] });`,
		"audit.ts":  `export default defineSchema("audit", { tables: [] });`,
	})

	if _, _, err := run(t, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var sent struct {
		Sources []struct{ Name, Source string } `json:"sources"`
	}
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("the body is not the sources envelope: %v — %q", err, gotBody)
	}
	if len(sent.Sources) != 2 || sent.Sources[0].Name != "audit" {
		t.Fatalf("both schemas must reach the writer, sorted: %+v", sent.Sources)
	}
}
