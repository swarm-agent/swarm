package agent

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCompiledRouterIsDistinctAndToolFree(t *testing.T) {
	if RouterAgentID == WorkspaceDefinitionAgentID || RouterAgentName == WorkspaceDefinitionAgentName {
		t.Fatalf("Router identity overlaps Workspace Definition: router=(%q, %q) workspace=(%q, %q)", RouterAgentID, RouterAgentName, WorkspaceDefinitionAgentID, WorkspaceDefinitionAgentName)
	}
	if RouterAgentPrompt() == WorkspaceDefinitionAgentPrompt() {
		t.Fatal("Router and Workspace Definition unexpectedly share a prompt")
	}

	registry, err := BuiltinSystemAgentRegistry()
	if err != nil {
		t.Fatalf("build system agent registry: %v", err)
	}
	parent := pebblestore.AgentProfile{Provider: "codex", Model: "router-model", Thinking: "high"}
	router, err := registry.Materialize(RouterAgentID, parent)
	if err != nil {
		t.Fatalf("materialize Router: %v", err)
	}
	workspaceDefinition, err := registry.Materialize(WorkspaceDefinitionAgentID, parent)
	if err != nil {
		t.Fatalf("materialize Workspace Definition: %v", err)
	}
	if router.Name == workspaceDefinition.Name || router.Prompt == workspaceDefinition.Prompt {
		t.Fatalf("compiled profiles are not distinct: router=%+v workspace=%+v", router, workspaceDefinition)
	}
	if router.ToolContract == nil || router.ToolContract.Preset != "custom" {
		t.Fatalf("Router lacks immutable custom tool contract: %+v", router.ToolContract)
	}
	if len(router.ToolContract.Tools) != 0 {
		t.Fatalf("Router has enabled/configured tools, want exactly zero: %+v", router.ToolContract.Tools)
	}
}
