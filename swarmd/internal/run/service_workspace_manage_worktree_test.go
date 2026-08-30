package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Requirement: a primary Swarm session starts in the user's current checkout,
// can move its primary workspace through the canonical V3 mutation boundary,
// and may explicitly adopt a managed worktree without creating a child or
// replacement session. These tests exercise the narrowest production control
// plane that can prove durable grants, immediate runtime scope refresh, and
// rollback with zero partial mutation on a dirty move.
type failingWorkspaceMapService struct{}

func (failingWorkspaceMapService) GetOrCreateDefault(string) (pebblestore.WorkspaceMap, error) {
	return pebblestore.WorkspaceMap{}, errors.New("workspace map unavailable")
}

func (failingWorkspaceMapService) Update(string, int64, string) (pebblestore.WorkspaceMap, error) {
	return pebblestore.WorkspaceMap{}, errors.New("workspace map unavailable")
}

type sameSessionWorktreeStub struct {
	allocation  worktreeruntime.Allocation
	allocateErr error
	states      map[string]worktreeruntime.TaskWorkspaceState
	rolledBack  []string
}

func (s *sameSessionWorktreeStub) AttachBranch(_, _, _ string) (string, error) { return "", nil }
func (s *sameSessionWorktreeStub) ResolveTaskBase(string) (worktreeruntime.TaskBase, error) {
	return worktreeruntime.TaskBase{}, nil
}
func (s *sameSessionWorktreeStub) AllocateTaskWorkspace(string, worktreeruntime.TaskBase, string, []string) (worktreeruntime.Allocation, error) {
	return worktreeruntime.Allocation{}, nil
}
func (s *sameSessionWorktreeStub) InspectTaskWorkspace(path string) (worktreeruntime.TaskWorkspaceState, error) {
	state, ok := s.states[path]
	if !ok {
		return worktreeruntime.TaskWorkspaceState{}, errors.New("unknown worktree")
	}
	return state, nil
}
func (s *sameSessionWorktreeStub) TaskCommitDescendsFrom(string, string, string) (bool, error) {
	return true, nil
}
func (s *sameSessionWorktreeStub) TaskCommitRangeIntegratedInto(string, string, string, string) (bool, error) {
	return false, nil
}
func (s *sameSessionWorktreeStub) RemoveIntegratedTaskWorkspace(string, string, string, string, string, string) error {
	return nil
}
func (s *sameSessionWorktreeStub) GetConfigForPrincipal(identity.Principal, string) (worktreeruntime.Config, error) {
	return worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"}, nil
}
func (s *sameSessionWorktreeStub) AllocateDetachedWorkspaceRequestedForPrincipal(identity.Principal, string, string, string, string) (worktreeruntime.Allocation, error) {
	return s.allocation, s.allocateErr
}
func (s *sameSessionWorktreeStub) RollbackAllocation(allocation worktreeruntime.Allocation) error {
	s.rolledBack = append(s.rolledBack, allocation.WorkspacePath)
	return nil
}

func TestManageWorkspaceAdoptWorktreeKeepsSameSessionAndRefreshesScope(t *testing.T) {
	principal := testRunPrincipal()
	workspacePath := t.TempDir()
	worktreePath := t.TempDir()
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	entry, err := workspaceSvc.AddForPrincipal(principal, workspacePath, "repo", "", true)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	sessionStore := pebblestore.NewSessionStore(rawStore)
	sessionID := "same-session"
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID: sessionID, WorkspacePath: workspacePath, WorkspaceName: "repo", Title: "same", Metadata: map[string]any{},
	}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessionStore, nil)
	worktrees := &sameSessionWorktreeStub{
		allocation: worktreeruntime.Allocation{WorkspacePath: worktreePath, BaseBranch: "dev", BaseCommit: strings.Repeat("a", 40), BranchName: "agent/same-session"},
		states:     map[string]worktreeruntime.TaskWorkspaceState{worktreePath: {WorkspacePath: worktreePath, BranchName: "agent/same-session", Clean: true}},
	}
	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, nil, nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	runSvc.SetWorktreeService(worktrees)
	runSvc.SetSessionWorkspaceCanonicalizer(func(input SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
		return SessionWorkspaceCanonicalization{
			WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, WorkspaceState: "active", WorkspaceName: "repo",
			SourceWorkspacePath: workspacePath, RuntimeWorkspacePath: workspacePath, WorkspaceBindingID: "binding", RuntimeSwarmID: "swarm",
			PlacementGeneration: 1, BindingGeneration: 1, AuthorityHostSwarmID: "swarm",
		}, nil
	})
	output, err := runSvc.executeManageWorkspaceTool(sessionID, `{"action":"adopt_worktree","workspace_id":"`+entry.WorkspaceID+`","worktree_name":"same-session"}`, principal, sessionSvc.ApplySessionMutation)
	if err != nil {
		t.Fatalf("adopt worktree: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if payload["session_id"] != sessionID || payload["runtime_worktree_path"] != worktreePath || payload["restart_turn"] != true {
		t.Fatalf("output = %#v", payload)
	}
	stored, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("load session: ok=%t err=%v", ok, err)
	}
	if stored.ID != sessionID || !stored.WorktreeEnabled || stored.WorktreeRootPath != worktreePath || stored.WorkspacePath != workspacePath {
		t.Fatalf("stored session = %+v", stored)
	}
	if stored.Metadata["swarm_v3_worktree_owner_session_id"] != sessionID || stored.Metadata["swarm_v3_runtime_workspace_path"] != worktreePath {
		t.Fatalf("metadata = %#v", stored.Metadata)
	}
	scope, err := runSvc.ResolveRuntimeWorkspaceScope(stored, principal)
	if err != nil {
		t.Fatalf("resolve adopted scope: %v", err)
	}
	if scope.PrimaryPath != worktreePath || !scope.WorktreeEnabled || scope.SessionID != sessionID {
		t.Fatalf("scope = %+v", scope)
	}
}

