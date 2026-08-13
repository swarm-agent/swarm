package tool_test

import (
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/tool"
)

func TestDesignerOutputContractsUseExistingToolsWithoutCreateFile(t *testing.T) {
	definitions := tool.NewRuntime(1).Definitions()
	definitionNames := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		definitionNames[definition.Name] = struct{}{}
	}
	if _, exists := definitionNames["create_file"]; exists {
		t.Fatal("tool runtime unexpectedly registers create_file")
	}

	managed := agentruntime.DesignerAgentToolContract()
	workspace := agentruntime.DesignerWorkspaceAgentToolContract()
	for _, name := range []string{"read", "search", "find", "list", "write", "edit", "manage_artifact"} {
		if _, exists := definitionNames[name]; !exists {
			t.Fatalf("Designer references missing existing tool %q", name)
		}
	}
	for _, name := range []string{"read", "search", "find", "list", "manage_artifact"} {
		if config := managed.Tools[name]; config.Enabled == nil || !*config.Enabled {
			t.Fatalf("managed Designer existing tool %q is not enabled", name)
		}
	}
	for _, name := range []string{"write", "edit"} {
		if config := managed.Tools[name]; config.Enabled == nil || *config.Enabled {
			t.Fatalf("managed Designer mutating tool %q is not disabled", name)
		}
		if config := workspace.Tools[name]; config.Enabled == nil || !*config.Enabled {
			t.Fatalf("workspace Designer existing tool %q is not enabled", name)
		}
	}
	for _, name := range []string{"media_inspect", "bash", "git_status", "git_diff", "git_add", "git_commit", "manage_artifact"} {
		if config := workspace.Tools[name]; config.Enabled == nil || *config.Enabled {
			t.Fatalf("workspace Designer unexpectedly enables %q", name)
		}
	}
	if _, exists := managed.Tools["create_file"]; exists {
		t.Fatal("Designer tool contract unexpectedly references create_file")
	}
}
