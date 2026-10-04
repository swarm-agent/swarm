package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
// Coder lane for either of two sources after explicit catalog/project authorization. resolveTaskTargetWorkspace,
// prepareDelegatedSubagentLaunchWithProfile and resolveRunWorkspaceScope own these
// boundaries. Temporary Git/Pebble prove persisted child isolation, source-write
// rejection and unchanged parent authority without a provider or running daemon.
func TestProjectConversationDelegatedCoderRetainsIsolatedWorktree(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	store := svc.sessions.Store()
	// Match daemon startup: delegated session registration requires completed
	// repository-history maintenance, including fixtures with legacy parents.
	if err := store.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
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
	secondRoot := programFixtureRepo(t)
	secondEntry, err := catalog.AddForAccount(parent.AccountScopeID, secondRoot, "Second repository")
	if err != nil {
		t.Fatal(err)
	}
	project.Workspaces = []pebblestore.ProjectWorkspaceRef{{Path: root, WorkspaceID: entry.WorkspaceID}, {Path: secondRoot, WorkspaceID: secondEntry.WorkspaceID}}
	if err := store.PutProject(parent.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{root, secondRoot} {
		assertProjectSourceDelegation(t, svc, parent, principal, root)
	}
	if programFixtureGit(t, root, "rev-parse", "HEAD") != head {
		t.Fatal("original source changed")
	}
}

func assertProjectSourceDelegation(t *testing.T, svc *Service, parent pebblestore.SessionSnapshot, principal identity.Principal, root string) {
	t.Helper()
	store := svc.sessions.Store()
	head := programFixtureGit(t, root, "rev-parse", "HEAD")
	spec := &taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: root}
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
		TargetWorkspacePath: target, TaskBase: &base, OwnedScope: []string{"source.txt"}, LogicalTaskID: "project-coder-" + filepath.Base(root),
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

// Purpose: providerManagedWorkspaceContext refreshes authority for V3 runtime
// tools before scope gating and execution. A project
// conversation must pass that refresh without a repository, while stale captured
// paths and foreign principals must fail closed. This service/store test exercises
// the actual provider-tool refresh boundary without a provider or permission bypass.
func TestProjectConversationProviderToolWorkspaceRefresh(t *testing.T) {
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
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user", SessionID: "conversation"}
	session := pebblestore.SessionSnapshot{ID: p.SessionID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Metadata: map[string]any{
		"swarm_v3_project_id": "project", "project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator",
	}}
	created, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: session.ID, AccountScopeID: session.AccountScopeID, UserID: session.UserID,
		Kind: sessionruntime.SessionMutationCreateSession, Session: &session,
		ClientRequestID: "create", IdempotencyKey: "create", PayloadHash: "create", RequestHash: "create",
	})
	if err != nil || created.Session == nil || created.Error != nil || created.Conflict != nil {
		t.Fatalf("create: %+v %v", created, err)
	}
	assertEmpty := func(ctx runWorkspaceContext) {
		t.Helper()
		if ctx.WorkspacePath != "" || ctx.OriginWorkspacePath != "" || len(ctx.WorkspaceRoots) != 0 || len(ctx.OriginWorkspaceRoots) != 0 || ctx.Scope.PrimaryPath != "" || len(ctx.Scope.Roots) != 0 || !ctx.Scope.RejectScopeExpansion || ctx.Scope.SessionID != session.ID || ctx.Scope.Principal.SessionID != p.SessionID || ctx.Scope.Principal.AccountScopeID != p.AccountScopeID || ctx.Scope.Principal.UserID != p.UserID {
			t.Fatalf("project tool authority widened or identity lost: %+v", ctx)
		}
	}
	ordinary := session
	ordinary.Metadata = nil
	unchanged := runWorkspaceContext{WorkspacePath: "captured"}
	if _, err := s.syncWorkspaceScopeFromSession(ordinary, p, &unchanged); err == nil {
		t.Fatal("ordinary session accepted without repository authority")
	}
	if unchanged.WorkspacePath != "captured" {
		t.Fatal("rejected refresh partially changed the context")
	}
	config := providerToolInvokerConfig{sessionID: session.ID}
	for i := 0; i < 2; i++ {
		ctx, err := s.providerManagedWorkspaceContext(config, p)
		if err != nil {
			t.Fatalf("tool refresh/reconnect %d: %v", i, err)
		}
		assertEmpty(ctx)
	}
	// Stale roots must never become authority when the durable session is
	// workspace-free, even when the caller omits its captured primary path.
	root := t.TempDir()
	config.workspaceRoots = []string{root}
	config.workspaceOriginPath = root
	config.workspaceOriginRoots = []string{root}
	ctx, err := s.providerManagedWorkspaceContext(config, p)
	if err != nil {
		t.Fatal(err)
	}
	assertEmpty(ctx)
	config.workspacePath = root
	if _, err := s.providerManagedWorkspaceContext(config, p); err == nil {
		t.Fatal("accepted stale captured repository authority")
	}
	config = providerToolInvokerConfig{sessionID: session.ID}
	for _, foreign := range []identity.Principal{
		{Type: identity.PrincipalTypeUser, AccountScopeID: "other-account", UserID: p.UserID},
		{Type: identity.PrincipalTypeUser, AccountScopeID: p.AccountScopeID, UserID: "other-user"},
	} {
		if _, err := s.providerManagedWorkspaceContext(config, foreign); err == nil {
			t.Fatal("accepted foreign tool principal")
		}
	}
	persisted, found, err := s.sessions.GetSession(session.ID)
	if err != nil || !found {
		t.Fatalf("persisted conversation: %+v %v", persisted, err)
	}
	if err := store.ValidateProjectConversation(persisted, p.AccountScopeID, p.UserID); err != nil {
		t.Fatalf("refresh changed durable authority: %v", err)
	}
}