func TestManageWorkspaceSetSessionPreservesGlobalWorkspaceGrantsAndRestartsTurn(t *testing.T) {
	principal := testRunPrincipal()
	firstPath := t.TempDir()
	secondPath := t.TempDir()
	thirdPath := t.TempDir()
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	first, err := workspaceSvc.AddForPrincipal(principal, firstPath, "first", "", true)
	if err != nil {
		t.Fatalf("add first workspace: %v", err)
	}
	second, err := workspaceSvc.AddForPrincipal(principal, secondPath, "second", "", false)
	if err != nil {
		t.Fatalf("add second workspace: %v", err)
	}
	third, err := workspaceSvc.AddForPrincipal(principal, thirdPath, "third", "", false)
	if err != nil {
		t.Fatalf("add third workspace: %v", err)
	}
	available := true
	sessionStore := pebblestore.NewSessionStore(rawStore)
	sessionID := "move-session"
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID: sessionID, WorkspacePath: firstPath, WorkspaceName: "first", Title: "move",
		Metadata: map[string]any{"swarm_v3_source_workspace_id": first.WorkspaceID, "swarm_v3_source_workspace_generation": "1"},
		WorkspaceGrants: []pebblestore.WorkspaceGrant{
			{Kind: pebblestore.WorkspaceGrantPrimary, WorkspaceID: first.WorkspaceID, WorkspaceGeneration: first.WorkspaceGeneration, Path: firstPath, Name: "first", Available: &available},
			{Kind: pebblestore.WorkspaceGrantAdditional, WorkspaceID: second.WorkspaceID, WorkspaceGeneration: second.WorkspaceGeneration, Path: secondPath, Name: "second", Available: &available},
			{Kind: pebblestore.WorkspaceGrantAdditional, WorkspaceID: third.WorkspaceID, WorkspaceGeneration: third.WorkspaceGeneration, Path: thirdPath, Name: "third", Available: &available},
		},
	}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessionStore, nil)
	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, nil, nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	runSvc.SetSessionWorkspaceCanonicalizer(func(input SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
		entries := map[string]struct {
			id         string
			generation int64
			name       string
			path       string
		}{
			first.WorkspaceID:  {first.WorkspaceID, first.WorkspaceGeneration, "first", firstPath},
			second.WorkspaceID: {second.WorkspaceID, second.WorkspaceGeneration, "second", secondPath},
			third.WorkspaceID:  {third.WorkspaceID, third.WorkspaceGeneration, "third", thirdPath},
		}
		entry, ok := entries[input.WorkspaceID]
		if !ok {
			return SessionWorkspaceCanonicalization{}, errors.New("unknown workspace")
		}
		return SessionWorkspaceCanonicalization{WorkspaceID: entry.id, WorkspaceGeneration: entry.generation, WorkspaceState: "active", WorkspaceName: entry.name, SourceWorkspacePath: entry.path, RuntimeWorkspacePath: entry.path, WorkspaceBindingID: "binding-" + entry.id, RuntimeSwarmID: "swarm", PlacementGeneration: 1, BindingGeneration: 1}, nil
	})

	output, err := runSvc.executeManageWorkspaceTool(sessionID, `{"action":"set_session","workspace_id":"`+second.WorkspaceID+`"}`, principal, sessionSvc.ApplySessionMutation)
	if err != nil {
		t.Fatalf("move session workspace: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if payload["workspace_id"] != second.WorkspaceID || payload["restart_turn"] != true {
		t.Fatalf("output = %#v", payload)
	}
	stored, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("load moved session: ok=%t err=%v", ok, err)
	}
	if stored.WorkspacePath != secondPath || stored.WorkspaceName != "second" || stored.WorktreeEnabled {
		t.Fatalf("moved session = %+v", stored)
	}
	grantsByID := map[string]string{}
	for _, grant := range pebblestore.NormalizeSessionWorkspaceGrants(stored) {
		grantsByID[grant.WorkspaceID] = grant.Kind
	}
	if grantsByID[second.WorkspaceID] != pebblestore.WorkspaceGrantPrimary || grantsByID[first.WorkspaceID] != pebblestore.WorkspaceGrantAdditional || grantsByID[third.WorkspaceID] != pebblestore.WorkspaceGrantAdditional {
		t.Fatalf("global workspace grants were not preserved: %+v", stored.WorkspaceGrants)
	}
	scope, err := runSvc.ResolveRuntimeWorkspaceScope(stored, principal)
	if err != nil {
		t.Fatalf("resolve moved scope: %v", err)
	}
	if scope.PrimaryPath != secondPath || !containsTrimmedString(scope.Roots, firstPath) || !containsTrimmedString(scope.Roots, secondPath) || !containsTrimmedString(scope.Roots, thirdPath) {
		t.Fatalf("moved scope = %+v", scope)
	}
}

