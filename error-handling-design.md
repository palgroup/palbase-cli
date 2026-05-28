## 1. Code Inventory

Requested files read:

- `/Users/salih/workspace/backend/palbase/sdk/palbase-ts/backend/src/errors.ts`
  - `HttpError` is the only exported class. It extends `Error` and carries `status`, `error`, and `errorDescription` fields (`errors.ts:2-12`).
  - `HttpError.toJSON(requestId?)` serializes `{ error, error_description, status, request_id? }` (`errors.ts:20-29`).

- `/Users/salih/workspace/backend/palbase/sdk/palbase-ts/backend/src/endpoint.ts`
  - `EndpointContext` exposes `input`, `params`, `query`, `headers`, `user`, `method`, `endpointPath`, `file`, `db`, `env`, `log`, `cache`, `queue`, `requestId`, `projectId`, and `environmentId` (`endpoint.ts:185-214`).
  - `EndpointConfig` includes `method`, `auth`, `rateLimit`, `input`, `output`, `schema`, `middleware`, and `handler`; it does not include declared errors (`endpoint.ts:272-288`).
  - `defineEndpoint` is the typed endpoint factory and returns the config unchanged (`endpoint.ts:297-305`).

- `/Users/salih/workspace/backend/palbase/sdk/palbase-ts/backend/src/index.ts`
  - Re-exports `defineEndpoint` (`index.ts:1`), `EndpointConfig` / `EndpointContext` and related endpoint types (`index.ts:2-21`), `HttpError` (`index.ts:116`), and `z` from Zod (`index.ts:154`).

- `/Users/salih/workspace/backend/palbase/sdk/palbackend-ios-src/Sources/Palbe/Backend/PalbaseBackend.swift`
  - `PalbaseBackend.call<I,O>` posts to a named endpoint and decodes success data, throwing `BackendError` on encode/decode/RPC failures (`PalbaseBackend.swift:46-57`).
  - The no-input `call<O>` overload delegates through `EmptyInput` (`PalbaseBackend.swift:62-68`, `PalbaseBackend.swift:215-221`).
  - `invokeRPC` builds the route path, encodes the body, adds `Content-Type`, adds idempotency keys, sends the request, maps non-2xx responses with `BackendError.from`, and returns only success body bytes (`PalbaseBackend.swift:85-155`).
  - `sendRPC` maps transport failures with `BackendError.from(transport:)` (`PalbaseBackend.swift:160-176`).
  - `openAPISpec()` fetches `/openapi.json` and maps non-2xx through `BackendError.from` (`PalbaseBackend.swift:182-198`).

- `/Users/salih/workspace/backend/palbase/sdk/palbackend-ios-src/Sources/Palbe/Facade.swift`
  - Public `pb.call` overloads are extension methods on `PalBackendClient` and throw `BackendError` (`Facade.swift:21-38`).
  - Generated-code SPI `_invoke` overloads are the call seam used by generated Swift; every overload throws `BackendError` (`Facade.swift:97-149`).
  - `backendClientSPI()` throws `.notConfigured` when backend state is missing (`Facade.swift:156-158`).

- `/Users/salih/workspace/backend/palbase/sdk/cli/internal/backend/swiftemit.go`
  - `emitSwiftWithConfig` writes one generated Swift file with `@_spi(Generated) import Palbe`, request/response types, and namespaced calls (`swiftemit.go:21-55`).
  - `emitTypeTree` emits nested public enums and currently only emits `Input` and `Output` at leaf operations (`swiftemit.go:172-233`).
  - `structLines`, `fieldType`, and `bareType` turn parsed schemas into Swift structs/typealiases (`swiftemit.go:236-349`).
  - `emitNamespaceTree` / `renderNSNode` emit `pb.<namespace>.<method>` calls; all four generated method shapes currently `throws(BackendError)` (`swiftemit.go:351-478`, especially `swiftemit.go:457-471`).

- `/Users/salih/workspace/backend/palbase/modules/backend/internal/runtime/worker.js`
  - `writeResult` writes the worker protocol result to stdout; `writeError` always emits `success: false`, `status_code: 500`, and string `error` (`worker.js:44-55`).
  - `createInternalClient` injects runtime `db`, `cache`, and `queue` surfaces (`worker.js:193-330`).
  - `validateOutput` validates the handler result with an output Zod schema and returns `{ ok, error }` (`worker.js:419-435`).
  - `main` builds `ctx` from stdin context and module clients (`worker.js:437-512`).
  - Input validation failures return `status_code: 400` and bodies with `error: "validation_error"`, `error_description`, and optional `details` (`worker.js:531-599`).
  - Handler execution result is validated, then normalized as 204 for null/undefined, explicit `statusCode` / `status_code` if present, or 200 plain JSON (`worker.js:601-648`).
  - The catch block for thrown handler errors calls `writeError(err.message || String(err))`, so this file does not special-case the TS `HttpError` class (`worker.js:649-652`).

- `/Users/salih/workspace/ios/palbase/palbase/palbaseApp.swift`
  - The app configures `pb` in `palbaseApp.init()` (`palbaseApp.swift:15-21`).
  - `reload`, `add`, `toggle`, and `remove` call `pb.call(...)` and catch broadly (`palbaseApp.swift:140-183`).
  - `toggle` currently contains `pb..call`, a syntax typo in the source as read (`palbaseApp.swift:165-168`).

