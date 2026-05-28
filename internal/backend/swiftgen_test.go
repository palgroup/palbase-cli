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
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}}}}}}}
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

	// Dump for the external compile check (PALBE_GEN_OUT set by the harness).
	if p := os.Getenv("PALBE_GEN_OUT"); p != "" {
		if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}