func TestManageWorkspacePermissionMetadataAndActionScopes(t *testing.T) {
	principal := testRunPrincipal()
	currentPath := t.TempDir()
	safePath := t.TempDir()
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	current, err := workspaceSvc.AddForPrincipal(principal, currentPath, "current", "", true)
	if err != nil {
		t.Fatalf("add current workspace: %v", err)
	}
	safe, err := workspaceSvc.AddForPrincipal(principal, safePath, "safe", "", false)
	if err != nil {
		t.Fatalf("add safe workspace: %v", err)
	}
	sessionStore := pebblestore.NewSessionStore(rawStore)
	sessionID := "workspace-permission-metadata"
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID: sessionID, WorkspacePath: currentPath, WorkspaceName: "current",
		Metadata: map[string]any{"swarm_v3_source_workspace_id": current.WorkspaceID},
	}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	runSvc := NewService(sessionruntime.NewService(sessionStore, nil), nil, nil, nil, nil, nil, nil, nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	runSvc.SetSessionWorkspaceCanonicalizer(testManageWorkspaceCanonicalizer(current, safe))
	args := `{"action":"update","workspace_id":"` + current.WorkspaceID + `","workspace_generation":1,"workspace_name":"renamed","intent":"Make the workspace label clearer."}`
	payload, err := runSvc.buildManageWorkspacePermissionPayload(sessionID, args)
	if err != nil {
		t.Fatalf("build permission payload: %v", err)
	}
	if payload["action"] != "update" || payload["permission_scope"] != "workspace_update" || payload["intent"] != "Make the workspace label clearer." {
		t.Fatalf("permission metadata = %#v", payload)
	}
	target, _ := payload["target"].(map[string]any)
	safety, _ := payload["safety"].(map[string]any)
	changes, _ := payload["requested_changes"].(map[string]any)
	if target["workspace_id"] != current.WorkspaceID || target["workspace_generation"] != current.WorkspaceGeneration || target["workspace_path"] != currentPath || changes["workspace_name"] != "renamed" || safety["session_switch_required"] != true || safety["restore_after_mutation"] != true {
		t.Fatalf("permission target/safety = target=%#v changes=%#v safety=%#v", target, changes, safety)
	}
	if safe.WorkspaceID == "" {
		t.Fatal("safe workspace identity is empty")
	}
	formatted, err := runSvc.permissionArgumentsForCall(sessionID, sessionruntime.ModeAuto, tool.Call{Name: "manage_workspace", Arguments: args})
	if err != nil {
		t.Fatalf("format permission arguments: %v", err)
	}
	var formattedPayload map[string]any
	if err := json.Unmarshal([]byte(formatted), &formattedPayload); err != nil {
		t.Fatalf("decode formatted permission payload: %v", err)
	}
	formattedSafety, _ := formattedPayload["safety"].(map[string]any)
	if formattedSafety["session_switch_required"] != true || formattedSafety["filesystem_contents_changed"] != false {
		t.Fatalf("formatted permission safety = %#v", formattedSafety)
	}
	formattedTarget, _ := formattedPayload["target"].(map[string]any)
	formattedChanges, _ := formattedPayload["requested_changes"].(map[string]any)
	if formattedTarget["workspace_id"] != current.WorkspaceID || formattedTarget["workspace_path"] != currentPath || formattedChanges["workspace_name"] != "renamed" {
		t.Fatalf("formatted permission target/changes = %#v / %#v", formattedTarget, formattedChanges)
	}
	approvedArgs, ok := formattedPayload["approved_arguments"].(map[string]any)
	if !ok || approvedArgs["permission_scope"] != "workspace_update" || approvedArgs["workspace_id"] != current.WorkspaceID || approvedArgs["workspace_name"] != "renamed" || approvedArgs["intent"] != "Make the workspace label clearer." {
		t.Fatalf("approved arguments = %#v", formattedPayload["approved_arguments"])
	}
	if got := manageWorkspaceMutationAction(`{"action":"edit"}`); got != "update" {
		t.Fatalf("edit action scope = %q, want update", got)
	}
	if _, err := runSvc.executeManageWorkspaceTool(sessionID, args, principal, nil); err == nil || !strings.Contains(err.Error(), "approved permission_scope") {
		t.Fatalf("unapproved direct mutation error = %v", err)
	}
	if nested, ok := formattedPayload["approved_arguments"].(map[string]any); !ok || nested["permission_scope"] != "workspace_update" {
		t.Fatalf("stored permission payload is not action-bound: %#v", formattedPayload)
	}

	for action, want := range map[string]string{"create": "workspace_create", "edit": "workspace_update", "update": "workspace_update", "delete": "workspace_delete"} {
		requirement, ask := permissionRequirement("auto", "manage_workspace", `{"action":"`+action+`"}`)
		if !ask || requirement != want {
			t.Fatalf("%s permission = %q/%t, want %q/true", action, requirement, ask, want)
		}
	}
	if requirement, ask := permissionRequirement("auto", "manage_workspace", `{"action":"inspect"}`); ask || requirement != "manage_workspace" {
		t.Fatalf("inspect permission = %q/%t", requirement, ask)
	}
	if badArgs := cloneGenericMap(approvedArgs); badArgs != nil {
		badArgs["permission_scope"] = "workspace_delete"
		raw, _ := json.Marshal(badArgs)
		if _, err := runSvc.executeManageWorkspaceTool(sessionID, string(raw), principal, nil); err == nil || !strings.Contains(err.Error(), "permission scope is invalid") {
			t.Fatalf("mismatched permission scope error = %v", err)
		}
	}
	for _, mode := range []string{"auto+bypass_permissions", "yolo", "read", "readwrite"} {
		if requirement, ask := permissionRequirement(mode, "manage_workspace", `{"action":"delete"}`); !ask || requirement != "workspace_delete" {
			t.Fatalf("delete %s permission = %q/%t, want workspace_delete/true", mode, requirement, ask)
		}
	}
}