Required search:

- `rg -rn 'public enum BackendError' /Users/salih/workspace/backend/palbase/sdk/palbackend-ios-src/` found `/Users/salih/workspace/backend/palbase/sdk/palbackend-ios-src/Sources/Palbe/Backend/BackendError.swift`. Numbered read confirmed `public enum BackendError: PalbaseError` at `BackendError.swift:29`.

Additional grounding files read:

- `/Users/salih/workspace/backend/palbase/sdk/palbackend-ios-src/Sources/Palbe/Backend/BackendError.swift`
  - `FieldError` models validation field failures (`BackendError.swift:8-15`).
  - `BackendError` cases include `.notConfigured`, `.server(code:status:message:requestId:)`, `.validation`, `.rateLimited`, `.unauthorized`, `.appAttestRequired`, `.attestationUnavailable`, `.network`, `.transport`, `.decode`, and `.encode` (`BackendError.swift:29-73`).
  - `BackendError.code`, `statusCode`, `requestId`, and `errorDescription` derive public metadata (`BackendError.swift:75-139`).
  - `BackendErrorEnvelope` decodes `error`, `errorDescription`, `status`, `requestId`, and `details: [FieldError]?` (`BackendError.swift:157-163`).
  - `BackendError.from(status:body:retryAfter:)` maps 400 validation, 401 app attest, 401 unauthorized, 429 rate-limited, and all other non-2xx responses to `.server` (`BackendError.swift:168-185`).
  - `BackendError.from(transport:)` maps `PalbaseCoreError` into `BackendError` (`BackendError.swift:192-214`).

- `/Users/salih/workspace/backend/palbase/sdk/cli/internal/backend/swiftgen.go`
  - `swiftSchema`, `swiftProp`, and `swiftOp` are the parsed codegen model; `swiftOp` currently has only `operationID`, `method`, `path`, `input`, and `output` (`swiftgen.go:23-43`).
  - `parseOpenAPIForSwift` reads OpenAPI `paths`, `operationId`, request schema, and response schema into `swiftOp` (`swiftgen.go:47-82`).
  - `responseSchema` considers only 2xx responses (`swiftgen.go:93-118`).
  - `schemaFromContent` skips `$ref` schemas (`swiftgen.go:120-139`).

- `/Users/salih/workspace/backend/palbase/modules/backend/internal/openapi/generator.go`
  - `Operation` has `Responses map[string]Response` and `Extensions` for x- extensions (`generator.go:34-43`).
  - `EndpointSpec` carries method/path/auth/rateLimit/input/output metadata but no declared error metadata (`generator.go:105-116`).
  - `GenerateSpec` creates OpenAPI 3.1.0 with paths and security components (`generator.go:138-187`).
  - `addEndpoint` emits request body from input schema and 200 response from output schema (`generator.go:230-254`).
  - `addEndpoint` currently adds only standard 400 and auth-required 401 error responses, with descriptions only (`generator.go:256-260`).

- `/Users/salih/workspace/backend/palbase/modules/backend/internal/pipeline/errors.go`
  - Go runtime `ErrorResponse` wire envelope is `{ error, error_description, status, request_id, details? }` (`errors.go:9-16`).
  - Go `HttpError`, `NewHttpError`, `NewValidationError`, and `WriteErrorResponse` model/write that envelope (`errors.go:24-68`).

- `/Users/salih/workspace/backend/palbase/modules/backend/internal/runtime/node_executor.go`
  - `NodeResponse` mirrors worker stdout with `success`, `status_code`, `headers`, `body`, `error`, and `logs` (`node_executor.go:89-97`).
  - When worker returns `success: false`, `NodeExecutor.Execute` converts it to HTTP 500 body `{ error: "handler_error", error_description: resp.Error }` (`node_executor.go:157-164`).
  - When worker succeeds, `NodeExecutor.Execute` returns the worker `status_code` and body unchanged (`node_executor.go:166-177`).

- `/Users/salih/workspace/backend/palbase/sdk/palbase-ts/backend/src/__tests__/integration/fixtures/sample-project/endpoints/rooms/[id]/get.ts`
  - Real fixture imports `defineEndpoint`, `z`, and `HttpError`, then throws `new HttpError(404, "room_not_found", "Room does not exist")` inside a handler (`get.ts:1-14`).

- `/Users/salih/workspace/backend/palbase/sdk/palbase-ts/backend/src/errors.test.ts`
  - Tests assert `HttpError` fields and `toJSON()` shape (`errors.test.ts:5-23`) and request-id behavior (`errors.test.ts:41-58`).

- `/Users/salih/workspace/backend/palbase/sdk/palbase-ts/backend/src/middleware.test.ts`
  - Middleware test throws `new HttpError(403, "forbidden", "Admin access required")` and expects propagation (`middleware.test.ts:261-270`).

Negative searches:

- `ctx.errors` / endpoint `errors` surface: NOT FOUND - searched `endpoint.ts`, `worker.js`, `swiftemit.go`, and `swiftgen.go` with `rg -n "ctx\\.errors|errors\\s*[:?]"`; no matches.
- OpenAPI union/discriminator parsing: NOT FOUND - searched `swiftgen.go`, `zod_to_schema.go`, and `generator.go` for `oneOf`, `anyOf`, `allOf`, `union`, `literal`, and `discriminator`; no matches.

