package backend

import (
	"archive/tar"
	"compress/gzip"
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
		".palbase/config.json",
	} {
		if !entries[want] {
			t.Errorf("the push does not carry %s", want)
		}
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
