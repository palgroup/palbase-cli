package backend

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Swift code generation from an OpenAPI 3.1 document, Go-native (no external
// tools). Emits a single PalbaseEndpoints.swift that extends the Palbe SDK's
// `pb` with namespaced, typed calls:
//
//   endpoints/rooms/create.ts (operationId "rooms.create")
//     → pb.rooms.create(.init(name: "lobby")) -> Rooms.Create.Output
//
// Mirrors the reference emitter in palbackend-ios/Sources/PalBackendCodegen.
// The generated file `import Palbe` and lowers every call to the public
// PalBackendClient._invoke seam, so it compiles in the consumer app target.

// --- Parsed model -----------------------------------------------------------

type swiftSchema struct {
	kind     string // string|number|integer|boolean|object|array|enum|any
	nullable bool
	props    []swiftProp  // object
	elem     *swiftSchema // array
	enumVals []string     // enum
}

type swiftProp struct {
	name     string
	schema   swiftSchema
	required bool
}

type swiftOp struct {
	operationID string
	method      string
	path        string
	input       *swiftSchema
	output      *swiftSchema
	errors      []swiftErrorDef // declared errors via defineEndpoint({ errors: { … } })
}

// swiftErrorDef describes one declared error from an endpoint's
// `x-palbase-errors` OpenAPI extension. The friendly TS-side `name` is
// the iOS enum case identifier; the wire `code` is what the envelope
// carries and what `TypedBackendError.init(envelope:)` matches on.
// `data`, when present, is the JSON schema for the structured payload
// the typed enum case lifts as an associated value.
type swiftErrorDef struct {
	name        string       // TS-side key (e.g. "todoLocked") — becomes the Swift case identifier
	code        string       // wire `error` value (e.g. "todo_locked") — matched at decode time
	status      int          // HTTP status — kept for doc-comments and quick-help
	description string       // optional human description
	data        *swiftSchema // nil when the error carries no payload
}

// --- Parse ------------------------------------------------------------------

func parseOpenAPIForSwift(specBytes []byte) ([]swiftOp, error) {
	var root map[string]any
	if err := json.Unmarshal(specBytes, &root); err != nil {
		return nil, fmt.Errorf("openapi.json is not valid JSON: %w", err)
	}
	paths, _ := root["paths"].(map[string]any)
	if paths == nil {
		return nil, fmt.Errorf("openapi.json has no `paths`")
	}

	var ops []swiftOp
	for path, item := range paths {
		methods, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for method, raw := range methods {
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			opID, _ := op["operationId"].(string)
			if opID == "" {
				continue
			}
			ops = append(ops, swiftOp{
				operationID: opID,
				method:      strings.ToUpper(method),
				path:        path,
				input:       requestSchema(op),
				output:      responseSchema(op),
				errors:      declaredErrors(op),
			})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].operationID < ops[j].operationID })
	return ops, nil
}

// declaredErrors reads the `x-palbase-errors` OpenAPI extension the
// backend SDK stashes on each operation when `defineEndpoint({errors:…})`
// is set. Returns nil when the endpoint has no declared errors — that
// flips the emit path back to plain `_invoke` (no `<Endpoint>.Error` enum,
// the wire envelope surfaces as `BackendError.server` on iOS).
//
// The extension shape mirrors palbase-ts/backend/src/openapi/convert.ts:
//
//	"x-palbase-errors": {
//	  "todoNotFound": { "status": 404, "code": "todo_not_found", "hasData": false, "description": "..." },
//	  "todoLocked":   { "status": 409, "code": "todo_locked",    "hasData": true,  "description": "..." }
//	}
//
// The matching response under responses["409"].content."application/json".schema
// holds the data payload's JSON schema (parsed here for the typed enum's
// associated value).
func declaredErrors(op map[string]any) []swiftErrorDef {
	extRaw, ok := op["x-palbase-errors"].(map[string]any)
	if !ok || len(extRaw) == 0 {
		return nil
	}
	responses, _ := op["responses"].(map[string]any)
	out := make([]swiftErrorDef, 0, len(extRaw))
	for name, raw := range extRaw {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		statusF, _ := entry["status"].(float64)
		code, _ := entry["code"].(string)
		description, _ := entry["description"].(string)
		hasData, _ := entry["hasData"].(bool)
		if code == "" || statusF == 0 {
			continue
		}
		def := swiftErrorDef{
			name:        name,
			code:        code,
			status:      int(statusF),
			description: description,
		}
		if hasData {
			def.data = errorDataSchema(responses, int(statusF), code)
		}
		out = append(out, def)
	}
	// Deterministic order: by case name.
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// errorDataSchema pulls the data-payload schema out of a declared error's
// response shape. The TS emitter writes the response as either the
// declared error's standalone schema (single error on a status) or as
// `oneOf` (multiple errors sharing a status). In the oneOf case we pick
// the variant whose `error: { const: <code> }` discriminator matches.
// Returns nil if the response is missing or no `data` property is set.
func errorDataSchema(responses map[string]any, status int, code string) *swiftSchema {
	if responses == nil {
		return nil
	}
	resp, _ := responses[strconv.Itoa(status)].(map[string]any)
	if resp == nil {
		return nil
	}
	content, _ := resp["content"].(map[string]any)
	if content == nil {
		return nil
	}
	jsonCT, _ := content["application/json"].(map[string]any)
	if jsonCT == nil {
		return nil
	}
	schema, _ := jsonCT["schema"].(map[string]any)
	if schema == nil {
		return nil
	}

	// oneOf: pick the variant whose `error.const` matches our code.
	if variants, ok := schema["oneOf"].([]any); ok {
		for _, v := range variants {
			vm, ok := v.(map[string]any)
			if !ok {
				continue
			}
			props, _ := vm["properties"].(map[string]any)
			if errProp, ok := props["error"].(map[string]any); ok {
				if c, _ := errProp["const"].(string); c == code {
					return extractDataProperty(vm)
				}
			}
		}
		return nil
	}
	return extractDataProperty(schema)
}

func extractDataProperty(schema map[string]any) *swiftSchema {
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		return nil
	}
	dm, ok := props["data"].(map[string]any)
	if !ok {
		return nil
	}
	s := parseSwiftSchema(dm)
	return &s
}

