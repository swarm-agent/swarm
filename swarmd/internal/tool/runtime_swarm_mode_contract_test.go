package tool

import (
	"testing"

	"swarm/packages/swarmd/internal/swarmmode"
)

func TestSwarmModeDefinitionSchema(t *testing.T) {
	definitions := NewRuntime(1).Definitions()
	var found *Definition
	for index := range definitions {
		if definitions[index].Name == "swarm_mode" {
			found = &definitions[index]
			break
		}
	}
	if found == nil {
		t.Fatal("swarm_mode definition not exposed")
	}
	properties, ok := found.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("swarm_mode properties = %#v", found.Parameters["properties"])
	}
	for _, name := range []string{"prompt", "agent_type", "count", "themes", "output_contract", "owned_scope_template"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("swarm_mode schema missing %q", name)
		}
	}
	count := properties["count"].(map[string]any)
	if count["maximum"] != swarmmode.HardMaxAgents {
		t.Fatalf("swarm_mode count maximum = %#v", count["maximum"])
	}
	themes := properties["themes"].(map[string]any)
	if themes["maxItems"] != swarmmode.HardMaxAgents {
		t.Fatalf("swarm_mode themes maximum = %#v", themes["maxItems"])
	}
	ownedScopeTemplate := properties["owned_scope_template"].(map[string]any)
	if ownedScopeTemplate["maxLength"] != swarmmode.MaxOwnedScopeTemplateRunes {
		t.Fatalf("swarm_mode owned scope maximum = %#v", ownedScopeTemplate["maxLength"])
	}
	if found.Parameters["additionalProperties"] != false {
		t.Fatalf("swarm_mode allows additional properties: %#v", found.Parameters)
	}
}
