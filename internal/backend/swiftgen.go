package backend

import (
	"encoding/json"
	"fmt"
	"sort"
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
			})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].operationID < ops[j].operationID })
	return ops, nil
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

	typ, _ := s["type"].(string)
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
