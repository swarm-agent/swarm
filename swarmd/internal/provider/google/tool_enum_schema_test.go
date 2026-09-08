package google

import (
	"encoding/json"
	"reflect"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	toolruntime "swarm/packages/swarmd/internal/tool"
)

// Purpose: buildGoogleRequest/sanitizeGoogleToolSchemaMap must encode Google's
// protobuf Schema.enum as strings without changing canonical numeric argument
// types or allowlists. This boundary test prevents a single numeric enum from
// rejecting every request, including nested items/alternatives and typed slices.
func TestGoogleToolEnumWireEncoding(t *testing.T) {
	for _, tc := range []struct {
		name, typ string
		values    any
		want      []string
	}{
		{"integers", "integer", []int{30, 60}, []string{"30", "60"}},
		{"decoded", "integer", []any{float64(1)}, []string{"1"}},
		{"numbers", "number", []float64{0.5, 2}, []string{"0.5", "2"}},
		{"strings", "string", []string{"ready", "30", "a\"b"}, []string{"ready", "30", "a\"b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaf := map[string]any{"type": tc.typ, "enum": tc.values}
			parameters := map[string]any{"type": "object", "properties": map[string]any{
				"enum":         leaf,
				"items":        map[string]any{"type": "array", "items": leaf},
				"choice":       map[string]any{"anyOf": []any{leaf}},
				"typed_choice": map[string]any{"anyOf": []map[string]any{leaf}},
			}}
			before, _ := json.Marshal(parameters)
			got := sanitizeGoogleToolParameters(parameters)
			props := got["properties"].(map[string]any)
			for _, schema := range []map[string]any{
				props["enum"].(map[string]any),
				props["items"].(map[string]any)["items"].(map[string]any),
				props["choice"].(map[string]any)["anyOf"].([]any)[0].(map[string]any),
				props["typed_choice"].(map[string]any)["anyOf"].([]map[string]any)[0],
			} {
				if schema["type"] != tc.typ || !reflect.DeepEqual(schema["enum"], tc.want) {
					t.Fatalf("wire schema=%#v; want type=%s enum=%v", schema, tc.typ, tc.want)
				}
			}
			after, _ := json.Marshal(parameters)
			if string(before) != string(after) {
				t.Fatal("canonical schema mutated")
			}
			if !reflect.DeepEqual(got, sanitizeGoogleToolParameters(got)) {
				t.Fatal("normalization is not idempotent")
			}
		})
	}
}

// Purpose: exercise the actual runtime tool catalog through the shared request
// builder used by streaming and non-streaming Google calls. Inspect wire JSON,
// not just fixtures, so new numeric enums cannot reintroduce TYPE_STRING errors.
// Also prove canonical schemas/allowlists remain byte-identical for other providers.
func TestGoogleToolCatalogEnumsOnWire(t *testing.T) {
	var tools []provideriface.ToolDefinition
	for _, d := range toolruntime.NewRuntime(1).Definitions() {
		tools = append(tools, provideriface.ToolDefinition{Type: d.Type, Name: d.Name, Description: d.Description, Parameters: d.Parameters})
	}
	before, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	request, err := buildGoogleRequest(provideriface.Request{Input: []map[string]any{{"role": "user", "content": "hello"}}, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var wire any
	if err = json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	count := 0
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				if key == "enum" {
					values, ok := child.([]any)
					if !ok {
						t.Fatalf("wire enum is not an array: %#v", child)
					}
					for _, value := range values {
						if _, ok := value.(string); !ok {
							t.Fatalf("non-string wire enum: %#v", value)
						}
					}
					count++
				} else {
					walk(child)
				}
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(wire)
	if count == 0 {
		t.Fatal("no catalog enums exercised")
	}
	found := false
	for _, d := range request.Tools[0].FunctionDeclarations {
		if d.Name != "manage_video" {
			continue
		}
		found = true
		props := d.Parameters["properties"].(map[string]any)
		fps := props["render_fps"].(map[string]any)
		version := props["plan"].(map[string]any)["properties"].(map[string]any)["composition_catalog"].(map[string]any)["properties"].(map[string]any)["schema_version"].(map[string]any)
		if fps["type"] != "integer" || !reflect.DeepEqual(fps["enum"], []string{"30", "60"}) {
			t.Fatalf("fps=%#v", fps)
		}
		if version["type"] != "integer" || !reflect.DeepEqual(version["enum"], []string{"1"}) {
			t.Fatalf("version=%#v", version)
		}
	}
	if !found {
		t.Fatal("manage_video missing from catalog")
	}
	after, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("request construction mutated canonical tool definitions")
	}
}
