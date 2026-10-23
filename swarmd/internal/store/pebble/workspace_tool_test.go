package pebblestore

import "testing"

// Purpose: the workspace setup request is a client tool the person's app
// answers, delivered like the machine tools, but gated by no machine access:
// an Orchestrator in a project chat can ask for a folder to be readied even
// when the person gave it no access to their own PC.
// Threat: the Orchestrator stranded at "the folder is not a Git repository",
// handing the person commands, with no way to ask the app.
func TestWorkspaceSetupRequestIsAClientToolWithoutMachineGate(t *testing.T) {
	definition, ok := MachineClientTool(WorkspaceToolSetup)
	if !ok || definition.Kind != AgentCustomToolKindClient || definition.Effect != AgentCustomToolEffectWrite {
		t.Fatalf("definition missing or wrong: %v %+v", ok, definition)
	}
	if IsMachineToolName(WorkspaceToolSetup) {
		t.Fatal("the workspace setup request must not be gated by machine access")
	}
	required, _ := definition.InputSchema["required"].([]string)
	if len(required) != 2 || required[0] != "workspace_path" || required[1] != "reason" {
		t.Fatalf("required inputs: %v", required)
	}
}
