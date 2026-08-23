package backend

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What a stack push must carry, and what it must never carry.
//
// A stack does not BUILD — it collects. So the archive has to include the build
// products, which the cloud tarball deliberately leaves out because the cloud
// builds them server-side. Getting this wrong is quiet: the push succeeds as far
// as the network, and the stack refuses with "no built code" about a project
// that built fine.
func entriesOf(t *testing.T, blob []byte) map[string]bool {
	t.Helper()
	gz, err := gzip.NewReader(strings.NewReader(string(blob)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.ToSlash(strings.TrimPrefix(h.Name, "./"))] = true
	}
	return out
}

func projectForPush(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("controllers/todo.controller.ts", "// source")
	write("db/schema.ts", "export default {}")
	write(".palbase/esm/controllers/controllers.js", "export const controllers = [];")
	write(".palbase/esm/tests/todos.test.js", "// the suite a deploy grades this release by")
	// A leftover from before the cutover. People have one of these on disk right
	// now, and it must NOT ride along: settings are written directly, so a copy
	// in a push could only overwrite what somebody set from the panel.
	write(".palbase/config.json", `{"flags":{}}`)
	write(".palbase/target.json", `{"url":"https://127.0.0.1"}`)
	write(".palbase/ios/palbase-config.json", `{"api_key":"pb_selfhost_c…"}`)
	write(".palbase/openapi.json", `{"openapi":"3.2.0"}`)
	write("node_modules/left-pad/index.js", "// huge")
	return dir
}

func TestAStackPushCarriesTheBuiltCode(t *testing.T) {
	blob, err := BuildStackTarball(projectForPush(t))
	if err != nil {
		t.Fatal(err)
	}
	entries := entriesOf(t, blob)
	for _, want := range []string{
		"controllers/todo.controller.ts",
		"db/schema.ts",
		".palbase/esm/controllers/controllers.js",
		// The suites travel BUILT. A deploy runs them against the release it
		// just made, in a container with no node_modules to resolve them.
		".palbase/esm/tests/todos.test.js",
	} {
		if !entries[want] {
			t.Errorf("the push does not carry %s", want)
		}
	}
	if entries[".palbase/config.json"] {
		t.Error("a stale config document rode along — it would overwrite what the panel set")
	}
}

func TestAStackPushLeavesTheCLIsOwnStateBehind(t *testing.T) {
	blob, err := BuildStackTarball(projectForPush(t))
	if err != nil {
		t.Fatal(err)
	}
	entries := entriesOf(t, blob)
	// None of these are the backend. `target.json` in particular describes the
	// machine that pushed, and shipping it would put one developer's link into
	// everybody's deployment.
	for _, unwanted := range []string{
		".palbase/target.json",
		".palbase/ios/palbase-config.json",
		".palbase/openapi.json",
		"node_modules/left-pad/index.js",
	} {
		if entries[unwanted] {
			t.Errorf("the push carries %s, which is not the backend", unwanted)
		}
	}
}

func TestTheCloudTarballIsUnchanged(t *testing.T) {
	// The cloud builds server-side from source, so its archive must not start
	// carrying build products: that would ship a bundle the cloud then rebuilds,
	// and the two could disagree.
	blob, err := BuildTarball(projectForPush(t))
	if err != nil {
		t.Fatal(err)
	}
	entries := entriesOf(t, blob)
	if entries[".palbase/esm/controllers/controllers.js"] || entries[".palbase/config.json"] {
		t.Error("the cloud tarball now carries build products")
	}
	if !entries["controllers/todo.controller.ts"] {
		t.Error("the cloud tarball lost the source")
	}
}

// A refusal a person has to ACT on must be readable, and three of them now are:
// the tests failed, the tests hung, or the schema cannot be applied while the
// running release still serves. Each carries a multi-line explanation — a
// failing assertion, or the objects to split a migration on — and the generic
// path truncates the raw JSON at 300 characters, which turns exactly the thing
// they need into a fragment of an escaped string.
func TestARefusalAPersonMustReadIsPrintedInFull(t *testing.T) {
	long := "the tests failed against the new release, so it was discarded and the " +
		"previous one keeps serving.\n" + strings.Repeat("  todos › ownership is enforced — expected 403, got 200\n", 12)

	for _, code := range []string{"tests_failed", "tests_timed_out", "schema_incompatible", "candidate_failed"} {
		var out strings.Builder
		body := []byte(`{"error":"` + code + `","error_description":` + quote(long) + `,"status":422}`)
		err := renderPushRefusal(&out, 422, body)
		if err == nil {
			t.Fatalf("%s: a refused push reported success", code)
		}
		printed := out.String() + err.Error()
		if !strings.Contains(printed, "ownership is enforced") {
			t.Errorf("%s: the reason was lost:\n%s", code, printed)
		}
		if strings.Contains(printed, "error_description") {
			t.Errorf("%s: the person is reading raw JSON:\n%s", code, printed)
		}
		if strings.Contains(printed, "…") {
			t.Errorf("%s: the reason was truncated:\n%s", code, printed)
		}
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
