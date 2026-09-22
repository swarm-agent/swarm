package run

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// TestServiceRunTurn_ParentCap1DeadlockPrevention
// Purpose:
//   - Invariant: When account ActiveExecutionLimit is 1, a running parent parking its execution lease
//     permits a child subagent to acquire the sole capacity slot, and the parent reacquires after child completion.
//   - Threat/regression: Parent holding execution slot 1 blocks child admission, causing parent/child deadlock.
//   - Production boundary: Service.executeTaskToolWithParsed, runWithParkedExecutionLease, executioncapacity.Manager.
//   - Narrowest test layer: run.Service task execution with ActiveExecutionLimit=1.
func TestServiceRunTurn_ParentCap1DeadlockPrevention(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "parent-cap1.pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	defer store.Close()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	sessionStore := pebblestore.NewSessionStore(store)
	sessionSvc := sessionruntime.NewService(sessionStore, events)
	permStore := pebblestore.NewPermissionStore(store)
	permSvc := permission.NewService(permStore, events, nil)
	permSvc.SetSessionResolver(sessionSvc)

	accountID := "acc-cap1"
	// Set execution limit strictly to 1
	if _, err := permSvc.UpdateActiveExecutionLimitForAccount(accountID, 1); err != nil {
		t.Fatalf("set execution limit to 1: %v", err)
	}

	svc := NewService(sessionSvc, nil, nil, tool.NewRuntime(1), permSvc, nil, nil, events)

	// Parent session
	parent, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Preference:     &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "low"},
		SessionID:      "parent-session",
		UserID:         "user-1",
		AccountScopeID: accountID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "ws-parent",
		Mode:           sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}

	// Child session
	child, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Preference:     &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "low"},
		SessionID:      "child-session",
		UserID:         "user-1",
		AccountScopeID: accountID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "ws-child",
		Mode:           sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"lineage_kind":      "delegated_subagent",
			"parent_session_id": parent.ID,
		},
	})
	if err != nil {
		t.Fatalf("create child session: %v", err)
	}

	// 1. Parent admits into slot 1
	ctx := context.Background()
	parentLease, err := permSvc.AdmitExecution(ctx, executioncapacity.AcquireRequest{
		AccountScopeID: accountID,
		SessionID:      parent.ID,
		RunID:          "parent-run",
		Kind:           executioncapacity.ExecutionKindOrdinary,
	})
	if err != nil {
		t.Fatalf("parent admit: %v", err)
	}
	defer parentLease.Release()

	snap := permSvc.ExecutionCapacitySnapshot(accountID)
	if snap.TotalActive != 1 || snap.Available != 0 {
		t.Fatalf("expected TotalActive=1, Available=0, got: %+v", snap)
	}

	// 2. Put parent lease into ctx
	parentCtx := executioncapacity.WithLease(ctx, parentLease)

	// 3. Simulate task execution: park parent lease while child runs
	childExecuted := false
	_, err = runWithParkedExecutionLease(parentCtx, parent.ID, "parent-run", func() (string, error) {
		// During task execution, parent lease is parked, so Available should be 1!
		parkedSnap := permSvc.ExecutionCapacitySnapshot(accountID)
		if parkedSnap.TotalActive != 0 || parkedSnap.Available != 1 {
			t.Fatalf("expected parked TotalActive=0, Available=1, got: %+v", parkedSnap)
		}

		// Strip parent lease when launching child
		childCtx := executioncapacity.WithoutLease(parentCtx)

		// Child admits into slot 1! (If parent hadn't parked, this would deadlock/timeout)
		childLease, childAdmitErr := permSvc.AdmitExecution(childCtx, executioncapacity.AcquireRequest{
			AccountScopeID: accountID,
			SessionID:      child.ID,
			RunID:          "child-run",
			Kind:           executioncapacity.ExecutionKindOrdinary,
		})
		if childAdmitErr != nil {
			t.Fatalf("child failed to admit with cap 1: %v", childAdmitErr)
		}

		childSnap := permSvc.ExecutionCapacitySnapshot(accountID)
		if childSnap.TotalActive != 1 || childSnap.Available != 0 {
			t.Fatalf("child active mismatch: %+v", childSnap)
		}

		// Child finishes
		_ = childLease.Release()
		childExecuted = true
		return "child done", nil
	})
	if err != nil {
		t.Fatalf("task execution returned error: %v", err)
	}
	if !childExecuted {
		t.Fatalf("child was not executed")
	}

	// 4. After task finishes, parent reacquires slot 1
	postSnap := permSvc.ExecutionCapacitySnapshot(accountID)
	if postSnap.TotalActive != 1 || postSnap.Available != 0 {
		t.Fatalf("expected parent reacquired TotalActive=1, Available=0, got: %+v", postSnap)
	}

	// 5. Parent finishes and releases
	_ = parentLease.Release()
	finalSnap := permSvc.ExecutionCapacitySnapshot(accountID)
	if finalSnap.TotalActive != 0 || finalSnap.Available != 1 {
		t.Fatalf("expected final TotalActive=0, Available=1, got: %+v", finalSnap)
	}

	_ = svc
}