## 2. Option A — Untyped grouped errors

### Backend TS side

What the customer writes today: no endpoint config changes. The real observed pattern is `throw new HttpError(...)` from a `defineEndpoint` handler (`get.ts:3-14`), using the exported `HttpError` class (`errors.ts:2-12`, `index.ts:116`).

```ts
import { defineEndpoint, z, HttpError } from "@palbase/backend";

export default defineEndpoint({
  method: "POST",
  auth: true,
  input: z.object({ id: z.string() }),
  output: z.object({ id: z.string(), done: z.boolean() }),
  handler: async (ctx) => {
    const todo = await ctx.db.findById("todos", ctx.input.id);
    if (!todo) {
      throw new HttpError(404, "todo_not_found", "Todo does not exist");
    }
    if (todo["locked"]) {
      throw new HttpError(409, "todo_locked", "Todo is locked");
    }
    if (todo["owner_id"] !== ctx.user.id) {
      throw new HttpError(403, "forbidden", "You cannot update this todo");
    }
    return { id: String(todo["id"]), done: !Boolean(todo["done"]) };
  },
});
```

SDK mapping: every non-2xx response uses the existing `BackendError.from(status:body:retryAfter:)` path (`BackendError.swift:168-185`). Today the catch-all case is `.server(code:status:message:requestId:)` (`BackendError.swift:37`), which is semantically the requested grouped case. To match the requested field name without source breakage, keep the case label `message` and add a documented alias/computed value named `description`, or introduce a new case only in a major version.

Concrete Swift mapping shape:

```swift
// Existing source-compatible shape, cited at BackendError.swift:37.
case server(code: String, status: Int, message: String, requestId: String?)

// INFERRED source-compatible alias for Option A naming.
var description: String {
    errorDescription ?? ""
}
```

If a major version can rename the label, the literal requested case would be:

```swift
// PROPOSED major-version shape.
case server(code: String, status: Int, description: String, requestId: String?)
```

### iOS Swift catch site

The customer distinguishes business errors by string code and status on the grouped `BackendError.server` case (`BackendError.swift:37`, `BackendError.swift:168-185`):

```swift
do {
    let result: ToggleResult = try await pb.call("todos/toggle", IdInput(id: todo.id))
    // use result
} catch BackendError.server(code: let code, status: let status, message: let message, requestId: _) where code == "todo_locked" {
    self.error = "Locked: \(message)"
} catch BackendError.server(code: let code, status: let status, message: let message, requestId: _) where code == "todo_not_found" {
    self.error = "Missing (\(status)): \(message)"
} catch BackendError.server(code: let code, status: let status, message: let message, requestId: _) where code == "forbidden" {
    self.error = "Forbidden (\(status)): \(message)"
} catch {
    self.error = "toggle: \(error)"
}
```

### Codegen impact (swiftemit.go)

No generated endpoint-specific types are required. `swiftemit.go` can keep emitting methods that `throws(BackendError)` for all four input/output shapes (`swiftemit.go:457-471`), and `swiftOp` does not need new fields (`swiftgen.go:37-43`).

Optional cleanup only: generated docs/comments could explain that custom endpoint errors arrive as `BackendError.server(code:status:message:requestId:)`.

### Runtime impact (worker.js)

Not zero. Based on the actual `worker.js`, thrown errors are currently caught at `worker.js:649-652`, passed to `writeError`, and `writeError` always emits `status_code: 500` (`worker.js:49-55`). `NodeExecutor` then turns `success: false` into `{ error: "handler_error", error_description: resp.Error }` with HTTP 500 (`node_executor.go:157-164`). Therefore, if the production worker path is this file, `throw new HttpError(404, ...)` will not honestly become a 404 without a runtime change.

Concrete worker change:

```js
// PROPOSED in worker.js catch block near worker.js:649.
} catch (err) {
  if (
    err &&
    err.name === 'HttpError' &&
    Number.isInteger(err.status) &&
    typeof err.error === 'string'
  ) {
    const body = typeof err.toJSON === 'function'
      ? err.toJSON(ctx.requestId)
      : {
          error: err.error,
          error_description: err.errorDescription || err.message || err.error,
          status: err.status,
          request_id: ctx.requestId,
        };
    writeResult({
      success: true,
      status_code: err.status,
      body,
      logs,
    });
    return;
  }
  writeError(err.message || String(err));
}
```

This preserves the existing `HttpError.toJSON(requestId?)` envelope (`errors.ts:20-29`) and the Go pipeline envelope (`errors.go:9-16`).

### Trade-offs

