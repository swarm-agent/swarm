package tool

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestManageWorktreeDefinitionKeepsIntegrateInputMinimal(t *testing.T) {
	var definition Definition
	for _, candidate := range NewRuntime(1).Definitions() {
		if candidate.Name == "manage-worktree" {
			definition = candidate
			break
		}
	}
	if definition.Name == "" {
		t.Fatal("manage-worktree definition not found")
	}
	properties, _ := definition.Parameters["properties"].(map[string]any)
	for _, forbidden := range []string{"integration_plan", "expected_parent_head", "preview"} {
		if _, ok := properties[forbidden]; ok {
			t.Fatalf("manage-worktree exposes model-authored %s", forbidden)
		}
	}
	for _, required := range []string{"action", "session_ids"} {
		if _, ok := properties[required]; !ok {
			t.Fatalf("manage-worktree missing %s", required)
		}
	}
}

type coderLineageSessionService struct {
	manageSessionService
	parent pebblestore.SessionSnapshot
}

func (s *coderLineageSessionService) GetSession(id string) (pebblestore.SessionSnapshot, bool, error) {
	if id != s.parent.ID {
		return pebblestore.SessionSnapshot{}, false, nil
	}
	return s.parent, true, nil
}

type coderLineageWorktreeService struct {
	manageWorktreeConfigService
	states           map[string]worktreeruntime.TaskWorkspaceState
	preparedChildren []worktreeruntime.TaskIntegrationChild
	prepareErr       error
	applyResult      worktreeruntime.TaskIntegrationResult
	applyCalls       int
}

func (s *coderLineageWorktreeService) InspectTaskWorkspace(path string) (worktreeruntime.TaskWorkspaceState, error) {
	state, ok := s.states[path]
	if !ok {
		return worktreeruntime.TaskWorkspaceState{}, errors.New("unknown test worktree")
	}
	return state, nil
}

func (s *coderLineageWorktreeService) TaskCommitDescendsFrom(_, _, _ string) (bool, error) {
	return false, nil
}

func (s *coderLineageWorktreeService) PrepareTaskIntegration(_ string, expectedParentHead string, children []worktreeruntime.TaskIntegrationChild) (worktreeruntime.TaskIntegrationPlan, error) {
	s.preparedChildren = append([]worktreeruntime.TaskIntegrationChild(nil), children...)
	if s.prepareErr != nil {
		return worktreeruntime.TaskIntegrationPlan{}, s.prepareErr
	}
	entries := make([]worktreeruntime.TaskIntegrationEntry, 0, len(children))
	commits := make([]string, 0, len(children))
	for _, child := range children {
		entries = append(entries, worktreeruntime.TaskIntegrationEntry{SessionID: child.SessionID, BaseCommit: child.BaseCommit, HeadCommit: child.HeadCommit, Commits: []string{child.HeadCommit}})
		commits = append(commits, child.HeadCommit)
	}
	return worktreeruntime.TaskIntegrationPlan{ParentHead: expectedParentHead, Entries: entries, Commits: commits}, nil
}

func (s *coderLineageWorktreeService) ApplyTaskIntegration(_ string, plan worktreeruntime.TaskIntegrationPlan) (worktreeruntime.TaskIntegrationResult, error) {
	s.applyCalls++
	if s.applyResult.ResultingParentHead == "" {
		s.applyResult = worktreeruntime.TaskIntegrationResult{TaskIntegrationPlan: plan, ResultingParentHead: "integrated-parent-head"}
	}
	return s.applyResult, nil
}

