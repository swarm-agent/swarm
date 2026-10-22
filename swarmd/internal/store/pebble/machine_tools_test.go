package pebblestore

import "testing"

func TestMachineClientTools(t *testing.T) {
	tools := MachineClientTools()
	if len(tools) != 4 {
		t.Fatalf("got %d machine tools", len(tools))
	}
	for _, definition := range tools {
		if !IsMachineToolName(definition.Name) || definition.Kind != AgentCustomToolKindClient || ClientToolRequiresConfirmation(definition) {
			t.Fatalf("%s: %+v", definition.Name, definition)
		}
		if definition.TimeoutMS != AgentClientToolMaxTimeoutMS {
			t.Fatalf("%s waits %dms", definition.Name, definition.TimeoutMS)
		}
	}
}
