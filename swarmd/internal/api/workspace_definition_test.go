package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestWorkspaceDefinitionPromptIsBoundedAndIncludesRootAgents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("workspace purpose: test routing"), 0o600); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Test workspace\nA routing fixture."), 0o600); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	for _, path := range []string{"cmd/app/main.go", "internal/service/service.go"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(path), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	prompt, err := buildWorkspaceDefinitionPrompt(pebblestore.WorkspaceEntry{Path: root, Name: "test workspace"})
	if err != nil {
		t.Fatalf("build prompt: %v", err)
	}
	if got := workspaceDefinitionInputTokenUpperBound(prompt); got > workspaceDefinitionMaxInputTokens {
		t.Fatalf("prompt token upper bound=%d, max=%d", got, workspaceDefinitionMaxInputTokens)
	}
	for _, want := range []string{"workspace purpose: test routing", "# Test workspace", "cmd/", "cmd/app/", "Root README.md", "untrusted data"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
}

func TestWorkspaceDefinitionPromptHardCapDoesNotPadSmallRepositories(t *testing.T) {
	root := t.TempDir()
	large := strings.Repeat("purpose and architecture ", workspaceDefinitionMaxInputTokens)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(large), 0o600); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(large), 0o600); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	prompt, err := buildWorkspaceDefinitionPrompt(pebblestore.WorkspaceEntry{Path: root, Name: "large workspace"})
	if err != nil {
		t.Fatalf("build large prompt: %v", err)
	}
	if got := workspaceDefinitionInputTokenUpperBound(prompt); got > workspaceDefinitionMaxInputTokens {
		t.Fatalf("prompt token upper bound=%d, max=%d", got, workspaceDefinitionMaxInputTokens)
	}
	if !strings.Contains(prompt, "hard token cap") {
		t.Fatalf("large prompt did not report truncation")
	}

	small := truncateWorkspaceDefinitionInput("small", workspaceDefinitionMaxInputTokens)
	if small != "small" {
		t.Fatalf("small context was padded or changed: %q", small)
	}
}

func TestWorkspaceDefinitionRouterProfileUsesMinimalReadTools(t *testing.T) {
	profile := workspaceDefinitionRouterProfile("codex", "router-model", "high", "fast")
	if profile.RuntimeMode != pebblestore.AgentRuntimeModeRead || profile.ExecutionSetting != pebblestore.AgentExecutionSettingRead {
		t.Fatalf("unexpected runtime profile: %+v", profile)
	}
	if profile.ToolContract == nil || len(profile.ToolContract.Tools) != 3 {
		t.Fatalf("Router workspace definition tools are not least privilege: %+v", profile.ToolContract)
	}
	for _, name := range []string{"read", "search", "list"} {
		config, ok := profile.ToolContract.Tools[name]
		if !ok || config.Enabled == nil || !*config.Enabled {
			t.Fatalf("Router workspace definition tool %q is unavailable: %+v", name, profile.ToolContract)
		}
	}
	if profile.Name != agentruntime.WorkspaceDefinitionAgentID {
		t.Fatalf("Router workspace definition identity = %q", profile.Name)
	}
	if profile.Provider != "codex" || profile.Model != "router-model" || profile.Thinking != "high" || profile.AutoServiceTier != "fast" {
		t.Fatalf("Router settings were not preserved: %+v", profile)
	}
}

func TestWorkspaceDefinitionSessionMetadataStoresCanonicalSystemProfile(t *testing.T) {
	profile := workspaceDefinitionRouterProfile("codex", "router-model", "high", "fast")
	metadata := workspaceDefinitionSessionMetadata(profile, pebblestore.WorkspaceEntry{WorkspaceID: "workspace-1", DefinitionGeneration: 2})
	stored, ok := metadata["agent_profile"].(pebblestore.AgentProfile)
	if !ok {
		t.Fatalf("agent_profile type = %T", metadata["agent_profile"])
	}
	if stored.Name != agentruntime.WorkspaceDefinitionAgentID || metadata["resolved_agent_name"] != agentruntime.WorkspaceDefinitionAgentID {
		t.Fatalf("workspace definition metadata identity mismatch: %+v", metadata)
	}
	if stored.ToolContract == nil || stored.ToolContract.Preset != "custom" || len(stored.ToolContract.Tools) != 3 {
		t.Fatalf("stored workspace definition profile is not least privilege: %+v", stored.ToolContract)
	}
	if metadata["workspace_definition_generation"] != int64(2) || metadata["workspace_id"] != "workspace-1" {
		t.Fatalf("workspace definition binding metadata mismatch: %+v", metadata)
	}
}
