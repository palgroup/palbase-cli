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

// controllerFixture renders a controllers/*.js CJS module that default-exports
// a ControllerDef ({ __palbase:'controller', basePath, routes }). The routes
// arg is a literal JS object body of `mapKey: <RouteDef literal>` entries. These
// are plain CJS objects with the right __palbase discriminants — they do NOT
// require @palbase/backend, because the /openapi.json builder only reads the
// def SHAPE off the default export (it never invokes the handler). The handler
// is a real function so `hasHandler`/dispatch checks pass.
func controllerFixture(basePath, routesBody string) string {
	return fmt.Sprintf(`
module.exports.default = {
  __palbase: 'controller',
  basePath: %q,
  routes: {
%s
  },
};
`, basePath, routesBody)
}

// TestServeOpenAPISpec boots the embedded dev-server against a tiny temp
// controllers/ fixture, fetches GET /openapi.json, and asserts the served spec
// matches the prod byte-shape contract (modules/backend/internal/openapi):
//   - openapi == "3.1.0"
//   - components.securitySchemes has bearerAuth + apiKey
//   - an auth-required route has security [bearerAuth, apiKey] + a 401
//   - an explicit auth:false route has security [{}] and NO 401
//   - a rateLimit route has x-rate-limit
//   - an input-schema route has requestBody.content.application/json.schema
//   - the full route path is basePath + route.path (controller composition)
//
// The fixture controllers are plain CJS modules that do NOT require
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

	// todos controller: basePath "/todos".
	//  - create: POST "/create" → full path /todos/create. auth required,
	//    rateLimit, input/output schemas (fake zod: zodToJSON only checks for a
	//    _def object, our stub converter returns a fixed schema), declared error.
	//  - byId: GET "/:id" → full path /todos/:id (→ {id}), auth omitted →
	//    secure-by-default (required), ByParam operationId.
	mustWrite(t, root, "controllers/todos.controller.js", controllerFixture("/todos", `
    create: {
      __palbase: 'route', method: 'POST', path: '/create',
      handler: {
        __palbase: 'handler',
        auth: { required: true },
        rateLimit: { max: 5, window: 60 },
        input: { _def: { kind: 'input' }, safeParse: (v) => ({ success: true, data: v }) },
        output: { _def: { kind: 'output' } },
        errors: { AlreadyExists: { status: 409, code: 'already_exists', description: 'Duplicate' } },
        handler: async () => ({ ok: true }),
      },
    },
    byId: {
      __palbase: 'route', method: 'GET', path: '/:id',
      handler: { __palbase: 'handler', handler: async () => ({ id: 1 }) },
    },`))

	// public controller: basePath "" → full path "/public". Explicit opt-out →
	// optional auth, no 401.
	mustWrite(t, root, "controllers/public.controller.js", controllerFixture("", `
    list: {
      __palbase: 'route', method: 'GET', path: '/public',
      handler: { __palbase: 'handler', auth: false, handler: async () => ({ public: true }) },
    },`))

	// A controller that throws on require must be SKIPPED, not break the spec.
	mustWrite(t, root, "controllers/broken.controller.js", `throw new Error('boom on require');`)

	// A file whose default export is NOT a controller must be SKIPPED (no v1
	// fallback) and must not leak any route into the spec.
	mustWrite(t, root, "controllers/notacontroller.js", `module.exports.default = { handler: async () => ({}) };`)

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

	// The broken controller (throws on require) + the non-controller file must
	// both have been skipped — no v1 fallback leaks a route.
	for _, p := range []string{"/broken", "/notacontroller"} {
		if _, ok := paths[p]; ok {
			t.Fatalf("non-controller surface %q leaked into spec: %v", p, paths)
		}
	}

	// auth-required route: POST /todos/create (basePath /todos + path /create) →
	// security [bearerAuth, apiKey] + 401.
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

	// :id → {id} path key + ByParam operationId; secure-by-default (auth
	// omitted). Full path = basePath /todos + route path /:id → /todos/{id}.
	byIdOp := operationAt(t, paths, "/todos/{id}", "get")
	if byIdOp["operationId"] != "getTodosById" {
		t.Fatalf("operationId = %v, want getTodosById", byIdOp["operationId"])
	}
	assertAuthRequiredSecurity(t, byIdOp)
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
	// Controller basePath "/things", route POST "/" → full path /things.
	mustWrite(t, root, "controllers/things.controller.js", controllerFixture("/things", `
    create: {
      __palbase: 'route', method: 'POST', path: '/',
      handler: {
        __palbase: 'handler',
        auth: { required: true },
        rateLimit: { max: 10, window: 45 },
        errors: {
          zeta: { status: 409, code: 'zeta_conflict', description: 'Zeta' },
          alpha: { status: 422, code: 'alpha_invalid', description: 'Alpha' },
          mid: { status: 409, code: 'mid_conflict', description: 'Mid' },
        },
        handler: async () => ({ ok: true }),
      },
    },`))

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

