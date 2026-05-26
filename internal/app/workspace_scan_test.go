package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsWorkspaceLikeDoesNotUseClaudeFile(t *testing.T) {
	root := t.TempDir()
	deepClaudeOnly := filepath.Join(root, "one", "two", "claude-only")
	deepAgents := filepath.Join(root, "one", "two", "agents-root")

	if err := os.MkdirAll(deepClaudeOnly, 0o755); err != nil {
		t.Fatalf("mkdir claude-only: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deepClaudeOnly, "CLAUDE.md"), []byte("claude"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}
	if isWorkspaceLike(deepClaudeOnly, 3) {
		t.Fatalf("CLAUDE.md-only directory should not be workspace-like")
	}

	if err := os.MkdirAll(deepAgents, 0o755); err != nil {
		t.Fatalf("mkdir agents-root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deepAgents, "AGENTS.md"), []byte("agents"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	if !isWorkspaceLike(deepAgents, 3) {
		t.Fatalf("AGENTS.md directory should be workspace-like")
	}
}
