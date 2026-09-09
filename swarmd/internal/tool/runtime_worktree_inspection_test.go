package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type inspectionConfigAuthority struct {
	*worktreeruntime.Service
	source string
	calls  int
}

func (s *inspectionConfigAuthority) GetConfigForPrincipal(principal identity.Principal, path string) (worktreeruntime.Config, error) {
	s.calls++
	if path != s.source {
		return worktreeruntime.Config{}, fmt.Errorf("configuration requested for execution lane instead of saved source")
	}
	return s.Service.GetConfigForPrincipal(principal, path)
}

// Purpose: manageWorktreeInspect must query saved configuration while Git reads
// the selected exact lane/source. The regression is managed paths being treated
// as saved catalog identities. Real allocated Git lanes plus the real workspace
// catalog and session store are the narrowest routing proof; a strict config
// double asserts the service argument rather than allowing an unsaved fallback.
func TestManageWorktreeInspectionAuthority(t *testing.T) {
	f := newRecoveryFixture(t, true)
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "config"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	config := &inspectionConfigAuthority{Service: worktreeruntime.NewService(pebblestore.NewWorktreeStore(store), f.workspace, nil), source: f.primarySource}
	f.runtime.worktrees = config
	f.scope.Roots = []string{f.primary}
	if err := os.WriteFile(filepath.Join(f.primary, "retained.txt"), []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _, _ := f.sessions.GetSession(f.scope.SessionID)
	head := recoveryGit(t, f.primary, "rev-parse", "HEAD")
	dirty := recoveryGit(t, f.primary, "status", "--porcelain")
	for _, requested := range []string{"", f.primary, f.primarySource} {
		output, err := f.runtime.executeManageWorktree(f.scope, map[string]any{"action": "inspect", "workspace_path": requested})
		if err != nil {
			t.Fatalf("inspect %q: %v", requested, err)
		}
		var payload struct {
			CurrentBranch string `json:"current_branch"`
			Workspace     struct {
				Path string `json:"path"`
			} `json:"workspace"`
			Config struct {
				Path string `json:"workspace_path"`
			} `json:"worktree_config"`
		}
		if err := json.Unmarshal([]byte(output), &payload); err != nil {
			t.Fatal(err)
		}
		wantPath, wantBranch := f.primary, before.WorktreeBranch
		if requested == f.primarySource {
			wantPath, wantBranch = f.primarySource, "dev"
		}
		if payload.Workspace.Path != wantPath || payload.CurrentBranch != wantBranch || payload.Config.Path != f.primarySource {
			t.Fatalf("wrong inspection routing: %s", output)
		}
	}
	calls := config.calls
	foreign := recoveryRepo(t)
	alias := filepath.Join(t.TempDir(), "lane-alias")
	if err := os.Symlink(f.primary, alias); err != nil {
		t.Fatal(err)
	}
	for _, requested := range []string{foreign, alias, f.goodPath, filepath.Join(f.primary, "retained.txt")} {
		if _, err := f.runtime.executeManageWorktree(f.scope, map[string]any{"action": "inspect", "workspace_path": requested}); err == nil {
			t.Fatalf("accepted foreign/non-root path %q", requested)
		}
	}
	scope := f.scope
	scope.Principal.AccountScopeID = "foreign-account"
	if _, err := f.runtime.executeManageWorktree(scope, map[string]any{"action": "inspect"}); err == nil {
		t.Fatal("accepted foreign account")
	}
	// A tampered source that is itself saved still cannot cross Git repositories.
	if _, err := f.workspace.AddForPrincipal(f.scope.Principal, foreign, "Foreign repository", "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := recoveryMetadata(f.sessions, f.scope.SessionID, map[string]any{"swarm_v3_source_workspace_path": foreign}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.executeManageWorktree(f.scope, map[string]any{"action": "inspect"}); err == nil {
		t.Fatal("accepted mismatched source repository")
	}
	if _, _, err := recoveryMetadata(f.sessions, f.scope.SessionID, map[string]any{"swarm_v3_source_workspace_path": f.primarySource}); err != nil {
		t.Fatal(err)
	}
	if config.calls != calls {
		t.Fatal("denied request reached configuration authority")
	}
	after, _, _ := f.sessions.GetSession(f.scope.SessionID)
	if !reflect.DeepEqual(before.Metadata, after.Metadata) || before.WorktreeRootPath != after.WorktreeRootPath || before.WorktreeBranch != after.WorktreeBranch {
		t.Fatal("inspection mutated session identity")
	}
	if recoveryGit(t, f.primary, "rev-parse", "HEAD") != head || recoveryGit(t, f.primary, "status", "--porcelain") != dirty || recoveryGit(t, f.primarySource, "rev-parse", "HEAD") != head {
		t.Fatal("inspection mutated retained work or source")
	}
}