func TestManageWorktreeRecallFindsParallelCoderLineage(t *testing.T) {
	runtime, scope, _, childIDs := newCoderLineageRuntime(t)
	output, err := runtime.manageWorktreeRecall(scope, map[string]any{"limit": 25})
	if err != nil {
		t.Fatalf("recall Coder lineage: %v", err)
	}
	var response struct {
		Children []struct {
			SessionID string `json:"child_session_id"`
			State     string `json:"child_state"`
		} `json:"children"`
		Integration struct {
			ReadySessionIDs []string `json:"ready_session_ids"`
		} `json:"integration"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode recall response: %v", err)
	}
	if len(response.Children) != 2 || response.Children[0].SessionID != childIDs[0] || response.Children[1].SessionID != childIDs[1] {
		t.Fatalf("recalled children = %#v, want %v", response.Children, childIDs)
	}
	for _, child := range response.Children {
		if child.State != "committed" {
			t.Fatalf("child %s state = %q, want committed", child.SessionID, child.State)
		}
	}
	if len(response.Integration.ReadySessionIDs) != 2 || response.Integration.ReadySessionIDs[0] != childIDs[0] || response.Integration.ReadySessionIDs[1] != childIDs[1] {
		t.Fatalf("ready session ids = %v, want %v", response.Integration.ReadySessionIDs, childIDs)
	}
}

func TestManageWorktreeIntegrateAppliesSelectedCoderBatchInDurableOrder(t *testing.T) {
	runtime, scope, worktrees, childIDs := newCoderLineageRuntime(t)
	output, err := runtime.manageWorktreeIntegrate(scope, map[string]any{"session_ids": []any{childIDs[1], childIDs[0]}})
	if err != nil {
		t.Fatalf("integrate Coder lineage: %v\n%s", err, output)
	}
	if len(worktrees.preparedChildren) != 2 || worktrees.preparedChildren[0].SessionID != childIDs[0] || worktrees.preparedChildren[1].SessionID != childIDs[1] {
		t.Fatalf("prepared children = %#v, want durable task-call order %v", worktrees.preparedChildren, childIDs)
	}
	var response struct {
		Status              string            `json:"status"`
		ChildStates         map[string]string `json:"child_states"`
		ResultingParentHead string            `json:"resulting_parent_head"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode integrate response: %v", err)
	}
	if response.Status != "ok" || response.ResultingParentHead != "integrated-parent-head" {
		t.Fatalf("integration response = %s", output)
	}
	for _, id := range childIDs {
		if response.ChildStates[id] != "integrated" {
			t.Fatalf("child %s state = %q, want integrated", id, response.ChildStates[id])
		}
	}
}

func TestManageWorktreeIntegrateReturnsActionableConflictWithoutApply(t *testing.T) {
	runtime, scope, worktrees, childIDs := newCoderLineageRuntime(t)
	worktrees.prepareErr = &worktreeruntime.TaskIntegrationConflictError{Commit: "child-one-head", Detail: "content conflict in AGENTS.md"}
	output, err := runtime.manageWorktreeIntegrate(scope, map[string]any{"session_ids": []any{childIDs[0], childIDs[1]}})
	if err == nil {
		t.Fatal("expected integration conflict")
	}
	var response struct {
		Status            string `json:"status"`
		ParentUnchanged   bool   `json:"parent_unchanged"`
		ConflictingCommit string `json:"conflicting_commit"`
		NextAction        string `json:"next_action"`
	}
	if decodeErr := json.Unmarshal([]byte(output), &response); decodeErr != nil {
		t.Fatalf("decode conflict response: %v; output=%s", decodeErr, output)
	}
	if response.Status != "conflict" || !response.ParentUnchanged || response.ConflictingCommit != "child-one-head" || !strings.Contains(response.NextAction, "Resolve") {
		t.Fatalf("conflict response = %s", output)
	}
	if worktrees.applyCalls != 0 {
		t.Fatalf("integration apply unexpectedly ran %d times", worktrees.applyCalls)
	}
}

func newCoderLineageRuntime(t *testing.T) (*Runtime, WorkspaceScope, *coderLineageWorktreeService, []string) {
	t.Helper()
	const parentID = "parent-session"
	const parentPath = "/repo"
	childIDs := []string{"child-one", "child-two"}
	launches := map[string]any{}
	states := map[string]worktreeruntime.TaskWorkspaceState{
		parentPath: {WorkspacePath: parentPath, BranchName: "dev", HeadCommit: "parent-head", Clean: true},
	}
	for index, id := range childIDs {
		path := "/worktrees/" + id
		branch := "agent/" + id
		head := id + "-head"
		callID := "call-b"
		if index == 0 {
			callID = "call-a"
		}
		launches[callID] = map[string]any{"launches": []any{map[string]any{
			"launch_index": index + 1, "child_session_id": id, "subagent": "system-coder",
			"worktree_root_path": path, "worktree_branch": branch,
			"base_commit": "parent-head", "head_commit": head,
		}}}
		states[path] = worktreeruntime.TaskWorkspaceState{WorkspacePath: path, BranchName: branch, HeadCommit: head, Clean: true}
	}
	parent := pebblestore.SessionSnapshot{ID: parentID, AccountScopeID: "account", UserID: "user", WorkspacePath: parentPath, Metadata: map[string]any{"task_launches": launches}}
	worktrees := &coderLineageWorktreeService{states: states}
	runtime := &Runtime{sessions: &coderLineageSessionService{parent: parent}, worktrees: worktrees}
	scope := WorkspaceScope{PrimaryPath: parentPath, Roots: []string{parentPath}, SessionID: parentID, Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account", SessionID: parentID}}
	return runtime, scope, worktrees, childIDs
}
