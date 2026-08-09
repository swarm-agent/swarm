package tool

import "testing"

func TestCompactToolRequiresBoundedHandoff(t *testing.T) {
	definitions := NewRuntime(1).Definitions()
	for _, definition := range definitions {
		if definition.Name != "compact" {
			continue
		}
		properties, _ := definition.Parameters["properties"].(map[string]any)
		handoff, _ := properties["handoff"].(map[string]any)
		if handoff["type"] != "string" || handoff["minLength"] != 1 {
			t.Fatalf("compact handoff schema = %#v", handoff)
		}
		required, _ := definition.Parameters["required"].([]string)
		if len(required) != 1 || required[0] != "handoff" {
			t.Fatalf("compact required fields = %#v", definition.Parameters["required"])
		}
		if definition.Parameters["additionalProperties"] != false {
			t.Fatal("compact permits undeclared arguments")
		}
		return
	}
	t.Fatal("compact definition missing")
}
