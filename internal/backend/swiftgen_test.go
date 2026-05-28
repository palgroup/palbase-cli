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
      "requestBody":{"content":{"application/json":{"schema":{"type":"object",
        "properties":{"name":{"type":"string"},"capacity":{"type":"integer"},"kind":{"type":"string","enum":["public","private"]}},
        "required":["name","kind"]}}}},
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object",
        "properties":{"id":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"score":{"type":"number","nullable":true}},
        "required":["id","tags","score"]}}}}}}},
    "/rooms/id/get":{"post":{"operationId":"rooms.id.get",
      "requestBody":{"content":{"application/json":{"schema":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}}},
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}}}}}},
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
		"import Palbe",
		"public extension PalBackendClient {",
		"var rooms: PBRoomsNamespace",
		"func create(_ input: Rooms.Create.Input) async throws(BackendError) -> Rooms.Create.Output",
		`_invoke(method: "POST", path: "/rooms/create", input, as: Rooms.Create.Output.self)`,
		"public enum KindValue: String, Codable, Sendable {",
		"case `public` = \"public\"", // keyword escaped
		"public let capacity: Int?",  // optional (not required)
		"public let score: Double?",  // nullable → optional
		"public let tags: [String]",  // array
		"struct PBRoomsIdNamespace",  // nested namespace
		"func get(_ input: Rooms.Id.Get.Input)",
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

	// Single merged `enum Rooms` (not duplicated per operation).
	if n := strings.Count(out, "public enum Rooms {"); n != 1 {
		t.Errorf("expected exactly one `enum Rooms`, got %d", n)
	}

	// Dump for the external compile check (PALBE_GEN_OUT set by the harness).
	if p := os.Getenv("PALBE_GEN_OUT"); p != "" {
		if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}