- Type safety: weak. Swift code switches on string codes inside `BackendError.server`; the compiler cannot prove that `todos/toggle` can throw `todo_locked`.
- Discoverability: weak. Xcode autocomplete shows `BackendError.server`, not endpoint-specific cases.
- Boilerplate: lowest for endpoint authors; they already write `throw new HttpError(status, code, description)` (`get.ts:12-13`).
- Backward compat: strongest for Swift API shape if the existing `.server` case label remains `message` (`BackendError.swift:37`) and `pb.call` remains unchanged (`Facade.swift:21-38`).
- OpenAPI fidelity: poor. Current OpenAPI generation adds only generic 400 and optional 401 descriptions (`generator.go:256-260`), and Swift parsing ignores non-2xx responses (`swiftgen.go:93-118`).
- Wire-format honesty: good after the worker fix above; it uses the actual envelope already modeled by TS `HttpError.toJSON` (`errors.ts:20-29`), Go `ErrorResponse` (`errors.go:9-16`), and Swift `BackendErrorEnvelope` (`BackendError.swift:157-163`).

## 3. Option B — Schema-declared errors

### Backend TS side

This is a proposed extension. Current `EndpointConfig` has no `errors` field (`endpoint.ts:272-288`), `EndpointContext` has no `ctx.errors` (`endpoint.ts:185-214`), and the negative search found no `ctx.errors` implementation.

Proposed endpoint shape:

```ts
import { defineEndpoint, z } from "@palbase/backend";

export default defineEndpoint({
  method: "POST",
  auth: true,
  input: z.object({ id: z.string() }),
  output: z.object({ id: z.string(), done: z.boolean() }),
  errors: {
    todoLocked: {
      code: "todo_locked",
      status: 409,
      description: "Todo is locked",
      schema: z.object({
        retryAfter: z.number(),
      }),
    },
    todoNotFound: {
      code: "todo_not_found",
      status: 404,
      description: "Todo does not exist",
      schema: z.object({}),
    },
    forbidden: {
      code: "forbidden",
      status: 403,
      description: "You cannot update this todo",
      schema: z.object({}),
    },
    tooManyRequests: {
      code: "too_many_requests",
      status: 429,
      description: "Too many requests",
      schema: z.object({
        retryAfter: z.number(),
      }),
    },
  },
  handler: async (ctx) => {
    const todo = await ctx.db.findById("todos", ctx.input.id);
    if (!todo) throw ctx.errors.todoNotFound();
    if (todo["locked"]) throw ctx.errors.todoLocked({ retryAfter: 30 });
    if (todo["owner_id"] !== ctx.user.id) throw ctx.errors.forbidden();

    // Required syntax example from this option:
    // throw ctx.errors.tooManyRequests({ retryAfter: 30 });

    return { id: String(todo["id"]), done: !Boolean(todo["done"]) };
  },
});
```

The Zod declaration for the requested `todo_locked` associated value is:

```ts
todoLocked: {
  code: "todo_locked",
  status: 409,
  description: "Todo is locked",
  schema: z.object({
    retryAfter: z.number(),
  }),
}
```

### iOS Swift catch site

Generated Swift should expose an endpoint-specific error enum. The existing generator already has per-operation type prefixes (`swiftemit.go:438-448`) and emits nested `Input` / `Output` types (`swiftemit.go:209-214`); Option B adds a sibling or leaf error enum.

Concrete generated enum:

```swift
// PROPOSED generated code.
public enum Todos {
    public enum MarkComplete {
        public struct Input: Codable, Sendable {
            public let id: String
            public init(id: String) { self.id = id }
        }

        public struct Output: Codable, Sendable {
            public let id: String
            public let done: Bool
            public init(id: String, done: Bool) {
                self.id = id
                self.done = done
            }
        }
    }

    public enum MarkCompleteError: Swift.Error, Sendable {
        case todoLocked(retryAfter: Int)
        case todoNotFound
        case forbidden
        case backend(BackendError)
    }
}
```

Concrete catch site:

```swift
do {
    let result = try await pb.todos.markComplete(.init(id: todo.id))
    if let i = todos.firstIndex(where: { $0.id == result.id }) {
        todos[i].done = result.done
    }
} catch Todos.MarkCompleteError.todoLocked(let retryAfter) {
    self.error = "Todo is locked. Retry after \(retryAfter)s."
} catch Todos.MarkCompleteError.todoNotFound {
    self.error = "Todo no longer exists."
} catch Todos.MarkCompleteError.forbidden {
    self.error = "You cannot update this todo."
} catch Todos.MarkCompleteError.backend(let error) {
    self.error = "toggle: \(error)"
}
```

Xcode autocomplete experience: after `catch Todos.MarkCompleteError.` the generated enum cases `todoLocked`, `todoNotFound`, `forbidden`, and `backend` are visible because they are real Swift enum cases. That is materially different from Option A, where autocomplete stops at `BackendError.server` (`BackendError.swift:37`).

### Codegen impact (swiftemit.go)

`swiftOp` must grow an error model because it currently has only `operationID`, `method`, `path`, `input`, and `output` (`swiftgen.go:37-43`). `parseOpenAPIForSwift` must parse non-2xx responses, while `responseSchema` currently only considers 2xx (`swiftgen.go:93-118`). `schemaFromContent` currently skips `$ref` schemas (`swiftgen.go:133-136`), so declared error responses should either inline schemas or the parser must resolve references.

Concrete Go model/template sketch:

```go
// PROPOSED in swiftgen.go.
type swiftError struct {
	name   string       // todoLocked
	code   string       // todo_locked
	status int          // 409
	schema *swiftSchema // associated-value payload, nil or empty object for no payload
}

type swiftOp struct {
	operationID string
	method      string
	path        string
	input       *swiftSchema
	output      *swiftSchema
	errors      []swiftError
}
```

