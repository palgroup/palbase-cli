package backend

import (
	"os"
	"strings"
	"testing"
)

const fixtureOpenAPI = `{
  "openapi":"3.1.0","info":{"title":"t","version":"1"},
  "paths":{
    "/rooms/create":{"post":{"operationId":"rooms.create",
      "parameters":[
        {"name":"if-match","in":"header","required":true,"schema":{"type":"string"}},
        {"name":"x-trace-id","in":"header","required":false,"schema":{"type":"string"}}
      ],
      "requestBody":{"content":{"application/json":{"schema":{"type":"object",
        "properties":{"name":{"type":"string"},"capacity":{"type":"integer"},"kind":{"type":"string","enum":["public","private"]}},
        "required":["name","kind"]}}}},
      "responses":{
        "200":{"content":{"application/json":{"schema":{"type":"object",
          "properties":{"id":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"score":{"type":"number","nullable":true}},
          "required":["id","tags","score"]}}}},
        "409":{"content":{"application/json":{"schema":{"type":"object",
          "properties":{"error":{"type":"string","const":"room_locked"},"data":{"type":"object","properties":{"retry_after":{"type":"integer"}},"required":["retry_after"]}},
          "required":["error","data"]}}}}
      },
      "x-palbase-errors":{
        "roomLocked":{"status":409,"code":"room_locked","hasData":true,"description":"Room is locked"},
        "roomFull":{"status":423,"code":"room_full","hasData":false,"description":"Room is full"}
      }}},
    "/rooms/id/get":{"post":{"operationId":"rooms.id.get",
      "requestBody":{"content":{"application/json":{"schema":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}}},
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}}}}}},
    "/posts/create":{"post":{"operationId":"posts.create",
      "requestBody":{"content":{"application/json":{"schema":{"type":"object",
        "properties":{
          "title":{"type":"string"},
          "body":{"type":"string"},
          "meta":{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string"}},"pinned":{"type":"boolean"}},"required":["tags"]}
        },
        "required":["title","meta"]}}}},
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}}}}}},
    "/has-nullable":{"post":{"operationId":"has.nullable",
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object",
        "properties":{"error":{"type":["string","null"]},"ok":{"type":"boolean"}},
        "required":["error","ok"]}}}}}}},
    "/auth/login":{"post":{"operationId":"auth.login",
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}}}}}}},
    "/todos":{"get":{"operationId":"todos.list",
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"array","items":{"type":"object",
        "properties":{"id":{"type":"string"},"title":{"type":"string"},"completed":{"type":"boolean"},"created_at":{"type":"string"}},
        "required":["id","title","completed","created_at"]}}}}}}}},
    "/todos/{id}":{
      "get":{"operationId":"todos.get",
        "parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],
        "responses":{"200":{"content":{"application/json":{"schema":{"type":"object","properties":{"id":{"type":"string"},"title":{"type":"string"}},"required":["id","title"]}}}}}},
      "patch":{"operationId":"todos.update",
        "parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],
        "requestBody":{"content":{"application/json":{"schema":{"type":"object","properties":{"title":{"type":"string"}}}}}},
        "responses":{"200":{"content":{"application/json":{"schema":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}}}}},
      "delete":{"operationId":"todos.delete",
        "parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}]}},
    "/orgs/{orgId}/members/{userId}":{"get":{"operationId":"orgs.members.get",
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object","properties":{"role":{"type":"string"}},"required":["role"]}}}}}}},
    "/search":{"get":{"operationId":"search.run",
      "parameters":[
        {"name":"q","in":"query","required":true,"schema":{"type":"string"}},
        {"name":"limit","in":"query","required":false,"schema":{"type":"integer"}}
      ],
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}}}}}}},
    "/docs":{"post":{"operationId":"docs.upload",
      "requestBody":{"content":{"application/json":{"schema":{"type":"object",
        "properties":{"title":{"type":"string"},"folder":{"type":"string"}},
        "required":["title"]}}}},
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object",
        "properties":{"id":{"type":"string"},"url":{"type":"string"}},
        "required":["id","url"]}}}}},
      "x-palbase-upload":{
        "bucket":"docs","pathTemplate":"{userId}/{uploadId}-{filename}"
      }}}
  }
}`