func TestManageWorkspaceActiveUpdateSwitchesMutatesAndRestores(t *testing.T) {
	principal := testRunPrincipal()
	currentPath := t.TempDir()
	safePath := t.TempDir()
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	current, err := workspaceSvc.AddForPrincipal(principal, currentPath, "current", "", true)
	if err != nil {
		t.Fatalf("add current: %v", err)
	}
	safe, err := workspaceSvc.AddForPrincipal(principal, safePath, "safe", "", false)
	if err != nil {
		t.Fatalf("add safe: %v", err)
	}
	available := true
	sessionStore := pebblestore.NewSessionStore(rawStore)
	sessionID := "safe-update"
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID: sessionID, WorkspacePath: currentPath, WorkspaceName: "current",
		Metadata: map[string]any{"swarm_v3_source_workspace_id": current.WorkspaceID, "swarm_v3_source_workspace_generation": "1"},
		WorkspaceGrants: []pebblestore.WorkspaceGrant{
			{Kind: pebblestore.WorkspaceGrantPrimary, WorkspaceID: current.WorkspaceID, WorkspaceGeneration: 1, Path: currentPath, Name: "current", Available: &available},
			{Kind: pebblestore.WorkspaceGrantAdditional, WorkspaceID: safe.WorkspaceID, WorkspaceGeneration: 1, Path: safePath, Name: "safe", Available: &available},
		},
	}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessionStore, nil)
	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, nil, nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	runSvc.SetSessionWorkspaceCanonicalizer(func(input SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
		entry, ok, err := workspaceSvc.GetByWorkspaceIDForPrincipal(principal, input.WorkspaceID)
		if err != nil || !ok {
			return SessionWorkspaceCanonicalization{}, errors.New("unknown workspace")
		}
		return SessionWorkspaceCanonicalization{
			WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, WorkspaceState: "active",
			WorkspaceName: entry.Name, SourceWorkspacePath: entry.Path, RuntimeWorkspacePath: entry.Path,
			WorkspaceBindingID: "binding-" + entry.WorkspaceID, RuntimeSwarmID: "swarm", PlacementGeneration: 1, BindingGeneration: 1,
		}, nil
	})
	output, err := runSvc.executeManageWorkspaceTool(sessionID, `{"action":"update","permission_scope":"workspace_update","intent":"Update the selected workspace.","workspace_id":"`+current.WorkspaceID+`","workspace_generation":1,"workspace_name":"renamed"}`, principal, sessionSvc.ApplySessionMutation)
	if err != nil {
		t.Fatalf("update active workspace: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	safety, _ := payload["safety"].(map[string]any)
	if safety["switched_before_mutation"] != true || safety["restored_after_mutation"] != true || payload["restart_turn"] != true {
		t.Fatalf("update safety = %#v payload=%#v", safety, payload)
	}
	stored, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("load session: ok=%t err=%v", ok, err)
	}
	if stored.WorkspacePath != currentPath || mapString(stored.Metadata, "swarm_v3_source_workspace_id") != current.WorkspaceID {
		t.Fatalf("restored session = %+v", stored)
	}
	foundUpdatedGrant := false
	for _, grant := range stored.WorkspaceGrants {
		if grant.WorkspaceID != current.WorkspaceID {
			continue
		}
		foundUpdatedGrant = true
		if grant.Name != "renamed" || grant.WorkspaceGeneration != current.WorkspaceGeneration {
			t.Fatalf("updated session grant is stale: %+v", stored.WorkspaceGrants)
		}
	}
	if !foundUpdatedGrant {
		t.Fatalf("updated session grant is missing: %+v", stored.WorkspaceGrants)
	}
	updated, ok, err := workspaceSvc.GetByWorkspaceIDForPrincipal(principal, current.WorkspaceID)
	if err != nil || !ok || updated.Name != "renamed" {
		t.Fatalf("updated catalog = %+v ok=%t err=%v", updated, ok, err)
	}
	if currentBinding, ok, err := workspaceSvc.CurrentBindingForPrincipal(principal); err != nil || !ok || currentBinding.WorkspaceID != current.WorkspaceID || currentBinding.WorkspaceName != "renamed" {
		t.Fatalf("updated account default = %+v ok=%t err=%v", currentBinding, ok, err)
	}
}

func TestManageWorkspaceActiveCreateSwitchesAndRestores(t *testing.T) {
	principal := testRunPrincipal()
	currentPath := t.TempDir()
	safePath := t.TempDir()
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	safe, err := workspaceSvc.AddForPrincipal(principal, safePath, "safe", "", true)
	if err != nil {
		t.Fatalf("add safe: %v", err)
	}
	sessionStore := pebblestore.NewSessionStore(rawStore)
	sessionID := "safe-create"
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: sessionID, WorkspacePath: currentPath, WorkspaceName: "unsaved", Metadata: map[string]any{}}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessionStore, nil)
	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, nil, nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	// Resolve from the live account catalog so the newly created identity becomes
	// available to the restore step immediately after creation.
	runSvc.SetSessionWorkspaceCanonicalizer(func(input SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
		entry, ok, err := workspaceSvc.GetByWorkspaceIDForPrincipal(principal, input.WorkspaceID)
		if err != nil || !ok {
			return SessionWorkspaceCanonicalization{}, errors.New("unknown workspace")
		}
		return SessionWorkspaceCanonicalization{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, WorkspaceState: "active", WorkspaceName: entry.Name, SourceWorkspacePath: entry.Path, RuntimeWorkspacePath: entry.Path, WorkspaceBindingID: "binding-" + entry.WorkspaceID, RuntimeSwarmID: "swarm", PlacementGeneration: 1, BindingGeneration: 1}, nil
	})
	output, err := runSvc.executeManageWorkspaceTool(sessionID, `{"action":"create","permission_scope":"workspace_create","intent":"Create this saved workspace.","workspace_path":"`+currentPath+`","workspace_name":"created"}`, principal, sessionSvc.ApplySessionMutation)
	if err != nil {
		t.Fatalf("create active workspace: %v", err)
	}
	createdScope, scopeErr := workspaceSvc.ScopeForPathForPrincipal(principal, currentPath)
	if scopeErr != nil || !createdScope.Matched || createdScope.WorkspaceID == "" {
		t.Fatalf("created workspace scope = %+v err=%v", createdScope, scopeErr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	safety, _ := payload["safety"].(map[string]any)
	if safety["switched_before_mutation"] != true || safety["restored_after_mutation"] != true {
		t.Fatalf("create safety = %#v", safety)
	}
	stored, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok || stored.WorkspacePath != currentPath || mapString(stored.Metadata, "swarm_v3_source_workspace_id") != createdScope.WorkspaceID {
		t.Fatalf("restored create session: %+v ok=%t err=%v", stored, ok, err)
	}
	if current, ok, err := workspaceSvc.CurrentBindingForPrincipal(principal); err != nil || !ok || current.WorkspaceID != safe.WorkspaceID {
		t.Fatalf("create unexpectedly changed account default: %+v ok=%t err=%v", current, ok, err)
	}
}

func TestManageWorkspaceRejectsRuntimeWorktreeCatalogIdentity(t *testing.T) {
	principal := testRunPrincipal()
	sourcePath := t.TempDir()
	runtimePath := t.TempDir()
	safePath := t.TempDir()
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	runtimeEntry, err := workspaceSvc.AddForPrincipal(principal, runtimePath, "runtime", "", false)
	if err != nil {
		t.Fatalf("add runtime identity: %v", err)
	}
	safe, err := workspaceSvc.AddForPrincipal(principal, safePath, "safe", "", false)
	if err != nil {
		t.Fatalf("add safe: %v", err)
	}
	sessionStore := pebblestore.NewSessionStore(rawStore)
	sessionID := "runtime-worktree-identity"
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID: sessionID, WorkspacePath: sourcePath, WorkspaceName: "source", WorktreeEnabled: true, WorktreeRootPath: runtimePath, WorktreeBranch: "agent/runtime-identity",
		Metadata: map[string]any{"swarm_v3_source_workspace_path": sourcePath},
	}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessionStore, nil)
	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, nil, nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	runSvc.SetSessionWorkspaceCanonicalizer(testManageWorkspaceCanonicalizer(runtimeEntry, safe))
	_, err = runSvc.executeManageWorkspaceTool(sessionID, `{"action":"delete","permission_scope":"workspace_delete","intent":"Unlink this saved workspace without deleting files.","workspace_id":"`+runtimeEntry.WorkspaceID+`","workspace_generation":1}`, principal, sessionSvc.ApplySessionMutation)
	if err == nil || !strings.Contains(err.Error(), "runtime checkout of a worktree-backed session") {
		t.Fatalf("runtime identity mutation error = %v", err)
	}
	if _, metadataErr := runSvc.buildManageWorkspacePermissionPayload(sessionID, `{"action":"delete","workspace_id":"`+runtimeEntry.WorkspaceID+`","workspace_generation":1}`); metadataErr == nil || !strings.Contains(metadataErr.Error(), "runtime checkout of a worktree-backed session") {
		t.Fatalf("permission metadata runtime identity error = %v", metadataErr)
	}
	if _, ok, lookupErr := workspaceSvc.GetByWorkspaceIDForPrincipal(principal, runtimeEntry.WorkspaceID); lookupErr != nil || !ok {
		t.Fatalf("runtime identity changed after rejection: ok=%t err=%v", ok, lookupErr)
	}
}

