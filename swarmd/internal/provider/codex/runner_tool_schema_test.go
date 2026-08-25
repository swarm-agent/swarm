package codex

import (
	"reflect"
	"testing"
)

func TestSanitizeCodexToolParametersPreservesTypedStringEnums(t *testing.T) {
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"capabilities", "inspect_context", "inspect_frames"},
			},
		},
	}

	cleaned := sanitizeCodexToolParameters(parameters)
	properties, _ := cleaned["properties"].(map[string]any)
	action, _ := properties["action"].(map[string]any)
	if got := action["enum"]; !reflect.DeepEqual(got, []any{"capabilities", "inspect_context", "inspect_frames"}) {
		t.Fatalf("action enum = %#v, want preserved string literals", got)
	}
}
