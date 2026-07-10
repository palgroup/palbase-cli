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
	"strings"
	"sync"
	"testing"
	"time"
)

// controllerFixture renders a controllers/*.js CJS module that default-exports
// a @Controller CLASS, stamped with the exact runtime registry symbols the real
// @palbase/backend decorators produce: `__palbase:'controller'` on the class,
// the controller meta on Symbol.for("palbase.backend.controllerMeta"), the route
// ARRAY on Symbol.for("palbase.backend.routes"), and the @Returns buffer on
// Symbol.for("palbase.backend.returnBuffer"). This is a faithful CJS twin of the
// compiled SDK output — the dev-server reads the registry the SAME way worker.js
// does, so a fixture that stamps these symbols exercises the real load path
// without needing decorator transpilation. `metaBody` is the controllerMeta
// object literal ({ __palbase:'controller', basePath, defaultAuth? }); `routes`
// is a JS array literal of RouteMeta; `methods` is a class-body of methods;
// `returnBuf` is the returnBuffer object literal (fnName → zod-ish). The methods
// are real functions so dispatch runs. `className` is the live class NAME the
// dev-server derives the dotted-operationId namespace from (TodosController →
// todos) — a named class expression keeps the template's `Ctrl` binding while
// giving Ctrl.name the fixture's chosen name.
func controllerFixture(className, metaBody, routes, methods, returnBuf string) string {
	return fmt.Sprintf(`
'use strict';
const ROUTES = Symbol.for('palbase.backend.routes');
const RETBUF = Symbol.for('palbase.backend.returnBuffer');
const CTRLMETA = Symbol.for('palbase.backend.controllerMeta');
const Ctrl = class %s {
%s
};
Ctrl.__palbase = 'controller';
Ctrl[CTRLMETA] = %s;
Ctrl[ROUTES] = %s;
Ctrl[RETBUF] = %s;
module.exports.default = Ctrl;
`, className, methods, metaBody, routes, returnBuf)
}

// fakeZod returns a JS expression for a fake zod schema: a `_def` object (so
// zodToJSON's `typeof z._def === 'object'` guard passes → the stub converter
// runs) plus a permissive safeParse (so request validation always passes through
// in the dispatch tests).
const fakeZod = `{ _def: { typeName: 'ZodObject' }, safeParse: (v) => ({ success: true, data: v }) }`