Concrete emit sketch:

```go
// PROPOSED in swiftemit.go near emitTypeTree leaf handling at swiftemit.go:208-214.
if len(node.op.errors) > 0 {
    lines = append(lines, indent(depth+1)+"public enum Error: Swift.Error, Sendable {")
    for _, e := range node.op.errors {
        caseName := identOf(e.name)
        if e.schema == nil || (e.schema.kind == "object" && len(e.schema.props) == 0) {
            lines = append(lines, indent(depth+2)+"case "+caseName)
            continue
        }
        fields := swiftAssociatedValues(e.schema)
        lines = append(lines, indent(depth+2)+"case "+caseName+"("+fields+")")
    }
    lines = append(lines, indent(depth+2)+"case backend(BackendError)")
    lines = append(lines, indent(depth+1)+"}")
}
```

Generated call wrapper must catch `BackendError`, map matching `code` / `status` to the endpoint enum, decode the declared payload, and wrap everything else:

```swift
// PROPOSED generated method replacing current throws(BackendError) emission
// currently emitted at swiftemit.go:460-471.
public func markComplete(_ input: Todos.MarkComplete.Input) async throws(Todos.MarkComplete.Error) -> Todos.MarkComplete.Output {
    do {
        return try await self._invoke(
            method: "POST",
            path: "/todos/mark-complete",
            input,
            as: Todos.MarkComplete.Output.self
        )
    } catch let error as BackendError {
        throw Todos.MarkComplete.Error.from(error)
    }
}
```

Important Swift SDK impact: current `BackendError.server` does not carry raw declared payload (`BackendError.swift:37`), and `BackendErrorEnvelope` has only `details: [FieldError]?` (`BackendError.swift:157-163`). `PalbaseBackend.invokeRPC` also returns only success `Data`; non-2xx returns are already converted to `BackendError` (`PalbaseBackend.swift:121-133`). Therefore Option B also requires either:

- extend `BackendError.server` with a payload/raw body while preserving the old case during a deprecation window, or
- add a generated-code SPI raw invoke that returns non-2xx body data for generated wrappers before `BackendError.from` drops it.

### Runtime impact (worker.js)

`worker.js` must read `endpoint.default?.errors || endpoint.errors`, build `ctx.errors`, and validate helper payloads against declared Zod schemas. Current `ctx` construction does not include `errors` (`worker.js:494-512`), and handler execution is a plain `await handler(ctx)` (`worker.js:601-602`).

Proposed worker behavior:

```js
// PROPOSED before ctx construction / handler execution in worker.js.
class DeclaredEndpointError extends Error {
  constructor(def, payload) {
    super(def.description || def.code);
    this.name = 'DeclaredEndpointError';
    this.def = def;
    this.payload = payload || {};
  }
}

function buildCtxErrors(errorDefs) {
  const helpers = {};
  for (const [name, def] of Object.entries(errorDefs || {})) {
    helpers[name] = (payload = {}) => {
      if (def.schema && typeof def.schema.safeParse === 'function') {
        const parsed = def.schema.safeParse(payload);
        if (!parsed.success) {
          throw new Error(`error payload validation failed for ${name}`);
        }
        payload = parsed.data;
      }
      return new DeclaredEndpointError(def, payload);
    };
  }
  return helpers;
}

ctx.errors = buildCtxErrors(endpoint.default?.errors || endpoint.errors);
```

Catch behavior:

```js
// PROPOSED inside worker.js catch block near worker.js:649.
if (err && err.name === 'DeclaredEndpointError') {
  writeResult({
    success: true,
    status_code: err.def.status,
    body: {
      error: err.def.code,
      error_description: err.def.description,
      status: err.def.status,
      request_id: ctx.requestId,
      payload: err.payload,
    },
    logs,
  });
  return;
}
```

Wire shape: this differs from the current TS `HttpError.toJSON` envelope (`errors.ts:20-29`) by adding `payload`. It also differs from current Swift `BackendErrorEnvelope`, which has `details: [FieldError]?` but no `payload` (`BackendError.swift:157-163`). Existing clients should ignore unknown fields, but generated typed clients need the new payload to build associated values.

### Trade-offs

- Type safety: strongest for generated Swift calls. Declared endpoint errors become a generated enum, and each associated value comes from the declared Zod schema.
- Discoverability: strongest. Xcode surfaces `Todos.MarkCompleteError.todoLocked`, `todoNotFound`, and `forbidden` as cases rather than string codes.
- Boilerplate: moderate. The simplest handler grows from `throw new HttpError(404, "todo_not_found", "...")` to an `errors` declaration plus `throw ctx.errors.todoNotFound()`.
- Backward compat: good if optional and staged. Existing `defineEndpoint` configs still compile because `errors` is additive to `EndpointConfig` (`endpoint.ts:272-288`), and raw `pb.call` still throws `BackendError` (`Facade.swift:21-38`). Regenerated typed methods need a deprecation plan because changing `throws(BackendError)` (`swiftemit.go:460-471`) to endpoint-specific typed throws is a source-level API change for generated-method callers.
- OpenAPI fidelity: best if the runtime generator emits per-error non-2xx response schemas. Current generator lacks error metadata (`generator.go:105-116`) and only emits generic 400/401 descriptions (`generator.go:256-260`), so this option requires OpenAPI generator work.
- Wire-format honesty: honest after runtime changes. The server would actually validate declared payloads and put them on the wire; without that worker and Swift-envelope work, associated values would be SDK invention.

