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
	pathParams  []string        // `{name}` path segments in path order → leading String method args
	input       *swiftSchema
	output      *swiftSchema
	headers     *swiftSchema    // declared request headers (parameters[in:header]) → <Op>Headers struct
	query       *swiftSchema    // declared query params (parameters[in:query]) → <Op>Query struct
	errors      []swiftErrorDef // inferred errors via the `x-palbase-errors` extension (stage-time throw analysis)
	upload      *swiftUpload    // direct-storage upload (@Upload) via `x-palbase-upload` — nil for a normal op
}

// swiftUpload describes one @Upload operation, parsed from the
// `x-palbase-upload` OpenAPI extension. Its presence makes the emitter generate
// a PBUploadEndpoint + pb.<ns>.upload(file:input:onProgress:) instead of a
// normal endpoint. The bytes go client→storage directly; the operation's
// response is the typed completion result.
type swiftUpload struct {
	bucket       string
	pathTemplate string
	// No maxSize/allowedTypes: @Upload names only the bucket — the size limit +
	// MIME allowlist live on the bucket (config/storage.ts) and are enforced by
	// storage at the PUT, so x-palbase-upload carries only bucket + pathTemplate.
}

// swiftErrorDef describes one inferred error from an endpoint's
// `x-palbase-errors` OpenAPI extension. The lowerCamel error-class
// `name` is the iOS enum case identifier; the wire `code` is what the
// envelope carries and what the generated `GeneratedFailure.init(_
// backend:)` matches against `ServerFailure.code`. `data`, when
// present, is the JSON schema for the structured payload the typed
// enum case lifts as an associated value.
type swiftErrorDef struct {
	name        string       // lowerCamel class name (e.g. "todoLocked") — becomes the Swift case identifier
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
				pathParams:  pathParamNames(path),
				input:       requestSchema(op),
				output:      responseSchema(op),
				headers:     headerSchema(op),
				query:       querySchema(op),
				errors:      declaredErrors(op),
				upload:      declaredUpload(op),
			})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].operationID < ops[j].operationID })
	return ops, nil
}

// declaredErrors reads the `x-palbase-errors` OpenAPI extension the
// backend runtime stashes on each operation. In the @palbase/backend
// 6.x class-controller model the error set is INFERRED from the
// endpoint's throw sites (controller/service `throw new NotFound()` /
// defineError classes, collected by stage-time throw analysis) — never
// declared by hand. Returns nil when no errors were inferred; the op
// still gets a `.other(BackendError)`-only GeneratedFailure enum, so
// the emit path is uniform across all operations.
//
// The extension shape (wire format v1, emitted identically by the prod
// generator, the dev-server, and palbase-ts's spec twin):
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

// declaredUpload reads the `x-palbase-upload` OpenAPI extension the backend
// runtime stashes on each @Upload operation. Returns nil for a normal op (the
// extension is absent). Its presence makes the emitter generate a
// PBUploadEndpoint. The extension shape (emitted identically by the prod
// generator via uploadExtension):
//
//	"x-palbase-upload": {
//	  "bucket": "docs", "pathTemplate": "{userId}/{uploadId}-{filename}"
//	}
//
// Only bucket + pathTemplate — the size/type limits live on the bucket and are
// storage-enforced, so the client (and thus this codegen) never needs them.
//
// bucket + pathTemplate are required; maxSize/allowedTypes are optional.
func declaredUpload(op map[string]any) *swiftUpload {
	ext, ok := op["x-palbase-upload"].(map[string]any)
	if !ok || len(ext) == 0 {
		return nil
	}
	bucket, _ := ext["bucket"].(string)
	pathTemplate, _ := ext["pathTemplate"].(string)
	if bucket == "" || pathTemplate == "" {
		// Malformed extension — treat as a non-upload op rather than emit a
		// broken PBUploadEndpoint (visible-fail over silent-wrong).
		return nil
	}
	return &swiftUpload{bucket: bucket, pathTemplate: pathTemplate}
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

// headerSchema collects the operation's `parameters[in:header]` entries
// into a synthetic object swiftSchema (one property per header), so the
// emitter can render an <Op>Headers struct exactly like a request body.
// Returns nil when the op declares no header parameters.
func headerSchema(op map[string]any) *swiftSchema {
	return parametersSchemaIn(op, "header")
}

// querySchema collects the operation's `parameters[in:query]` entries into a
// synthetic object swiftSchema (one property per query param), so the emitter
// can render an <Op>Query struct + asQueryString(). Returns nil when the op
// declares no query parameters. Mirrors headerSchema (the generator emits
// query/header parameters the same way — name-sorted, schema-typed props).
func querySchema(op map[string]any) *swiftSchema {
	return parametersSchemaIn(op, "query")
}

// parametersSchemaIn collects the operation's `parameters[in:<where>]` entries
// into a synthetic object swiftSchema (one property per parameter), name-sorted
// for deterministic output. Returns nil when the op declares no parameter in
// that location. Shared by headerSchema (in:header) + querySchema (in:query);
// path params are threaded separately (pathParamNames → leading method args).
func parametersSchemaIn(op map[string]any, where string) *swiftSchema {
	paramsRaw, ok := op["parameters"].([]any)
	if !ok || len(paramsRaw) == 0 {
		return nil
	}
	var props []swiftProp
	for _, p := range paramsRaw {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if in, _ := pm["in"].(string); in != where {
			continue
		}
		name, _ := pm["name"].(string)
		if name == "" {
			continue
		}
		required, _ := pm["required"].(bool)
		var ps swiftSchema
		if sm, ok := pm["schema"].(map[string]any); ok {
			ps = parseSwiftSchema(sm)
		} else {
			ps = swiftSchema{kind: "string"}
		}
		props = append(props, swiftProp{name: name, schema: ps, required: required})
	}
	if len(props) == 0 {
		return nil
	}
	// Deterministic field order (parameters arrive sorted from the
	// generator, but don't rely on map iteration upstream).
	sort.Slice(props, func(i, j int) bool { return props[i].name < props[j].name })
	return &swiftSchema{kind: "object", props: props}
}

// pathParamNames extracts the `{name}` template segments from an OpenAPI
// path, in left-to-right path order (the order they must appear as leading
// method arguments). The path string is the authoritative source: the
// emitter substitutes each `{name}` back into the wire path, so the names
// must match the literal template exactly. Returns nil when the path has no
// templated segments (the common case) — that keeps the emit path byte-
// identical for non-parameterised operations. Empty `{}` is ignored.
func pathParamNames(path string) []string {
	var out []string
	for {
		open := strings.IndexByte(path, '{')
		if open < 0 {
			break
		}
		close := strings.IndexByte(path[open:], '}')
		if close < 0 {
			break
		}
		close += open
		name := path[open+1 : close]
		if name != "" {
			out = append(out, name)
		}
		path = path[close+1:]
	}
	return out
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