func TestManageWorkspaceActiveUpdateRestoresAfterMutationFailure(t *testing.T) {
	principal := testRunPrincipal()
	currentPath := t.TempDir()
	safePath := t.TempDir()
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	current, err := workspaceSvc.AddForPrincipal(principal, currentPath, "current", "", true)
	if err != nil {
		t.Fatalf("add current: %v", err)
	}
	safe, err := workspaceSvc.AddForPrincipal(principal, safePath, "safe", "", false)
	if err != nil {
		t.Fatalf("add safe: %v", err)
	}
	available := true
	sessionStore := pebblestore.NewSessionStore(rawStore)
	sessionID := "failed-update-restore"
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID: sessionID, WorkspacePath: currentPath, WorkspaceName: "current",
		Metadata: map[string]any{"swarm_v3_source_workspace_id": current.WorkspaceID, "swarm_v3_source_workspace_generation": "1"},
		WorkspaceGrants: []pebblestore.WorkspaceGrant{
			{Kind: pebblestore.WorkspaceGrantPrimary, WorkspaceID: current.WorkspaceID, WorkspaceGeneration: 1, Path: currentPath, Name: "current", Available: &available},
			{Kind: pebblestore.WorkspaceGrantAdditional, WorkspaceID: safe.WorkspaceID, WorkspaceGeneration: 1, Path: safePath, Name: "safe", Available: &available},
		},
	}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessionStore, nil)
	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, nil, nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	runSvc.SetSessionWorkspaceCanonicalizer(testManageWorkspaceCanonicalizer(current, safe))
	missingPath := filepath.Join(t.TempDir(), "missing")
	_, err = runSvc.executeManageWorkspaceTool(sessionID, `{"action":"update","permission_scope":"workspace_update","intent":"Update the selected workspace.","workspace_id":"`+current.WorkspaceID+`","workspace_generation":1,"workspace_path":"`+missingPath+`"}`, principal, sessionSvc.ApplySessionMutation)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("failed update error = %v", err)
	}
	stored, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok || stored.WorkspacePath != currentPath || mapString(stored.Metadata, "swarm_v3_source_workspace_id") != current.WorkspaceID {
		t.Fatalf("session was not restored after failed mutation: %+v ok=%t err=%v", stored, ok, err)
	}
}

func TestManageWorkspaceActiveMutationFailsClosedWithoutSafeWorkspace(t *testing.T) {
	principal := testRunPrincipal()
	currentPath := t.TempDir()
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	current, err := workspaceSvc.AddForPrincipal(principal, currentPath, "current", "", true)
	if err != nil {
		t.Fatalf("add current: %v", err)
	}
	sessionStore := pebblestore.NewSessionStore(rawStore)
	sessionID := "no-safe-workspace"
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID: sessionID, WorkspacePath: currentPath, WorkspaceName: "current",
		Metadata: map[string]any{"swarm_v3_source_workspace_id": current.WorkspaceID, "swarm_v3_source_workspace_generation": "1"},
	}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessionStore, nil)
	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, nil, nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	runSvc.SetSessionWorkspaceCanonicalizer(testManageWorkspaceCanonicalizer(current))
	_, err = runSvc.executeManageWorkspaceTool(sessionID, `{"action":"update","permission_scope":"workspace_update","intent":"Update the selected workspace.","workspace_id":"`+current.WorkspaceID+`","workspace_generation":1,"workspace_name":"unsafe"}`, principal, sessionSvc.ApplySessionMutation)
	if err == nil || !strings.Contains(err.Error(), "no different authorized safe workspace") {
		t.Fatalf("active mutation error = %v", err)
	}
	entry, ok, err := workspaceSvc.GetByWorkspaceIDForPrincipal(principal, current.WorkspaceID)
	if err != nil || !ok || entry.Name != "current" {
		t.Fatalf("catalog changed after fail-closed rejection: %+v ok=%t err=%v", entry, ok, err)
	}
	stored, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok || stored.WorkspacePath != currentPath {
		t.Fatalf("session changed after fail-closed rejection: %+v ok=%t err=%v", stored, ok, err)
	}
}