func TestEmitSwift(t *testing.T) {
	ops, err := parseOpenAPIForSwift([]byte(fixtureOpenAPI))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := emitSwift(ops)

	must := []string{
		// Generated code now targets the PUBLIC typed-network surface, so a
		// plain `import Palbe` (no `@_spi(Generated)`) — the old `_invoke`
		// seam is no longer referenced.
		"import Palbe",
		"public extension PalBackendClient {",
		"var rooms: PBRoomsNamespace",
		// Top-level Request / Response structs per operation — no
		// nested `enum Rooms { typealias Input = ... }` walk.
		"public nonisolated struct RoomsCreateRequest: Codable, Sendable {",
		"public nonisolated struct RoomsCreateResponse: Codable, Sendable {",
		"public nonisolated struct RoomsIdGetRequest: Codable, Sendable {",
		"public nonisolated struct RoomsIdGetResponse: Codable, Sendable {",
		// Per-op ENDPOINT STRUCT — `: PBEndpoint` for a body-returning op,
		// stored inputs as `let`s, computed `pbRequest`. rooms.create
		// declares headers, so the struct holds `headers` and forwards
		// `headers.asHeaderDict()` into the PBRequest. Path is the segment
		// array (literals bare, params `.param(...)`), NOT an interpolated
		// string.
		"public nonisolated struct RoomsCreateEndpoint: PBEndpoint {",
		"public typealias Response = RoomsCreateResponse",
		"public typealias Failure = RoomsCreateError",
		"let input: RoomsCreateRequest",
		`public var pbRequest: PBRequest { PBRequest(.post, ["rooms", "create"], body: input, headers: headers.asHeaderDict()) }`,
		// Call signature is UNCHANGED (path params/body/query/headers, typed
		// throws, @discardableResult); only the BODY collapses to one
		// `pb.call(<Op>Endpoint(...))` forwarding line — no as:/failing:/where.
		"func create(_ input: RoomsCreateRequest, headers: RoomsCreateHeaders) async throws(RoomsCreateError) -> RoomsCreateResponse",
		`return try await _pb.call(RoomsCreateEndpoint(input: input, headers: headers))`,
		// <Op>Headers struct: required header non-optional, optional one
		// String?, wire names preserved via CodingKeys, asHeaderDict()
		// flattens to [String:String] (required direct, optional if-let).
		"public nonisolated struct RoomsCreateHeaders: Codable, Sendable {",
		"public let ifMatch: String",
		"public let xTraceId: String?",
		"public func asHeaderDict() -> [String: String] {",
		`out["if-match"] = ifMatch`,
		`if let v = xTraceId { out["x-trace-id"] = v }`,
		// Nested enum (string union) declared inside the parent struct.
		"public nonisolated enum KindValue: String, Codable, Sendable {",
		"case `public` = \"public\"", // keyword escaped
		"public let capacity: Int?",  // optional (not in `required`)
		"public let score: Double?",  // nullable → optional
		"public let tags: [String]",  // array
		"struct PBRoomsIdNamespace",  // nested namespace still tree-walked
		"func get(_ input: RoomsIdGetRequest)",
		// Nested object property → struct declared INSIDE the parent
		// struct body (Swift type-nesting); field references the short
		// name. `body` is optional (not in required); `meta` is required.
		"public nonisolated struct PostsCreateRequest: Codable, Sendable {",
		"public nonisolated struct Meta: Codable, Sendable {", // nested struct
		"public let pinned: Bool?",                            // nested optional
		"public let tags: [String]",                           // nested required
		"public let body: String?",                            // parent optional
		"public let meta: Meta",                               // parent → short ref
		"public let title: String",                            // parent required
		// `type: ["string","null"]` (zod-to-json-schema for
		// z.string().nullable()) lowers to String? — NOT
		// AnyCodableValue. Without the type-array lowering the
		// generated code would expose AnyCodableValue in public
		// position.
		"public nonisolated struct HasNullableResponse: Codable, Sendable {",
		"public let error: String?",
		"public let ok: Bool",
		// PATH PARAMETERS. A `{id}` path segment threads an `id: String`
		// LEADING method arg; the endpoint struct stores it and emits it as a
		// `.param(id)` PBPathSegment (NOT an interpolated literal). Body-
		// bearing ops put path params FIRST, then the input; no-body ops
		// (get/delete) take just the path param.
		"func get(id: String) async throws(TodosGetError) -> TodosGetResponse",
		`return try await _pb.call(TodosGetEndpoint(id: id))`,
		`public var pbRequest: PBRequest { PBRequest(.get, ["todos", .param(id)]) }`,
		// No-body op → `: PBVoidEndpoint` (no Response typealias), void
		// method body (no @discardableResult, no `return`).
		"public nonisolated struct TodosDeleteEndpoint: PBVoidEndpoint {",
		"func delete(id: String) async throws(TodosDeleteError) {",
		`try await _pb.call(TodosDeleteEndpoint(id: id))`,
		`public var pbRequest: PBRequest { PBRequest(.delete, ["todos", .param(id)]) }`,
		"func update(id: String, _ input: TodosUpdateRequest) async throws(TodosUpdateError) -> TodosUpdateResponse",
		`return try await _pb.call(TodosUpdateEndpoint(id: id, input: input))`,
		`public var pbRequest: PBRequest { PBRequest(.patch, ["todos", .param(id)], body: input) }`,
		// Multiple path params → both as args in path order, both as `.param`.
		"func get(orgId: String, userId: String) async throws(OrgsMembersGetError) -> OrgsMembersGetResponse",
		`return try await _pb.call(OrgsMembersGetEndpoint(orgId: orgId, userId: userId))`,
		`public var pbRequest: PBRequest { PBRequest(.get, ["orgs", .param(orgId), "members", .param(userId)]) }`,
		// ARRAY-OF-OBJECT RESPONSE. `GET /todos` emits a NAMED item struct
		// (Codable+Sendable, snake_case→camelCase) and the response is a
		// typed `[Item]`, not an opaque `[AnyCodableValue]`.
		"public nonisolated struct TodosListResponseItem: Codable, Sendable {",
		"public let completed: Bool",
		"public let createdAt: String", // created_at → camelCase
		"public typealias TodosListResponse = [TodosListResponseItem]",
		// QUERY PARAMETERS. A `parameters[in:query]` op gains a `query:
		// <Op>Query` method arg; the endpoint struct stores it and passes it
		// as `PBRequest(query:)` — the SDK's renderQuery does the encoding, so
		// the <Op>Query struct is a BARE Codable struct (NO asQueryString()).
		"public nonisolated struct SearchRunQuery: Codable, Sendable {",
		"public let q: String",   // required query param
		"public let limit: Int?", // optional query param
		"func run(query: SearchRunQuery) async throws(SearchRunError) -> SearchRunResponse",
		`return try await _pb.call(SearchRunEndpoint(query: query))`,
		`public var pbRequest: PBRequest { PBRequest(.get, ["search"], query: query) }`,
		// @UPLOAD. An op carrying x-palbase-upload emits a DIFFERENT endpoint
		// protocol (PBUploadEndpoint, NOT PBEndpoint) that computes an
		// `authorizeRequest` (the POST JSON pre-flight carrying the author body)
		// instead of `pbRequest`; the bytes go client→storage directly. The
		// namespace method takes `file: PBFileSource` + the author `input:` +
		// trailing `onProgress:`, and forwards both to the upload overload
		// `pb.call(_:file:onProgress:)`. Response/Failure infer from the endpoint
		// exactly like a normal call.
		"public nonisolated struct DocsUploadEndpoint: PBUploadEndpoint {",
		"public var authorizeRequest: PBRequest { PBRequest(.post, [\"docs\"], body: input) }",
		"func upload(file: PBFileSource, input: DocsUploadRequest, onProgress: (@Sendable (BackendUploadProgress) -> Void)? = nil) async throws(DocsUploadError) -> DocsUploadResponse {",
		"return try await _pb.call(DocsUploadEndpoint(input: input), file: file, onProgress: onProgress)",
	}
	for _, m := range must {
		if !strings.Contains(out, m) {
			t.Errorf("generated Swift missing: %q\n---\n%s", m, out)
		}
	}

	// Typed-throws error enums (golden blocks, exact). EVERY operation gets a
	// GeneratedFailure enum — even with zero declared errors — so the wrapper's
	// `throws(<Op>Error)` always names a concrete type. Ops WITH x-palbase-errors
	// additionally get payload structs + cases + a code-switch init that maps
	// `ServerFailure.code` and tolerantly decodes the payload (decode miss →
	// `.other(backend)`, never a trap). Case order is sorted by case name.
	goldenBlocks := []string{
		// (a) no-errors op → `.other`-only enum with a one-line init.
		`public nonisolated enum TodosGetError: PBError {
    case other(BackendError)
    public nonisolated init(_ backend: BackendError) { self = .other(backend) }
}`,
		// (b) with-errors op → payload struct + bare/data cases + code-switch init.
		`public nonisolated enum RoomsCreateError: PBError {
    public nonisolated struct RoomLockedData: Codable, Sendable {
        public let retryAfter: Int
        public init(retryAfter: Int) {
            self.retryAfter = retryAfter
        }
    }
    case roomFull
    case roomLocked(RoomLockedData)
    case other(BackendError)
    public nonisolated init(_ backend: BackendError) {
        guard case .server(let f) = backend else { self = .other(backend); return }
        switch f.code {
        case "room_full": self = .roomFull
        case "room_locked":
            if let data = f.decodeData(RoomLockedData.self) { self = .roomLocked(data) } else { self = .other(backend) }
        default: self = .other(backend)
        }
    }
}`,
		// (c) the per-op ENDPOINT STRUCT (exact): PBEndpoint, stored input,
		// computed pbRequest with a path-segment array.
		`public nonisolated struct TodosGetEndpoint: PBEndpoint {
    public typealias Response = TodosGetResponse
    public typealias Failure = TodosGetError
    let id: String
    public var pbRequest: PBRequest { PBRequest(.get, ["todos", .param(id)]) }
}`,
		// (d) wrapper lowering (exact): unchanged signature, body is ONE
		// `pb.call(<Op>Endpoint(...))` line — the SDK infers Response/Failure
		// from the endpoint (no as:/failing:/where) and fires onError once at
		// _invokeCore.
		`    public func get(id: String) async throws(TodosGetError) -> TodosGetResponse {
        return try await _pb.call(TodosGetEndpoint(id: id))
    }`,
		// (e) the @Upload ENDPOINT STRUCT (exact): adopts PBUploadEndpoint, holds
		// only the author `input` (the file source is a call-site arg, never
		// stored), and computes `authorizeRequest` — the POST pre-flight — rather
		// than `pbRequest`.
		`public nonisolated struct DocsUploadEndpoint: PBUploadEndpoint {
    public typealias Response = DocsUploadResponse
    public typealias Failure = DocsUploadError
    let input: DocsUploadRequest
    public var authorizeRequest: PBRequest { PBRequest(.post, ["docs"], body: input) }
}`,
		// (f) the @Upload NAMESPACE METHOD (exact): file + input + trailing
		// onProgress, forwarding both to the upload overload of pb.call. Body is
		// ONE line; @discardableResult like any body-returning call.
		`    @discardableResult
    public func upload(file: PBFileSource, input: DocsUploadRequest, onProgress: (@Sendable (BackendUploadProgress) -> Void)? = nil) async throws(DocsUploadError) -> DocsUploadResponse {
        return try await _pb.call(DocsUploadEndpoint(input: input), file: file, onProgress: onProgress)
    }`,
	}
	for _, g := range goldenBlocks {
		if !strings.Contains(out, g) {
			t.Errorf("generated Swift missing golden block:\n%s\n---\n%s", g, out)
		}
	}

	// One PBError enum per emitted op (11 usable ops in the fixture: 10 normal
	// + docs.upload; auth.login is reserved-skipped). New output emits the
	// `PBError` spelling, not the old `GeneratedFailure`.
	if n := strings.Count(out, ": PBError {"); n != 11 {
		t.Errorf("expected 11 PBError enums (one per op), got %d\n---\n%s", n, out)
	}

	// One endpoint struct per emitted op. The 10 NON-upload ops adopt
	// PBEndpoint (body-returning) or PBVoidEndpoint (no body); the single
	// @Upload op (docs.upload) adopts PBUploadEndpoint instead — it MUST NOT
	// land in the PBEndpoint/PBVoidEndpoint bucket (that would mean the
	// upload-kind branch was skipped and the bytes would route through the
	// br-pod).
	if n := strings.Count(out, ": PBEndpoint {") + strings.Count(out, ": PBVoidEndpoint {"); n != 10 {
		t.Errorf("expected 10 endpoint structs (PBEndpoint + PBVoidEndpoint), got %d\n---\n%s", n, out)
	}
	if n := strings.Count(out, ": PBUploadEndpoint {"); n != 1 {
		t.Errorf("expected exactly 1 PBUploadEndpoint struct (docs.upload), got %d\n---\n%s", n, out)
	}

	// Phase B retires the old typed seam: generated code now forwards to the
	// public `pb.call(<Op>Endpoint(...))` surface, so NONE of the old
	// `_invoke`/`failing:`/`asQueryString`/path-interpolation/`@_spi`/
	// `GeneratedFailure`-spelling artifacts may appear. No op may fall back to
	// `throws(BackendError)` either.
	for _, banned := range []string{"_invoke", "_invokeTyped", "failing:",
		"asQueryString", "pbURIComponentEncoded", "pbURIComponentAllowed",
		"@_spi(Generated)", ": GeneratedFailure", "TypedBackendError",
		"init?(envelope:", ", errors: ", "throws(BackendError)"} {
		if strings.Contains(out, banned) {
			t.Errorf("generated Swift references removed surface %q\n---\n%s", banned, out)
		}
	}

	// `auth.*` is reserved — must NOT generate an auth namespace/method.
	if strings.Contains(out, "auth.login") || strings.Contains(out, "func login") {
		t.Errorf("auth.login should be skipped (reserved), but appeared:\n%s", out)
	}

	// No more wrapper `enum Rooms` / `enum Create` shells — types are
	// top-level structs now, not nested under an operation-id enum.
	if strings.Contains(out, "public enum Rooms {") || strings.Contains(out, "public enum Create {") {
		t.Errorf("legacy nested enum shell should be gone:\n%s", out)
	}

	// Gap 2 — the array-of-object list response must NOT collapse to the
	// opaque `[AnyCodableValue]` it used to (a named item struct now carries
	// the element shape).
	if strings.Contains(out, "public typealias TodosListResponse = [AnyCodableValue]") {
		t.Errorf("array-of-object response should be typed, not [AnyCodableValue]:\n%s", out)
	}

	// Dump for the external compile check (PALBE_GEN_OUT set by the harness).
	if p := os.Getenv("PALBE_GEN_OUT"); p != "" {
		if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}

// TestEmitSwift_SkipsReservedNamespaces locks that endpoints whose top
// operationId segment is an SDK-owned namespace (auth / analytics / flags /
// realtime / notifications / perf) are skipped, so a customer endpoint can't
// shadow `pb.auth.*`, `pb.analytics.*`, `pb.flags.*`, `pb.realtime.*`,
// `pb.notifications.*`, or `pb.perf.*`. A non-reserved endpoint in the same set
// is still emitted (the skip is precise, not a blanket drop).
func TestEmitSwift_SkipsReservedNamespaces(t *testing.T) {
	ops := []swiftOp{
		{operationID: "auth.login", method: "post", path: "/auth/login"},
		{operationID: "analytics.track", method: "post", path: "/analytics/track"},
		{operationID: "flags.list", method: "post", path: "/flags/list"},
		{operationID: "realtime.publish", method: "post", path: "/realtime/publish"},
		{operationID: "notifications.send", method: "post", path: "/notifications/send"},
		{operationID: "perf.startTrace", method: "post", path: "/perf/start-trace"},
		{operationID: "rooms.create", method: "post", path: "/rooms/create"},
	}
	out := emitSwift(ops)

	for _, reserved := range []string{"login", "track", "func list", "publish", "send", "startTrace"} {
		if strings.Contains(out, "func "+reserved) {
			t.Errorf("reserved-namespace endpoint should be skipped, but %q appeared:\n%s", reserved, out)
		}
	}
	// The non-reserved endpoint must still be generated.
	if !strings.Contains(out, "func create") {
		t.Errorf("non-reserved rooms.create should be emitted, but was missing:\n%s", out)
	}

	// Visible skip comments must appear at the top of the namespaces section.
	for _, ns := range []string{"analytics", "auth", "flags", "notifications", "perf", "realtime"} {
		want := `// codegen: skipped reserved namespace "` + ns + `" (SDK-owned)`
		if !strings.Contains(out, want) {
			t.Errorf("missing reserved-namespace skip comment for %q:\n%s", ns, out)
		}
	}
}

// TestEmitSwift_ReservedPerf is the focused lock for the PalPerf reservation
// (Faz 1, Task 1b-5): `perf` is reserved like auth/analytics so a customer's
// `perf` controller can never collide with the `pb.perf` facade.
func TestEmitSwift_ReservedPerf(t *testing.T) {
	ops := []swiftOp{
		{operationID: "perf.record", method: "post", path: "/perf/record"},
		{operationID: "rooms.create", method: "post", path: "/rooms/create"},
	}
	out := emitSwift(ops)

	if strings.Contains(out, "func record") {
		t.Errorf("perf.record should be skipped (reserved), but appeared:\n%s", out)
	}
	if !strings.Contains(out, `// codegen: skipped reserved namespace "perf" (SDK-owned)`) {
		t.Errorf("missing reserved-namespace skip comment for perf:\n%s", out)
	}
	if !strings.Contains(out, "func create") {
		t.Errorf("non-reserved rooms.create should still be emitted:\n%s", out)
	}
}

// TestStructLines_IdentifierCollisionDeduped guards the codegen against
// schemas that declare the same field in two conventions (e.g. an AI-written
// Zod object with both `deviceId` and `device_id`). Both wire keys sanitize to
// the Swift ident `deviceId`; without a guard the emitter produced duplicate
// `public let`, init params, and CodingKeys cases → "Invalid redeclaration"
// (uncompilable). The first key wins; the collision is dropped with a visible
// comment, so the output compiles.
func TestStructLines_IdentifierCollisionDeduped(t *testing.T) {
	props := []swiftProp{
		{name: "deviceId", schema: swiftSchema{kind: "string"}},
		{name: "device_id", schema: swiftSchema{kind: "string"}}, // collides → deviceId
		{name: "appVersion", schema: swiftSchema{kind: "string"}},
		{name: "app_version", schema: swiftSchema{kind: "string"}}, // collides → appVersion
		{name: "platform", schema: swiftSchema{kind: "string"}},
	}
	out := strings.Join(structLines("PostAppV1InitRequest", props, 0), "\n")

	// Exactly ONE stored property per colliding ident.
	if n := strings.Count(out, "public let deviceId:"); n != 1 {
		t.Errorf("expected exactly 1 `public let deviceId`, got %d\n---\n%s", n, out)
	}
	if n := strings.Count(out, "public let appVersion:"); n != 1 {
		t.Errorf("expected exactly 1 `public let appVersion`, got %d\n---\n%s", n, out)
	}
	// Exactly ONE init-body assignment per ident (no duplicate self.x = x).
	if n := strings.Count(out, "self.deviceId = deviceId"); n != 1 {
		t.Errorf("expected exactly 1 `self.deviceId = deviceId`, got %d\n---\n%s", n, out)
	}
	// All surviving idents equal their wire key (deviceId/appVersion/platform),
	// so NO CodingKeys enum is emitted — and crucially the dropped snake_case
	// keys must NOT have smuggled in a `case ... = "device_id"`.
	if strings.Contains(out, "enum CodingKeys") {
		t.Errorf("no CodingKeys expected (all idents match wire keys)\n---\n%s", out)
	}
	if strings.Contains(out, `case deviceId = "device_id"`) || strings.Contains(out, `case appVersion = "app_version"`) {
		t.Errorf("dropped snake_case wire key leaked into a CodingKeys case\n---\n%s", out)
	}
	// The collision is visible, not silent.
	if !strings.Contains(out, "skipped duplicate key") {
		t.Errorf("expected a `skipped duplicate key` comment for the dropped key\n---\n%s", out)
	}
	// The non-colliding field survives.
	if !strings.Contains(out, "public let platform:") {
		t.Errorf("non-colliding field `platform` should survive\n---\n%s", out)
	}
}

// TestStructLines_MixedCaseVariants is the pathological edge case: the same
// concept declared four ways. identOf collapses device_id+deviceId → deviceId
// (one collision, deduped), while DeviceID → deviceID and deviceid → deviceid
// are DISTINCT idents that survive. The CodingKeys must be strategy-aware:
// the snake_case winner (device_id → deviceId) needs NO rawValue (the SDK's
// .convertFromSnakeCase recovers it), but DeviceID can't be reconstructed so it
// keeps an explicit rawValue. This proves first-wins dedup + round-trip-aware
// CodingKeys compose correctly.
func TestStructLines_MixedCaseVariants(t *testing.T) {
	props := []swiftProp{
		{name: "device_id", schema: swiftSchema{kind: "string"}},
		{name: "deviceId", schema: swiftSchema{kind: "string"}}, // collides → deviceId
		{name: "DeviceID", schema: swiftSchema{kind: "string"}}, // → deviceID (distinct)
		{name: "deviceid", schema: swiftSchema{kind: "string"}}, // → deviceid (distinct)
	}
	out := strings.Join(structLines("EdgeReq", props, 0), "\n")

	for _, want := range []string{
		"public let deviceId: String?",
		"public let deviceID: String?",
		"public let deviceid: String?",
		`case deviceID = "DeviceID"`, // not snake-recoverable → explicit rawValue
		"skipped duplicate key",      // device_id/deviceId collision dropped
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n---\n%s", want, out)
		}
	}
	// The snake_case winner must NOT carry a rawValue (the strategy handles it).
	if strings.Contains(out, `case deviceId = "device_id"`) {
		t.Errorf("deviceId must have no rawValue (.convertFromSnakeCase recovers it)\n---\n%s", out)
	}
	// Exactly three surviving stored properties (one collision removed).
	if n := strings.Count(out, "public let "); n != 3 {
		t.Errorf("expected 3 stored properties, got %d\n---\n%s", n, out)
	}
}