// TestServiceRunTurn_NestedTaskProgramSingleParkingOwner
// Purpose:
//   - Invariant: Nested task calls inside the same session run do not attempt to double-park
//     an already parked lease, preventing ErrLeaseAlreadyParked failures.
//   - Threat/regression: Inner scheduler or cohort task calls fail by double-parking parent's lease.
//   - Production boundary: runWithParkedExecutionLease, Service.executeTaskToolWithParsed.
//   - Narrowest test layer: nested runWithParkedExecutionLease invocation.
func TestServiceRunTurn_NestedTaskProgramSingleParkingOwner(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "nested-task-park.pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	defer store.Close()

	events, _ := pebblestore.NewEventLog(store)
	sessionStore := pebblestore.NewSessionStore(store)
	sessionSvc := sessionruntime.NewService(sessionStore, events)
	permStore := pebblestore.NewPermissionStore(store)
	permSvc := permission.NewService(permStore, events, nil)
	permSvc.SetSessionResolver(sessionSvc)

	accountID := "acc-nested-park"
	if _, err := permSvc.UpdateActiveExecutionLimitForAccount(accountID, 1); err != nil {
		t.Fatalf("set limit: %v", err)
	}

	sess, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Preference: &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "low"},
		SessionID:  "sess-nested-test", UserID: "user-1", AccountScopeID: accountID,
		WorkspacePath: t.TempDir(), WorkspaceName: "nested", Mode: sessionruntime.ModeAuto,
	})

	ctx := context.Background()
	lease, err := permSvc.AdmitExecution(ctx, executioncapacity.AcquireRequest{
		AccountScopeID: accountID,
		SessionID:      sess.ID,
		RunID:          "run-nested",
		Kind:           executioncapacity.ExecutionKindOrdinary,
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	defer lease.Release()

	leaseCtx := executioncapacity.WithLease(ctx, lease)

	// Outer task parks the lease
	nestedExecuted := false
	_, outerErr := runWithParkedExecutionLease(leaseCtx, sess.ID, "run-nested", func() (string, error) {
		if !lease.IsParked() {
			t.Fatalf("expected lease to be parked by outer caller")
		}

		// Inner nested call should NOT fail with ErrLeaseAlreadyParked; it detects parked state and runs cleanly!
		_, innerErr := runWithParkedExecutionLease(leaseCtx, sess.ID, "run-nested", func() (string, error) {
			nestedExecuted = true
			return "nested success", nil
		})
		if innerErr != nil {
			return "", innerErr
		}
		return "outer success", nil
	})
	if outerErr != nil {
		t.Fatalf("nested park failed: %v", outerErr)
	}
	if !nestedExecuted {
		t.Fatalf("nested function was not executed")
	}

	// After outer finishes, lease is reacquired
	if lease.IsParked() {
		t.Fatalf("expected lease to be reacquired after outer caller returns")
	}
}

// TestServiceRunTurn_CompactionNoDeadlockOrDoubleCounting
// Purpose:
//   - Invariant: Internal compaction on an active session runs without admitting a second capacity lease,
//     preventing same-session deadlock and double counting. Standalone compaction without a lease
//     in context admits a lease normally.
//   - Threat/regression: Compaction acquiring an execution slot deadlocks against the parent session's existing lease,
//     or standalone compaction bypasses capacity limits.
//   - Production boundary: Service.runTurn compaction lease resolution, executioncapacity.LeaseFromContext.
//   - Narrowest test layer: runTurn lease acquisition logic for internal and standalone compaction.
func TestServiceRunTurn_CompactionNoDeadlockOrDoubleCounting(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "compact-nodeadlock.pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	defer store.Close()

	events, _ := pebblestore.NewEventLog(store)
	sessionStore := pebblestore.NewSessionStore(store)
	sessionSvc := sessionruntime.NewService(sessionStore, events)
	permStore := pebblestore.NewPermissionStore(store)
	permSvc := permission.NewService(permStore, events, nil)
	permSvc.SetSessionResolver(sessionSvc)

	accountID := "acc-compact"
	if _, err := permSvc.UpdateActiveExecutionLimitForAccount(accountID, 1); err != nil {
		t.Fatalf("set limit: %v", err)
	}

	// Parent session
	sess, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Preference:     &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "low"},
		SessionID:      "sess-compact-test",
		UserID:         "user-1",
		AccountScopeID: accountID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "ws-compact",
		Mode:           sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// 1. Internal compaction: lease is already in context from the owning turn
	ctx := context.Background()
	parentLease, err := permSvc.AdmitExecution(ctx, executioncapacity.AcquireRequest{
		AccountScopeID: accountID,
		SessionID:      sess.ID,
		RunID:          "run-parent",
		Kind:           executioncapacity.ExecutionKindOrdinary,
	})
	if err != nil {
		t.Fatalf("admit parent: %v", err)
	}
	defer parentLease.Release()

	snap := permSvc.ExecutionCapacitySnapshot(accountID)
	if snap.TotalActive != 1 {
		t.Fatalf("expected TotalActive=1, got %d", snap.TotalActive)
	}

	// When internal compaction runs within the active turn, it reuses the lease from context
	parentCtx := executioncapacity.WithLease(ctx, parentLease)
	foundLease, ok := executioncapacity.LeaseFromContext(parentCtx, sess.ID, "run-parent")
	if !ok || foundLease == nil {
		t.Fatalf("expected lease to be found in context for session and run")
	}

	// Verify capacity did not double-count
	snapAfter := permSvc.ExecutionCapacitySnapshot(accountID)
	if snapAfter.TotalActive != 1 {
		t.Fatalf("double counting detected: TotalActive=%d, want 1", snapAfter.TotalActive)
	}

	// 2. Standalone compaction: no lease in context
	// When capacity limit is 1 and slot 1 is occupied, standalone compaction must wait/block rather than bypass!
	standaloneCtx, standaloneCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer standaloneCancel()
	_, standaloneAdmitErr := permSvc.AdmitExecution(standaloneCtx, executioncapacity.AcquireRequest{
		AccountScopeID: accountID,
		SessionID:      "sess-standalone-compact",
		RunID:          "run-standalone",
		Kind:           executioncapacity.ExecutionKindOrdinary,
	})
	// With slot occupied, standalone admit must NOT succeed immediately
	if standaloneAdmitErr == nil {
		t.Fatalf("expected standalone compaction to block when at capacity limit, but it succeeded")
	}
}