// Purpose: project context injection must use durable project ownership, not
// filesystem readiness or the first project. resolveRunExecutionContext and
// composeInstructionsForScope are the narrowest service boundaries proving
// missing source paths do not suppress context or grant filesystem authority.
func TestProjectConversationContextSurvivesMissingSources(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "context.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	for _, project := range []*pebblestore.ProjectRecord{
		{ID: "other", Name: "Other", ProjectContext: "wrong-project-context"},
		{ID: "project", Name: "Project", ProjectContext: "stored-context-marker", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: filepath.Join(t.TempDir(), "missing")}}},
	} {
		if err := store.PutProject(p.AccountScopeID, project); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{sessions: sessionruntime.NewService(store, nil)}
	snapshot := pebblestore.SessionSnapshot{ID: "context-conversation", AccountScopeID: p.AccountScopeID, UserID: p.UserID, Metadata: map[string]any{"project_id": "project", "swarm_v3_project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator"}}
	result, err := svc.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: snapshot.ID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Kind: sessionruntime.SessionMutationCreateSession, Session: &snapshot, ClientRequestID: "create", IdempotencyKey: "create", PayloadHash: "create", RequestHash: "create"})
	if err != nil || result.Session == nil || result.Error != nil || result.Conflict != nil {
		t.Fatalf("create: %+v %v", result, err)
	}
	for i := 0; i < 2; i++ {
		saved, found, err := svc.sessions.GetSession(snapshot.ID)
		if err != nil || !found {
			t.Fatalf("reopen: %v", err)
		}
		execution, err := svc.resolveRunExecutionContext(saved, RunExecutionContext{}, p)
		if err != nil {
			t.Fatal(err)
		}
		instructions := svc.composeInstructionsForScope(execution.Scope, pebblestore.AgentProfile{Name: "system-orchestrator", Mode: "primary"}, "")
		if !strings.Contains(instructions, "stored-context-marker") || strings.Contains(instructions, "wrong-project-context") {
			t.Fatalf("incorrect project context: %s", instructions)
		}
		if execution.Scope.PrimaryPath != "" || len(execution.Scope.Roots) != 0 || !execution.Scope.RejectScopeExpansion {
			t.Fatalf("authority widened: %+v", execution.Scope)
		}
	}
	foreign := p
	foreign.AccountScopeID = "other-account"
	if _, err := svc.resolveRunExecutionContext(*result.Session, RunExecutionContext{}, foreign); err == nil {
		t.Fatal("foreign context accepted")
	}
}