## 4. Option C — Discriminated-union output (no throw for business errors)

### Backend TS side

Business errors become part of the success output schema. The handler does not throw for expected business outcomes. This fits the current worker's plain-object success path, which returns 200 JSON for ordinary handler results (`worker.js:642-648`), and still uses thrown errors for validation/output/runtime failures (`worker.js:531-652`).

```ts
import { defineEndpoint, z } from "@palbase/backend";

const TodoData = z.object({
  id: z.string(),
  done: z.boolean(),
});

const TodoBusinessError = z.object({
  code: z.enum(["todo_locked", "todo_not_found", "forbidden"]),
  description: z.string(),
  status: z.number(),
  retryAfter: z.number().optional(),
});

const MarkCompleteOutput = z.union([
  z.object({ data: TodoData }),
  z.object({ error: TodoBusinessError }),
]);

export default defineEndpoint({
  method: "POST",
  auth: true,
  input: z.object({ id: z.string() }),
  output: MarkCompleteOutput,
  handler: async (ctx) => {
    const todo = await ctx.db.findById("todos", ctx.input.id);
    if (!todo) {
      return { error: { code: "todo_not_found", description: "Todo does not exist", status: 404 } };
    }
    if (todo["locked"]) {
      return { error: { code: "todo_locked", description: "Todo is locked", status: 409, retryAfter: 30 } };
    }
    if (todo["owner_id"] !== ctx.user.id) {
      return { error: { code: "forbidden", description: "You cannot update this todo", status: 403 } };
    }
    return { data: { id: String(todo["id"]), done: !Boolean(todo["done"]) } };
  },
});
```

The current negative search found no `oneOf` / `anyOf` / union support in `swiftgen.go`, `zod_to_schema.go`, or `generator.go`, so the output schema is proposed as a contract but not currently supported end-to-end.

### iOS Swift catch site

Generated Swift should return a result union. `try/catch` remains necessary for network, transport, decode, validation, and server failures because `pb.call` / `_invoke` still throw `BackendError` (`Facade.swift:21-38`, `Facade.swift:97-149`).

```swift
// PROPOSED generated output.
public enum TodosMarkCompleteResult: Decodable, Sendable {
    case data(Todos.MarkComplete.Output)
    case error(Todos.MarkComplete.BusinessError)
}

public struct TodosMarkCompleteBusinessError: Decodable, Sendable {
    public let code: String
    public let description: String
    public let status: Int
    public let retryAfter: Int?
}
```

Customer code:

```swift
do {
    let result = try await pb.todos.markComplete(.init(id: todo.id))
    switch result {
    case .data(let value):
        if let i = todos.firstIndex(where: { $0.id == value.id }) {
            todos[i].done = value.done
        }
    case .error(let error) where error.code == "todo_locked":
        self.error = "Todo is locked. Retry after \(error.retryAfter ?? 0)s."
    case .error(let error) where error.code == "todo_not_found":
        self.error = "Todo no longer exists."
    case .error(let error) where error.code == "forbidden":
        self.error = "You cannot update this todo."
    case .error(let error):
        self.error = error.description
    }
} catch {
    self.error = "Network/decode/server failure: \(error)"
}
```

### Codegen impact (swiftemit.go)

`swiftemit.go` must emit a Swift enum for a discriminated output union instead of only structs/typealiases. Current `declLines` emits an object struct or `bareType` alias (`swiftemit.go:236-241`), and `fieldType` / `bareType` do not model unions (`swiftemit.go:303-349`). `swiftgen.go` also does not parse `oneOf` / `anyOf` based on the negative search above.

Concrete generated wrapper:

```swift
// PROPOSED generated method.
@discardableResult
public func markComplete(_ input: Todos.MarkComplete.Input) async throws(BackendError) -> Todos.MarkComplete.Result {
    try await self._invoke(
        method: "POST",
        path: "/todos/mark-complete",
        input,
        as: Todos.MarkComplete.Result.self
    )
}
```

The method can still use the existing `_invoke` seam because business errors are in the 2xx decoded body; `_invoke` already decodes success bytes and throws `BackendError` only on transport/non-2xx/decode (`Facade.swift:107-115`).

### Runtime impact (worker.js)

If business errors are returned as plain `{ error: ... }` objects and the HTTP status remains 200, no worker special-case is required: the plain-object return path writes `status_code: 200` and `body: result` (`worker.js:642-648`). Output validation must be updated only if the runtime/OpenAPI Zod converter cannot represent `z.union`; current `validateOutput` delegates to `outputSchema.safeParse` when present (`worker.js:424-434`, `worker.js:604-607`), so runtime validation itself can validate a real Zod union.

Wire shape:

```json
{
  "error": {
    "code": "todo_locked",
    "description": "Todo is locked",
    "status": 409,
    "retryAfter": 30
  }
}
```

