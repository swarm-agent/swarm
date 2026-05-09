package api

import (
	"path/filepath"
	"testing"
)

func TestPeerWorkspaceImportRootUsesHomeWorkspaces(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))

	root, err := peerWorkspaceImportRoot()
	if err != nil {
		t.Fatalf("peerWorkspaceImportRoot() error = %v", err)
	}
	want := filepath.Join(homeDir, "workspaces")
	if root != want {
		t.Fatalf("peerWorkspaceImportRoot() = %q, want %q", root, want)
	}
}
