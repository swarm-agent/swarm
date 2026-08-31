package tool

import (
	"encoding/json"
	"errors"
	"fmt"
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
	for _, required := range []string{"action", "session_ids", "task_call_id", "source_session_id", "source_branch", "source_head", "target_workspace_path", "target_branch", "target_head"} {
		if _, ok := properties[required]; !ok {
			t.Fatalf("manage-worktree missing %s", required)
		}
	}
}

type coderLineageSessionService struct {
	manageSessionService
	parent   pebblestore.SessionSnapshot
	children map[string]pebblestore.SessionSnapshot
}

func (s *coderLineageSessionService) GetSession(id string) (pebblestore.SessionSnapshot, bool, error) {
	if id == s.parent.ID {
		return s.parent, true, nil
	}
	child, ok := s.children[id]
	return child, ok, nil
}

type coderLineageWorktreeService struct {
	manageWorktreeConfigService
	states           map[string]worktreeruntime.TaskWorkspaceState
	preparedChildren []worktreeruntime.TaskIntegrationChild
	prepareErr       error
	applyResult      worktreeruntime.TaskIntegrationResult
	applyCalls       int
	integrated       map[string]bool
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

func (s *coderLineageWorktreeService) TaskCommitRangeIntegratedInto(_, _, headCommit, _ string) (bool, error) {
	return s.integrated[headCommit], nil
}

func (s *coderLineageWorktreeService) VerifyTaskIntegrationWorkspace(_, childPath, _, branchName, _, headCommit string) (worktreeruntime.TaskWorkspaceState, error) {
	state, ok := s.states[childPath]
	if !ok || state.BranchName != branchName || state.HeadCommit != headCommit {
		return worktreeruntime.TaskWorkspaceState{}, errors.New("invalid test lineage")
	}
	return state, nil
}

func (s *coderLineageWorktreeService) PrepareTaskIntegration(_ string, expectedParentBranch, expectedParentHead string, children []worktreeruntime.TaskIntegrationChild) (worktreeruntime.TaskIntegrationPlan, error) {
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
	return worktreeruntime.TaskIntegrationPlan{ParentBranch: expectedParentBranch, ParentHead: expectedParentHead, Entries: entries, Commits: commits}, nil
}

func (s *coderLineageWorktreeService) ApplyTaskIntegration(_ string, plan worktreeruntime.TaskIntegrationPlan) (worktreeruntime.TaskIntegrationResult, error) {
	s.applyCalls++
	if s.applyResult.ResultingParentHead == "" {
		s.applyResult = worktreeruntime.TaskIntegrationResult{TaskIntegrationPlan: plan, ResultingParentHead: "integrated-parent-head"}
	}
	if s.integrated == nil {
		s.integrated = map[string]bool{}
	}
	for _, entry := range s.applyResult.Entries {
		s.integrated[entry.HeadCommit] = true
	}
	parent := s.states["/repo"]
	parent.HeadCommit = s.applyResult.ResultingParentHead
	s.states["/repo"] = parent
	return s.applyResult, nil
}

func TestManageWorktreeIntegrateRejectsCapturedCheckoutParent(t *testing.T) {
	runtime, scope, worktrees, childIDs := newCoderLineageRuntime(t)
	sessions := runtime.sessions.(*coderLineageSessionService)
	sessions.parent.WorktreeEnabled = false
	sessions.parent.WorktreeRootPath = ""
	sessions.parent.WorktreeBranch = ""
	_, err := runtime.manageWorktreeIntegrate(scope, map[string]any{"session_ids": []any{childIDs[0]}})
	if err == nil || !strings.Contains(err.Error(), "session-owned parent lane") {
		t.Fatalf("captured parent integration error = %v", err)
	}
	if worktrees.applyCalls != 0 {
		t.Fatalf("captured parent was advanced %d times", worktrees.applyCalls)
	}
}

func TestManageWorktreePromoteRequiresExactLineage(t *testing.T) {
	runtime, scope, worktrees, _ := newCoderLineageRuntime(t)
	scope.Roots = append(scope.Roots, "/captured")
	sessions := runtime.sessions.(*coderLineageSessionService)
	source := sessions.parent
	source.Metadata["swarm_v3_source_workspace_path"] = "/captured"
	source.Metadata["base_commit"] = "captured-head"
	sessions.parent = source
	worktrees.states["/captured"] = worktreeruntime.TaskWorkspaceState{WorkspacePath: "/captured", BranchName: "dev", HeadCommit: "captured-head", Clean: true}
	_, err := runtime.manageWorktreePromote(scope, map[string]any{
		"source_session_id": source.ID, "source_branch": source.WorktreeBranch, "source_head": "stale-source-head",
		"target_workspace_path": "/captured", "target_branch": "dev", "target_head": "captured-head",
	})
	if err == nil || !strings.Contains(err.Error(), `expected branch "agent/parent-session" at full HEAD "stale-source-head", found branch "agent/parent-session" at full HEAD "parent-head"`) || !strings.Contains(err.Error(), "manage-sessions git_status head_oid") {
		t.Fatalf("stale promotion error = %v", err)
	}
	if worktrees.applyCalls != 0 {
		t.Fatalf("stale promotion applied %d times", worktrees.applyCalls)
	}
}

// TestManageWorktreePromoteReportsDirtyTargetBeforeApply proves promotion keeps the exact target clean-state guard while telling the model what state blocked delivery. The regression threat is an opaque branch/HEAD error that sends the model into repeated stale-lineage retries or unsafe app restarts; the tool runtime is the narrowest layer that owns this no-apply preflight.
func TestManageWorktreePromoteReportsDirtyTargetBeforeApply(t *testing.T) {
	runtime, scope, worktrees, _ := newCoderLineageRuntime(t)
	scope.Roots = append(scope.Roots, "/captured")
	sessions := runtime.sessions.(*coderLineageSessionService)
	source := sessions.parent
	source.Metadata["swarm_v3_source_workspace_path"] = "/captured"
	source.Metadata["base_commit"] = "captured-head"
	sessions.parent = source
	worktrees.states["/captured"] = worktreeruntime.TaskWorkspaceState{WorkspacePath: "/captured", BranchName: "dev", HeadCommit: "captured-head", Status: " M docs/swarm-atlas.md", Clean: false}

	_, err := runtime.manageWorktreePromote(scope, map[string]any{
		"source_session_id": source.ID, "source_branch": source.WorktreeBranch, "source_head": "parent-head",
		"target_workspace_path": "/captured", "target_branch": "dev", "target_head": "captured-head",
	})
	if err == nil || !strings.Contains(err.Error(), `target checkout is dirty at branch "dev" full HEAD "captured-head"`) || !strings.Contains(err.Error(), "preserve or finish those changes before promotion") {
		t.Fatalf("dirty target promotion error = %v", err)
	}
	if worktrees.applyCalls != 0 {
		t.Fatalf("dirty target promotion applied %d times", worktrees.applyCalls)
	}
}

func TestManageWorktreePromoteReportsCurrentTargetLineageBeforeApply(t *testing.T) {
	runtime, scope, worktrees, _ := newCoderLineageRuntime(t)
	scope.Roots = append(scope.Roots, "/captured")
	sessions := runtime.sessions.(*coderLineageSessionService)
	source := sessions.parent
	source.Metadata["swarm_v3_source_workspace_path"] = "/captured"
	source.Metadata["base_commit"] = "captured-base"
	sessions.parent = source
	worktrees.states["/captured"] = worktreeruntime.TaskWorkspaceState{WorkspacePath: "/captured", BranchName: "dev", HeadCommit: "current-dev-head", Clean: true}

	_, err := runtime.manageWorktreePromote(scope, map[string]any{
		"source_session_id": source.ID, "source_branch": source.WorktreeBranch, "source_head": "parent-head",
		"target_workspace_path": "/captured", "target_branch": "dev", "target_head": "stale-dev-head",
	})
	if err == nil || !strings.Contains(err.Error(), `expected branch "dev" at full HEAD "stale-dev-head", found branch "dev" at full HEAD "current-dev-head"`) || !strings.Contains(err.Error(), "refresh both values") {
		t.Fatalf("stale target promotion error = %v", err)
	}
	if worktrees.applyCalls != 0 {
		t.Fatalf("stale target promotion applied %d times", worktrees.applyCalls)
	}
}

// TestManageWorktreePromoteAllowsExactCapturedAccountWorkspaceOutsideActiveLane proves the promotion authority can reach only the source session's exact captured account-owned checkout, even when the active mutation lane is the owned worktree. This prevents the active-lane containment rule from making guarded delivery impossible while retaining principal-backed target authentication; the tool runtime is the narrowest layer that owns this resolver and no-apply boundary.
func TestManageWorktreePromoteAllowsExactCapturedAccountWorkspaceOutsideActiveLane(t *testing.T) {
	runtime, scope, worktrees, _ := newCoderLineageRuntime(t)
	sessions := runtime.sessions.(*coderLineageSessionService)
	source := sessions.parent
	source.Metadata["swarm_v3_source_workspace_path"] = "/captured"
	source.Metadata["base_commit"] = "captured-base"
	sessions.parent = source
	worktrees.states["/captured"] = worktreeruntime.TaskWorkspaceState{WorkspacePath: "/captured", BranchName: "dev", HeadCommit: "current-dev-head", Clean: true}
	runtime.workspace = &gitManageWorkspaceService{owned: map[string]bool{"/captured": true}}

	output, err := runtime.manageWorktreePromote(scope, map[string]any{
		"source_session_id": source.ID, "source_branch": source.WorktreeBranch, "source_head": "parent-head",
		"target_workspace_path": "/captured", "target_branch": "dev", "target_head": "current-dev-head",
	})
	if err != nil {
		t.Fatalf("promote exact captured account workspace: %v", err)
	}
	if worktrees.applyCalls != 1 {
		t.Fatalf("promotion applied %d times, want 1", worktrees.applyCalls)
	}
	if len(worktrees.preparedChildren) != 1 || worktrees.preparedChildren[0].BaseCommit != "captured-base" {
		t.Fatalf("prepared children = %#v, want captured base lineage", worktrees.preparedChildren)
	}
	if !strings.Contains(output, `"target_workspace_path":"/captured"`) {
		t.Fatalf("promotion response = %s", output)
	}
}

// TestManageWorktreePromoteRejectsForeignTargetOutsideActiveLane proves a model-supplied path cannot use captured-lineage metadata to promote into a checkout the authenticated account does not own. The regression threat is cross-workspace mutation outside the active lane; the tool runtime is the narrowest layer that can assert rejection before integration apply.
func TestManageWorktreePromoteRejectsForeignTargetOutsideActiveLane(t *testing.T) {
	runtime, scope, worktrees, _ := newCoderLineageRuntime(t)
	sessions := runtime.sessions.(*coderLineageSessionService)
	source := sessions.parent
	source.Metadata["swarm_v3_source_workspace_path"] = "/captured"
	source.Metadata["base_commit"] = "captured-base"
	sessions.parent = source
	runtime.workspace = &gitManageWorkspaceService{owned: map[string]bool{"/captured": false}}

	_, err := runtime.manageWorktreePromote(scope, map[string]any{
		"source_session_id": source.ID, "source_branch": source.WorktreeBranch, "source_head": "parent-head",
		"target_workspace_path": "/captured", "target_branch": "dev", "target_head": "current-dev-head",
	})
	if err == nil || !strings.Contains(err.Error(), "not an account-owned workspace") {
		t.Fatalf("foreign promotion error = %v", err)
	}
	if worktrees.applyCalls != 0 {
		t.Fatalf("foreign promotion applied %d times", worktrees.applyCalls)
	}
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

func TestManageWorktreeRecallReportsIntegratedAfterBatchApply(t *testing.T) {
	runtime, scope, _, childIDs := newCoderWaveRuntime(t, 5)
	if _, err := runtime.manageWorktreeIntegrate(scope, map[string]any{"task_call_id": "call-wave"}); err != nil {
		t.Fatalf("integrate Coder wave: %v", err)
	}
	output, err := runtime.manageWorktreeRecall(scope, map[string]any{"task_call_id": "call-wave"})
	if err != nil {
		t.Fatalf("recall integrated Coder wave: %v", err)
	}
	var response struct {
		Children []struct {
			SessionID string `json:"child_session_id"`
			State     string `json:"child_state"`
		} `json:"children"`
		StateCounts map[string]int `json:"state_counts"`
		Integration struct {
			ReadyCount       int            `json:"ready_count"`
			IntegrateRequest map[string]any `json:"integrate_request"`
		} `json:"integration"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode integrated recall response: %v", err)
	}
	if response.StateCounts["integrated"] != len(childIDs) || response.StateCounts["committed"] != 0 {
		t.Fatalf("post-integration state counts = %#v, want integrated:%d", response.StateCounts, len(childIDs))
	}
	for _, child := range response.Children {
		if child.State != "integrated" {
			t.Fatalf("child %s state = %q, want integrated", child.SessionID, child.State)
		}
	}
	if response.Integration.ReadyCount != 0 || response.Integration.IntegrateRequest != nil {
		t.Fatalf("integrated wave remains ready for integration: %#v", response.Integration)
	}
}

func TestManageWorktreeRecallScopesLargeWaveAndReturnsCompactIntegrateRequest(t *testing.T) {
	runtime, scope, _, childIDs := newCoderWaveRuntime(t, 100)
	output, err := runtime.manageWorktreeRecall(scope, map[string]any{"task_call_id": "call-wave", "limit": 25})
	if err != nil {
		t.Fatalf("recall large Coder wave: %v", err)
	}
	var response struct {
		TaskCallID  string         `json:"task_call_id"`
		Total       int            `json:"total"`
		Returned    int            `json:"returned"`
		HasMore     bool           `json:"has_more"`
		StateCounts map[string]int `json:"state_counts"`
		Integration struct {
			ReadyCount       int            `json:"ready_count"`
			ReadySessionIDs  []string       `json:"ready_session_ids"`
			IntegrateRequest map[string]any `json:"integrate_request"`
		} `json:"integration"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode large recall response: %v", err)
	}
	if response.TaskCallID != "call-wave" || response.Total != len(childIDs) || response.Returned != 25 || !response.HasMore {
		t.Fatalf("large recall response = %s", output)
	}
	if response.StateCounts["committed"] != len(childIDs) || response.Integration.ReadyCount != len(childIDs) {
		t.Fatalf("large recall counts = %#v", response)
	}
	if len(response.Integration.ReadySessionIDs) != 0 || response.Integration.IntegrateRequest["task_call_id"] != "call-wave" {
		t.Fatalf("large recall did not return compact task-call integration request: %s", output)
	}
}

func TestManageWorktreeIntegrateSelectsCompleteLargeWaveByTaskCall(t *testing.T) {
	runtime, scope, worktrees, childIDs := newCoderWaveRuntime(t, 100)
	output, err := runtime.manageWorktreeIntegrate(scope, map[string]any{"task_call_id": "call-wave"})
	if err != nil {
		t.Fatalf("integrate large Coder wave: %v\n%s", err, output)
	}
	if len(worktrees.preparedChildren) != len(childIDs) {
		t.Fatalf("prepared %d children, want %d", len(worktrees.preparedChildren), len(childIDs))
	}
	for index, child := range worktrees.preparedChildren {
		if child.SessionID != childIDs[index] {
			t.Fatalf("prepared child %d = %q, want %q", index, child.SessionID, childIDs[index])
		}
	}
	var response struct {
		Status        string `json:"status"`
		Selection     string `json:"selection"`
		TaskCallID    string `json:"task_call_id"`
		SelectedCount int    `json:"selected_count"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode large integrate response: %v", err)
	}
	if response.Status != "ok" || response.Selection != "complete_task_call" || response.TaskCallID != "call-wave" || response.SelectedCount != len(childIDs) {
		t.Fatalf("large integration response = %s", output)
	}
}

func TestManageWorktreeRecallExposesDirtyRecoverableChangedFilesAndBlocker(t *testing.T) {
	runtime, scope, worktrees, childIDs := newCoderWaveRuntime(t, 2)
	dirtyPath := "/worktrees/" + childIDs[1]
	dirty := worktrees.states[dirtyPath]
	dirty.Clean = false
	dirty.Status = " M parser.go\n?? schema.json"
	worktrees.states[dirtyPath] = dirty
	sessions := runtime.sessions.(*coderLineageSessionService)
	entry := sessions.parent.Metadata["task_launches"].(map[string]any)["call-wave"].(map[string]any)
	row := entry["launches"].([]any)[1].(map[string]any)
	row["phase"] = "blocked"
	row["reason"] = "schema token required"
	row["blocker_code"] = "required_input"
	row["blocker_evidence"] = []any{"API returned 401"}
	row["completed_scope"] = []any{"parser implemented"}
	row["resolution_requirement"] = "provide a scoped schema token"
	row["changed_files"] = []any{"parser.go", "schema.json"}

	output, err := runtime.manageWorktreeRecall(scope, map[string]any{"task_call_id": "call-wave"})
	if err != nil {
		t.Fatalf("recall blocked Coder wave: %v", err)
	}
	var response struct {
		Children []struct {
			SessionID             string   `json:"child_session_id"`
			State                 string   `json:"child_state"`
			Phase                 string   `json:"phase"`
			BlockerCode           string   `json:"blocker_code"`
			BlockerEvidence       []string `json:"blocker_evidence"`
			CompletedScope        []string `json:"completed_scope"`
			ResolutionRequirement string   `json:"resolution_requirement"`
			ChangedFiles          []string `json:"changed_files"`
			GitStatus             string   `json:"git_status"`
		} `json:"children"`
		StateCounts map[string]int `json:"state_counts"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode blocked recall response: %v", err)
	}
	if len(response.Children) != 2 || response.Children[1].SessionID != childIDs[1] || response.Children[1].State != "dirty-recoverable" || response.Children[1].Phase != "blocked" || response.Children[1].BlockerCode != "required_input" || len(response.Children[1].BlockerEvidence) != 1 || len(response.Children[1].CompletedScope) != 1 || response.Children[1].ResolutionRequirement == "" || len(response.Children[1].ChangedFiles) != 2 || !strings.Contains(response.Children[1].GitStatus, "parser.go") || response.StateCounts["dirty-recoverable"] != 1 {
		t.Fatalf("dirty-recoverable recall = %s", output)
	}
}

func TestManageWorktreeIntegrateLargeWavePropagatesDirtyChildWithoutApply(t *testing.T) {
	runtime, scope, worktrees, childIDs := newCoderWaveRuntime(t, 100)
	dirtyPath := "/worktrees/" + childIDs[73]
	dirty := worktrees.states[dirtyPath]
	dirty.Clean = false
	dirty.Status = " M conflicted.go"
	worktrees.states[dirtyPath] = dirty

	_, err := runtime.manageWorktreeIntegrate(scope, map[string]any{"task_call_id": "call-wave"})
	if err == nil || !strings.Contains(err.Error(), childIDs[73]) || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty large-wave error = %v", err)
	}
	if worktrees.applyCalls != 0 {
		t.Fatalf("integration apply unexpectedly ran %d times", worktrees.applyCalls)
	}
}

func TestManageWorktreeIntegrateLargeWavePropagatesChildFailureWithoutApply(t *testing.T) {
	runtime, scope, worktrees, childIDs := newCoderWaveRuntime(t, 100)
	sessions := runtime.sessions.(*coderLineageSessionService)
	entry := sessions.parent.Metadata["task_launches"].(map[string]any)["call-wave"].(map[string]any)
	rows := entry["launches"].([]any)
	failed := rows[41].(map[string]any)
	failed["error"] = "provider run failed"

	_, err := runtime.manageWorktreeIntegrate(scope, map[string]any{"task_call_id": "call-wave"})
	if err == nil || !strings.Contains(err.Error(), childIDs[41]) || !strings.Contains(err.Error(), "provider run failed") {
		t.Fatalf("failed large-wave error = %v", err)
	}
	if worktrees.applyCalls != 0 {
		t.Fatalf("integration apply unexpectedly ran %d times", worktrees.applyCalls)
	}
}

func TestManageWorktreeIntegrateRejectsAmbiguousSelectors(t *testing.T) {
	runtime, scope, worktrees, childIDs := newCoderWaveRuntime(t, 2)
	_, err := runtime.manageWorktreeIntegrate(scope, map[string]any{"task_call_id": "call-wave", "session_ids": []any{childIDs[0]}})
	if err == nil || !strings.Contains(err.Error(), "exactly one selector") {
		t.Fatalf("ambiguous selector error = %v", err)
	}
	if worktrees.applyCalls != 0 {
		t.Fatalf("integration apply unexpectedly ran %d times", worktrees.applyCalls)
	}
}

func TestManageWorktreeIntegrateRejectsChildWithoutIndependentParentLineage(t *testing.T) {
	runtime, scope, worktrees, childIDs := newCoderLineageRuntime(t)
	sessions := runtime.sessions.(*coderLineageSessionService)
	child := sessions.children[childIDs[0]]
	child.Metadata["parent_session_id"] = "other-parent"
	sessions.children[childIDs[0]] = child

	_, err := runtime.manageWorktreeIntegrate(scope, map[string]any{"session_ids": []any{childIDs[0]}})
	if err == nil || !strings.Contains(err.Error(), "not an owned Coder child") {
		t.Fatalf("lineage rejection = %v", err)
	}
	if worktrees.applyCalls != 0 {
		t.Fatalf("integration apply unexpectedly ran %d times", worktrees.applyCalls)
	}
}

func TestManageWorktreeIntegrateReturnsActionableConflictWithoutApply(t *testing.T) {
	runtime, scope, worktrees, childIDs := newCoderLineageRuntime(t)
	worktrees.prepareErr = &worktreeruntime.TaskIntegrationConflictError{SessionID: childIDs[0], Commit: "child-one-head", Detail: "content conflict in AGENTS.md"}
	output, err := runtime.manageWorktreeIntegrate(scope, map[string]any{"session_ids": []any{childIDs[0], childIDs[1]}})
	if err == nil {
		t.Fatal("expected integration conflict")
	}
	var response struct {
		Status            string `json:"status"`
		ParentUnchanged   bool   `json:"parent_unchanged"`
		ConflictingChild  string `json:"conflicting_child_session_id"`
		ConflictingCommit string `json:"conflicting_commit"`
		NextAction        string `json:"next_action"`
	}
	if decodeErr := json.Unmarshal([]byte(output), &response); decodeErr != nil {
		t.Fatalf("decode conflict response: %v; output=%s", decodeErr, output)
	}
	if response.Status != "conflict" || !response.ParentUnchanged || response.ConflictingChild != childIDs[0] || response.ConflictingCommit != "child-one-head" || !strings.Contains(response.NextAction, "Resolve") {
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
	children := map[string]pebblestore.SessionSnapshot{}
	states := map[string]worktreeruntime.TaskWorkspaceState{
		parentPath: {WorkspacePath: parentPath, BranchName: "agent/parent-session", HeadCommit: "parent-head", Clean: true},
	}
	for index, id := range childIDs {
		callID := "call-b"
		if index == 0 {
			callID = "call-a"
		}
		row := coderLineageRow(parentID, id, index+1, states, children)
		launches[callID] = map[string]any{"launches": []any{row}}
	}
	return coderLineageRuntime(parentID, parentPath, launches, states, children, childIDs)
}

func newCoderWaveRuntime(t *testing.T, count int) (*Runtime, WorkspaceScope, *coderLineageWorktreeService, []string) {
	t.Helper()
	const parentID = "parent-session"
	const parentPath = "/repo"
	childIDs := make([]string, 0, count)
	children := map[string]pebblestore.SessionSnapshot{}
	states := map[string]worktreeruntime.TaskWorkspaceState{
		parentPath: {WorkspacePath: parentPath, BranchName: "agent/parent-session", HeadCommit: "parent-head", Clean: true},
	}
	rows := make([]any, 0, count)
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("child-%03d", index+1)
		childIDs = append(childIDs, id)
		rows = append(rows, coderLineageRow(parentID, id, index+1, states, children))
	}
	launches := map[string]any{"call-wave": map[string]any{"launches": rows}}
	return coderLineageRuntime(parentID, parentPath, launches, states, children, childIDs)
}

func coderLineageRow(parentID, id string, launchIndex int, states map[string]worktreeruntime.TaskWorkspaceState, children map[string]pebblestore.SessionSnapshot) map[string]any {
	path := "/worktrees/" + id
	branch := "agent/" + id
	head := id + "-head"
	states[path] = worktreeruntime.TaskWorkspaceState{WorkspacePath: path, BranchName: branch, HeadCommit: head, Clean: true}
	children[id] = pebblestore.SessionSnapshot{
		ID: id, AccountScopeID: "account", UserID: "user", WorkspacePath: path,
		WorktreeRootPath: path, WorktreeBranch: branch,
		Metadata: map[string]any{
			"parent_session_id": parentID,
			"lineage_kind":      "delegated_subagent",
			"subagent":          "system-coder",
		},
	}
	return map[string]any{
		"launch_index": launchIndex, "child_session_id": id, "subagent": "system-coder",
		"worktree_root_path": path, "worktree_branch": branch,
		"base_commit": "parent-head", "head_commit": head,
	}
}

func coderLineageRuntime(parentID, parentPath string, launches map[string]any, states map[string]worktreeruntime.TaskWorkspaceState, children map[string]pebblestore.SessionSnapshot, childIDs []string) (*Runtime, WorkspaceScope, *coderLineageWorktreeService, []string) {
	parent := pebblestore.SessionSnapshot{ID: parentID, AccountScopeID: "account", UserID: "user", WorkspacePath: parentPath, WorktreeEnabled: true, WorktreeRootPath: parentPath, WorktreeBranch: "agent/parent-session", WorktreeBaseBranch: "dev", Metadata: map[string]any{"task_launches": launches}}
	worktrees := &coderLineageWorktreeService{states: states}
	runtime := &Runtime{sessions: &coderLineageSessionService{parent: parent, children: children}, worktrees: worktrees}
	scope := WorkspaceScope{PrimaryPath: parentPath, Roots: []string{parentPath}, SessionID: parentID, Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account", SessionID: parentID}}
	return runtime, scope, worktrees, childIDs
}
