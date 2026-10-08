package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

const (
	clientToolSchemaMaxBytes    = 16 << 10
	ClientToolArgumentsMaxBytes = 32 << 10
	ClientToolResultMaxBytes    = 64 << 10
)

// CompileClientToolSchema validates a client tool's input schema and returns
// it resolved for argument validation. The schema must describe a JSON object
// and may only reference itself: nothing is fetched while compiling or
// validating.
func CompileClientToolSchema(schema map[string]any) (*jsonschema.Resolved, error) {
	if len(schema) == 0 {
		return nil, errors.New("client tool input_schema is required")
	}
	if typ, _ := schema["type"].(string); typ != "object" {
		return nil, errors.New(`client tool input_schema must have "type": "object"`)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("client tool input_schema is not JSON: %w", err)
	}
	if len(raw) > clientToolSchemaMaxBytes {
		return nil, fmt.Errorf("client tool input_schema exceeds %d bytes", clientToolSchemaMaxBytes)
	}
	if err := rejectExternalSchemaRefs(schema); err != nil {
		return nil, err
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("client tool input_schema is invalid: %w", err)
	}
	resolved, err := parsed.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("client tool input_schema is invalid: %w", err)
	}
	return resolved, nil
}

// ValidateClientToolArguments checks a model's raw arguments against schema.
func ValidateClientToolArguments(schema map[string]any, arguments string) (map[string]any, error) {
	if len(arguments) > ClientToolArgumentsMaxBytes {
		return nil, fmt.Errorf("arguments exceed %d bytes", ClientToolArgumentsMaxBytes)
	}
	resolved, err := CompileClientToolSchema(schema)
	if err != nil {
		return nil, err
	}
	var instance map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(arguments)), &instance); err != nil || instance == nil {
		return nil, errors.New("arguments must be a JSON object")
	}
	if err := resolved.Validate(instance); err != nil {
		return nil, fmt.Errorf("arguments do not match the tool's schema: %w", err)
	}
	return instance, nil
}

func rejectExternalSchemaRefs(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "$ref", "$dynamicRef":
				ref, _ := child.(string)
				if !strings.HasPrefix(ref, "#") {
					return fmt.Errorf("client tool input_schema may only use local %s references", key)
				}
			case "$id", "$schema", "$anchor", "$dynamicAnchor", "$vocabulary":
				if key == "$id" {
					return errors.New("client tool input_schema may not set $id")
				}
			}
			if err := rejectExternalSchemaRefs(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectExternalSchemaRefs(child); err != nil {
				return err
			}
		}
	}
	return nil
}
