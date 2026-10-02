package run

import (
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
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

// Purpose: a workspace-free project parent must still allocate a real isolated
// Coder lane after explicit catalog/project authorization. resolveTaskTargetWorkspace,
// prepareDelegatedSubagentLaunchWithProfile and resolveRunWorkspaceScope own these
// boundaries. Temporary Git/Pebble prove persisted child isolation, source-write
// rejection and unchanged parent authority without a provider or running daemon.
func TestProjectConversationDelegatedCoderRetainsIsolatedWorktree(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	store := svc.sessions.Store()
	if err := store.PutProject("test-account", &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	parent := pebblestore.SessionSnapshot{ID: "project-parent", AccountScopeID: "test-account", UserID: "test-user", Mode: "auto", Metadata: map[string]any{
		"swarm_v3_project_id": "project", "project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator",
	}}
	created, err := svc.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: parent.ID, AccountScopeID: parent.AccountScopeID, UserID: parent.UserID,
		Kind: sessionruntime.SessionMutationCreateSession, Session: &parent,
		ClientRequestID: "project-parent", IdempotencyKey: "project-parent", PayloadHash: "project-parent", RequestHash: "project-parent",
	})
	if err != nil || created.Session == nil || created.Error != nil || created.Conflict != nil {
		t.Fatalf("create parent: %+v %v", created, err)
	}
	parent = *created.Session
	root := programFixtureRepo(t)
	head := programFixtureGit(t, root, "rev-parse", "HEAD")
	catalogDB, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalogDB.Close()
	catalog := pebblestore.NewWorkspaceStore(catalogDB)
	entry, err := catalog.AddForAccount(parent.AccountScopeID, root, "Repository")
	if err != nil {
		t.Fatal(err)
	}
	svc.workspace = workspace.NewService(catalog)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, SessionID: parent.ID}
	spec := &taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: root}
	before := programFixtureGit(t, root, "worktree", "list", "--porcelain")
	if _, _, err := svc.resolveTaskTargetWorkspace(parent, principal, spec); err == nil {
		t.Fatal("catalog-only target accepted without project membership")
	}
	if after := programFixtureGit(t, root, "worktree", "list", "--porcelain"); after != before {
		t.Fatal("rejected target allocated a lane")
	}
	project, _, err := store.GetProject(parent.AccountScopeID, "project")
	if err != nil {
		t.Fatal(err)
	}
	project.Workspaces = []pebblestore.ProjectWorkspaceRef{{Path: root, WorkspaceID: entry.WorkspaceID}}
	if err := store.PutProject(parent.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	target, _, err := svc.resolveTaskTargetWorkspace(parent, principal, spec)
	if err != nil || target != root {
		t.Fatalf("authorized target: %q %v", target, err)
	}
	wt := &worktreeruntime.Service{}
	svc.SetWorktreeService(wt)
	base, err := wt.ResolveTaskBase(target)
	if err != nil {
		t.Fatal(err)
	}
	profile, virtual, source, err := svc.resolveTaskLaunchProfile(parent, "coder")
	if err != nil {
		t.Fatal(err)
	}
	launch, err := svc.prepareDelegatedSubagentLaunchWithProfile(parent, "auto", taskLaunchPrepared{
		RequestedSubagent: "coder", MetaPrompt: "Update source.txt", VirtualTarget: virtual,
		TargetWorkspacePath: target, TaskBase: &base, OwnedScope: []string{"source.txt"}, LogicalTaskID: "project-coder",
	}, "Update source", "", &profile, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	child, found, err := svc.sessions.GetSession(launch.ChildSession.ID)
	if err != nil || !found || !child.WorktreeEnabled || child.WorktreeRootPath == "" || child.WorkspacePath != child.WorktreeRootPath || child.WorkspacePath == root {
		t.Fatalf("persisted isolation: %+v %v", child, err)
	}
	if pebblestore.ProjectConversationID(child) != "" || metadataStringForTest(child.Metadata, "parent_session_id") != parent.ID || metadataStringForTest(child.Metadata, "swarm_v3_source_workspace_path") != root {
		t.Fatalf("child provenance: %+v", child.Metadata)
	}
	if got := programFixtureGit(t, child.WorkspacePath, "rev-parse", "HEAD"); got != head {
		t.Fatalf("child base = %s, want %s", got, head)
	}
	if got, err := os.ReadFile(filepath.Join(child.WorkspacePath, "source.txt")); err != nil || string(got) != "base\n" {
		t.Fatalf("child source = %q %v", got, err)
	}
	scope, err := svc.resolveRunWorkspaceScope(child, principal)
	if err != nil {
		t.Fatal(err)
	}
	if scope.PrimaryPath != child.WorkspacePath {
		t.Fatalf("tool scope = %+v", scope)
	}
	if _, expansion, err := tool.ScopeExpansionForCall(scope, tool.Call{Name: "write", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(root, "source.txt"), "content": "forbidden"})}); err == nil && !expansion {
		t.Fatal("source checkout write permitted")
	}
	if programFixtureGit(t, root, "rev-parse", "HEAD") != head || programFixtureGit(t, root, "status", "--porcelain") != "" {
		t.Fatal("captured source changed")
	}
	persisted, found, err := svc.sessions.GetSession(parent.ID)
	if err != nil || !found {
		t.Fatalf("parent: %+v %v", persisted, err)
	}
	if err := store.ValidateProjectConversation(persisted, parent.AccountScopeID, parent.UserID); err != nil {
		t.Fatalf("parent gained filesystem authority: %v", err)
	}
}
