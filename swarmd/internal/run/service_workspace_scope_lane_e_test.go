package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/discovery"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// TestLaneE_E2E027RejectsStaleWorkspaceIDWithoutGrantMutation covers
// E2E-027/REQ-GEN-002: path equality must not allow a stale captured identity.
func TestLaneE_E2E027RejectsStaleWorkspaceIDWithoutGrantMutation(t *testing.T) {
	primary := t.TempDir()
	principal := testRunPrincipal()
	workspaceSvc, _, _, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	created, err := workspaceSvc.AddForPrincipal(principal, primary, "primary", "", true)
	if err != nil {
		t.Fatalf("save primary workspace: %v", err)
	}

	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	session := pebblestore.SessionSnapshot{
		ID: "lane-e-stale-id", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: primary,
		Metadata: map[string]any{
			"swarm_v3_source_workspace_id":         created.WorkspaceID + "-stale",
			"swarm_v3_source_workspace_generation": created.WorkspaceGeneration,
		},
	}

	_, err = runSvc.resolveRunWorkspaceScope(session, principal)
	if err == nil || !strings.Contains(err.Error(), "workspace identity is stale") || !strings.Contains(err.Error(), created.WorkspaceID) {
		t.Fatalf("stale workspace identity error = %v", err)
	}
	if len(session.TemporaryWorkspaceRoots) != 0 || len(session.WorkspaceGrants) != 0 {
		t.Fatalf("stale identity mutated session scope: roots=%v grants=%+v", session.TemporaryWorkspaceRoots, session.WorkspaceGrants)
	}
}

// TestLaneE_E2E026RejectsSavedRootSymlinkReplacementWithoutScopeExpansion
// covers E2E-026/REQ-PATH-001: saved canonical identity is revalidated after a
// directory is replaced by a symlink, before any temporary authority is added.
func TestLaneE_E2E026RejectsSavedRootSymlinkReplacementWithoutScopeExpansion(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(primary, 0o755); err != nil {
		t.Fatalf("create primary: %v", err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatalf("create outside: %v", err)
	}
	principal := testRunPrincipal()
	workspaceSvc, _, _, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	if _, err := workspaceSvc.AddForPrincipal(principal, primary, "primary", "", true); err != nil {
		t.Fatalf("save primary workspace: %v", err)
	}
	moved := primary + "-moved"
	if err := os.Rename(primary, moved); err != nil {
		t.Fatalf("move primary: %v", err)
	}
	if err := os.Symlink(outside, primary); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	session := pebblestore.SessionSnapshot{ID: "lane-e-symlink-race", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: primary}
	_, err := runSvc.resolveRunWorkspaceScope(session, principal)
	if err == nil || !strings.Contains(err.Error(), "no longer resolves to its saved identity") {
		t.Fatalf("symlink replacement error = %v", err)
	}
	if len(session.TemporaryWorkspaceRoots) != 0 || len(session.WorkspaceGrants) != 0 {
		t.Fatalf("symlink replacement mutated session scope: roots=%v grants=%+v", session.TemporaryWorkspaceRoots, session.WorkspaceGrants)
	}
}

// TestLaneE_E2E024FilesystemRootCannotBecomeTemporaryScope covers
// E2E-024/REQ-PATH-002 at the production scope-expansion boundary.
func TestLaneE_E2E024FilesystemRootCannotBecomeTemporaryScope(t *testing.T) {
	primary := t.TempDir()
	_, needsApproval, err := tool.ScopeExpansionForCall(tool.WorkspaceScope{PrimaryPath: primary, Roots: []string{primary}}, tool.Call{
		CallID: "lane-e-root", Name: "read", Arguments: `{"path":"/"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("filesystem-root expansion error = %v", err)
	}
	if needsApproval {
		t.Fatal("filesystem root was offered for approval")
	}
}

// TestLaneE_E2E021ChecksLaterBatchedPathBeforeApproval covers
// E2E-021/REQ-EXT-003: an in-scope first path cannot hide a later external path.
func TestLaneE_E2E021ChecksLaterBatchedPathBeforeApproval(t *testing.T) {
	primary := t.TempDir()
	external := filepath.Join(t.TempDir(), "external.txt")
	request, needsApproval, err := tool.ScopeExpansionForCall(tool.WorkspaceScope{PrimaryPath: primary, Roots: []string{primary}}, tool.Call{
		CallID: "lane-e-batch", Name: "read", Arguments: `{"paths":["` + filepath.Join(primary, "inside.txt") + `","` + external + `"]}`,
	})
	if err != nil {
		t.Fatalf("inspect batched paths: %v", err)
	}
	if !needsApproval || request.ArgumentName != "paths" || request.RequestedPath != external {
		t.Fatalf("batched external request = %+v needsApproval=%t", request, needsApproval)
	}
}

// TestLaneE_E2E022ChecksEveryNestedTaskWorkspaceBeforeLaunch covers
// E2E-022/REQ-EXT-003: later nested launches cannot inherit the first launch's scope.
func TestLaneE_E2E022ChecksEveryNestedTaskWorkspaceBeforeLaunch(t *testing.T) {
	primary := t.TempDir()
	external := t.TempDir()
	request, needsApproval, err := tool.ScopeExpansionForCall(tool.WorkspaceScope{PrimaryPath: primary, Roots: []string{primary}}, tool.Call{
		CallID: "lane-e-task", Name: "task", Arguments: `{"launches":[{"workspace_path":"` + primary + `"},{"workspace_path":"` + external + `"}]}`,
	})
	if err != nil {
		t.Fatalf("inspect task workspace paths: %v", err)
	}
	if !needsApproval || request.ArgumentName != "workspace_path" || request.RequestedPath != external {
		t.Fatalf("nested task external request = %+v needsApproval=%t", request, needsApproval)
	}
}
