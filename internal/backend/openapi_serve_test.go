package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestServeOpenAPISpec boots the embedded dev-server against a tiny temp
// endpoints/ fixture, fetches GET /openapi.json, and asserts the served spec
// matches the prod byte-shape contract (modules/backend/internal/openapi):
//   - openapi == "3.1.0"
//   - components.securitySchemes has bearerAuth + apiKey
//   - an auth-required endpoint has security [bearerAuth, apiKey] + a 401
//   - an explicit auth:false endpoint has security [{}] and NO 401
//   - a rateLimit endpoint has x-rate-limit
//   - an input-schema endpoint has requestBody.content.application/json.schema
//
// The fixture endpoints are plain CJS modules that do NOT require
// @palbase/backend (the dev-server only needs that per-request inside the
// handler, which /openapi.json never hits). zod-to-json-schema is supplied as a
// local stub via NODE_PATH so the input-schema body is deterministic and the
// test is fully self-contained — no global npm install required.
func TestServeOpenAPISpec(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}

	root := t.TempDir()

	// auth omitted → secure-by-default (required). Has input + output schemas
	// (fake zod objects: zodToJSON only checks for a _def object, our stub
	// converter returns a fixed schema) and a rateLimit.
	mustWrite(t, root, "endpoints/todos/create/post.js", `
module.exports.default = {
  auth: { required: true },
  rateLimit: { max: 5, window: 60 },
  input: { _def: { kind: 'input' } },
  output: { _def: { kind: 'output' } },
  errors: {
    AlreadyExists: { status: 409, code: 'already_exists', description: 'Duplicate' },
  },
  handler: async () => ({ ok: true }),
};
`)

	// explicit opt-out → optional auth, no 401.
	mustWrite(t, root, "endpoints/public/get.js", `
module.exports.default = {
  auth: false,
  handler: async () => ({ public: true }),
};
`)

	// path param [id] → {id}; auth omitted → secure-by-default (required).
	mustWrite(t, root, "endpoints/rooms/[id]/get.js", `
module.exports.default = {
  handler: async () => ({ id: 1 }),
};
`)

	// A module that throws on require must be SKIPPED, not break the whole spec.
	mustWrite(t, root, "endpoints/broken/get.js", `throw new Error('boom on require');`)

	writeZodToJSONStub(t, root)

	// Copy the embedded dev-server (+ its module-clients.js sibling) to a temp
	// dir, exactly like newDevCmd does.
	devDir := t.TempDir()
	if err := extractFS(devServerFS, "devjs", devDir); err != nil {
		t.Fatalf("extract dev server: %v", err)
	}

	port := freeTCPPort(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, nodeBin, filepath.Join(devDir, "dev-server.js"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PALBASE_DEV_PORT=%d", port),
		fmt.Sprintf("PALBASE_DEV_ROOT=%s", root),
		fmt.Sprintf("NODE_PATH=%s", filepath.Join(root, "node_modules")),
		"PALBASE_PROJECT_REF=local",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dev-server: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	spec := fetchSpecUntilReady(t, ctx, port)

	// openapi == "3.1.0"
	if got := spec["openapi"]; got != "3.1.0" {
		t.Fatalf("openapi = %v, want 3.1.0", got)
	}

	// info block.
	info, _ := spec["info"].(map[string]any)
	if info == nil || info["title"] != "Palbase Backend" || info["version"] != "1.0.0" {
		t.Fatalf("info = %v, want title=Palbase Backend version=1.0.0", spec["info"])
	}

	// components.securitySchemes has bearerAuth + apiKey, both present.
	comps, _ := spec["components"].(map[string]any)
	schemes, _ := comps["securitySchemes"].(map[string]any)
	if schemes == nil || schemes["bearerAuth"] == nil || schemes["apiKey"] == nil {
		t.Fatalf("components.securitySchemes missing bearerAuth/apiKey: %v", comps)
	}

	paths, _ := spec["paths"].(map[string]any)
	if paths == nil {
		t.Fatalf("paths missing: %v", spec)
	}

	// The broken endpoint must have been skipped, not present.
	if _, ok := paths["/broken"]; ok {
		t.Fatalf("broken endpoint leaked into spec: %v", paths)
	}

	// auth-required endpoint: POST /todos/create → security [bearerAuth, apiKey] + 401.
	createOp := operationAt(t, paths, "/todos/create", "post")
	assertAuthRequiredSecurity(t, createOp)
	resps, _ := createOp["responses"].(map[string]any)
	if resps["401"] == nil {
		t.Fatalf("auth-required op missing 401: %v", resps)
	}
	if resps["400"] == nil || resps["200"] == nil {
		t.Fatalf("op missing standard 200/400: %v", resps)
	}
	if createOp["operationId"] != "postTodosCreate" {
		t.Fatalf("operationId = %v, want postTodosCreate", createOp["operationId"])
	}
	// rateLimit → x-rate-limit { max, window }.
	rl, _ := createOp["x-rate-limit"].(map[string]any)
	if rl == nil || rl["max"] != float64(5) || rl["window"] != float64(60) {
		t.Fatalf("x-rate-limit = %v, want {max:5,window:60}", createOp["x-rate-limit"])
	}
	// input schema → requestBody.content.application/json.schema.
	reqBody, _ := createOp["requestBody"].(map[string]any)
	if reqBody == nil || reqBody["required"] != true {
		t.Fatalf("requestBody missing/required!=true: %v", createOp["requestBody"])
	}
	rbContent, _ := reqBody["content"].(map[string]any)
	rbJSON, _ := rbContent["application/json"].(map[string]any)
	if rbJSON == nil || rbJSON["schema"] == nil {
		t.Fatalf("requestBody.content.application/json.schema missing: %v", reqBody)
	}
	// declared error 409 present.
	if resps["409"] == nil {
		t.Fatalf("declared error 409 missing: %v", resps)
	}
	if createOp["x-palbase-errors"] == nil {
		t.Fatalf("x-palbase-errors missing on op with declared errors")
	}

	// explicit auth:false endpoint: GET /public → security [{}], NO 401.
	publicOp := operationAt(t, paths, "/public", "get")
	sec, _ := publicOp["security"].([]any)
	if len(sec) != 1 {
		t.Fatalf("auth:false op security = %v, want [{}]", publicOp["security"])
	}
	if m, _ := sec[0].(map[string]any); len(m) != 0 {
		t.Fatalf("auth:false op security[0] = %v, want empty {}", sec[0])
	}
	pubResps, _ := publicOp["responses"].(map[string]any)
	if pubResps["401"] != nil {
		t.Fatalf("auth:false op must NOT have 401: %v", pubResps)
	}

	// [id] → {id} path key + ByParam operationId; secure-by-default (auth omitted).
	roomsOp := operationAt(t, paths, "/rooms/{id}", "get")
	if roomsOp["operationId"] != "getRoomsById" {
		t.Fatalf("operationId = %v, want getRoomsById", roomsOp["operationId"])
	}
	assertAuthRequiredSecurity(t, roomsOp)
}

// assertAuthRequiredSecurity checks security == [{bearerAuth:[]},{apiKey:[]}].
func assertAuthRequiredSecurity(t *testing.T, op map[string]any) {
	t.Helper()
	sec, _ := op["security"].([]any)
	if len(sec) != 2 {
		t.Fatalf("auth-required security = %v, want 2 entries", op["security"])
	}
	first, _ := sec[0].(map[string]any)
	second, _ := sec[1].(map[string]any)
	if _, ok := first["bearerAuth"]; !ok {
		t.Fatalf("security[0] = %v, want bearerAuth", sec[0])
	}
	if _, ok := second["apiKey"]; !ok {
		t.Fatalf("security[1] = %v, want apiKey", sec[1])
	}
}

func operationAt(t *testing.T, paths map[string]any, path, method string) map[string]any {
	t.Helper()
	item, ok := paths[path].(map[string]any)
	if !ok {
		t.Fatalf("path %q missing from spec: %v", path, paths)
	}
	op, ok := item[method].(map[string]any)
	if !ok {
		t.Fatalf("method %q missing on path %q: %v", method, path, item)
	}
	return op
}

// fetchSpecUntilReady polls GET /openapi.json until the dev-server is listening
// (or the context deadline hits), then returns the decoded JSON object.
func fetchSpecUntilReady(t *testing.T, ctx context.Context, port int) map[string]any {
	t.Helper()
	specURL := fmt.Sprintf("http://127.0.0.1:%d/openapi.json", port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, specURL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var out map[string]any
				if jerr := json.Unmarshal(body, &out); jerr != nil {
					t.Fatalf("decode /openapi.json: %v\nbody: %s", jerr, body)
				}
				return out
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("dev-server did not serve /openapi.json in time (last err: %v)", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// writeZodToJSONStub installs a deterministic zod-to-json-schema replacement in
// the fixture's node_modules so the dev-server's lazy resolve finds it and the
// input/output bodies are stable. Returns a fixed object schema (with the outer
// $schema the dev-server strips) for any value.
func writeZodToJSONStub(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, root, "node_modules/zod-to-json-schema/package.json",
		`{"name":"zod-to-json-schema","version":"0.0.0-test","main":"index.js"}`)
	mustWrite(t, root, "node_modules/zod-to-json-schema/index.js", `
'use strict';
function zodToJsonSchema(z, opts) {
  return {
    $schema: 'http://json-schema.org/draft-07/schema#',
    type: 'object',
    properties: { title: { type: 'string' } },
    required: ['title'],
  };
}
module.exports = { zodToJsonSchema };
`)
}

// freeTCPPort grabs an OS-assigned free port on the loopback, then releases it
// so the dev-server can bind it. Best-effort (a racing process could grab it),
// which is fine for a single-developer test.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestServeOpenAPIErrorOrdering is a regression test for the dev-server's
// x-palbase-errors key ordering. The Go OpenAPI generator stores declared
// errors in a Go map[string]any and re-marshals it through a flat map, so the
// served error-NAME keys come out GLOBALLY lexicographically sorted. The
// dev-server originally built that object grouped by status, so its insertion
// order was status-grouped (NOT global-sorted), and for an endpoint whose error
// names don't happen to sort the same as their status grouping the served bytes
// diverged from prod — breaking byte-identical local codegen.
//
// Fixture: things/post declares three errors whose names are NOT in
// status-grouping order — zeta@409, alpha@422, mid@409. Built grouped by status
// (409 seen first → mid,zeta; then 422 → alpha) the insertion order would be
// [mid, zeta, alpha]. The fix re-emits globally sorted, so the SERIALIZED keys
// must be [alpha, mid, zeta]. We read the order from the RAW response bytes
// (JSON.stringify preserves object insertion order) because a parsed Go map
// loses key order.
func TestServeOpenAPIErrorOrdering(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}

	root := t.TempDir()

	// Three errors deliberately NOT in status-grouping order. Also an object-form
	// rateLimit { max, window } (prod emits object-only x-rate-limit too).
	mustWrite(t, root, "endpoints/things/post.js", `
module.exports.default = {
  auth: { required: true },
  rateLimit: { max: 10, window: 45 },
  errors: {
    zeta: { status: 409, code: 'zeta_conflict', description: 'Zeta' },
    alpha: { status: 422, code: 'alpha_invalid', description: 'Alpha' },
    mid: { status: 409, code: 'mid_conflict', description: 'Mid' },
  },
  handler: async () => ({ ok: true }),
};
`)

	writeZodToJSONStub(t, root)

	// Boot the embedded dev-server exactly like TestServeOpenAPISpec.
	devDir := t.TempDir()
	if err := extractFS(devServerFS, "devjs", devDir); err != nil {
		t.Fatalf("extract dev server: %v", err)
	}

	port := freeTCPPort(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, nodeBin, filepath.Join(devDir, "dev-server.js"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PALBASE_DEV_PORT=%d", port),
		fmt.Sprintf("PALBASE_DEV_ROOT=%s", root),
		fmt.Sprintf("NODE_PATH=%s", filepath.Join(root, "node_modules")),
		"PALBASE_PROJECT_REF=local",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dev-server: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Wait until ready (parsed), then re-fetch the RAW bytes to read key order.
	spec := fetchSpecUntilReady(t, ctx, port)
	raw := fetchSpecRaw(t, ctx, port)

	paths, _ := spec["paths"].(map[string]any)
	postOp := operationAt(t, paths, "/things", "post")

	// Sanity: the extension object exists and has exactly the three names.
	xerr, _ := postOp["x-palbase-errors"].(map[string]any)
	if len(xerr) != 3 || xerr["alpha"] == nil || xerr["mid"] == nil || xerr["zeta"] == nil {
		t.Fatalf("x-palbase-errors = %v, want keys {alpha,mid,zeta}", postOp["x-palbase-errors"])
	}

	// Object-form rateLimit → x-rate-limit { max:10, window:45 }.
	rl, _ := postOp["x-rate-limit"].(map[string]any)
	if rl == nil || rl["max"] != float64(10) || rl["window"] != float64(45) {
		t.Fatalf("x-rate-limit = %v, want {max:10,window:45}", postOp["x-rate-limit"])
	}

	// SERIALIZED key order of x-palbase-errors on POST /things — read from raw.
	got := orderedKeysOf(t, raw, "paths", "/things", "post", "x-palbase-errors")
	want := []string{"alpha", "mid", "zeta"}
	if !equalStrings(got, want) {
		t.Fatalf("x-palbase-errors serialized key order = %v, want %v "+
			"(status-grouped insertion order would be [mid zeta alpha])", got, want)
	}
}

// fetchSpecRaw GETs /openapi.json once (the server is already known-ready) and
// returns the raw response body so callers can inspect SERIALIZED key order,
// which a parsed Go map would lose.
func fetchSpecRaw(t *testing.T, ctx context.Context, port int) []byte {
	t.Helper()
	specURL := fmt.Sprintf("http://127.0.0.1:%d/openapi.json", port)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, specURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch raw /openapi.json: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read raw /openapi.json body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw /openapi.json status = %d, want 200\nbody: %s", resp.StatusCode, body)
	}
	return body
}

// orderedKeysOf navigates raw JSON object bytes down the given key path and
// returns the target object's keys IN SERIALIZED ORDER. Each level is decoded
// into map[string]json.RawMessage (which preserves the child bytes), then the
// final object's keys are read in textual order with a json.Decoder token scan.
func orderedKeysOf(t *testing.T, raw []byte, path ...string) []string {
	t.Helper()
	cur := json.RawMessage(raw)
	for _, key := range path {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			t.Fatalf("decode object at %q: %v", key, err)
		}
		next, ok := obj[key]
		if !ok {
			t.Fatalf("key %q missing while navigating %v", key, path)
		}
		cur = next
	}
	keys, err := topLevelKeysInOrder(cur)
	if err != nil {
		t.Fatalf("read ordered keys at %v: %v", path, err)
	}
	return keys
}

// topLevelKeysInOrder returns the keys of a JSON object in serialized order by
// scanning tokens (json.Decoder hands object keys back in document order).
func topLevelKeysInOrder(raw []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected object, got %v", tok)
	}
	var keys []string
	for dec.More() {
		ktok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := ktok.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key, got %v", ktok)
		}
		keys = append(keys, key)
		// Consume the value (object/array/scalar) so the next token is a key.
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mustWrite writes body to root/rel, creating parent dirs. Shared by the
// serve fixtures that materialise a temp endpoints/ tree.
func mustWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
