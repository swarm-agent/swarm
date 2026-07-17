package run

import (
	"context"
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/todo"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestParseAITaskPreparationStrict(t *testing.T) {
	got, err := ParseAITaskPreparation(`{"title":"Fix sidebar","prompt":"Implement it","mode":"auto","worktree":true}`)
	if err != nil || got.Mode != "auto" || !got.Worktree {
		t.Fatalf("got %#v, %v", got, err)
	}
	for _, raw := range []string{
		`{"title":"x","prompt":"y","mode":"later","worktree":true}`,
		`{"title":"x","prompt":"y","mode":"auto","worktree":true,"session_id":"escape"}`,
		`{"title":"x","prompt":"","mode":"auto","worktree":true}`,
	} {
		if _, err := ParseAITaskPreparation(raw); err == nil {
			t.Fatalf("expected rejection for %s", raw)
		}
	}
}

func TestExecutePreparedAITaskCreatesManagedWorktreeSessionAndDurableLinkage(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	const (
		accountScopeID = "account-ai-task"
		userID         = "user-ai-task"
	)
	if _, _, _, err := svc.agents.RestoreDefaultsForAccount(accountScopeID); err != nil {
		t.Fatalf("restore account agents: %v", err)
	}
	if _, _, _, err := svc.agents.UpsertForAccount(accountScopeID, agentruntime.UpsertInput{
		Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "codex", Model: "configured-auto-model", Thinking: "high",
		RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, Enabled: pebblestore.BoolPtr(true),
	}); err != nil {
		t.Fatalf("configure account Swarm: %v", err)
	}
	if _, _, _, err := svc.agents.ActivatePrimaryForAccount(accountScopeID, "swarm"); err != nil {
		t.Fatalf("activate account Swarm: %v", err)
	}
	state, err := svc.agents.ListStateForAccount(accountScopeID, 20)
	if err != nil {
		t.Fatalf("list account agents: %v", err)
	}
	if state.ActivePrimary != "swarm" {
		t.Fatalf("active account primary = %q, profiles=%#v", state.ActivePrimary, state.Profiles)
	}
	resolvedSwarm, err := svc.agents.ResolveSystemAgent("swarm", pebblestore.AgentProfile{})
	if err != nil || !resolvedSwarm.Enabled || resolvedSwarm.Mode != agentruntime.ModePrimary {
		t.Fatalf("resolved Swarm = %#v err=%v", resolvedSwarm, err)
	}

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: userID, AccountScopeID: accountScopeID}
	workspacePath := t.TempDir()
	workspaceStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace.pebble"))
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	defer workspaceStore.Close()
	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(workspaceStore))
	if _, err := workspaceSvc.AddForPrincipal(principal, workspacePath, "AI task workspace", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	scope, err := workspaceSvc.ScopeForPathForPrincipal(principal, workspacePath)
	if err != nil || !scope.Matched {
		t.Fatalf("resolve workspace scope: matched=%t err=%v", scope.Matched, err)
	}
	svc.SetWorkspaceService(workspaceSvc)

	parent, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID: userID, AccountScopeID: accountScopeID, Title: "AI task parent",
		WorkspacePath: workspacePath, WorkspaceName: "AI task workspace", Mode: sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "configured-auto-model", Thinking: "high"},
	})
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}

	todoStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "todo.pebble"))
	if err != nil {
		t.Fatalf("open todo store: %v", err)
	}
	defer todoStore.Close()
	todoEvents, err := pebblestore.NewEventLog(todoStore)
	if err != nil {
		t.Fatalf("open todo event log: %v", err)
	}
	todoSvc := todo.NewService(pebblestore.NewWorkspaceTodoStore(todoStore), todoEvents, nil, svc.sessions)
	queued, _, _, err := todoSvc.CreateAITask(todo.CreateAITaskInput{WorkspacePath: workspacePath, Request: "Fix the queued task", IdempotencyKey: "request-1"})
	if err != nil {
		t.Fatalf("create queued AI task: %v", err)
	}
	svc.SetAITaskBinder(todoSvc)

	managedPath := filepath.Join(t.TempDir(), "managed-worktree")
	worktrees := &taskLaunchWorktreeStub{allocation: worktreeruntime.Allocation{
		RepoRoot: workspacePath, WorkspacePath: managedPath, WorkspaceID: "ws_ai_task",
		BaseBranch: "dev", BranchName: "agent/fix-queued-task",
	}}
	svc.SetWorktreeService(worktrees)
	svc.SetSessionDeployCanonicalizer(func(input SessionDeployCanonicalizeInput) (SessionDeployCanonicalization, error) {
		metadata := input.Metadata
		metadata["swarm_v3_source_workspace_path"] = workspacePath
		metadata["swarm_v3_source_workspace_name"] = "AI task workspace"
		return SessionDeployCanonicalization{
			Metadata: metadata, SourceWorkspaceID: scope.WorkspaceID,
			SourceWorkspaceGeneration: scope.WorkspaceGeneration,
			SourceWorkspaceName:       "AI task workspace", SourceWorkspacePath: workspacePath,
			RuntimeWorkspacePath: workspacePath,
		}, nil
	})
	var enqueuedSessionID, enqueuedRunID, enqueuedParentID string
	svc.SetSessionDeployEnqueuer(func(got identity.Principal, sessionID, runID, parentSessionID string) bool {
		if got.UserID != userID || got.AccountScopeID != accountScopeID {
			t.Fatalf("enqueue principal = %#v", got)
		}
		enqueuedSessionID, enqueuedRunID, enqueuedParentID = sessionID, runID, parentSessionID
		return true
	})

	preparation := AITaskPreparation{Title: "Fix queued task", Prompt: "Implement the narrow fix", Mode: sessionruntime.ModeAuto, Worktree: true}
	if _, err := svc.ExecutePreparedAITask(context.Background(), parent.ID, workspacePath, queued.ID, preparation, svc.sessions.ApplySessionMutation); err != nil {
		t.Fatalf("execute prepared AI task: %v", err)
	}

	linked, ok, err := pebblestore.NewWorkspaceTodoStore(todoStore).Get(workspacePath, queued.ID)
	if err != nil || !ok {
		t.Fatalf("load linked task: ok=%t err=%v", ok, err)
	}
	if linked.AIState != pebblestore.WorkspaceTodoAIStateInProgress || !linked.InProgress || linked.AIMode != sessionruntime.ModeAuto || !linked.AIWorktree {
		t.Fatalf("task execution state = %#v", linked)
	}
	if linked.ManagedSessionID == "" || linked.ManagedSessionID != enqueuedSessionID || enqueuedRunID == "" || enqueuedParentID != parent.ID {
		t.Fatalf("task/run linkage: task=%q enqueue=%q/%q parent=%q", linked.ManagedSessionID, enqueuedSessionID, enqueuedRunID, enqueuedParentID)
	}
	managed, ok, err := svc.sessions.GetSession(linked.ManagedSessionID)
	if err != nil || !ok {
		t.Fatalf("load managed session: ok=%t err=%v", ok, err)
	}
	if !managed.WorktreeEnabled || managed.WorkspacePath != managedPath || managed.WorktreeBranch != "agent/fix-queued-task" {
		t.Fatalf("managed worktree session = %#v", managed)
	}
	if mapString(managed.Metadata, "ai_task_id") != queued.ID || mapString(managed.Metadata, "ai_task_workspace_path") != workspacePath {
		t.Fatalf("reciprocal AI task metadata = %#v", managed.Metadata)
	}
}