func TestManageWorkspaceActiveDeleteRemainsSafeAndLeavesFiles(t *testing.T) {
	principal := testRunPrincipal()
	currentPath := t.TempDir()
	safePath := t.TempDir()
	marker := filepath.Join(currentPath, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	current, err := workspaceSvc.AddForPrincipal(principal, currentPath, "current", "", true)
	if err != nil {
		t.Fatalf("add current: %v", err)
	}
	safe, err := workspaceSvc.AddForPrincipal(principal, safePath, "safe", "", false)
	if err != nil {
		t.Fatalf("add safe: %v", err)
	}
	available := true
	sessionStore := pebblestore.NewSessionStore(rawStore)
	sessionID := "safe-delete"
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID: sessionID, WorkspacePath: currentPath, WorkspaceName: "current",
		Metadata: map[string]any{"swarm_v3_source_workspace_id": current.WorkspaceID, "swarm_v3_source_workspace_generation": "1"},
		WorkspaceGrants: []pebblestore.WorkspaceGrant{
			{Kind: pebblestore.WorkspaceGrantPrimary, WorkspaceID: current.WorkspaceID, WorkspaceGeneration: 1, Path: currentPath, Name: "current", Available: &available},
			{Kind: pebblestore.WorkspaceGrantAdditional, WorkspaceID: safe.WorkspaceID, WorkspaceGeneration: 1, Path: safePath, Name: "safe", Available: &available},
		},
	}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessionStore, nil)
	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, nil, nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	runSvc.SetSessionWorkspaceCanonicalizer(testManageWorkspaceCanonicalizer(current, safe))
	output, err := runSvc.executeManageWorkspaceTool(sessionID, `{"action":"delete","permission_scope":"workspace_delete","intent":"Unlink this saved workspace without deleting files.","workspace_id":"`+current.WorkspaceID+`","workspace_generation":1}`, principal, sessionSvc.ApplySessionMutation)
	if err != nil {
		t.Fatalf("delete active workspace: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("catalog delete changed filesystem: %v", err)
	}
	if _, ok, err := workspaceSvc.GetByWorkspaceIDForPrincipal(principal, current.WorkspaceID); err != nil || ok {
		t.Fatalf("deleted workspace remains: ok=%t err=%v", ok, err)
	}
	if _, ok, err := workspaceSvc.CurrentBindingForPrincipal(principal); err != nil || ok {
		t.Fatalf("deleted account default remains: ok=%t err=%v", ok, err)
	}
	stored, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("load session: ok=%t err=%v", ok, err)
	}
	for _, grant := range stored.WorkspaceGrants {
		if grant.WorkspaceID == current.WorkspaceID {
			t.Fatalf("deleted workspace grant remains: %+v", stored.WorkspaceGrants)
		}
	}
	if stored.WorkspacePath != safePath || strings.TrimSpace(mapString(stored.Metadata, "swarm_v3_source_workspace_id")) != safe.WorkspaceID {
		t.Fatalf("session did not remain safe: %+v", stored)
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(output), &payload)
	safety, _ := payload["safety"].(map[string]any)
	if safety["left_in_safe_workspace"] != true || safety["delete_restore_valid"] != false {
		t.Fatalf("delete safety = %#v", safety)
	}
}

// Requirement: an automatically approved Workspace Map mutation must carry the
// backend-built canonical payload into control-plane execution. The regression
// threat is an approved call with empty arguments that fails after authorization;
// gateToolCalls is the narrowest layer that proves the handoff contract.
func TestManageWorkspaceAutomaticPolicyApprovalReturnsCanonicalMapArguments(t *testing.T) {
	svc, sessionID, permissions, cleanup := newTaskLaunchPermissionServiceWithPermissions(t)
	defer cleanup()
	mapStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace-map.pebble"))
	if err != nil {
		t.Fatalf("open workspace map store: %v", err)
	}
	defer mapStore.Close()
	svc.SetWorkspaceMapService(pebblestore.NewWorkspaceMapService(pebblestore.NewWorkspaceMapStore(mapStore)))
	if _, err := permissions.UpsertRuleForAccount("test-account", permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "workspace_map_update"}); err != nil {
		t.Fatalf("persist workspace map mutation rule: %v", err)
	}
	content := "# Workspace Map\n\n- billing means the payments workspace.\n"
	arguments := mustJSON(t, map[string]any{
		"action":            "update_map",
		"expected_revision": 1,
		"content":           content,
		"intent":            "Record the user's billing workspace keyword.",
	})
	_, approved, _, mask, feedback, err := svc.gateToolCalls(context.Background(), sessionID, "run-map-update", 1, sessionruntime.ModeAuto, []tool.Call{{CallID: "call-map-update", Name: "manage_workspace", Arguments: arguments}}, nil, nil)
	if err != nil {
		t.Fatalf("gate workspace map update: %v", err)
	}
	if len(approved) != 1 || len(mask) != 1 || !mask[0] || len(feedback) != 1 {
		t.Fatalf("automatic workspace map approval = approved=%#v mask=%v feedback=%#v", approved, mask, feedback)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(feedback[0].ApprovedArguments), &payload); err != nil {
		t.Fatalf("decode approved workspace map payload: %v", err)
	}
	canonical, ok := payload["approved_arguments"].(map[string]any)
	if !ok {
		t.Fatalf("approved workspace map payload = %#v, want wrapped canonical arguments", payload)
	}
	if mapString(canonical, "action") != "update_map" || mapString(canonical, "permission_scope") != "workspace_map_update" || manageWorkspaceInt64(canonical["expected_revision"]) != 1 || mapString(canonical, "content") != content {
		t.Fatalf("canonical workspace map arguments = %#v", canonical)
	}
}