The HTTP status would be 200 under the current plain-object path. If worker instead used `error.status` as HTTP status, the current Swift client would treat it as `BackendError` and the customer could not check `.data` vs `.error` in the decoded result.

### Trade-offs

- Type safety: good for returned result shape if union codegen exists, but business errors are no longer Swift thrown errors.
- Discoverability: moderate. Xcode can autocomplete result cases (`.data`, `.error`) and error fields, but not catch cases.
- Boilerplate: highest in handlers and schemas because every success response is wrapped in `{ data }` and every business error is returned as `{ error }`.
- Backward compat: weakest at API semantics. Existing success output shapes change from `T` to `{ data: T } | { error: ... }`, so existing `pb.call("todos/toggle")` decoders break unless adopted per endpoint.
- OpenAPI fidelity: good only after union support is added. Current OpenAPI/codegen search found no `oneOf` / `anyOf` support, and current Swift schema parsing has no union branch.
- Wire-format honesty: honest if the product accepts HTTP 200 for business errors. It is dishonest if `status: 404` inside the body is expected to mean HTTP 404.

## 5. Recommendation

Recommend Option B because it is the only option that gives generated Swift callers endpoint-specific compile-time errors and Xcode-discoverable cases while preserving raw `pb.call` as the backward-compatible `BackendError` escape hatch.

Type safety: yes, for generated calls whose endpoints declare `errors`. The generated method can `throws(Todos.MarkCompleteError)` and the enum cases come from the endpoint's declared error map, not from ad hoc string matching. Raw `pb.call` remains untyped and continues to throw `BackendError` (`Facade.swift:21-38`).

Discoverability: yes. Xcode autocomplete can surface `todoLocked`, `todoNotFound`, and `forbidden` because they are generated Swift enum cases. Option A cannot do that because the only current business-error surface is `BackendError.server(code:status:message:requestId:)` (`BackendError.swift:37`).

Boilerplate: the simplest handler grows by one declared `errors` entry and a helper throw. Instead of `throw new HttpError(404, "todo_not_found", "Todo does not exist")`, the endpoint author writes `errors: { todoNotFound: ... }` and `throw ctx.errors.todoNotFound()`. This is a real increase, but it is localized to endpoints that want typed errors.

Backward compat: existing `pb.call("todos/toggle", ...)` handlers and broad `catch {}` sites still compile because `pb.call` stays unchanged (`Facade.swift:21-38`). Existing generated methods currently `throws(BackendError)` (`swiftemit.go:460-471`), so changing them directly is a source change for callers that catch `BackendError`; the migration should emit legacy `throws(BackendError)` methods or keep raw `pb.call` documented during a deprecation window.

OpenAPI fidelity: Option B requires adding declared error metadata to `EndpointSpec`, because it currently has no error field (`generator.go:105-116`). Once added, the runtime can emit per-status response schemas instead of only generic 400/401 descriptions (`generator.go:256-260`), and Swift codegen can parse non-2xx schemas instead of ignoring them (`swiftgen.go:93-118`).

Wire-format honesty: Option B requires server/runtime work before the Swift SDK can honestly expose associated values. Today TS `HttpError.toJSON` has only the standard envelope (`errors.ts:20-29`), Swift `BackendErrorEnvelope` has no payload field (`BackendError.swift:157-163`), and `worker.js` does not special-case thrown `HttpError` (`worker.js:649-652`). The recommendation is honest only if `worker.js` validates declared payloads and sends them on the wire.

## 6. Migration Path

Demo app current shape in `/Users/salih/workspace/ios/palbase/palbase/palbaseApp.swift`:

```swift
private func toggle(_ todo: Todo) async {
    do {
        let r: ToggleResult = try await pb..call("todos/toggle", IdInput(id: todo.id))
        if let i = todos.firstIndex(where: { $0.id == r.id }) { todos[i].done = r.done }
    } catch {
        self.error = "toggle: \(error)"
    }
}
```

The snippet above is the source as read, including the `pb..call` typo (`palbaseApp.swift:165-168`). The migration should also fix that typo in the app when code is actually changed, but this document does not modify it.

After Option B generated client adoption:

```swift
private func toggle(_ todo: Todo) async {
    do {
        let r = try await pb.todos.toggle(.init(id: todo.id))
        if let i = todos.firstIndex(where: { $0.id == r.id }) {
            todos[i].done = r.done
        }
    } catch Todos.ToggleError.todoLocked(let retryAfter) {
        self.error = "Todo is locked. Retry after \(retryAfter)s."
    } catch Todos.ToggleError.todoNotFound {
        self.error = "Todo no longer exists."
    } catch Todos.ToggleError.forbidden {
        self.error = "You cannot update this todo."
    } catch Todos.ToggleError.backend(let error) {
        self.error = "toggle: \(error)"
    }
}
```

Deprecation window: old `BackendError` catch sites still compile if they keep using raw `pb.call`, because the public facade remains `throws(BackendError)` (`Facade.swift:21-38`). For generated-method users, emit a legacy wrapper for one release, for example `pb.todos.toggleBackendError(...) async throws(BackendError)`, while the primary generated method moves to `throws(Todos.ToggleError)`. After the deprecation window, users should catch the endpoint-specific enum or keep using `pb.call` as the untyped escape hatch.