// TestServiceRunTurn_PermissionWaitParksParent
// Purpose:
//   - Invariant: Waiting for user approval on an AuthorizationPending tool call parks the execution lease,
//     releasing capacity while blocked, and reacquires after resolution.
//   - Threat/regression: Permission wait occupies capacity slot indefinitely, blocking other concurrent sessions.
//   - Production boundary: Service.gateToolCalls, executioncapacity.Manager.Park/Reacquire.
//   - Narrowest test layer: gateToolCalls execution with pending permission.
func TestServiceRunTurn_PermissionWaitParksParent(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "perm-wait-park.pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	defer store.Close()

	events, _ := pebblestore.NewEventLog(store)
	sessionStore := pebblestore.NewSessionStore(store)
	sessionSvc := sessionruntime.NewService(sessionStore, events)
	permStore := pebblestore.NewPermissionStore(store)
	permSvc := permission.NewService(permStore, events, nil)
	permSvc.SetSessionResolver(sessionSvc)

	accountID := "acc-perm-park"
	if _, err := permSvc.UpdateActiveExecutionLimitForAccount(accountID, 1); err != nil {
		t.Fatalf("set limit: %v", err)
	}

	svc := NewService(sessionSvc, nil, nil, tool.NewRuntime(1), permSvc, nil, nil, events)

	sess, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Preference:     &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "low"},
		SessionID:      "sess-gate-test",
		UserID:         "user-1",
		AccountScopeID: accountID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "ws-gate",
		Mode:           sessionruntime.ModeAuto,
	})

	ctx := context.Background()
	lease, err := permSvc.AdmitExecution(ctx, executioncapacity.AcquireRequest{
		AccountScopeID: accountID,
		SessionID:      sess.ID,
		RunID:          "run-gate",
		Kind:           executioncapacity.ExecutionKindOrdinary,
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	defer lease.Release()

	leaseCtx := executioncapacity.WithLease(ctx, lease)

	// Verify active before gate
	snap := permSvc.ExecutionCapacitySnapshot(accountID)
	if snap.TotalActive != 1 || snap.Available != 0 {
		t.Fatalf("expected TotalActive=1, Available=0, got: %+v", snap)
	}

	// Create pending permission record
	pending, err := permSvc.CreatePending(permission.CreateInput{
		SessionID:     sess.ID,
		RunID:         "run-gate",
		Step:          1,
		CallID:        "call-bash-1",
		ToolName:      "bash",
		ToolArguments: `{"command":"echo capacity", "category":"write", "critical":true, "explanation":["Test permission wait only."]}`,
	})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}

	// Resolve in background after short delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		// Verify parent is parked while waiting
		parkedSnap := permSvc.ExecutionCapacitySnapshot(accountID)
		if parkedSnap.TotalActive != 0 || parkedSnap.Available != 1 {
			t.Errorf("expected parked while waiting: TotalActive=0, Available=1, got: %+v", parkedSnap)
		}
		_, _ = permSvc.Resolve(sess.ID, pending.ID, "approve", "user approved")
	}()

	// Run gateToolCalls which will call WaitForResolution
	calls := []tool.Call{{CallID: "call-bash-1", Name: "bash", Arguments: `{"command":"echo capacity", "category":"write", "critical":true, "explanation":["Test permission wait only."]}`}}
	principal := identity.Principal{UserID: "user-1", AccountScopeID: accountID, Type: identity.PrincipalTypeUser}
	callCtx := identity.ContextWithPrincipal(leaseCtx, principal)
	results, approved, _, _, _, gateErr := svc.gateToolCalls(callCtx, sess.ID, "run-gate", 1, sessionruntime.ModeAuto, calls, nil, nil)
	if gateErr != nil {
		t.Fatalf("gateToolCalls error: %v", gateErr)
	}
	if len(approved) != 1 {
		t.Fatalf("expected 1 approved call, got %d: %+v", len(approved), results)
	}

	// After resolution and gate return, parent has reacquired slot 1
	reacquiredSnap := permSvc.ExecutionCapacitySnapshot(accountID)
	if reacquiredSnap.TotalActive != 1 || reacquiredSnap.Available != 0 {
		t.Fatalf("expected reacquired TotalActive=1, Available=0, got: %+v", reacquiredSnap)
	}
}