func TestManageWorkspaceMapInspectUpdateRefreshAndStaleRejection(t *testing.T) {
	principal := testRunPrincipal()
	workspacePath := t.TempDir()
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	if _, err := workspaceSvc.AddForPrincipal(principal, workspacePath, "repo", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	sessionStore := pebblestore.NewSessionStore(rawStore)
	sessionID := "workspace-map-session"
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: sessionID, WorkspacePath: workspacePath, WorkspaceName: "repo", Metadata: map[string]any{}}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	runSvc := NewService(sessionruntime.NewService(sessionStore, nil), nil, nil, nil, nil, nil, nil, nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	runSvc.SetWorkspaceMapService(pebblestore.NewWorkspaceMapService(pebblestore.NewWorkspaceMapStore(rawStore)))

	if _, err := parseManageWorkspaceArguments(`{"action":"inspect_map"} {}`); err == nil {
		t.Fatal("parser accepted trailing JSON")
	}
	if _, err := parseManageWorkspaceArguments(`null`); err == nil {
		t.Fatal("parser accepted null arguments")
	}
	if _, err := parseManageWorkspaceArguments(`{"action":7}`); err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("non-string action error = %v", err)
	}
	if _, err := runSvc.executeManageWorkspaceTool("", `{"action":"inspect_map"}`, principal, nil); err == nil || !strings.Contains(err.Error(), "active session") {
		t.Fatalf("sessionless inspect error = %v", err)
	}
	inspect, err := runSvc.executeManageWorkspaceTool(sessionID, `{"action":"inspect_map"}`, principal, nil)
	if err != nil {
		t.Fatalf("inspect map: %v", err)
	}
	if !strings.Contains(inspect, `"revision":1`) || !strings.Contains(inspect, `"digest":"`) {
		t.Fatalf("inspect output = %s", inspect)
	}
	getOutput, err := runSvc.executeManageWorkspaceTool(sessionID, `{"action":"get_map"}`, principal, nil)
	if err != nil || !strings.Contains(getOutput, `"revision":1`) {
		t.Fatalf("get map: output=%s err=%v", getOutput, err)
	}
	initialPrompt := runSvc.composeInstructionsForScope(tool.WorkspaceScope{PrimaryPath: workspacePath, Roots: []string{workspacePath}, Principal: principal, SessionID: sessionID}, pebblestore.AgentProfile{Name: "swarm", Mode: "primary"}, "")
	if !strings.Contains(initialPrompt, "Account Workspace Map") || !strings.Contains(initialPrompt, "revision: 1") || !strings.Contains(initialPrompt, "lower authority than system/developer instructions and workspace AGENTS.md") {
		t.Fatalf("initial prompt omitted map contract: %s", initialPrompt)
	}
	mapIndex, rulesIndex := strings.Index(initialPrompt, "Account Workspace Map"), strings.Index(initialPrompt, "Loaded instruction sources:")
	if rulesIndex >= 0 && mapIndex > rulesIndex {
		t.Fatalf("Workspace Map must render before AGENTS.md sources")
	}
	nonPrimaryPrompt := runSvc.composeInstructionsForScope(tool.WorkspaceScope{PrimaryPath: workspacePath, Roots: []string{workspacePath}, Principal: principal, SessionID: sessionID}, pebblestore.AgentProfile{Name: "coder", Mode: "subagent"}, "")
	if strings.Contains(nonPrimaryPrompt, "Account Workspace Map") {
		t.Fatalf("Workspace Map leaked to non-primary agent")
	}

	content := "# Workspace Map\n\n- billing means the payments workspace.\n"
	callArgs := `{"action":"update_map","expected_revision":1,"content":` + strconv.Quote(content) + `,"intent":"Record the user's billing workspace keyword."}`
	if requirement, ask := permissionRequirement(sessionruntime.ModeAuto, "manage_workspace", callArgs); !ask || requirement != "workspace_map_update" {
		t.Fatalf("map update permission = %q/%t", requirement, ask)
	}
	if _, err := runSvc.executeManageWorkspaceTool(sessionID, callArgs, principal, nil); err == nil || !strings.Contains(err.Error(), "permission_scope") {
		t.Fatalf("model-authored map update bypassed approval: %v", err)
	}
	permissionPayload, err := runSvc.buildManageWorkspacePermissionPayload(sessionID, callArgs)
	if err != nil {
		t.Fatalf("permission payload: %v", err)
	}
	if target, ok := permissionPayload["target"].(map[string]any); !ok || target["account_scope_id"] != nil || target["account_scoped"] != true {
		t.Fatalf("permission payload exposed account identity or omitted scope: %#v", permissionPayload)
	}
	approved := permissionPayload["approved_arguments"].(map[string]any)
	rawApproved, _ := json.Marshal(approved)
	approvedPayloadRaw, _ := json.Marshal(permissionPayload)
	ctx := identity.ContextWithPrincipal(context.Background(), principal)
	if handled, _, err := runSvc.executeControlPlaneTool(ctx, sessionID, sessionruntime.ModeAuto, pebblestore.AgentProfile{Name: "coder", Mode: "subagent"}, 1, tool.Call{Name: "manage_workspace", Arguments: callArgs}, string(approvedPayloadRaw), nil); err == nil || !handled || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("non-primary control-plane access: handled=%t err=%v", handled, err)
	}
	wrongApprovedPayload := cloneGenericMap(permissionPayload)
	wrongApproved := cloneGenericMap(approved)
	wrongApproved["content"] = "# Workspace Map\n\n- wrong\n"
	wrongApprovedPayload["approved_arguments"] = wrongApproved
	wrongApprovedRaw, _ := json.Marshal(wrongApprovedPayload)
	if _, _, err := runSvc.executeControlPlaneTool(ctx, sessionID, sessionruntime.ModeAuto, pebblestore.AgentProfile{Name: "swarm", Mode: "primary"}, 1, tool.Call{Name: "manage_workspace", Arguments: callArgs}, string(wrongApprovedRaw), nil); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("mismatched approved map content error = %v", err)
	}
	handled, controlResult, err := runSvc.executeControlPlaneTool(ctx, sessionID, sessionruntime.ModeAuto, pebblestore.AgentProfile{Name: "swarm", Mode: "primary"}, 1, tool.Call{Name: "manage_workspace", Arguments: callArgs}, string(approvedPayloadRaw), nil)
	if err != nil || !handled || !strings.Contains(controlResult.Output, `"revision":2`) {
		t.Fatalf("control-plane update: handled=%t output=%s err=%v", handled, controlResult.Output, err)
	}
	updated := controlResult.Output
	if !strings.Contains(updated, `"revision":2`) || !strings.Contains(updated, `"mutation_evidence"`) || !strings.Contains(updated, `"durable":true`) {
		t.Fatalf("update output = %s", updated)
	}
	laterPrompt := runSvc.composeInstructionsForScope(tool.WorkspaceScope{PrimaryPath: workspacePath, Roots: []string{workspacePath}, Principal: principal, SessionID: sessionID}, pebblestore.AgentProfile{Name: "swarm", Mode: "primary"}, "")
	if !strings.Contains(laterPrompt, "revision: 2") || !strings.Contains(laterPrompt, "billing means") {
		t.Fatalf("later prompt did not refresh: %s", laterPrompt)
	}
	if _, err := runSvc.buildManageWorkspacePermissionPayload(sessionID, callArgs); !errors.Is(err, pebblestore.ErrWorkspaceMapRevisionConflict) {
		t.Fatalf("stale permission payload error = %v", err)
	}
	if _, err := runSvc.executeManageWorkspaceTool(sessionID, string(rawApproved), principal, nil); !errors.Is(err, pebblestore.ErrWorkspaceMapRevisionConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	freshPrincipal := principal
	freshPrincipal.SessionID = "fresh-checkpoint-run"
	freshPrompt := runSvc.composeInstructionsForScope(tool.WorkspaceScope{PrimaryPath: workspacePath, Roots: []string{workspacePath}, Principal: freshPrincipal, SessionID: "fresh-checkpoint-run"}, pebblestore.AgentProfile{Name: "swarm", Mode: "primary"}, "")
	if !strings.Contains(freshPrompt, "revision: 2") || !strings.Contains(freshPrompt, "billing means") {
		t.Fatalf("fresh provider request omitted current map: %s", freshPrompt)
	}
	runSvc.SetWorkspaceMapService(failingWorkspaceMapService{})
	availablePrompt := runSvc.composeInstructionsForScope(tool.WorkspaceScope{PrimaryPath: workspacePath, Roots: []string{workspacePath}, Principal: principal, SessionID: sessionID}, pebblestore.AgentProfile{Name: "swarm", Mode: "primary"}, "")
	if strings.Contains(availablePrompt, "Account Workspace Map") || !strings.Contains(availablePrompt, "Master harness prompt") {
		t.Fatalf("map failure blocked or polluted prompt composition: %s", availablePrompt)
	}
	runSvc.SetWorkspaceMapService(pebblestore.NewWorkspaceMapService(pebblestore.NewWorkspaceMapStore(rawStore)))
	other := principal
	other.AccountScopeID = "other-account"
	if _, err := runSvc.executeManageWorkspaceTool(sessionID, `{"action":"inspect_map"}`, other, nil); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("cross-account inspect error = %v", err)
	}
	if isolatedPrompt := runSvc.composeInstructionsForScope(tool.WorkspaceScope{PrimaryPath: workspacePath, Roots: []string{workspacePath}, Principal: other, SessionID: sessionID}, pebblestore.AgentProfile{Name: "swarm", Mode: "primary"}, ""); strings.Contains(isolatedPrompt, "billing means") {
		t.Fatalf("account-a map leaked to account-b prompt: %s", isolatedPrompt)
	}
}

