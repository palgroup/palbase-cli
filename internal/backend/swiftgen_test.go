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
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object",
        "properties":{"id":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"score":{"type":"number","nullable":true}},
        "required":["id","tags","score"]}}}}}}},
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
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}}}}}}}
  }
}`

func TestEmitSwift(t *testing.T) {
	ops, err := parseOpenAPIForSwift([]byte(fixtureOpenAPI))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := emitSwift(ops)

	must := []string{
		"@_spi(Generated) import Palbe",
		"public extension PalBackendClient {",
		"var rooms: PBRoomsNamespace",
		// Top-level Request / Response structs per operation — no
		// nested `enum Rooms { typealias Input = ... }` walk.
		"public nonisolated struct RoomsCreateRequest: Codable, Sendable {",
		"public nonisolated struct RoomsCreateResponse: Codable, Sendable {",
		"public nonisolated struct RoomsIdGetRequest: Codable, Sendable {",
		"public nonisolated struct RoomsIdGetResponse: Codable, Sendable {",
		// Call signature references the flat top-level names. rooms.create
		// declares headers, so the method gains a `headers:` parameter and
		// the seam call forwards `headers.asHeaderDict()`.
		"func create(_ input: RoomsCreateRequest, headers: RoomsCreateHeaders) async throws(BackendError) -> RoomsCreateResponse",
		`_invoke(method: "POST", path: "/rooms/create", input, as: RoomsCreateResponse.self, headers: headers.asHeaderDict())`,
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
		"public nonisolated struct Meta: Codable, Sendable {",        // nested struct
		"public let pinned: Bool?",                       // nested optional
		"public let tags: [String]",                      // nested required
		"public let body: String?",                       // parent optional
		"public let meta: Meta",                          // parent → short ref
		"public let title: String",                       // parent required
		// `type: ["string","null"]` (zod-to-json-schema for
		// z.string().nullable()) lowers to String? — NOT
		// AnyCodableValue. Without the type-array lowering the
		// generated code would expose AnyCodableValue in public
		// position and the SDK's SPI gate would reject it.
		"public nonisolated struct HasNullableResponse: Codable, Sendable {",
		"public let error: String?",
		"public let ok: Bool",
		// Gap 1 — PATH PARAMETERS. A `{id}` path segment threads an `id:
		// String` LEADING method arg, and the seam `path:` literal becomes a
		// percent-encoding interpolation of that arg (the `{id}` is no longer
		// emitted literally). Body-bearing ops put path params FIRST, then
		// the input. No-body ops (get/delete) take just the path param.
		"func get(id: String) async throws(BackendError) -> TodosGetResponse",
		`_invoke(method: "GET", path: "/todos/\(id.addingPercentEncoding(withAllowedCharacters: .pbURIComponentAllowed) ?? id)", as: TodosGetResponse.self)`,
		"func delete(id: String) async throws(BackendError) {",
		`_invoke(method: "DELETE", path: "/todos/\(id.addingPercentEncoding(withAllowedCharacters: .pbURIComponentAllowed) ?? id)")`,
		"func update(id: String, _ input: TodosUpdateRequest) async throws(BackendError) -> TodosUpdateResponse",
		`_invoke(method: "PATCH", path: "/todos/\(id.addingPercentEncoding(withAllowedCharacters: .pbURIComponentAllowed) ?? id)", input, as: TodosUpdateResponse.self)`,
		// Multiple path params → both as args in path order, both interpolated.
		"func get(orgId: String, userId: String) async throws(BackendError) -> OrgsMembersGetResponse",
		`path: "/orgs/\(orgId.addingPercentEncoding(withAllowedCharacters: .pbURIComponentAllowed) ?? orgId)/members/\(userId.addingPercentEncoding(withAllowedCharacters: .pbURIComponentAllowed) ?? userId)"`,
		// Gap 2 — ARRAY-OF-OBJECT RESPONSE. `GET /todos` emits a NAMED item
		// struct (Codable+Sendable, snake_case→camelCase) and the response is
		// a typed `[Item]`, not an opaque `[AnyCodableValue]`.
		"public nonisolated struct TodosListResponseItem: Codable, Sendable {",
		"public let completed: Bool",
		"public let createdAt: String", // created_at → camelCase
		"public typealias TodosListResponse = [TodosListResponseItem]",
		// QUERY PARAMETERS. A `parameters[in:query]` op gains a `query:
		// <Op>Query` method arg and the seam `path:` literal is suffixed with
		// `+ query.asQueryString()` so the params ride the existing _invoke.
		"public nonisolated struct SearchRunQuery: Codable, Sendable {",
		"public let q: String",     // required query param
		"public let limit: Int?",   // optional query param
		"public func asQueryString() -> String {",
		"func run(query: SearchRunQuery) async throws(BackendError) -> SearchRunResponse",
		`_invoke(method: "GET", path: "/search" + query.asQueryString(), as: SearchRunResponse.self)`,
	}
	for _, m := range must {
		if !strings.Contains(out, m) {
			t.Errorf("generated Swift missing: %q\n---\n%s", m, out)
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
// realtime) are skipped, so a customer endpoint can't shadow `pb.auth.*`,
// `pb.analytics.*`, `pb.flags.*`, or `pb.realtime.*`. A non-reserved endpoint
// in the same set is still emitted (the skip is precise, not a blanket drop).
func TestEmitSwift_SkipsReservedNamespaces(t *testing.T) {
	ops := []swiftOp{
		{operationID: "auth.login", method: "post", path: "/auth/login"},
		{operationID: "analytics.track", method: "post", path: "/analytics/track"},
		{operationID: "flags.list", method: "post", path: "/flags/list"},
		{operationID: "realtime.publish", method: "post", path: "/realtime/publish"},
		{operationID: "rooms.create", method: "post", path: "/rooms/create"},
	}
	out := emitSwift(ops)

	for _, reserved := range []string{"login", "track", "func list", "publish"} {
		if strings.Contains(out, "func "+reserved) {
			t.Errorf("reserved-namespace endpoint should be skipped, but %q appeared:\n%s", reserved, out)
		}
	}
	// The non-reserved endpoint must still be generated.
	if !strings.Contains(out, "func create") {
		t.Errorf("non-reserved rooms.create should be emitted, but was missing:\n%s", out)
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