// TestTopLevelErrorEnumLines_OtherIdentReserved guards the new `.other`
// fallback case: every GeneratedFailure enum ends with `case
// other(BackendError)`, so a declared error whose name sanitizes to `other`
// (or to a previous case's ident) would emit an uncompilable redeclaration.
// Such a case is skipped with a visible comment — the wire code still
// surfaces at runtime through `.other(backend)`.
func TestTopLevelErrorEnumLines_OtherIdentReserved(t *testing.T) {
	errs := []swiftErrorDef{
		{name: "notFound", code: "not_found", status: 404},
		{name: "other", code: "weird_other", status: 409},
	}
	out := strings.Join(topLevelErrorEnumLines("RoomsCreateError", errs), "\n")

	if n := strings.Count(out, "case other(BackendError)"); n != 1 {
		t.Errorf("expected exactly 1 `case other(BackendError)`, got %d\n---\n%s", n, out)
	}
	if !strings.Contains(out, "case notFound") {
		t.Errorf("surviving declared case notFound missing\n---\n%s", out)
	}
	if !strings.Contains(out, `case "not_found": self = .notFound`) {
		t.Errorf("switch mapping for surviving code missing\n---\n%s", out)
	}
	// The colliding declared error is dropped visibly, and its code must NOT
	// appear in the switch (it would map onto the fallback case's ident).
	if !strings.Contains(out, "skipped declared error") {
		t.Errorf("expected a visible skip comment for the `other` collision\n---\n%s", out)
	}
	if strings.Contains(out, `"weird_other"`) {
		t.Errorf("dropped error's code leaked into the switch\n---\n%s", out)
	}
}

