package pebblestore

import "testing"

// The four machine tools are gated by the session's machine access; the
// workspace setup request travels with them but is not.
func TestMachineClientTools(t *testing.T) {
	tools := MachineClientTools()
	if len(tools) != 7 {
		t.Fatalf("got %d client tools", len(tools))
	}
	machine, browser := 0, 0
	for _, definition := range tools {
		if definition.Kind != AgentCustomToolKindClient || ClientToolRequiresConfirmation(definition) {
			t.Fatalf("%s: %+v", definition.Name, definition)
		}
		if definition.TimeoutMS != AgentClientToolMaxTimeoutMS {
			t.Fatalf("%s waits %dms", definition.Name, definition.TimeoutMS)
		}
		if IsMachineToolName(definition.Name) {
			machine++
		} else if IsBrowserToolName(definition.Name) {
			browser++
		} else if definition.Name != WorkspaceToolSetup {
			t.Fatalf("%s is neither a machine tool, a browser tool nor the workspace setup request", definition.Name)
		}
	}
	if machine != 4 || browser != 2 {
		t.Fatalf("%d machine tools and %d browser tools gated by access", machine, browser)
	}
}