Endpoint author TS change:

```ts
export default defineEndpoint({
  method: "POST",
  auth: true,
  input: z.object({ id: z.string() }),
  output: z.object({ id: z.string(), done: z.boolean() }),
  errors: {
    todoLocked: {
      code: "todo_locked",
      status: 409,
      description: "Todo is locked",
      schema: z.object({ retryAfter: z.number() }),
    },
    todoNotFound: {
      code: "todo_not_found",
      status: 404,
      description: "Todo does not exist",
      schema: z.object({}),
    },
    forbidden: {
      code: "forbidden",
      status: 403,
      description: "You cannot update this todo",
      schema: z.object({}),
    },
  },
  handler: async (ctx) => {
    const todo = await ctx.db.findById("todos", ctx.input.id);
    if (!todo) throw ctx.errors.todoNotFound();
    if (todo["locked"]) throw ctx.errors.todoLocked({ retryAfter: 30 });
    if (todo["owner_id"] !== ctx.user.id) throw ctx.errors.forbidden();
    return { id: String(todo["id"]), done: !Boolean(todo["done"]) };
  },
});
```

Concrete migration steps:

1. Add optional `errors` support to `EndpointConfig` and `EndpointContext` in `endpoint.ts` next to the existing `input` / `output` / `handler` fields (`endpoint.ts:272-288`, `endpoint.ts:185-214`).
2. Update `worker.js` to inject `ctx.errors`, validate helper payloads, and serialize declared errors with HTTP status and payload before the generic catch path (`worker.js:494-512`, `worker.js:649-652`).
3. Extend OpenAPI endpoint metadata and generation so declared errors are emitted as non-2xx response schemas instead of only generic 400/401 descriptions (`generator.go:105-116`, `generator.go:256-260`).
4. Extend `swiftgen.go` so `swiftOp` includes parsed errors and non-2xx response schemas (`swiftgen.go:37-43`, `swiftgen.go:93-118`).
5. Extend `swiftemit.go` so it emits endpoint error enums and generated wrappers that map `BackendError` plus payload to endpoint-specific typed throws (`swiftemit.go:172-233`, `swiftemit.go:457-471`).
6. Regenerate the iOS client and migrate `palbaseApp.swift` from raw `pb.call("todos/toggle", ...)` broad catch to generated `pb.todos.toggle(...)` endpoint-specific catches (`palbaseApp.swift:165-171`).

## 7. Pitfalls

- `worker.js` currently turns every thrown handler error into `writeError(...)` at `worker.js:649-652`, and `writeError` always uses `status_code: 500` at `worker.js:49-55`. If Option A or B assumes `throw new HttpError(404, ...)` works in production, the actual Node worker path contradicts that unless it special-cases the TS `HttpError` shape from `errors.ts:2-20`.

- `NodeExecutor.Execute` converts `success: false` from the worker into `{ error: "handler_error", error_description: resp.Error }` with status 500 (`node_executor.go:157-164`). Even if the thrown JS object has `status: 404`, that data is lost once `worker.js` emits `success: false`.

- `BackendErrorEnvelope` currently decodes `details` as `[FieldError]?` and has no raw payload field (`BackendError.swift:157-163`). If Option B adds associated values like `todoLocked(retryAfter:)` but only changes codegen, the generated Swift wrapper cannot recover `retryAfter` from the current `BackendError.server` case (`BackendError.swift:37`).

- `PalbaseBackend.invokeRPC` maps non-2xx responses to `BackendError` before returning to generated code (`PalbaseBackend.swift:121-133`). A generated Option B wrapper that calls the existing `_invoke` overloads (`Facade.swift:97-149`) never sees the original non-2xx body unless the SDK adds raw-body SPI or extends `BackendError`.

- `swiftemit.go` currently emits every generated method as `throws(BackendError)` (`swiftemit.go:460-471`). Changing that to `throws(Todos.MarkCompleteError)` is a source-level API change for generated-client users that have `catch BackendError...` patterns.

- `swiftOp` has no error field (`swiftgen.go:37-43`), and `responseSchema` intentionally ignores non-2xx responses (`swiftgen.go:93-118`). Adding OpenAPI error responses alone will not affect Swift output until the parser and emitter are both changed.

- `schemaFromContent` skips `$ref` schemas (`swiftgen.go:133-136`). If the OpenAPI generator emits shared component schemas for error payloads, current Swift codegen will drop them and the generated error associated values will be missing.

- `openapi.EndpointSpec` lacks declared error metadata (`generator.go:105-116`), and `addEndpoint` emits only generic 400/401 response descriptions (`generator.go:256-260`). Option B cannot preserve OpenAPI fidelity without changing the runtime metadata extraction path, not just the Swift generator.

- `BackendError.server` uses the associated-value label `message` (`BackendError.swift:37`), while Option A asks for `description`. Renaming the label directly can break label-based Swift pattern matches; preserve `message` and add an alias unless doing a major version.

- `palbaseApp.swift` has `pb..call` in `toggle` (`palbaseApp.swift:165-168`). Migration examples should not copy that typo; generated-client migration should move to `pb.todos.toggle(...)` or the fixed raw call `pb.call(...)`.
