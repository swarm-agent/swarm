package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestWorkspaceDefinitionPromptIsBoundedAndIncludesRootAgents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("workspace purpose: test routing"), 0o600); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	for _, path := range []string{"cmd/app/main.go", "internal/service/service.go", "README.md"} {
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
	if len(prompt) > workspaceDefinitionMaxPromptBytes {
		t.Fatalf("prompt bytes=%d, max=%d", len(prompt), workspaceDefinitionMaxPromptBytes)
	}
	for _, want := range []string{"workspace purpose: test routing", "cmd/", "cmd/app/", "README.md", "untrusted data"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
}

func TestWorkspaceDefinitionRouterProfileIsToolFreeReadMode(t *testing.T) {
	profile := workspaceDefinitionRouterProfile("codex", "router-model", "high", "fast")
	if profile.RuntimeMode != pebblestore.AgentRuntimeModeRead || profile.ExecutionSetting != pebblestore.AgentExecutionSettingRead {
		t.Fatalf("unexpected runtime profile: %+v", profile)
	}
	if profile.ToolContract == nil || len(profile.ToolContract.Tools) != 0 {
		t.Fatalf("Router workspace definition tools are not empty: %+v", profile.ToolContract)
	}
	if profile.Provider != "codex" || profile.Model != "router-model" || profile.Thinking != "high" || profile.AutoServiceTier != "fast" {
		t.Fatalf("Router settings were not preserved: %+v", profile)
	}
}
