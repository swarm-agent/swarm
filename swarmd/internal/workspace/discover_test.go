package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectWorkspaceSignalsDoesNotUseClaudeFiles(t *testing.T) {
	root := t.TempDir()
	claudeOnly := filepath.Join(root, "claude-only")
	agentsRoot := filepath.Join(root, "agents-root")

	if err := os.MkdirAll(filepath.Join(claudeOnly, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeOnly, "CLAUDE.md"), []byte("claude"), 0o644); err != nil {
		t.Fatalf("write claude file: %v", err)
	}
	if err := os.MkdirAll(agentsRoot, 0o755); err != nil {
		t.Fatalf("mkdir agents root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsRoot, "AGENTS.md"), []byte("agents"), 0o644); err != nil {
		t.Fatalf("write agents file: %v", err)
	}

	isGitRepo, hasSwarm := detectWorkspaceSignals(claudeOnly)
	if isGitRepo || hasSwarm {
		t.Fatalf("claude-only directory produced workspace signals: git=%t swarm=%t", isGitRepo, hasSwarm)
	}
	if entry := buildWorkspaceDiscoverEntry(claudeOnly, "claude-only", mustStatDir(t, claudeOnly)); entry != nil {
		t.Fatalf("claude-only directory produced discover entry: %#v", entry)
	}

	_, hasSwarm = detectWorkspaceSignals(agentsRoot)
	if !hasSwarm {
		t.Fatalf("AGENTS.md root did not produce workspace signal")
	}
}

func mustStatDir(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}