// TestServeControllerDispatch boots the embedded dev-server against a v2
// controller fixture (db/schema.ts + controllers/todos.controller.ts +
// handlers/todos/*.ts shape, lowered to CJS) and asserts the dev-server
// actually DISPATCHES a request to the route's handler:
//
//	GET /todos → 200 with the handler's output {items:[...]}
//
// This is the controller→route-table→handler path (not just the openapi
// shape): basePath "/todos" + route.path "/" composes to /todos, the GET verb
// matches, and the handler at routes.list.handler.handler runs. The route is
// auth:false so no palauth round-trip is needed. A minimal @palbase/backend
// stub on NODE_PATH satisfies installRuntime()'s `require('@palbase/backend')
// .__setRuntime(...)` (the handler imports nothing — it returns a literal).
func TestServeControllerDispatch(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}

	root := t.TempDir()

	// A handler colocated under handlers/, imported by the controller — the v2
	// authoring layout. Lowered to CJS: the handler is a plain function and the
	// def carries the __palbase:"handler" discriminant.
	mustWrite(t, root, "handlers/todos/list.js", `
module.exports.default = {
  __palbase: 'handler',
  auth: false,
  handler: async (req) => ({ items: [{ id: 't1', title: 'first' }], gotMethod: req.method }),
};
`)
	// The controller wires the handler at route map key "list": GET "/" under
	// basePath "/todos" → full path /todos.
	mustWrite(t, root, "controllers/todos.controller.js", `
const list = require('../handlers/todos/list.js').default;
module.exports.default = {
  __palbase: 'controller',
  basePath: '/todos',
  routes: {
    list: { __palbase: 'route', method: 'GET', path: '/', handler: list },
  },
};
`)

	writeBackendStub(t, root)

	port := startDevServer(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Poll GET /todos until the server is up, then assert the dispatched body.
	body := getJSONUntilReady(t, ctx, port, "/todos")
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("GET /todos body items = %v, want one item", body["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != "t1" || first["title"] != "first" {
		t.Fatalf("dispatched handler output = %v, want {id:t1,title:first}", first)
	}
	// The dev-server threaded the real PBRequest (req.method) into the handler.
	if body["gotMethod"] != "GET" {
		t.Fatalf("handler saw req.method = %v, want GET", body["gotMethod"])
	}
}

// startDevServer extracts the embedded dev-server into a temp dir and launches
// it against `root` on a free port, registering a t.Cleanup to kill it. Returns
// the port. Shared by the dispatch test (and any future serve test that needs a
// running server rather than just /openapi.json).
func startDevServer(t *testing.T, root string) int {
	t.Helper()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	devDir := t.TempDir()
	if err := extractFS(devServerFS, "devjs", devDir); err != nil {
		t.Fatalf("extract dev server: %v", err)
	}
	port := freeTCPPort(t)
	cmd := exec.Command(nodeBin, filepath.Join(devDir, "dev-server.js"))
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
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return port
}

// getJSONUntilReady polls GET <path> until the dev-server returns 200 (or the
// deadline hits), then decodes the JSON body. Used by the dispatch test to wait
// for boot AND assert the handler's output in one shot.
func getJSONUntilReady(t *testing.T, ctx context.Context, port int, path string) map[string]any {
	t.Helper()
	u := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	deadline := time.Now().Add(12 * time.Second)
	var lastErr error
	var lastStatus int
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastStatus = resp.StatusCode
			if resp.StatusCode == http.StatusOK {
				var out map[string]any
				if jerr := json.Unmarshal(data, &out); jerr != nil {
					t.Fatalf("decode GET %s: %v\nbody: %s", path, jerr, data)
				}
				return out
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("dev-server did not serve 200 on GET %s in time (last status %d, err %v)", path, lastStatus, lastErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// writeBackendStub installs a minimal @palbase/backend on the fixture's
// node_modules so the dev-server's installRuntime() can
// `require('@palbase/backend').__setRuntime(...)` without a real install. The
// stub also no-ops the Resource API (so a resources/ dir, if present, is a clean
// no-op). The fixture handlers import nothing from it.
func writeBackendStub(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, root, "node_modules/@palbase/backend/package.json",
		`{"name":"@palbase/backend","version":"0.0.0-test","main":"index.js"}`)
	mustWrite(t, root, "node_modules/@palbase/backend/index.js", `
'use strict';
class Resource {}
module.exports = {
  __setRuntime() {},
  Resource,
  __registerResource() {},
  async __runResourceBoot() {},
  async __shutdownResources() {},
};
`)
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
