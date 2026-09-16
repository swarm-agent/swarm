package run

import (
	"errors"
	"strings"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: adoption cannot race a running program even before its first
// lane allocation. EnsureWorkspaceTransitionIdle must reject without reconciling
// the durable program or changing the parent. The run/session store is the
// narrowest layer proving this admission postcondition.
func TestWorkspaceSuccessorActiveProgramGuard(t *testing.T) {
	svc, parentID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, _, err := svc.sessions.GetSession(parentID)
	if err != nil {
		t.Fatal(err)
	}
	spec := &taskProgramSpec{ID: "active-successor", Stages: []taskProgramStage{{ID: "build", DependencyEvidence: "ready"}}, Jobs: []taskProgramJob{{ID: "build", StageID: "build", RequestedSubagentType: "coder", OwnedScope: []string{"source.txt"}}}}
	initial, err := taskProgramInitialRecord(parentID, "run", "call", spec)
	if err != nil {
		t.Fatal(err)
	}
	initial.State = pebblestore.TaskProgramStateRunning
	saved, _, err := svc.sessions.CreateTaskProgram(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ensureWorkspaceTransitionIdle(parent, testRunPrincipal()); err == nil || !strings.Contains(err.Error(), "active Task Program") {
		t.Fatalf("guard: %v", err)
	}
	// Simulate admission after the caller's preflight: the canonical mutation
	// must independently reject under its session lock with no published state.
	next := parent
	next.WorktreeRootPath = "unpublished-successor"
	for i := 0; i < 2; i++ {
		_, err := svc.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: parentID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, Kind: sessionruntime.SessionMutationUpdateSettings, ClientRequestID: "racing-adoption", IdempotencyKey: "racing-adoption", PayloadHash: "racing-adoption", RequestHash: "racing-adoption", EventType: "session.worktree.adopted", Session: &next})
		if err == nil || !strings.Contains(err.Error(), "active Task Program") {
			t.Fatalf("atomic admission: %v", err)
		}
	}
	// Repeated admission is read-only, not GetTaskProgram reconciliation.
	if err := svc.sessions.EnsureWorkspaceTransitionIdle(parentID); err == nil {
		t.Fatal("guard reconciled running record")
	}
	after, _, _ := svc.sessions.GetSession(parentID)
	if mustJSON(t, parent) != mustJSON(t, after) {
		t.Fatal("guard mutated parent")
	}
	if saved.RepositoryLane != nil || saved.State != pebblestore.TaskProgramStateRunning {
		t.Fatal("fixture did not exercise preallocation")
	}
}

// Requirement: name collisions get one distinct server-authored retry; ordinary
// allocation failures do not retry or claim success. The allocator adapter owns
// this policy, so a deterministic stub is the narrowest assertion layer.
func TestWorkspaceSuccessorAllocationCollisionBound(t *testing.T) {
	stub := &sameSessionWorktreeStub{conflictOnce: true}
	svc := NewService(nil, nil, nil, nil, nil, nil, nil, nil)
	svc.SetWorktreeService(stub)
	if _, err := svc.allocateManagedSessionWorktree(testRunPrincipal(), "source", "owner", "next"); err != nil {
		t.Fatal(err)
	}
	if len(stub.requestedBranches) != 2 || stub.requestedBranches[0] == stub.requestedBranches[1] {
		t.Fatalf("retry: %v", stub.requestedBranches)
	}
	stub.requestedBranches = nil
	stub.allocateErr = errors.New("allocation unavailable")
	if _, err := svc.allocateManagedSessionWorktree(testRunPrincipal(), "source", "owner", "next"); err == nil {
		t.Fatal("failure hidden")
	}
	if len(stub.requestedBranches) != 1 {
		t.Fatal("ordinary failure retried")
	}
}
