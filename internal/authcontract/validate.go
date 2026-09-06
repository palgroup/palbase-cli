// Package authcontract validates the API schema emitted by the auth module.
package authcontract

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
)

// Update schema.json with v2's socialcontract -sdk-root, then regenerate.
//
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config codegen.yaml schema.json
//go:embed schema.json
var schemaJSON []byte

var schemaOnce = sync.OnceValues(func() (*openapi3.T, error) {
	doc, err := openapi3.NewLoader().LoadFromData(schemaJSON)
	if err != nil {
		return nil, err
	}
	return doc, doc.Validate(context.Background())
})

func Validate(name string, raw []byte) error {
	if len(raw) > 256*1024 {
		return fmt.Errorf("auth config exceeds 256 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	value, err := jsonValue(d, "", 0)
	if err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("auth config contains trailing JSON")
	}
	doc, err := schemaOnce()
	if err != nil {
		return fmt.Errorf("invalid embedded auth schema: %w", err)
	}
	schema := doc.Components.Schemas[name]
	if schema == nil {
		return fmt.Errorf("unknown auth schema %s", name)
	}
	return schema.Value.VisitJSON(value, openapi3.SetSchemaErrorMessageCustomizer(func(e *openapi3.SchemaError) string {
		// The validator's default includes the entire supplied value.
		// Config/credential errors must never echo a secret.
		return "/" + strings.Join(e.JSONPointer(), "/") + ": does not match " + name + " (" + e.SchemaField + ")"
	}))
}

// DecodeStrict applies the same structural JSON rules to local selections.
func DecodeStrict(raw []byte, target any) error {
	if len(raw) > 256*1024 {
		return fmt.Errorf("auth config exceeds 256 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if _, err := jsonValue(d, "", 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("auth config contains trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(target)
}

func jsonValue(d *json.Decoder, path string, depth int) (any, error) {
	if depth > 32 {
		return nil, fmt.Errorf("%s: JSON nesting is too deep", path)
	}
	token, err := d.Token()
	if err != nil {
		return nil, fmt.Errorf("%s: invalid JSON", path)
	}
	if token == nil {
		return nil, fmt.Errorf("%s: null is not accepted", path)
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delim {
	case '{':
		out := map[string]any{}
		for d.More() {
			t, err := d.Token()
			if err != nil {
				return nil, fmt.Errorf("%s: invalid object", path)
			}
			key, ok := t.(string)
			if !ok {
				return nil, fmt.Errorf("%s: invalid object key", path)
			}
			field := path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
			if _, exists := out[key]; exists {
				return nil, fmt.Errorf("%s: duplicate field", field)
			}
			value, err := jsonValue(d, field, depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = value
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("%s: invalid object", path)
		}
		return out, nil
	case '[':
		out := []any{}
		for d.More() {
			value, err := jsonValue(d, fmt.Sprintf("%s/%d", path, len(out)), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		if end, err := d.Token(); err != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("%s: invalid array", path)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: invalid JSON value", path)
}
