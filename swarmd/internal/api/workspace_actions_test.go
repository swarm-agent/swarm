package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	actionruntime "swarm/packages/swarmd/internal/action"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestWorkspaceActionsResolveManagedWorktreeToBoundParentWorkspace(t *testing.T) {
	server, parentPath, store := newWorkspaceOverviewTopologyTestServer(t)
	actionSvc := actionruntime.NewService(pebblestore.NewWorkspaceActionStore(store))
	server.SetActionService(actionSvc)

	principal := testPrincipal()
	parentScope, err := server.workspace.ScopeForPathForPrincipal(principal, parentPath)
	if err != nil || !parentScope.Matched || parentScope.WorkspaceID == "" {
		t.Fatalf("resolve parent workspace: scope=%#v err=%v", parentScope, err)
	}
	parentAction, err := actionSvc.Create(actionruntime.CreateInput{
		Scope:      actionruntime.Scope{AccountScopeID: principal.AccountScopeID, WorkspaceID: parentScope.WorkspaceID, WorkspacePath: parentScope.WorkspacePath},
		Name:       "Parent Action",
		Entrypoint: "scripts/parent-action.sh",
	})
	if err != nil {
		t.Fatalf("create parent action: %v", err)
	}

	unrelatedPath := filepath.Join(t.TempDir(), "unrelated-workspace")
	if err := os.MkdirAll(unrelatedPath, 0o755); err != nil {
		t.Fatalf("mkdir unrelated workspace: %v", err)
	}
	if _, err := server.workspace.AddForPrincipal(principal, unrelatedPath, "unrelated", "", false); err != nil {
		t.Fatalf("add unrelated workspace: %v", err)
	}
	unrelatedScope, err := server.workspace.ScopeForPathForPrincipal(principal, unrelatedPath)
	if err != nil || !unrelatedScope.Matched || unrelatedScope.WorkspaceID == "" {
		t.Fatalf("resolve unrelated workspace: scope=%#v err=%v", unrelatedScope, err)
	}
	unrelatedAction, err := actionSvc.Create(actionruntime.CreateInput{
		Scope:      actionruntime.Scope{AccountScopeID: principal.AccountScopeID, WorkspaceID: unrelatedScope.WorkspaceID, WorkspacePath: unrelatedScope.WorkspacePath},
		Name:       "Unrelated Action",
		Entrypoint: "scripts/unrelated-action.sh",
	})
	if err != nil {
		t.Fatalf("create unrelated action: %v", err)
	}

	const (
		runtimeID = "runtime-action-worktree"
		bindingID = "binding-action-worktree"
	)
	if _, err := server.topology.PutRuntimeForAccount(principal.AccountScopeID, pebblestore.TopologyRuntimeRecord{
		SwarmID: runtimeID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Name: runtimeID,
	}); err != nil {
		t.Fatalf("put runtime: %v", err)
	}
	if _, err := server.topology.PutRuntimePlacementForAccount(principal.AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{
		RuntimeSwarmID: runtimeID, AccountScopeID: principal.AccountScopeID, AuthorityHostSwarmID: runtimeID,
		RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive,
	}); err != nil {
		t.Fatalf("put runtime placement: %v", err)
	}
	if _, err := server.topology.PutWorkspaceBindingForAccount(principal.AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID: bindingID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		SourceWorkspaceID: parentScope.WorkspaceID, SourceWorkspaceGeneration: parentScope.WorkspaceGeneration,
		SourceWorkspacePath: parentScope.WorkspacePath, SourceWorkspaceName: parentScope.WorkspaceName,
		DestinationRuntimeSwarmID: runtimeID, DestinationAuthorityHostSwarmID: runtimeID,
		DestinationRuntimeKind: pebblestore.TopologyRuntimeKindHost, DestinationHostSwarmID: runtimeID,
		DestinationWorkspacePath: parentScope.WorkspacePath, PlacementGeneration: 1, BindingGeneration: 1,
		State: pebblestore.TopologyWorkspaceBindingStateBound, AccessMode: pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:   pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID: runtimeID, Writable: true,
	}); err != nil {
		t.Fatalf("put workspace binding: %v", err)
	}

	worktreePath := filepath.Join(t.TempDir(), "managed-worktree")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	parentSession, _, err := server.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Title: "Parent session",
		WorkspacePath: parentPath, WorkspaceName: parentScope.WorkspaceName, Mode: sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"swarm_v3_workspace_binding_id": bindingID,
			"local_workspace_binding_id":    bindingID,
			"swarm_v3_source_workspace_id":  parentScope.WorkspaceID,
		},
	})
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	childSession, _, err := server.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Title: "Managed worktree child",
		WorkspacePath: worktreePath, WorkspaceName: "managed-worktree", Mode: sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{RootPath: worktreePath, BaseBranch: "dev", BranchName: "agent/action-worktree", WorkspaceID: "action-worktree"},
		Metadata: map[string]any{
			"parent_session_id": parentSession.ID,
		},
	})
	if err != nil {
		t.Fatalf("create worktree child session: %v", err)
	}

	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/actions?workspace_path="+worktreePath+"&session_id="+childSession.ID, nil))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list worktree actions status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		WorkspaceID   string                        `json:"workspace_id"`
		WorkspacePath string                        `json:"workspace_path"`
		Actions       []pebblestore.WorkspaceAction `json:"actions"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode worktree actions: %v", err)
	}
	if response.WorkspaceID != parentScope.WorkspaceID || filepath.Clean(response.WorkspacePath) != filepath.Clean(parentScope.WorkspacePath) {
		t.Fatalf("resolved workspace=%q/%q want parent=%q/%q", response.WorkspaceID, response.WorkspacePath, parentScope.WorkspaceID, parentScope.WorkspacePath)
	}
	if len(response.Actions) != 1 || response.Actions[0].ID != parentAction.ID {
		t.Fatalf("worktree actions=%+v want parent action %q without unrelated %q", response.Actions, parentAction.ID, unrelatedAction.ID)
	}

	scope, err := server.resolveWorkspaceActionScope(request, worktreePath, childSession.ID)
	if err != nil {
		t.Fatalf("resolve worktree action scope: %v", err)
	}
	if filepath.Clean(scope.RuntimePath) != filepath.Clean(worktreePath) || filepath.Clean(scope.WorkspacePath) != filepath.Clean(parentPath) {
		t.Fatalf("action paths runtime=%q canonical=%q", scope.RuntimePath, scope.WorkspacePath)
	}

	mismatchedRequest := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/actions", nil))
	if _, err := server.resolveWorkspaceActionScope(mismatchedRequest, parentPath, childSession.ID); err == nil {
		t.Fatal("expected worktree session with mismatched runtime path to be rejected")
	}
}

func TestWorkspaceActionsResolveDirectWorkspaceSessionWithoutBinding(t *testing.T) {
	server, workspacePath, _ := newWorkspaceOverviewTopologyTestServer(t)
	principal := testPrincipal()
	directSession, _, err := server.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Title: "Direct workspace session",
		WorkspacePath: workspacePath, WorkspaceName: filepath.Base(workspacePath), Mode: sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("create direct session: %v", err)
	}
	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/actions", nil))
	scope, err := server.resolveWorkspaceActionScope(request, workspacePath, directSession.ID)
	if err != nil {
		t.Fatalf("resolve direct session scope: %v", err)
	}
	if scope.RuntimePath != scope.WorkspacePath || filepath.Clean(scope.WorkspacePath) != filepath.Clean(workspacePath) {
		t.Fatalf("direct action scope = %+v", scope)
	}
}
