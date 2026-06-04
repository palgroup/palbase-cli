package backend

import (
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
// are real functions so dispatch runs.
func controllerFixture(metaBody, routes, methods, returnBuf string) string {
	return fmt.Sprintf(`
'use strict';
const ROUTES = Symbol.for('palbase.backend.routes');
const RETBUF = Symbol.for('palbase.backend.returnBuffer');
const CTRLMETA = Symbol.for('palbase.backend.controllerMeta');
class Ctrl {
%s
}
Ctrl.__palbase = 'controller';
Ctrl[CTRLMETA] = %s;
Ctrl[ROUTES] = %s;
Ctrl[RETBUF] = %s;
module.exports.default = Ctrl;
`, methods, metaBody, routes, returnBuf)
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
//   - a @Query route emits in:query parameters; a @Param route emits in:path
//   - the full route path is basePath + subpath (controller composition)
//   - errors are global throw classes → NO x-palbase-errors / declared statuses
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
	requireEsbuild(t)

	root := t.TempDir()

	// todos controller: basePath "/todos".
	//  - create: POST "/create" → full path /todos/create. auth required,
	//    rateLimit, @Body + @Returns (fake zod: zodToJSON only checks for a _def
	//    object; the stub converter returns a fixed schema).
	//  - byId: GET "/:id" → full path /todos/:id (→ {id}), auth omitted →
	//    secure-by-default (required), ByParam operationId, @Param("id").
	mustWrite(t, root, "controllers/todos.controller.js", controllerFixture(
		`{ __palbase: 'controller', basePath: '/todos' }`,
		`[
      { method: 'POST', subpath: '/create', fnName: 'create',
        options: { auth: { required: true }, rateLimit: { max: 5, window: 60 } },
        params: [ { index: 0, kind: 'body', schema: `+fakeZod+` } ] },
      { method: 'GET', subpath: '/:id', fnName: 'byId',
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
	// The route also declares a @Query so the spec carries in:query parameters.
	mustWrite(t, root, "controllers/public.controller.js", controllerFixture(
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
	// Errors are global throw classes now: NO declared 409 + NO x-palbase-errors.
	if resps["409"] != nil {
		t.Fatalf("unexpected declared error 409 (errors are global throw classes): %v", resps)
	}
	if createOp["x-palbase-errors"] != nil {
		t.Fatalf("x-palbase-errors must NOT be emitted (errors are global throw classes)")
	}

	// explicit auth:false (via @Controller defaultAuth:false cascading to the
	// route): GET /public → security [{}], NO 401, and an in:query parameter.
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
	// @Query → an in:query parameter (the stub schema exposes a `title` prop).
	if !hasParameterIn(publicOp, "query") {
		t.Fatalf("@Query route missing in:query parameters: %v", publicOp["parameters"])
	}

	// :id → {id} path key + ByParam operationId; secure-by-default (auth
	// omitted). Full path = basePath /todos + subpath /:id → /todos/{id}. The
	// @Param("id") synthesizes an in:path parameter named "id".
	byIdOp := operationAt(t, paths, "/todos/{id}", "get")
	if byIdOp["operationId"] != "getTodosById" {
		t.Fatalf("operationId = %v, want getTodosById", byIdOp["operationId"])
	}
	assertAuthRequiredSecurity(t, byIdOp)
	if !hasParameterNamedIn(byIdOp, "id", "path") {
		t.Fatalf("@Param('id') route missing in:path parameter 'id': %v", byIdOp["parameters"])
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
// injection end-to-end: a @Query param receives the parsed URL query and a
// @Param param receives the matched path segment, both injected by index. The
// fixture stamps the registry symbols on a class (the real load path) and the
// methods echo back what they received so the test asserts the injected values.
//
//	GET /echo/abc?name=joe → method(query, id) → { name:'joe', id:'abc' }
func TestServeQueryAndParamDispatch(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}

	root := t.TempDir()

	// A controller whose method declares @Query (index 0) + @Param("id") (index
	// 1). The fakeZod query schema passes the raw query through. basePath "/echo",
	// subpath "/:id" → full path /echo/{id}; auth:false via defaultAuth.
	mustWrite(t, root, "controllers/echo.controller.js", controllerFixture(
		`{ __palbase: 'controller', basePath: '/echo', defaultAuth: false }`,
		`[
      { method: 'GET', subpath: '/:id', fnName: 'echo',
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
		t.Fatalf("@Query injection: name = %v, want joe (body %v)", body["name"], body)
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
	// Controller imports the service WITHOUT a .ts/.js extension (canonical). The
	// method declares @Req so the dev-server injects the whole request (req.method
	// echoes back). auth:false via @Controller defaultAuth.
	mustWrite(t, root, "controllers/todos.controller.ts", `
import { Controller, Get, Req } from "@palbase/backend";
import { listTodos } from "../services/todo.service";

@Controller("/todos", { auth: false })
export default class TodosController {
  @Get("")
  list(@Req() req: any) {
    return { items: listTodos(), gotMethod: req.method };
  }
}
`)

	// A second controller (no service), also class-shaped.
	mustWrite(t, root, "controllers/health.controller.ts", `
import { Controller, Get } from "@palbase/backend";

@Controller("/health", { auth: false })
export default class HealthController {
  @Get("")
  check() {
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
	requireEsbuild(t)
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

// requireEsbuild skips the test when esbuild is not reachable via `npx --yes
// esbuild` — the dev-server now bundles controllers/ + resources/ through it
// (mirroring the deploy bundler), so a serve test cannot run without it. Kept
// cheap: a single `--version` probe with a short timeout.
func requireEsbuild(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not on PATH (esbuild bundling unavailable)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "npx", "--yes", "esbuild", "--version").Run(); err != nil {
		t.Skipf("esbuild not reachable via npx (%v)", err)
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
// { Controller, Get, Post, Body, Query, Param, User, Req, Returns } from
// "@palbase/backend"` exactly as real class-controller code does. The decorators
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
const Query = paramDecorator('query');
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

class Resource {}
const registry = [];
module.exports = {
  Controller, Get, Post, Put, Patch, Delete,
  Body, Query, Headers, Param, User, OptionalUser, Client, RequestId, TraceId, Req, Returns,
  Resource,
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
