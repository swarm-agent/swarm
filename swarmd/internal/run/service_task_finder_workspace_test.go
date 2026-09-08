package run

import (
	"os"
	"path/filepath"
	"testing"

	worktree "swarm/packages/swarmd/internal/worktree"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: prepareDelegatedSubagentLaunchWithProfile must persist an authorized
// Finder's selected read root without stale parent worktree identity overriding
// it in resolveRunWorkspaceScope. This real session-store boundary is narrower
// than a provider test and proves persisted identity, actual source visibility,
// and rejection of a path outside the resolved roots. Same-root inheritance is
// retained; cross-root launches must not inherit unrelated temporary grants.
func TestTaskFinderSelectedWorkspaceDoesNotInheritParentRoot(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, cross := range []bool{false, true} {
		name := "same-root"
		if cross {
			name = "cross-root"
		}
		t.Run(name, func(t *testing.T) {
			svc, parentID, cleanup := newTaskLaunchPermissionTestService(t)
			defer cleanup()
			parent, ok, err := svc.sessions.GetSession(parentID)
			if err != nil || !ok {
				t.Fatalf("parent: %v", err)
			}
			sourceRepo := programFixtureRepo(t)
			wt := &worktree.Service{}
			base, err := wt.ResolveTaskBase(sourceRepo)
			if err != nil {
				t.Fatal(err)
			}
			lane, err := wt.AllocateTaskWorkspace(sourceRepo, base, "finder-parent-"+name, nil)
			if err != nil {
				t.Fatal(err)
			}
			parent.WorkspacePath = sourceRepo
			parent.WorktreeEnabled = true
			parent.WorktreeRootPath = lane.WorkspacePath
			parent.WorktreeBranch = lane.BranchName
			parent.WorktreeBaseBranch = base.ParentBranch
			parent.Metadata = map[string]any{}
			parent.Metadata["swarm_v3_source_workspace_path"] = sourceRepo
			parent.Metadata["swarm_v3_runtime_workspace_path"] = lane.WorkspacePath
			parent.Metadata["swarm_v3_worktree_base_commit"] = base.BaseCommit
			parent.TemporaryWorkspaceRoots = []string{t.TempDir()}
			target := lane.WorkspacePath
			if cross {
				target = programFixtureRepo(t)
			}
			if err := os.WriteFile(filepath.Join(target, "catalog.md"), []byte("selected research\n"), 0600); err != nil {
				t.Fatal(err)
			}
			profile, virtual, source, err := svc.resolveTaskLaunchProfile(parent, "finder")
			if err != nil {
				t.Fatal(err)
			}
			selector := target
			if !cross {
				selector = ""
			} // omitted target must use runtime, not captured source
			launch, err := svc.prepareDelegatedSubagentLaunchWithProfile(parent, "auto", taskLaunchPrepared{RequestedSubagent: "finder", MetaPrompt: "Read catalog.md", TargetWorkspacePath: selector, VirtualTarget: virtual, LogicalTaskID: "finder-root-" + name}, "research", "", &profile, source, nil)
			if err != nil {
				t.Fatal(err)
			}
			child, ok, err := svc.sessions.GetSession(launch.ChildSession.ID)
			if err != nil || !ok {
				t.Fatalf("persisted child: %v", err)
			}
			if child.WorkspacePath != target {
				t.Fatalf("workspace=%q want %q", child.WorkspacePath, target)
			}
			if cross {
				if child.WorktreeEnabled || child.WorktreeRootPath != "" || child.WorktreeBranch != "" || child.WorktreeBaseBranch != "" || len(child.TemporaryWorkspaceRoots) != 0 {
					t.Fatalf("cross-root child inherited parent identity: %#v", child)
				}
			} else if !child.WorktreeEnabled || child.WorktreeRootPath != target || child.WorktreeBranch != parent.WorktreeBranch {
				t.Fatal("same-root inheritance lost")
			}
			principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, SessionID: child.ID, AccountScopeSource: identity.AccountScopeSourceSession}
			scope, err := svc.resolveRunWorkspaceScope(child, principal)
			if err != nil {
				t.Fatal(err)
			}
			if scope.PrimaryPath != target || launch.ChildWorkspacePath != target {
				t.Fatalf("worker/tool target mismatch: %+v", scope)
			}
			if !cross && scope.WorktreeBaseCommit != base.BaseCommit {
				t.Fatal("shared worker lost captured base")
			}
			if _, expand, err := tool.ScopeExpansionForCall(scope, tool.Call{Name: "read", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(target, "catalog.md")})}); err != nil || expand {
				t.Fatalf("selected catalog inaccessible: expand=%v err=%v", expand, err)
			}
			foreign := filepath.Join(t.TempDir(), "outside.md")
			if _, expand, err := tool.ScopeExpansionForCall(scope, tool.Call{Name: "read", Arguments: mustJSON(t, map[string]any{"path": foreign})}); err != nil || !expand {
				t.Fatalf("foreign read did not require expansion: expand=%v err=%v", expand, err)
			}
			content, err := os.ReadFile(filepath.Join(child.WorkspacePath, "catalog.md"))
			if err != nil || string(content) != "selected research\n" {
				t.Fatalf("wrong actual source: %q %v", content, err)
			}
		})
	}
}
