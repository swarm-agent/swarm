package tool

import (
	"strings"
	"testing"
)

func TestAskUserDefinitionRequiresConcreteChoicesAndHidesCustomControls(t *testing.T) {
	var definition Definition
	for _, candidate := range NewRuntime(1).Definitions() {
		if candidate.Name == "ask-user" {
			definition = candidate
			break
		}
	}
	if definition.Name == "" {
		t.Fatal("ask-user definition not found")
	}
	if !strings.Contains(definition.Description, "at least two concrete choices") || !strings.Contains(definition.Description, "protected option labeled exactly \"Custom response\"") || !strings.Contains(definition.Description, "freely type a different answer") {
		t.Fatalf("ask-user description does not explain the choice/freeform contract: %q", definition.Description)
	}

	properties, _ := definition.Parameters["properties"].(map[string]any)
	options, _ := properties["options"].(map[string]any)
	if options["minItems"] != 2 {
		t.Fatalf("single-question options minItems = %#v, want 2", options["minItems"])
	}
	assertAskUserOptionSchemaHasNoCustomControls(t, options)

	questions, _ := properties["questions"].(map[string]any)
	questionItems, _ := questions["items"].(map[string]any)
	questionProperties, _ := questionItems["properties"].(map[string]any)
	questionOptions, _ := questionProperties["options"].(map[string]any)
	if questionOptions["minItems"] != 2 {
		t.Fatalf("multi-question options minItems = %#v, want 2", questionOptions["minItems"])
	}
	assertAskUserOptionSchemaHasNoCustomControls(t, questionOptions)
}

func assertAskUserOptionSchemaHasNoCustomControls(t *testing.T, schema map[string]any) {
	t.Helper()
	items, _ := schema["items"].(map[string]any)
	variants, _ := items["oneOf"].([]any)
	if len(variants) != 2 {
		t.Fatalf("option variants = %d, want 2", len(variants))
	}
	objectVariant, _ := variants[1].(map[string]any)
	properties, _ := objectVariant["properties"].(map[string]any)
	for _, forbidden := range []string{"allow_custom", "allowCustom"} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("ask-user exposes model-authored custom control %q", forbidden)
		}
	}
}