func requestSchema(op map[string]any) *swiftSchema {
	body, _ := op["requestBody"].(map[string]any)
	if body == nil {
		return nil
	}
	return schemaFromContent(body["content"])
}

func responseSchema(op map[string]any) *swiftSchema {
	responses, _ := op["responses"].(map[string]any)
	if responses == nil {
		return nil
	}
	// Prefer 200, then 201, then any other 2xx (sorted).
	order := []string{"200", "201"}
	var others []string
	for code := range responses {
		if strings.HasPrefix(code, "2") && code != "200" && code != "201" {
			others = append(others, code)
		}
	}
	sort.Strings(others)
	order = append(order, others...)
	for _, code := range order {
		resp, ok := responses[code].(map[string]any)
		if !ok {
			continue
		}
		if s := schemaFromContent(resp["content"]); s != nil {
			return s
		}
	}
	return nil
}

func schemaFromContent(content any) *swiftSchema {
	c, ok := content.(map[string]any)
	if !ok {
		return nil
	}
	jsonCt, ok := c["application/json"].(map[string]any)
	if !ok {
		return nil
	}
	schema, ok := jsonCt["schema"].(map[string]any)
	if !ok {
		return nil
	}
	// Skip $ref'd shared components (error envelope etc.).
	if _, hasRef := schema["$ref"]; hasRef {
		return nil
	}
	s := parseSwiftSchema(schema)
	return &s
}

func parseSwiftSchema(s map[string]any) swiftSchema {
	nullable, _ := s["nullable"].(bool)

	if enumRaw, ok := s["enum"].([]any); ok {
		var cases []string
		allStrings := true
		for _, v := range enumRaw {
			if str, ok := v.(string); ok {
				cases = append(cases, str)
			} else {
				allStrings = false
				break
			}
		}
		if allStrings && len(cases) > 0 {
			return swiftSchema{kind: "enum", nullable: nullable, enumVals: cases}
		}
	}

	// Draft 7 / OpenAPI 3.1 allow `type` as an array — `["string","null"]`
	// is what `zod-to-json-schema` emits for `z.string().nullable()`. Lower
	// it to the single non-null type + nullable=true so the Swift side
	// gets `String?` instead of falling through to AnyCodableValue.
	typ, _ := s["type"].(string)
	if typ == "" {
		if arr, ok := s["type"].([]any); ok {
			for _, v := range arr {
				if str, ok := v.(string); ok {
					if str == "null" {
						nullable = true
					} else if typ == "" {
						typ = str
					}
				}
			}
		}
	}
	switch typ {
	case "string":
		return swiftSchema{kind: "string", nullable: nullable}
	case "number":
		return swiftSchema{kind: "number", nullable: nullable}
	case "integer":
		return swiftSchema{kind: "integer", nullable: nullable}
	case "boolean":
		return swiftSchema{kind: "boolean", nullable: nullable}
	case "array":
		var elem swiftSchema
		if items, ok := s["items"].(map[string]any); ok {
			elem = parseSwiftSchema(items)
		} else {
			elem = swiftSchema{kind: "any"}
		}
		return swiftSchema{kind: "array", nullable: nullable, elem: &elem}
	case "object":
		return parseSwiftObject(s, nullable)
	default:
		if _, hasProps := s["properties"]; hasProps {
			return parseSwiftObject(s, nullable)
		}
		return swiftSchema{kind: "any", nullable: nullable}
	}
}

func parseSwiftObject(s map[string]any, nullable bool) swiftSchema {
	propsRaw, _ := s["properties"].(map[string]any)
	requiredSet := map[string]bool{}
	if reqRaw, ok := s["required"].([]any); ok {
		for _, r := range reqRaw {
			if str, ok := r.(string); ok {
				requiredSet[str] = true
			}
		}
	}
	var names []string
	for name := range propsRaw {
		names = append(names, name)
	}
	sort.Strings(names)
	var props []swiftProp
	for _, name := range names {
		var ps swiftSchema
		if pm, ok := propsRaw[name].(map[string]any); ok {
			ps = parseSwiftSchema(pm)
		} else {
			ps = swiftSchema{kind: "any"}
		}
		props = append(props, swiftProp{name: name, schema: ps, required: requiredSet[name]})
	}
	return swiftSchema{kind: "object", nullable: nullable, props: props}
}
