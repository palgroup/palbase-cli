package backend

import (
	"os"
	"strings"
	"testing"
)

// TestEmitSwift_RealEmitterFixture is the M1 cross-binding golden: the input
// OpenAPI document is the REAL @palbase/backend emitter's output over its
// sample-project integration fixture — NOT a synthetic spec written here. It
// locks the producer↔consumer boundary with real bytes: if the SDK twin's
// x-palbase-errors emission and swiftgen's reader drift apart, this goes red.
//
// REGENERATING testdata/openapi_real_fixture.json (NEVER hand-edit):
//
//	# in palgroup/palbase-ts:
//	pnpm --filter @palbase/backend emit:openapi-fixture ./fixture.json
//	cp fixture.json <sdk/cli>/internal/backend/testdata/openapi_real_fixture.json
//
// The fixture's rooms.create infers RoomLocked (defineError, 409, data
// {retryAfter}) from a direct controller-body throw + NotFound (built-in 404)
// through the rooms service; rooms.getOne throws an OVERRIDDEN unknown code
// (room_not_found) which the emitter correctly SKIPS (unknown → no extension).
func TestEmitSwift_RealEmitterFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/openapi_real_fixture.json")
	if err != nil {
		t.Fatalf("read real-emitter fixture: %v", err)
	}
	ops, err := parseOpenAPIForSwift(raw)
	if err != nil {
		t.Fatalf("parse real-emitter fixture: %v", err)
	}
	out := emitSwift(ops)

	// The with-errors op: enum cases from the REAL emitter's extension.
	must := []string{
		"public nonisolated enum RoomsCreateError: PBError {",
		"case notFound",
		"case roomLocked(RoomLockedData)",
		"case other(BackendError)",
		`case "room_locked":`,
		`case "not_found": self = .notFound`,
		"if let data = f.decodeData(RoomLockedData.self) { self = .roomLocked(data) } else { self = .other(backend) }",
		// The payload struct decoded from the emitter's responses[409] data
		// schema. NOTE the REAL boundary byte this golden locked on first run:
		// zod's z.number() lowers to JSON-schema "number" → Swift Double (a
		// synthetic fixture had assumed Int). z.number().int() would emit
		// "integer" → Int.
		"public nonisolated struct RoomLockedData: Codable, Sendable {",
		"public let retryAfter: Double",
		// Typed-throws signature on the generated method.
		"async throws(RoomsCreateError)",
	}
	for _, m := range must {
		if !strings.Contains(out, m) {
			t.Fatalf("real-fixture swift missing %q\n----\n%s", m, out)
		}
	}

	// EVERY op gets its own enum — ops with no inferred errors carry only
	// `.other`. The fixture's getOne (unknown overridden code, extension
	// skipped by the emitter) must be exactly the bare shape.
	if !strings.Contains(out, "public nonisolated enum RoomsGetOneError: PBError {\n    case other(BackendError)") {
		t.Fatalf("no-errors op must get the bare .other enum\n----\n%s", out)
	}
	if strings.Contains(out, "RoomsGetOneError: PBError {\n    case roomNotFound") {
		t.Fatalf("unknown overridden code must NOT produce a typed case")
	}
}
