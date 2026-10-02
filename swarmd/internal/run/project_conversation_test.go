package run

import (
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: resolveRunExecutionContext and resolveTaskTargetWorkspace must keep
// project identity separate from host filesystem authority. This narrow service
// test rejects overrides and implicit delegation before any allocation occurs.
func TestProjectConversationRunHasNoFilesystemAuthority(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "project.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	if err := store.PutProject("account", &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	s := &Service{sessions: sessionruntime.NewService(store, nil)}
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	session := pebblestore.SessionSnapshot{ID: "conversation", AccountScopeID: p.AccountScopeID, UserID: p.UserID, Metadata: map[string]any{
		"swarm_v3_project_id": "project", "project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator",
	}}
	resolved, err := s.resolveRunExecutionContext(session, RunExecutionContext{}, p)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Scope.PrimaryPath != "" || len(resolved.Scope.Roots) != 0 || !resolved.Scope.RejectScopeExpansion || resolved.WorktreeMode != RunWorktreeModeOff {
		t.Fatalf("project scope widened: %+v", resolved)
	}
	for _, override := range []RunExecutionContext{
		{WorkspacePath: t.TempDir()}, {CWD: "."}, {WorktreeMode: RunWorktreeModeOn}, {WorktreeRootPath: t.TempDir()}, {WorktreeBranch: "branch"},
	} {
		if _, err := s.resolveRunExecutionContext(session, override, p); err == nil {
			t.Fatalf("accepted execution override %+v", override)
		}
	}
	other := p
	other.UserID = "other"
	if _, err := s.resolveRunExecutionContext(session, RunExecutionContext{}, other); err == nil {
		t.Fatal("accepted another principal")
	}
	for _, target := range []string{"", ".", t.TempDir()} {
		launch := &taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: target}
		if _, _, err := s.resolveTaskTargetWorkspace(session, p, launch); err == nil {
			t.Fatalf("accepted unregistered delegation target %q", target)
		}
	}
	catalog := pebblestore.NewWorkspaceStore(db)
	root := t.TempDir()
	entry, err := catalog.AddForAccount(p.AccountScopeID, root, "Repository")
	if err != nil {
		t.Fatal(err)
	}
	s.workspace = workspace.NewService(catalog)
	project, _, err := store.GetProject(p.AccountScopeID, "project")
	if err != nil {
		t.Fatal(err)
	}
	launch := &taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: root}
	if _, _, err := s.resolveTaskTargetWorkspace(session, p, launch); err == nil {
		t.Fatal("catalog registration alone granted project delegation")
	}
	project.Workspaces = []pebblestore.ProjectWorkspaceRef{{Path: root, WorkspaceID: entry.WorkspaceID}}
	if err := store.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	if target, _, err := s.resolveTaskTargetWorkspace(session, p, launch); err != nil || target != root {
		t.Fatalf("explicit authorized target = %q %v", target, err)
	}
}