// bodylessUploadOpenAPI mirrors the REAL shape the backend runtime emits for an
// @Upload op: the 200 response is declared but carries NO JSON body (content is
// empty), so responseSchema → nil and op.output is nil. This is exactly the
// todoapp `POST /docs/` (docs.upload) op observed on the live OpenAPI
// (responses 200/400/401, all with empty content). The earlier TestEmitSwift
// fixture gave docs.upload a FULL 200 body, which is why it never caught this.
const bodylessUploadOpenAPI = `{
  "openapi":"3.1.0","info":{"title":"t","version":"1"},
  "paths":{
    "/docs/":{"post":{"operationId":"docs.upload",
      "requestBody":{"content":{"application/json":{"schema":{"type":"object",
        "properties":{"title":{"type":"string"}},"required":["title"]}}}},
      "responses":{
        "200":{"description":"ok"},
        "400":{"description":"bad"},
        "401":{"description":"unauthorized"}
      },
      "x-palbase-upload":{"bucket":"docs","pathTemplate":"{userId}/{uploadId}-{filename}"}}}
  }
}`

// TestEmitSwift_bodylessUploadEmitsResponseStruct locks the fix for the @Upload
// codegen bug that broke the entire iOS SDK build: an @Upload op whose OpenAPI
// 200 has no JSON body still references `<Op>Response` in its endpoint struct
// (`typealias Response = DocsUploadResponse`) and namespace method, so the
// emitter MUST emit a concrete `DocsUploadResponse` type or the generated file
// fails to compile with "cannot find type 'DocsUploadResponse' in scope".
//
// Mutation proof: revert the `else if op.upload != nil` Response-struct branch
// in emitTypeTree and this test goes RED (no DocsUploadResponse declaration),
// while the dangling `typealias Response = DocsUploadResponse` still appears.
func TestEmitSwift_bodylessUploadEmitsResponseStruct(t *testing.T) {
	ops, err := parseOpenAPIForSwift([]byte(bodylessUploadOpenAPI))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := emitSwift(ops)

	// The endpoint struct (and method) reference DocsUploadResponse...
	if !strings.Contains(out, "public typealias Response = DocsUploadResponse") {
		t.Fatalf("upload endpoint must reference DocsUploadResponse\n---\n%s", out)
	}
	// ...so a concrete DocsUploadResponse TYPE must be declared, or the file
	// won't compile. An empty Codable struct is the contract-correct stand-in.
	if !strings.Contains(out, "struct DocsUploadResponse: Codable, Sendable {") {
		t.Errorf("body-less @Upload op must still emit a concrete DocsUploadResponse struct (else the generated SDK fails to build)\n---\n%s", out)
	}
	// And it must stay an @Upload (PBUploadEndpoint), not silently fall back.
	if !strings.Contains(out, "struct DocsUploadEndpoint: PBUploadEndpoint {") {
		t.Errorf("body-less upload op must still adopt PBUploadEndpoint\n---\n%s", out)
	}
}
