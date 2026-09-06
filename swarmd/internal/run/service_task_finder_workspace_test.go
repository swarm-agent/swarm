package run

import (
	"os"
	"path/filepath"
	"testing"

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
			parent.WorktreeEnabled = true
			parent.WorktreeRootPath = parent.WorkspacePath
			parent.WorktreeBranch = "agent/parent"
			parent.WorktreeBaseBranch = "dev"
			parent.TemporaryWorkspaceRoots = []string{t.TempDir()}
			target := parent.WorkspacePath
			if cross {
				target = t.TempDir()
			}
			if err := os.WriteFile(filepath.Join(target, "catalog.md"), []byte("selected research\n"), 0600); err != nil {
				t.Fatal(err)
			}
			profile, virtual, source, err := svc.resolveTaskLaunchProfile(parent, "finder")
			if err != nil {
				t.Fatal(err)
			}
			launch, err := svc.prepareDelegatedSubagentLaunchWithProfile(parent, "auto", taskLaunchPrepared{RequestedSubagent: "finder", MetaPrompt: "Read catalog.md", TargetWorkspacePath: target, VirtualTarget: virtual, LogicalTaskID: "finder-root-" + name}, "research", "", &profile, source, nil)
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
