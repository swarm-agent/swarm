package tool

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: manageWorktreeInspectionPaths must select an exact authorized added
// repository independently of the default, while rejecting aliases, revoked
// sources and foreign principals before querying config. Real catalog/session
// state and Git lanes are the narrowest proof of routing and unchanged identity.
func TestManageWorktreeInspectionSecondaryTargets(t *testing.T) {
	f := newRecoveryFixture(t)
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "config"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	config := &inspectionConfigAuthority{Service: worktreeruntime.NewService(pebblestore.NewWorktreeStore(store), f.workspace, nil), source: f.source}
	f.runtime.worktrees = config
	f.scope.Roots = append(f.scope.Roots, f.source)
	before, _, _ := f.sessions.GetSession(f.scope.SessionID)
	for _, path := range []string{f.source, f.lane} {
		if _, err := f.runtime.executeManageWorktree(f.scope, map[string]any{"action": "inspect", "workspace_path": path}); err != nil {
			t.Fatalf("inspect %s: %v", path, err)
		}
		got, source, err := f.runtime.manageWorktreeInspectionPaths(f.scope, path)
		if err != nil || got != path || source != f.source {
			t.Fatalf("routing: %s %s %v", got, source, err)
		}
	}
	unsaved := recoveryRepo(t)
	f.scope.Roots = append(f.scope.Roots, unsaved)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(f.source, alias); err != nil {
		t.Fatal(err)
	}
	calls := config.calls
	for _, path := range []string{unsaved, alias, f.goodPath, filepath.Join(f.source, "nested")} {
		if _, err := f.runtime.executeManageWorktree(f.scope, map[string]any{"action": "inspect", "workspace_path": path}); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	foreign := f.scope
	foreign.Principal.UserID = "foreign"
	if _, _, err := f.runtime.manageWorktreeInspectionPaths(foreign, f.source); err == nil {
		t.Fatal("accepted foreign principal")
	}
	if _, err := f.workspace.DeleteForPrincipal(f.scope.Principal, f.source); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.source, f.lane} {
		if _, err := f.runtime.executeManageWorktree(f.scope, map[string]any{"action": "inspect", "workspace_path": path}); err == nil {
			t.Fatal("accepted revoked source")
		}
	}
	after, _, _ := f.sessions.GetSession(f.scope.SessionID)
	if config.calls != calls || !reflect.DeepEqual(before, after) || recoveryGit(t, f.lane, "rev-parse", "HEAD") != f.base || recoveryGit(t, f.source, "rev-parse", "HEAD") != f.base {
		t.Fatal("inspection changed state or denial reached config")
	}
}

// Purpose: manageWorktreeIntegrate must honor an explicit source/lane assertion,
// reject mismatched or symlink selectors and live competing ownership before
// any mutation, and never advance the default or captured source. Real durable
// child lineage plus Git proves the actual destination, not a status-only mock.
func TestManageWorktreeIntegrationExplicitTarget(t *testing.T) {
	for _, selector := range []string{"source", "lane", "wrong", "alias", "active"} {
		t.Run(selector, func(t *testing.T) {
			f := newRecoveryFixture(t)
			before, _, _ := f.sessions.GetSession(f.scope.SessionID)
			primaryHead := recoveryGit(t, f.primary, "rev-parse", "HEAD")
			path := f.source
			switch selector {
			case "lane":
				path = f.lane
			case "wrong":
				path = f.primarySource
			case "alias":
				path = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(f.source, path); err != nil {
					t.Fatal(err)
				}
			case "active":
				record, _, err := f.sessions.GetTaskProgram(f.scope.SessionID, "recovery-program")
				if err != nil {
					t.Fatal(err)
				}
				running := pebblestore.TaskProgramStateRunning
				if _, _, err := f.sessions.TransitionTaskProgram(f.scope.SessionID, record.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: record.Revision, MutationID: "active", State: &running}); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.runtime.manageWorktreeIntegrate(f.scope, map[string]any{"session_ids": []string{"recovery-good"}, "workspace_path": path})
			rejected := selector == "wrong" || selector == "alias" || selector == "active"
			if rejected {
				if err == nil {
					t.Fatal("unsafe selector accepted")
				}
				after, _, _ := f.sessions.GetSession(f.scope.SessionID)
				if !reflect.DeepEqual(before, after) || recoveryGit(t, f.lane, "rev-parse", "HEAD") != f.base {
					t.Fatal("denial mutated destination or parent")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				content, err := os.ReadFile(filepath.Join(f.lane, "change.txt"))
				if err != nil || string(content) != "recovery-good" {
					t.Fatalf("wrong destination: %q %v", content, err)
				}
			}
			if recoveryGit(t, f.primary, "rev-parse", "HEAD") != primaryHead || recoveryGit(t, f.source, "rev-parse", "HEAD") != f.base {
				t.Fatal("default or source advanced")
			}
		})
	}
}