func testManageWorkspaceCanonicalizer(entries ...workspaceruntime.Resolution) SessionWorkspaceCanonicalizer {
	return func(input SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
		for _, entry := range entries {
			if input.WorkspaceID == entry.WorkspaceID {
				return SessionWorkspaceCanonicalization{
					WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, WorkspaceState: "active",
					WorkspaceName: entry.WorkspaceName, SourceWorkspacePath: entry.WorkspacePath, RuntimeWorkspacePath: entry.WorkspacePath,
					WorkspaceBindingID: "binding-" + entry.WorkspaceID, RuntimeSwarmID: "swarm", PlacementGeneration: 1, BindingGeneration: 1,
				}, nil
			}
		}
		return SessionWorkspaceCanonicalization{}, errors.New("unknown workspace")
	}
}

func TestManageWorkspaceAdoptWorktreeRollsBackWhenCurrentWorktreeIsDirty(t *testing.T) {
	principal := testRunPrincipal()
	workspacePath := t.TempDir()
	currentPath := t.TempDir()
	allocatedPath := t.TempDir()
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	entry, err := workspaceSvc.AddForPrincipal(principal, workspacePath, "repo", "", true)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	sessionStore := pebblestore.NewSessionStore(rawStore)
	sessionID := "dirty-session"
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID: sessionID, WorkspacePath: workspacePath, WorkspaceName: "repo", Title: "dirty", WorktreeEnabled: true,
		WorktreeRootPath: currentPath, WorktreeBranch: "agent/current", Metadata: map[string]any{"swarm_v3_source_workspace_id": entry.WorkspaceID},
	}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessionStore, nil)
	worktrees := &sameSessionWorktreeStub{
		allocation: worktreeruntime.Allocation{WorkspacePath: allocatedPath, BaseBranch: "dev", BaseCommit: strings.Repeat("b", 40), BranchName: "agent/next"},
		states:     map[string]worktreeruntime.TaskWorkspaceState{currentPath: {WorkspacePath: currentPath, BranchName: "agent/current", Clean: false, Status: " M file"}},
	}
	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, nil, nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	runSvc.SetWorktreeService(worktrees)
	runSvc.SetSessionWorkspaceCanonicalizer(func(SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
		return SessionWorkspaceCanonicalization{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, WorkspaceState: "active", WorkspaceName: "repo", SourceWorkspacePath: workspacePath, RuntimeWorkspacePath: workspacePath, WorkspaceBindingID: "binding", RuntimeSwarmID: "swarm", PlacementGeneration: 1, BindingGeneration: 1}, nil
	})
	_, err = runSvc.executeManageWorkspaceTool(sessionID, `{"action":"adopt_worktree","workspace_id":"`+entry.WorkspaceID+`","worktree_name":"next"}`, principal, sessionSvc.ApplySessionMutation)
	if err == nil || !strings.Contains(err.Error(), "dirty worktree") {
		t.Fatalf("adopt error = %v", err)
	}
	if len(worktrees.rolledBack) != 1 || worktrees.rolledBack[0] != allocatedPath {
		t.Fatalf("rolled back = %v", worktrees.rolledBack)
	}
	stored, _, _ := sessionSvc.GetSession(sessionID)
	if stored.WorktreeRootPath != currentPath {
		t.Fatalf("session mutated after rejection: %+v", stored)
	}
}
