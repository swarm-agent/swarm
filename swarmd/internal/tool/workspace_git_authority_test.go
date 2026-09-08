package tool

import (
	"os"
	"path/filepath"
	"testing"
)

// Purpose: source read grants must not authorize a sibling lane's Git admin
// files. Exercise both permission preflight and actual path resolution so an
// allowed ancestor cannot turn a preflight-only denial into a successful read.
func TestWorkspaceGitAdminReadBoundary(t *testing.T) {
	source := t.TempDir()
	lane := t.TempDir()
	own := filepath.Join(source, ".git", "worktrees", "own")
	sibling := filepath.Join(source, ".git", "worktrees", "sibling")
	for _, dir := range []string{own, sibling} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "gitdir"), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	scope := WorkspaceScope{PrimaryPath: lane, Roots: []string{lane}, ReadOnlyRoots: []string{source, own}, RejectScopeExpansion: true}
	if _, err := resolveWorkspacePath(scope, filepath.Join(own, "gitdir")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorkspacePath(scope, filepath.Join(sibling, "gitdir")); err == nil {
		t.Fatal("sibling administration readable")
	}
	alias := filepath.Join(source, "alias")
	if err := os.Symlink(sibling, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorkspacePath(scope, filepath.Join(alias, "gitdir")); err == nil {
		t.Fatal("symlink bypassed admin boundary")
	}
	if body, err := os.ReadFile(filepath.Join(sibling, "gitdir")); err != nil || string(body) != "fixture" {
		t.Fatal("denial mutated sibling")
	}
}
