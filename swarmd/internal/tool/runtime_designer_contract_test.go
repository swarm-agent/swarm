package tool_test

import (
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/tool"
)

func TestDesignerUsesExistingFilesystemToolsWithoutCreateFile(t *testing.T) {
	definitions := tool.NewRuntime(1).Definitions()
	definitionNames := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		definitionNames[definition.Name] = struct{}{}
	}
	if _, exists := definitionNames["create_file"]; exists {
		t.Fatal("tool runtime unexpectedly registers create_file")
	}

	contract := agentruntime.DesignerAgentToolContract()
	for _, name := range []string{"read", "search", "find", "list", "write", "edit"} {
		if _, exists := definitionNames[name]; !exists {
			t.Fatalf("Designer references missing existing tool %q", name)
		}
		config, exists := contract.Tools[name]
		if !exists || config.Enabled == nil || !*config.Enabled {
			t.Fatalf("Designer existing tool %q is not enabled", name)
		}
	}
	if _, exists := contract.Tools["create_file"]; exists {
		t.Fatal("Designer tool contract unexpectedly references create_file")
	}
}