// TestServeOpenAPISpec boots the embedded dev-server against a tiny temp
// controllers/ fixture, fetches GET /openapi.json, and asserts the served spec
// matches the prod byte-shape contract (modules/backend/internal/openapi):
//   - openapi == "3.1.0"
//   - components.securitySchemes has bearerAuth + apiKey
//   - an auth-required route has security [bearerAuth, apiKey] + a 401
//   - an explicit auth:false route (via @Controller defaultAuth:false) has
//     security [{}] and NO 401
//   - a rateLimit route has x-rate-limit
//   - a @Body route has requestBody.content.application/json.schema
//   - a @QueryParams route emits in:query parameters; a @Param route emits in:path
//   - the full route path is basePath + subpath (controller composition)
//   - a route without throws meta → NO x-palbase-errors / declared statuses
//   - operationIds are DOTTED `<controllerName>.<fnName>` (TodosController →
//     todos.create), with the flat verb-prefixed derivation as the fallback
//     when the class name lowers to "" — matching the prod generator.go +
//     extract_meta.js contract
//
// The fixture controllers stamp the real runtime registry symbols on a class
// (controllerFixture), so the dev-server's getRoutes/symbol-fallback load path
// is exercised. zod-to-json-schema is supplied as a local stub via NODE_PATH so
// the schema bodies are deterministic and the test is self-contained.
func TestServeOpenAPISpec(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}

	root := t.TempDir()
	seedEsbuild(t, root)

	// todos controller: basePath "/todos".
	//  - create: POST "/create" → full path /todos/create. auth required,
	//    rateLimit, @Body + @Returns (fake zod: zodToJSON only checks for a _def
	//    object; the stub converter returns a fixed schema).
	//  - byId: GET "/{id}" → full path /todos/{id}, auth omitted →
	//    secure-by-default (required), ByParam operationId, @Param("id"). Path
	//    params use the canonical BRACE form ({id}) — the one true form the route
	//    registry + dev-server + OpenAPI generator agree on (legacy :id is gone).
	mustWrite(t, root, "controllers/todos.controller.js", controllerFixture(
		"TodosController",
		`{ __palbase: 'controller', basePath: '/todos' }`,
		`[
      { method: 'POST', subpath: '/create', fnName: 'create',
        options: { auth: { required: true }, rateLimit: { max: 5, window: 60 } },
        params: [ { index: 0, kind: 'body', schema: `+fakeZod+` } ] },
      { method: 'GET', subpath: '/{id}', fnName: 'byId',
        options: {},
        params: [ { index: 0, kind: 'param', name: 'id' } ] },
    ]`,
		`
  async create() { return { ok: true }; }
  async byId() { return { id: 1 }; }`,
		`{ create: { _def: { typeName: 'ZodObject' } } }`,
	))

	// public controller: basePath "" + @Controller defaultAuth:false → full path
	// "/public". Explicit opt-out cascades to the route → optional auth, no 401.
	// The route also declares a @QueryParams so the spec carries in:query parameters.
	mustWrite(t, root, "controllers/public.controller.js", controllerFixture(
		"PublicController",
		`{ __palbase: 'controller', basePath: '', defaultAuth: false }`,
		`[
      { method: 'GET', subpath: '/public', fnName: 'list',
        options: {},
        params: [ { index: 0, kind: 'query', schema: `+fakeZod+` } ] },
    ]`,
		`
  async list() { return { public: true }; }`,
		`{}`,
	))

	// A class named exactly "Controller" derives an EMPTY controllerName
	// (strip-one-suffix leaves nothing) → the operationId must fall back to the
	// FLAT verb-prefixed derivation, mirroring extract_meta.js + generator.go.
	mustWrite(t, root, "controllers/legacy.controller.js", controllerFixture(
		"Controller",
		`{ __palbase: 'controller', basePath: '/legacy', defaultAuth: false }`,
		`[
      { method: 'GET', subpath: '/', fnName: 'list', options: {}, params: [] },
    ]`,
		`
  async list() { return { legacy: true }; }`,
		`{}`,
	))

	// A controller that throws on require must be SKIPPED, not break the spec.
	mustWrite(t, root, "controllers/broken.controller.js", `throw new Error('boom on require');`)

	// A file whose default export is NOT a controller class must be SKIPPED and
	// must not leak any route into the spec.
	mustWrite(t, root, "controllers/notacontroller.js", `module.exports.default = { handler: async () => ({}) };`)

	writeZodToJSONStub(t, root)

	// Copy the embedded dev-server (+ its module-clients.js sibling) to a temp
	// dir, exactly like newDevCmd does.
	devDir := t.TempDir()
	if err := extractFS(devServerFS, "devjs", devDir); err != nil {
		t.Fatalf("extract dev server: %v", err)
	}

	port := freeTCPPort(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	if createOp["operationId"] != "todos.create" {
		t.Fatalf("operationId = %v, want todos.create (dotted: TodosController → todos)", createOp["operationId"])
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
	// No inferred throws meta on this route → NO declared 409 + NO
	// x-palbase-errors (typed errors are emitted only for routes carrying
	// recordThrows metadata — see TestServeOpenAPISpec_TypedErrors).
	if resps["409"] != nil {
		t.Fatalf("unexpected declared error 409 (route has no throws meta): %v", resps)
	}
	if createOp["x-palbase-errors"] != nil {
		t.Fatalf("x-palbase-errors must NOT be emitted for a route without throws meta")
	}

	// explicit auth:false (via @Controller defaultAuth:false cascading to the
	// route): GET /public → security [{}], NO 401, and an in:query parameter.
	publicOp := operationAt(t, paths, "/public", "get")
	if publicOp["operationId"] != "public.list" {
		t.Fatalf("operationId = %v, want public.list (dotted: PublicController → public)", publicOp["operationId"])
	}
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
	// @QueryParams → an in:query parameter (the stub schema exposes a `title` prop).
	if !hasParameterIn(publicOp, "query") {
		t.Fatalf("@QueryParams route missing in:query parameters: %v", publicOp["parameters"])
	}

	// {id} → {id} path key + dotted operationId; secure-by-default (auth
	// omitted). Full path = basePath /todos + subpath /{id} → /todos/{id}. The
	// @Param("id") synthesizes an in:path parameter named "id".
	byIdOp := operationAt(t, paths, "/todos/{id}", "get")
	if byIdOp["operationId"] != "todos.byId" {
		t.Fatalf("operationId = %v, want todos.byId (dotted: TodosController → todos)", byIdOp["operationId"])
	}
	assertAuthRequiredSecurity(t, byIdOp)
	if !hasParameterNamedIn(byIdOp, "id", "path") {
		t.Fatalf("@Param('id') route missing in:path parameter 'id': %v", byIdOp["parameters"])
	}

	// A class named exactly "Controller" → controllerName "" → FLAT fallback
	// operationId (getLegacy), never ".list" or a bare dot-id.
	legacyOp := operationAt(t, paths, "/legacy", "get")
	if legacyOp["operationId"] != "getLegacy" {
		t.Fatalf("operationId = %v, want getLegacy (flat fallback for empty controllerName)", legacyOp["operationId"])
	}
}

// hasParameterIn reports whether the operation declares at least one parameter
// with the given `in` location.
func hasParameterIn(op map[string]any, where string) bool {
	params, _ := op["parameters"].([]any)
	for _, p := range params {
		pm, _ := p.(map[string]any)
		if pm != nil && pm["in"] == where {
			return true
		}
	}
	return false
}

// hasParameterNamedIn reports whether the operation declares a parameter with
// the given name AND `in` location.
func hasParameterNamedIn(op map[string]any, name, where string) bool {
	params, _ := op["parameters"].([]any)
	for _, p := range params {
		pm, _ := p.(map[string]any)
		if pm != nil && pm["name"] == name && pm["in"] == where {
			return true
		}
	}
	return false
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
	// Generous deadline: the dev-server esbuild-bundles controllers/ at boot
	// before serving (a cold `npx esbuild` under concurrent test load can take
	// several seconds). The success path returns the moment it's listening.
	deadline := time.Now().Add(45 * time.Second)
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

// TestServeQueryAndParamDispatch verifies the class-controller positional param
// injection end-to-end: a @QueryParams param receives the parsed URL query and
// a @Param param receives the matched path segment, both injected by index. The
// fixture stamps the registry symbols on a class (the real load path) and the
// methods echo back what they received so the test asserts the injected values.
//
//	GET /echo/abc?name=joe → method(query, id) → { name:'joe', id:'abc' }
func TestServeQueryAndParamDispatch(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}

	root := t.TempDir()

	// A controller whose method declares @QueryParams (index 0) + @Param("id")
	// (index 1). The fakeZod query schema passes the raw query through. basePath "/echo",
	// subpath "/{id}" → full path /echo/{id}; auth:false via defaultAuth. Path
	// params use the canonical BRACE form ({id}) the dev-server matches — the
	// dispatcher's urlToRegex turns a wholly-{name} segment into a capture group
	// (a legacy :id segment is matched LITERALLY → never matches /echo/abc → 404).
	mustWrite(t, root, "controllers/echo.controller.js", controllerFixture(
		"EchoController",
		`{ __palbase: 'controller', basePath: '/echo', defaultAuth: false }`,
		`[
      { method: 'GET', subpath: '/{id}', fnName: 'echo',
        options: {},
        params: [
          { index: 0, kind: 'query', schema: `+fakeZod+` },
          { index: 1, kind: 'param', name: 'id' },
        ] },
    ]`,
		`
  async echo(query, id) { return { name: query.name, id: id }; }`,
		`{}`,
	))

	writeBackendStub(t, root)

	port := startDevServer(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	body := getJSONUntilReady(t, ctx, port, "/echo/abc?name=joe")
	if body["name"] != "joe" {
		t.Fatalf("@QueryParams injection: name = %v, want joe (body %v)", body["name"], body)
	}
	if body["id"] != "abc" {
		t.Fatalf("@Param injection: id = %v, want abc (body %v)", body["id"], body)
	}
}

// TestServeControllerDispatch boots the embedded dev-server against a
// class-controller fixture (the registry symbols stamped on a class) and asserts
// the dev-server actually DISPATCHES a request by calling the class method:
//
//	GET /todos → 200 with the method's output {items:[...]}
//
// This is the controller→registry→method-call path (not just the openapi
// shape): basePath "/todos" + subpath "/" composes to /todos, the GET verb
// matches, the route is found by fnName, the class is instantiated once, and the
// method runs with `this` bound (the @Req param hands the whole request through
// so the test can read req.method). The route is auth:false (defaultAuth) so no
// palauth round-trip is needed. A minimal @palbase/backend stub on NODE_PATH
// satisfies installRuntime()'s __setRuntime call.
func TestServeControllerDispatch(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}

	root := t.TempDir()

	// A class controller stamped with the registry symbols. The method declares a
	// @Req param (index 0) so the dev-server injects the whole request and the
	// test can assert req.method threaded through. `this.first` exercises the
	// cached-instance `this` binding.
	mustWrite(t, root, "controllers/todos.controller.js", controllerFixture(
		"TodosController",
		`{ __palbase: 'controller', basePath: '/todos', defaultAuth: false }`,
		`[
      { method: 'GET', subpath: '/', fnName: 'list',
        options: {},
        params: [ { index: 0, kind: 'req' } ] },
    ]`,
		`
  constructor() { this.title = 'first'; }
  async list(req) { return { items: [{ id: 't1', title: this.title }], gotMethod: req.method }; }`,
		`{}`,
	))

	writeBackendStub(t, root)

	port := startDevServer(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Poll GET /todos until the server is up, then assert the dispatched body.
	body := getJSONUntilReady(t, ctx, port, "/todos")
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("GET /todos body items = %v, want one item", body["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != "t1" || first["title"] != "first" {
		t.Fatalf("dispatched method output = %v, want {id:t1,title:first} (this-binding broken?)", first)
	}
	// The dev-server threaded the real PBRequest (req.method) into the method.
	if body["gotMethod"] != "GET" {
		t.Fatalf("method saw req.method = %v, want GET", body["gotMethod"])
	}
}

// TestServeExtensionlessImports is the regression test for the real-project
// bug: `palbase serve` loaded controllers by require()ing the raw .ts, which
// Node's loader cannot resolve when controllers use the canonical EXTENSIONLESS
// relative imports (`import x from "../services/foo"`). Result: every controller
// failed to load and 0 routes registered.
//
// The fixture is the REAL class-controller shape — .ts files with @Controller/
// @Get/@Req decorators and extensionless imports across controllers → services:
//
//	controllers/todos.controller.ts import "../services/todo.service" (extensionless)
//	services/todo.service.ts
//	controllers/health.controller.ts (no import)
//
// The dev-server must esbuild-bundle these (resolving the extensionless .ts
// imports + applying experimentalDecorators from the fixture tsconfig.json +
// keeping @palbase/backend external) exactly as deploy does, then register +
// dispatch by calling the class method. We assert BOTH routes register and
// return 200 with the method output that flows through the imported service. The
// backend stub implements the real decorators (stamping the registry symbols).
func TestServeExtensionlessImports(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}

	root := t.TempDir()

	// experimentalDecorators tsconfig — esbuild auto-discovers it from the entry
	// file's directory and lowers the @Controller/@Get decorators.
	mustWrite(t, root, "tsconfig.json", `{ "compilerOptions": { "experimentalDecorators": true, "target": "es2022", "module": "esnext" } }`)

	// A service imported by the controller — extensionless import.
	mustWrite(t, root, "services/todo.service.ts", `
export function listTodos() {
  return [{ id: "t1", title: "first" }];
}
`)

	// Response models — the canonical authoring shape: a named exported zod const
	// the controller imports and names as the method's RETURN TYPE. The return-type
	// stager (return_types.js, shared verbatim with deploy) reads that annotation
	// and binds the schema as the route's response — there is no @Returns anymore,
	// and a route method with NO return type is a HARD build error. (`z.infer` is
	// type-only and erased by esbuild; the const is the runtime value the stager
	// binds.)
	mustWrite(t, root, "models/todos/list.ts", `
import { z } from "@palbase/backend";

export const TodosListResponse = z.object({
  items: z.array(z.object({ id: z.string(), title: z.string() })),
  gotMethod: z.string(),
});
export type TodosListResponse = z.infer<typeof TodosListResponse>;
`)
	mustWrite(t, root, "models/health/check.ts", `
import { z } from "@palbase/backend";

export const HealthStatus = z.object({ status: z.string() });
export type HealthStatus = z.infer<typeof HealthStatus>;
`)

	// Controller imports the service WITHOUT a .ts/.js extension (canonical). The
	// method declares @Req so the dev-server injects the whole request (req.method
	// echoes back) and names its response via the imported model schema (the new
	// return-type contract). auth:false via @Controller defaultAuth.
	mustWrite(t, root, "controllers/todos.controller.ts", `
import { Controller, Get, Req } from "@palbase/backend";
import { listTodos } from "../services/todo.service";
import { TodosListResponse } from "../models/todos/list";

@Controller("/todos", { auth: false })
export default class TodosController {
  @Get("")
  list(@Req() req: any): TodosListResponse {
    return { items: listTodos(), gotMethod: req.method };
  }
}
`)

	// A second controller (no service), also class-shaped, with its own named
	// return-type model.
	mustWrite(t, root, "controllers/health.controller.ts", `
import { Controller, Get } from "@palbase/backend";
import { HealthStatus } from "../models/health/check";

@Controller("/health", { auth: false })
export default class HealthController {
  @Get("")
  check(): HealthStatus {
    return { status: "ok" };
  }
}
`)

	writeBackendStub(t, root)

	port := startDevServer(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// GET /todos → 200, output flows through the imported service.
	todos := getJSONUntilReady(t, ctx, port, "/todos")
	items, _ := todos["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("GET /todos items = %v, want one (extensionless service import failed?)", todos["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != "t1" || first["title"] != "first" {
		t.Fatalf("GET /todos output = %v, want {id:t1,title:first}", first)
	}
	if todos["gotMethod"] != "GET" {
		t.Fatalf("method saw req.method = %v, want GET", todos["gotMethod"])
	}

	// GET /health → 200 (basePath "/health" + subpath "" → /health).
	health := getJSONUntilReady(t, ctx, port, "/health")
	if health["status"] != "ok" {
		t.Fatalf("GET /health = %v, want {status:ok}", health)
	}
}

// TestServeQueryMethodRoute locks the QUERY method (RFC 10008, @Query method
// decorator) through the serve path end-to-end:
//   - return_types.js + throw_analysis.js (devjs twins of the deploy copies)
//     must recognize @Query as a route decorator — dropping 'Query' from their
//     method-name lists SILENTLY loses the 200 response schema and the inferred
//     error set for QUERY routes (the spec op still appears, just hollow)
//   - the dispatcher routes a REAL QUERY request and carries its JSON body into
//     @Body (QUERY is body-carrying like POST, safe+idempotent like GET)
//   - the CORS preflight offers QUERY (a browser fetch always preflights a
//     custom method, so a missing entry kills QUERY from web apps before dispatch)
func TestServeQueryMethodRoute(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}

	root := t.TempDir()
	mustWrite(t, root, "tsconfig.json", `{ "compilerOptions": { "experimentalDecorators": true, "target": "es2022", "module": "esnext" } }`)

	mustWrite(t, root, "models/todos/search.ts", `
import { z } from "@palbase/backend";

export const TodosSearchResponse = z.object({
  items: z.array(z.object({ id: z.string(), title: z.string() })),
  filter: z.string(),
});
export type TodosSearchResponse = z.infer<typeof TodosSearchResponse>;
`)

	// The QUERY route: input arrives in the BODY (@Body — the RFC rule), the
	// return type is a named zod schema (return_types.js must treat @Query as a
	// route to bind it), and the body throws a built-in on missing input
	// (throw_analysis.js must treat @Query as a route to record it).
	mustWrite(t, root, "controllers/search.controller.ts", `
import { Controller, Query, Body, NotFound } from "@palbase/backend";
import { TodosSearchResponse } from "../models/todos/search";

@Controller("/todos", { auth: false })
export default class SearchController {
  @Query("/search")
  search(@Body() input: any): Promise<TodosSearchResponse> {
    if (!input) throw new NotFound();
    return Promise.resolve({ items: [{ id: "t1", title: "first" }], filter: input.q });
  }
}
`)

	writeZodToJSONStub(t, root)
	writeBackendStub(t, root)

	port := startDevServer(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Spec: the op sits under the lowercased method key ("query") and carries
	// BOTH analyzer outputs — the return-type-bound 200 schema and the inferred
	// 404 + x-palbase-errors.
	spec := fetchSpecUntilReady(t, ctx, port)
	paths, _ := spec["paths"].(map[string]any)
	op := operationAt(t, paths, "/todos/search", "query")
	ok200, _ := op["responses"].(map[string]any)["200"].(map[string]any)
	content, _ := ok200["content"].(map[string]any)
	if content["application/json"] == nil {
		t.Fatalf("QUERY op lost its return-type 200 schema (return_types.js @Query recognition): %v", op["responses"])
	}
	resps, _ := op["responses"].(map[string]any)
	if resps["404"] == nil || op["x-palbase-errors"] == nil {
		t.Fatalf("QUERY op lost its inferred NotFound (throw_analysis.js @Query recognition): %v", op)
	}

	// Dispatch: a real QUERY request with a JSON body → 200, the body flows
	// through @Body into the method.
	qURL := fmt.Sprintf("http://127.0.0.1:%d/todos/search", port)
	req, err := http.NewRequestWithContext(ctx, "QUERY", qURL, strings.NewReader(`{"q":"milk"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("QUERY dispatch: %v", err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("QUERY /todos/search = %d, want 200 (body %s)", resp.StatusCode, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode QUERY response: %v\nbody: %s", err, data)
	}
	if out["filter"] != "milk" {
		t.Fatalf("@Body did not flow into the QUERY method: filter = %v (body %s)", out["filter"], data)
	}

	// Preflight: Access-Control-Allow-Methods must offer QUERY.
	pre, err := http.NewRequestWithContext(ctx, http.MethodOptions, qURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	pre.Header.Set("Origin", "http://localhost:3000")
	pre.Header.Set("Access-Control-Request-Method", "QUERY")
	preResp, err := http.DefaultClient.Do(pre)
	if err != nil {
		t.Fatalf("OPTIONS preflight: %v", err)
	}
	preResp.Body.Close()
	if allow := preResp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(allow, "QUERY") {
		t.Fatalf("preflight Access-Control-Allow-Methods = %q, must offer QUERY", allow)
	}
}

// TestServeDottedIdSurvivesBundleNameCollision pins the --keep-names
// requirement on the dev-server's esbuild bundling (prod parity — the deploy
// bundler carries it as a correctness flag): when a controller class and a
// transitively-imported service class share a NAME, esbuild's scope hoisting
// renames the controller's binding (ClashController → ClashController2).
// Without --keep-names the live Ctrl.name is the RENAMED identifier and the
// dotted operationId silently becomes clashController2.status locally while
// the deployed pod emits clash.status — codegen output would differ between
// serve and prod. --keep-names preserves `class.name`, so local spec == prod.
func TestServeDottedIdSurvivesBundleNameCollision(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}

	root := t.TempDir()
	mustWrite(t, root, "tsconfig.json", `{ "compilerOptions": { "experimentalDecorators": true, "target": "es2022", "module": "esnext" } }`)

	// A service class with the SAME name as the controller class — the bundle
	// scope collision that triggers esbuild's rename.
	mustWrite(t, root, "services/clash.service.ts", `
export class ClashController {
  tag(): string { return "service"; }
}
`)
	mustWrite(t, root, "models/clash/status.ts", `
import { z } from "@palbase/backend";

export const ClashStatus = z.object({ tag: z.string() });
export type ClashStatus = z.infer<typeof ClashStatus>;
`)
	mustWrite(t, root, "controllers/clash.controller.ts", `
import { Controller, Get } from "@palbase/backend";
import { ClashController as ServiceClash } from "../services/clash.service";
import { ClashStatus } from "../models/clash/status";

@Controller("/clash", { auth: false })
export default class ClashController {
  @Get("")
  status(): ClashStatus {
    return { tag: new ServiceClash().tag() };
  }
}
`)

	writeBackendStub(t, root)

	port := startDevServer(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	spec := fetchSpecUntilReady(t, ctx, port)
	paths, _ := spec["paths"].(map[string]any)
	op := operationAt(t, paths, "/clash", "get")
	if op["operationId"] != "clash.status" {
		t.Fatalf("operationId = %v, want clash.status — a bundle class-name collision leaked the renamed identifier into the dotted id (is --keep-names on bundleSrcDir?)", op["operationId"])
	}
}

// TestBundleSrcDirKeepsNames is the always-running twin of
// TestServeDottedIdSurvivesBundleNameCollision (which needs a Node toolchain
// and skips only without one): the embedded dev-server's
// bundleSrcDir esbuild invocation MUST carry --keep-names — prod parity with
// the deploy bundler, whose bundler_test pins the same flag on its args. The
// dotted operationId namespace derives from the live Ctrl.name, which only
// survives esbuild scope-hoisting renames under --keep-names.
func TestBundleSrcDirKeepsNames(t *testing.T) {
	src, err := devServerFS.ReadFile("devjs/dev-server.js")
	if err != nil {
		t.Fatalf("read embedded dev-server.js: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "function bundleSrcDir")
	if start < 0 {
		t.Fatal("bundleSrcDir not found in embedded dev-server.js")
	}
	fn := body[start:]
	if end := strings.Index(fn, "\nfunction "); end >= 0 {
		fn = fn[:end]
	}
	if !strings.Contains(fn, "'--keep-names'") {
		t.Fatal("bundleSrcDir esbuild args must include --keep-names (dotted-id parity: Ctrl.name must survive bundle scope-hoisting renames)")
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
	// Self-sufficient fixture: the return-type stager needs `typescript`, the
	// bundler needs `esbuild` — seed BOTH into <root>/node_modules. Seeding only
	// typescript left a node_modules without esbuild, which broke the
	// dev-server's `npx --yes esbuild` resolution on hosts where npx wouldn't
	// auto-install (npm treats the fixture as the local project and stops
	// there) — and on hosts without a resolvable typescript the tests silently
	// skipped. With both seeded, npx prefers the LOCAL bin: deterministic, no
	// network, same production resolution path.
	seedNodePkg(t, root, "typescript")
	seedEsbuild(t, root)
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

// ── self-sufficient fixture deps (typescript + esbuild) ─────────────────────
//
// The dev-server needs two REAL npm packages at runtime: `typescript` (the
// return-type stager parses controllers with it) and `esbuild` (controller
// bundling via `npx --yes esbuild`). Both are seeded into the fixture's
// node_modules so a serve test is hermetic and runs on ANY host with a Node
// toolchain. (Seeding typescript alone created a fixture node_modules WITHOUT
// esbuild — npm then treats the fixture as the local project and, on hosts
// where npx doesn't fall through to an install, `npx esbuild` exits 127; on
// hosts without a resolvable typescript the tests silently skipped. They were
// host-dependent either way.)
//
// Per package, resolution order: the host's own install (Node's resolver from
// the test's cwd), else a one-time `npm i --no-save` into a cached prefix
// under os.TempDir() reused across runs. Only a genuinely missing toolchain
// (no node / no npm) skips; an install failure FAILS the test — the host
// claims the capability, so the error is actionable, not skippable.

var (
	testDepMu   sync.Mutex
	testDepDirs = map[string]string{}
)

// hostNodePkgDir returns the package ROOT directory of an installed npm
// package, resolving the host's own install first and falling back to the
// cached test-dep prefix. Results are memoized for the test run.
func hostNodePkgDir(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	testDepMu.Lock()
	defer testDepMu.Unlock()
	if dir, ok := testDepDirs[name]; ok {
		return dir
	}
	// 1) Host-resolvable install. Prefer <name>/package.json (gives the pkg
	// root directly); when an exports map blocks that subpath, resolve the main
	// entry and walk up to the directory holding package.json.
	resolve := fmt.Sprintf(`
const path = require('path'), fs = require('fs');
let d;
try { d = path.dirname(require.resolve(%[1]q + '/package.json')); }
catch {
  d = path.dirname(require.resolve(%[1]q));
  while (!fs.existsSync(path.join(d, 'package.json'))) d = path.dirname(d);
}
process.stdout.write(d);`, name)
	// TypeScript's unversioned package is now 7.x, while generated Palbase
	// backends intentionally support ^5. Always seed that dependency from the
	// pinned-major cache; a host-global 7.x install must not make tests drift.
	if name != "typescript" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "node", "-e", resolve).Output(); err == nil && len(out) > 0 {
			testDepDirs[name] = string(out)
			return testDepDirs[name]
		}
	}
	// 2) Cached prefix install — downloads once per machine, reused across runs.
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
	prefix := filepath.Join(os.TempDir(), "palbase-cli-testdeps", name)
	pkgDir := filepath.Join(prefix, "node_modules", name)
	if _, err := os.Stat(filepath.Join(pkgDir, "package.json")); err != nil {
		ictx, icancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer icancel()
		installSpec := name
		if name == "typescript" {
			// The generated backend template supports TypeScript 5 (`^5`). The
			// unversioned npm tag now resolves to TypeScript 7, whose package root
			// intentionally exposes only version metadata instead of the parser API
			// used by return_types.js and throw_analysis.js. Keep this hermetic
			// fixture on the same supported major as real Palbase projects.
			installSpec = "typescript@^5"
		}
		cmd := exec.CommandContext(ictx, "npm", "i", "--no-save", "--prefix", prefix, installSpec)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("npm i %s into test-dep cache %s: %v\n%s", name, prefix, err, out)
		}
	}
	testDepDirs[name] = pkgDir
	return pkgDir
}

// seedNodePkg symlinks an installed npm package into <root>/node_modules/<name>
// so the dev-server (cwd + NODE_PATH resolution) finds it. No-op when the
// fixture already provides one (e.g. a stub written by the test).
func seedNodePkg(t *testing.T, root, name string) string {
	t.Helper()
	dst := filepath.Join(root, "node_modules", name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if _, err := os.Lstat(dst); err == nil {
		return dst // already present (e.g. a stub written by the fixture)
	}
	if err := os.Symlink(hostNodePkgDir(t, name), dst); err != nil {
		t.Fatalf("link %s into fixture: %v", name, err)
	}
	return dst
}

// seedEsbuild seeds the real esbuild package AND a node_modules/.bin/esbuild
// launcher into the fixture so the dev-server's `npx --yes esbuild` resolves
// the LOCAL install — npx prefers a project-local bin: deterministic, no
// network, and the production resolution path stays exercised. The .bin entry
// points at the package's own bin/esbuild (the native binary after a normal
// npm install, or the JS shim — both runnable); the platform package
// (@esbuild/<os>-<arch>) resolves from the REAL package directory through the
// symlink (Node realpaths it), so it needs no seeding of its own.
func seedEsbuild(t *testing.T, root string) {
	t.Helper()
	pkg := seedNodePkg(t, root, "esbuild")
	binDir := filepath.Join(root, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir node_modules/.bin: %v", err)
	}
	dst := filepath.Join(binDir, "esbuild")
	if _, err := os.Lstat(dst); err == nil {
		return
	}
	if err := os.Symlink(filepath.Join(pkg, "bin", "esbuild"), dst); err != nil {
		t.Fatalf("link esbuild bin into fixture: %v", err)
	}
}

// getJSONUntilReady polls GET <path> until the dev-server returns 200 (or the
// deadline hits), then decodes the JSON body. Used by the dispatch test to wait
// for boot AND assert the handler's output in one shot.
func getJSONUntilReady(t *testing.T, ctx context.Context, port int, path string) map[string]any {
	t.Helper()
	u := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	// Generous deadline: the dev-server now esbuild-bundles controllers/ at boot
	// (a cold `npx esbuild` under concurrent test load can take several seconds),
	// so a tight bound would flake. The success path returns as soon as it's up.
	deadline := time.Now().Add(45 * time.Second)
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

// writeBackendStub installs a minimal but FAITHFUL @palbase/backend on the
// fixture's node_modules. It is kept external by the dev-server's esbuild
// bundling (just like the real package), so a fixture controller can `import
// { Controller, Get, Post, Query, Body, QueryParams, Param, User, Req, Returns }
// from "@palbase/backend"` exactly as real class-controller code does. The decorators
// stamp the SAME runtime registry symbols the compiled SDK produces
// (Symbol.for("palbase.backend.routes") array, controllerMeta, returnBuffer),
// so the dev-server's symbol-fallback load path is exercised end-to-end. The
// runtime/Resource hooks satisfy installRuntime() + bootResources() without a
// real install.
func writeBackendStub(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, root, "node_modules/@palbase/backend/package.json",
		`{"name":"@palbase/backend","version":"0.0.0-test","main":"index.js"}`)
	mustWrite(t, root, "node_modules/@palbase/backend/index.js", `
'use strict';
// Faithful mini-SDK: the decorators accumulate route + param + return metadata
// per class then stamp the registry symbols the dev-server (and worker.js) read.
const ROUTES = Symbol.for('palbase.backend.routes');
const RETBUF = Symbol.for('palbase.backend.returnBuffer');
const CTRLMETA = Symbol.for('palbase.backend.controllerMeta');
const PARAMBUF = Symbol.for('palbase.backend.paramBuffer');

// Per-method param buffers live on the prototype during decoration (parameter
// decorators run before the method decorator), keyed by fnName.
function paramBufOf(proto) {
  if (!proto[PARAMBUF]) proto[PARAMBUF] = {};
  return proto[PARAMBUF];
}
function paramDecorator(kind) {
  return (schema) => (proto, propertyKey, index) => {
    const buf = paramBufOf(proto);
    if (!buf[propertyKey]) buf[propertyKey] = [];
    const entry = { index, kind };
    if (schema && typeof schema === 'object') entry.schema = schema;
    buf[propertyKey].push(entry);
  };
}
// @Param("name") takes a string name, not a schema.
const Param = (name) => (proto, propertyKey, index) => {
  const buf = paramBufOf(proto);
  if (!buf[propertyKey]) buf[propertyKey] = [];
  buf[propertyKey].push({ index, kind: 'param', name });
};
const Body = paramDecorator('body');
const QueryParams = paramDecorator('query');
const Headers = paramDecorator('headers');
const User = () => paramDecorator('user')();
const OptionalUser = () => paramDecorator('optionalUser')();
const Client = () => paramDecorator('client')();
const RequestId = () => paramDecorator('requestId')();
const TraceId = () => paramDecorator('traceId')();
const Req = () => paramDecorator('req')();

function methodDecorator(method) {
  return (subpath, options) => (proto, propertyKey) => {
    const Ctrl = proto.constructor;
    if (!Ctrl[ROUTES]) Object.defineProperty(Ctrl, ROUTES, { value: [], enumerable: false, writable: true, configurable: true });
    const params = (proto[PARAMBUF] && proto[PARAMBUF][propertyKey]) || [];
    Ctrl[ROUTES].push({
      method, subpath: subpath || '', fnName: propertyKey,
      options: options || {},
      params: params.slice().sort((a, b) => a.index - b.index),
    });
  };
}
const Get = methodDecorator('GET');
const Post = methodDecorator('POST');
const Put = methodDecorator('PUT');
const Patch = methodDecorator('PATCH');
const Delete = methodDecorator('DELETE');
const Query = methodDecorator('QUERY');

// @Returns(schema) buffers the return schema by fnName.
const Returns = (schema) => (proto, propertyKey) => {
  const Ctrl = proto.constructor;
  if (!Ctrl[RETBUF]) Object.defineProperty(Ctrl, RETBUF, { value: {}, enumerable: false, writable: true, configurable: true });
  Ctrl[RETBUF][propertyKey] = schema;
};

// @Controller(basePath, options?) stamps __palbase + controllerMeta.
function Controller(basePath, options) {
  return (Ctrl) => {
    Ctrl.__palbase = 'controller';
    const meta = { __palbase: 'controller', basePath: basePath || '' };
    if (options && options.auth !== undefined) meta.defaultAuth = options.auth;
    Object.defineProperty(Ctrl, CTRLMETA, { value: meta, enumerable: false, writable: true, configurable: true });
    if (!Ctrl[ROUTES]) Object.defineProperty(Ctrl, ROUTES, { value: [], enumerable: false, writable: true, configurable: true });
    return Ctrl;
  };
}

// Minimal permissive zod-ish 'z' — the surface a model file uses
// (z.object/z.string/z.number/z.boolean/z.array, and the type-only z.infer,
// erased by esbuild). Each builder returns a schema carrying a '_def' object
// (so zodToJSON's 'typeof z._def === object' guard passes → the openapi body
// converts) PLUS a permissive safeParse (so request/output validation always
// passes through, the same philosophy as fakeZod). The return-type stager
// (return_types.js) names a model's exported schema as the route's response
// type and injects 'z.array(Schema)'/'Schema' bindings; this 'z' makes those
// bindings real runtime values so a controller authored the canonical way
// (named-zod return type) loads + dispatches under serve exactly as on deploy.
function makeSchema(typeName) {
  return { _def: { typeName }, safeParse: (v) => ({ success: true, data: v }) };
}
const z = {
  object: () => makeSchema('ZodObject'),
  string: () => makeSchema('ZodString'),
  number: () => makeSchema('ZodNumber'),
  boolean: () => makeSchema('ZodBoolean'),
  array: () => makeSchema('ZodArray'),
};

// recordThrows is the stager-injected carrier for inferred throw descriptors
// (the appended IIFE calls require("@palbase/backend").recordThrows(...)). The
// IIFE always runs AFTER the decorators, so the route entry already exists —
// no buffered-ordering fallback needed here, unlike the real SDK.
const recordThrows = (proto, fnName, throwsArr) => {
  const Ctrl = proto.constructor;
  const route = (Ctrl[ROUTES] || []).find((r) => r.fnName === fnName);
  if (route) route.throws = throwsArr;
};

// The SDK's named throw classes — one built-in is enough for fixtures. The
// error registry mirrors the real getErrorRegistry surface so throw-inferred
// codes join to spec responses exactly as on deploy.
class NotFound extends Error {
  constructor(message) { super(message || 'not found'); this.status = 404; this.error = 'not_found'; }
}
const errorRegistry = new Map([
  ['not_found', { code: 'not_found', status: 404, className: 'NotFound', builtin: true }],
]);

class Resource {}
const registry = [];
module.exports = {
  Controller, Get, Post, Put, Patch, Delete, Query,
  Body, QueryParams, Headers, Param, User, OptionalUser, Client, RequestId, TraceId, Req, Returns,
  NotFound, recordThrows,
  getErrorRegistry: () => errorRegistry,
  z, Resource,
  // Runtime seam: record the installed services (fixtures return literals, so a
  // no-op store is enough).
  __setRuntime(services) { module.exports.__runtime = services; },
  __registerResource(r) { registry.push(r); },
  async __runResourceBoot() {},
  async __shutdownResources() { registry.length = 0; },
};
`)
}

// mustWrite writes body to root/rel, creating parent dirs. Shared by the
// serve fixtures that materialise a temp controllers/ tree.
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

// writePalbaseBackendStub installs a minimal @palbase/backend with ONLY
// getErrorRegistry — the dev-server's typed-error join surface. getRoutes is
// deliberately absent so route loading exercises the raw-symbol fallback the
// fixtures stamp. The registry carries one defineError-style entry (with a
// fake zod dataSchema the zod-to-json-schema stub converts) and one built-in.
func writePalbaseBackendStub(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, root, "node_modules/@palbase/backend/package.json",
		`{"name":"@palbase/backend","version":"0.0.0-test","main":"index.js"}`)
	mustWrite(t, root, "node_modules/@palbase/backend/index.js", `
'use strict';
const registry = new Map();
registry.set('room_locked', {
  code: 'room_locked', status: 409, className: 'RoomLocked', builtin: false,
  dataSchema: { _def: { typeName: 'ZodObject' } },
});
registry.set('not_found', { code: 'not_found', status: 404, className: 'NotFound', builtin: true });
module.exports = {
  getErrorRegistry: () => registry,
  // dispatch installs the runtime clients into the SDK on first request —
  // a no-op here (the typed-errors test only exercises spec + guard).
  __setRuntime: () => {},
};
`)
}

// TestServeOpenAPISpec_TypedErrors locks the dev-server's typed-error spec
// twin against the prod generator.go byte-shape: a route carrying recordThrows
// metadata emits x-palbase-errors (v1) + per-status envelope responses; an
// unknown code is skipped silently; an analyzed-EMPTY route emits nothing.
func TestServeOpenAPISpec_TypedErrors(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}

	root := t.TempDir()
	seedEsbuild(t, root)

	// rooms controller: create carries inferred throws (one defineError'd code
	// with data, one built-in, one GHOST code absent from the registry → must
	// be skipped silently). list is analyzed-empty (throws: []) → guard armed
	// at runtime but NO spec surface.
	mustWrite(t, root, "controllers/rooms.controller.js", controllerFixture(
		"RoomsController",
		`{ __palbase: 'controller', basePath: '/rooms', defaultAuth: false }`,
		`[
      { method: 'POST', subpath: '/create', fnName: 'create', options: {}, params: [],
        throws: [
          { name: 'RoomLocked', code: 'room_locked' },
          { name: 'NotFound', code: 'not_found' },
          { name: 'Ghost', code: 'ghost_code' },
        ] },
      { method: 'GET', subpath: '/list', fnName: 'list', options: {}, params: [], throws: [] },
      { method: 'POST', subpath: '/boom', fnName: 'boom', options: {}, params: [], throws: [] },
      { method: 'POST', subpath: '/gone', fnName: 'gone', options: {}, params: [], throws: [] },
    ]`,
		`
  async create() { return { ok: true }; }
  async list() { return { rooms: [] }; }
  async boom() { const e = new Error('whoops'); e.status = 409; e.error = 'mystery_code'; throw e; }
  async gone() { const e = new Error('gone'); e.status = 404; e.error = 'not_found'; throw e; }`,
		`{}`,
	))

	writeZodToJSONStub(t, root)
	writePalbaseBackendStub(t, root)

	devDir := t.TempDir()
	if err := extractFS(devServerFS, "devjs", devDir); err != nil {
		t.Fatalf("extract dev server: %v", err)
	}

	port := freeTCPPort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, nodeBin, filepath.Join(devDir, "dev-server.js"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PALBASE_DEV_PORT=%d", port),
		fmt.Sprintf("PALBASE_DEV_ROOT=%s", root),
		fmt.Sprintf("NODE_PATH=%s", filepath.Join(root, "node_modules")),
		"PALBASE_PROJECT_REF=local",
	)
	var serveErr syncBuffer
	cmd.Stdout = os.Stderr
	cmd.Stderr = &serveErr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dev-server: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	spec := fetchSpecUntilReady(t, ctx, port)
	paths, _ := spec["paths"].(map[string]any)
	if paths == nil {
		t.Fatalf("paths missing: %v", spec)
	}

	createOp := operationAt(t, paths, "/rooms/create", "post")

	// x-palbase-errors — BYTE-shape lock vs generator.go (encoding/json sorts
	// map keys, so re-marshaling the parsed map reproduces Go's exact bytes;
	// the expected literal is copied from the prod generator_test golden form).
	extJSON, err := json.Marshal(createOp["x-palbase-errors"])
	if err != nil {
		t.Fatalf("marshal extension: %v", err)
	}
	wantExt := `{"notFound":{"code":"not_found","description":"","hasData":false,"status":404},` +
		`"roomLocked":{"code":"room_locked","description":"","hasData":true,"status":409}}`
	if string(extJSON) != wantExt {
		t.Fatalf("x-palbase-errors byte-shape drift\n got: %s\nwant: %s", extJSON, wantExt)
	}

	resps, _ := createOp["responses"].(map[string]any)
	conflictJSON, err := json.Marshal(resps["409"])
	if err != nil {
		t.Fatalf("marshal 409: %v", err)
	}
	// The data schema is the zod-to-json-schema STUB's fixed output.
	want409 := `{"content":{"application/json":{"schema":` +
		`{"properties":{` +
		`"data":{"properties":{"title":{"type":"string"}},"required":["title"],"type":"object"},` +
		`"error":{"const":"room_locked"},` +
		`"error_description":{"type":"string"},` +
		`"request_id":{"type":"string"},` +
		`"status":{"type":"integer"}},` +
		`"required":["error","error_description","status"],"type":"object"}}},` +
		`"description":"Error: room_locked"}`
	if string(conflictJSON) != want409 {
		t.Fatalf("409 envelope byte-shape drift\n got: %s\nwant: %s", conflictJSON, want409)
	}
	if resps["404"] == nil {
		t.Fatalf("built-in not_found must emit a 404 response: %v", resps)
	}
	// Ghost code skipped: exactly 2 extension keys (asserted by wantExt) and
	// no response for a status the registry never produced beyond 404/409/200/400.

	// Analyzed-empty route: no extension, no declared statuses.
	listOp := operationAt(t, paths, "/rooms/list", "get")
	if listOp["x-palbase-errors"] != nil {
		t.Fatalf("analyzed-empty route must NOT emit x-palbase-errors: %v", listOp)
	}
	listResps, _ := listOp["responses"].(map[string]any)
	if listResps["409"] != nil || listResps["404"] != nil {
		t.Fatalf("analyzed-empty route must NOT declare error statuses: %v", listResps)
	}

	// Conformance guard (worker.js twin): an analyzed route (EMPTY set) that
	// throws an unlisted code goes RED on the serve console; a built-in code
	// stays silent. The envelope must be untouched either way.
	boomResp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/rooms/boom", port), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /rooms/boom: %v\nserve stderr:\n%s", err, serveErr.String())
	}
	boomBody, _ := io.ReadAll(boomResp.Body)
	boomResp.Body.Close()
	if boomResp.StatusCode != 409 || !strings.Contains(string(boomBody), `"error":"mystery_code"`) {
		t.Fatalf("guard must not change the envelope: %d %s", boomResp.StatusCode, boomBody)
	}
	goneResp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/rooms/gone", port), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /rooms/gone: %v", err)
	}
	goneResp.Body.Close()

	wantGuard := `[palbase] RoomsController.boom threw "mystery_code" — statically invisible throw site; ` +
		`make the throw resolvable (direct throw / singleton service method) so typed clients see it`
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(serveErr.String(), wantGuard) {
		time.Sleep(50 * time.Millisecond)
	}
	stderrText := serveErr.String()
	if !strings.Contains(stderrText, wantGuard) {
		t.Fatalf("serve console missing the guard line\nwant: %s\nstderr:\n%s", wantGuard, stderrText)
	}
	if strings.Contains(stderrText, `threw "not_found"`) {
		t.Fatalf("guard must stay SILENT for built-in codes\nstderr:\n%s", stderrText)
	}
}

// syncBuffer is a mutex-guarded bytes.Buffer safe for the exec stderr pipe
// writing concurrently with the test's polling reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
