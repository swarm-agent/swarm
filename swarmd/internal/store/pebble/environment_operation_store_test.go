package pebblestore

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// Purpose: Multi-tenant isolation requires strict scoping by AccountScopeID and WorkspaceID.
// Threat: Cross-account or cross-workspace operation leakage.
func TestEnvironmentOperationStore_Ownership(t *testing.T) {
	store := openEphemeralStore(t)
	ops := NewEnvironmentOperationStore(store)

	now := time.Now().UnixMilli()
	op := environments.EnvironmentOperation{
		OperationID:    "op-owner-1",
		AccountScopeID: "acc-1",
		WorkspaceID:    "ws-1",
		Action:         environments.OperationActionExec,
		Status:         environments.OperationStatusQueued,
		CreatedAt:      now,
		Deadline:       now + 300000,
	}

	admitted, err := ops.AdmitOperation(op)
	if err != nil {
		t.Fatalf("admit operation: %v", err)
	}
	if admitted.OperationID != "op-owner-1" {
		t.Fatalf("expected op-owner-1, got %s", admitted.OperationID)
	}

	// 1. Same account, different workspace
	_, found, err := ops.Get("acc-1", "ws-2", "op-owner-1")
	if err != nil || found {
		t.Fatalf("expected not found for different workspace: found=%v err=%v", found, err)
	}

	// 2. Different account, same workspace ID
	_, found, err = ops.Get("acc-2", "ws-1", "op-owner-1")
	if err != nil || found {
		t.Fatalf("expected not found for different account: found=%v err=%v", found, err)
	}

	// 3. Query history in wrong workspace
	page, err := ops.QueryHistory(environments.OperationHistoryQuery{
		AccountScopeID: "acc-1",
		WorkspaceID:    "ws-2",
	})
	if err != nil {
		t.Fatalf("query history wrong workspace: %v", err)
	}
	if len(page.Operations) != 0 {
		t.Fatalf("expected 0 operations in ws-2, got %d", len(page.Operations))
	}
}

// Purpose: Idempotent admission must return existing operation on retry, while conflicting
// parameters or overlapping deployments must be blocked.
// Threats: Duplicate execution on retry, race conditions executing two commands on one deployment,
// or reusing deployments left in unknown/cleanup_failed states.
func TestEnvironmentOperationStore_IdempotentAndConflictingAdmission(t *testing.T) {
	store := openEphemeralStore(t)
	ops := NewEnvironmentOperationStore(store)

	now := time.Now().UnixMilli()

	// 1. Admitting operation with IdempotencyKey
	op1 := environments.EnvironmentOperation{
		OperationID:    "op-idemp-1",
		AccountScopeID: "acc-test",
		WorkspaceID:    "ws-test",
		Action:         environments.OperationActionExec,
		DeploymentID:   "dep-1",
		IdempotencyKey: "client-req-001",
		Status:         environments.OperationStatusQueued,
		CreatedAt:      now,
		Deadline:       now + 300000,
	}

	admitted1, err := ops.AdmitOperation(op1)
	if err != nil {
		t.Fatalf("admit op1: %v", err)
	}

	// Idempotent retry with exact same parameters
	retry1, err := ops.AdmitOperation(op1)
	if err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if retry1.OperationID != admitted1.OperationID || retry1.Revision != admitted1.Revision {
		t.Fatalf("idempotent retry returned different operation: %+v vs %+v", retry1, admitted1)
	}

	// Reusing same IdempotencyKey with DIFFERENT action must fail
	conflictOp := op1
	conflictOp.OperationID = "op-idemp-diff"
	conflictOp.Action = environments.OperationActionDeploy
	_, err = ops.AdmitOperation(conflictOp)
	if !errors.Is(err, environments.ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got: %v", err)
	}

	// 2. Conflicting admission on same deployment while op1 is active (queued)
	op2 := environments.EnvironmentOperation{
		OperationID:    "op-clash-2",
		AccountScopeID: "acc-test",
		WorkspaceID:    "ws-test",
		Action:         environments.OperationActionExec,
		DeploymentID:   "dep-1", // same deployment
		CreatedAt:      now,
		Deadline:       now + 300000,
	}
	_, err = ops.AdmitOperation(op2)
	if !errors.Is(err, environments.ErrDeploymentOperationConflict) {
		t.Fatalf("expected ErrDeploymentOperationConflict for active deployment, got: %v", err)
	}

	// 3. Transition op1 to cleanup_failed
	_, err = ops.TransitionOperation(OperationTransitionInput{
		AccountScopeID:   "acc-test",
		WorkspaceID:      "ws-test",
		OperationID:      admitted1.OperationID,
		ExpectedRevision: admitted1.Revision,
		TargetStatus:     environments.OperationStatusRunning,
	})
	if err != nil {
		t.Fatalf("transition to running: %v", err)
	}

	op1Running, _, _ := ops.Get("acc-test", "ws-test", admitted1.OperationID)
	_, err = ops.TransitionOperation(OperationTransitionInput{
		AccountScopeID:   "acc-test",
		WorkspaceID:      "ws-test",
		OperationID:      admitted1.OperationID,
		ExpectedRevision: op1Running.Revision,
		TargetStatus:     environments.OperationStatusCleanupFailed,
	})
	if err != nil {
		t.Fatalf("transition to cleanup_failed: %v", err)
	}

	// 4. Admitting new operation while deployment is in unresolved cleanup_failed state must be BLOCKED
	_, err = ops.AdmitOperation(op2)
	if !errors.Is(err, environments.ErrDeploymentOperationBlocked) {
		t.Fatalf("expected ErrDeploymentOperationBlocked for cleanup_failed deployment, got: %v", err)
	}

	// 5. Resolving the operation to cancelled unblocks the deployment
	op1CleanupFailed, _, _ := ops.Get("acc-test", "ws-test", admitted1.OperationID)
	_, err = ops.TransitionOperation(OperationTransitionInput{
		AccountScopeID:   "acc-test",
		WorkspaceID:      "ws-test",
		OperationID:      admitted1.OperationID,
		ExpectedRevision: op1CleanupFailed.Revision,
		TargetStatus:     environments.OperationStatusCancelled,
	})
	if err != nil {
		t.Fatalf("resolve cleanup_failed to cancelled: %v", err)
	}

	// Now op2 can be admitted successfully!
	admitted2, err := ops.AdmitOperation(op2)
	if err != nil {
		t.Fatalf("expected admission of op2 after deployment unblocked, got: %v", err)
	}
	if admitted2.OperationID != "op-clash-2" {
		t.Fatalf("unexpected admitted op: %+v", admitted2)
	}
}

// Purpose: CAS transitions must guard terminal states and revisions.
// Threat: Worker or provider watchdog late-success report overwriting a cancelled or timed-out operation.
func TestEnvironmentOperationStore_CASLateSuccessRejection(t *testing.T) {
	store := openEphemeralStore(t)
	ops := NewEnvironmentOperationStore(store)

	now := time.Now().UnixMilli()
	op := environments.EnvironmentOperation{
		OperationID:    "op-cas-1",
		AccountScopeID: "acc-test",
		WorkspaceID:    "ws-test",
		Action:         environments.OperationActionExec,
		Status:         environments.OperationStatusQueued,
		CreatedAt:      now,
		Deadline:       now + 300000,
	}

	admitted, err := ops.AdmitOperation(op)
	if err != nil {
		t.Fatalf("admit op: %v", err)
	}

	// Transition to running
	running, err := ops.TransitionOperation(OperationTransitionInput{
		AccountScopeID:   "acc-test",
		WorkspaceID:      "ws-test",
		OperationID:      admitted.OperationID,
		ExpectedRevision: admitted.Revision,
		TargetStatus:     environments.OperationStatusRunning,
	})
	if err != nil {
		t.Fatalf("transition to running: %v", err)
	}

	// Wrong revision CAS check
	_, err = ops.TransitionOperation(OperationTransitionInput{
		AccountScopeID:   "acc-test",
		WorkspaceID:      "ws-test",
		OperationID:      admitted.OperationID,
		ExpectedRevision: 999, // wrong revision
		TargetStatus:     environments.OperationStatusCancelling,
	})
	if !errors.Is(err, environments.ErrOperationConflict) {
		t.Fatalf("expected ErrOperationConflict on revision mismatch, got: %v", err)
	}

	// Timeout / cancellation occurs
	timedOut, err := ops.TransitionOperation(OperationTransitionInput{
		AccountScopeID:   "acc-test",
		WorkspaceID:      "ws-test",
		OperationID:      admitted.OperationID,
		ExpectedRevision: running.Revision,
		TargetStatus:     environments.OperationStatusTimedOut,
	})
	if err != nil {
		t.Fatalf("transition to timed_out: %v", err)
	}
	if !timedOut.IsTerminal() {
		t.Fatal("timedOut should be terminal")
	}

	// Late success report by worker MUST be rejected
	_, err = ops.TransitionOperation(OperationTransitionInput{
		AccountScopeID:   "acc-test",
		WorkspaceID:      "ws-test",
		OperationID:      admitted.OperationID,
		ExpectedRevision: timedOut.Revision,
		TargetStatus:     environments.OperationStatusSucceeded,
	})
	if !errors.Is(err, environments.ErrOperationTerminal) {
		t.Fatalf("expected ErrOperationTerminal on late success, got: %v", err)
	}

	// Verify the operation remains in timed_out state
	current, _, err := ops.Get("acc-test", "ws-test", admitted.OperationID)
	if err != nil {
		t.Fatalf("get operation: %v", err)
	}
	if current.Status != environments.OperationStatusTimedOut {
		t.Fatalf("expected operation status timed_out, got: %s", current.Status)
	}
}

// Purpose: Realtime participant mutations must be completely atomic.
// Threat: Partial state persistence where foreign keys or validation errors leave dangling records.
func TestEnvironmentOperationStore_AtomicFailure(t *testing.T) {
	store := openEphemeralStore(t)

	wakes := 0
	store.SetEnvironmentPublisher(func(record V3RealtimeOutboxRecord) {
		wakes++
	})

	// Invalid mutation with foreign key outside environment scope
	mutation := &environmentRealtimeMutation{
		accountScopeID: "acc-test",
		workspaceID:    "ws-test",
		writes: map[string][]byte{
			"foreign/unauthorized/key": []byte(`{"data": "bad"}`),
		},
		eventPayload: json.RawMessage(`{}`),
	}

	err := store.commitEnvironmentRealtime(mutation)
	if err == nil {
		t.Fatal("expected error on foreign key mutation")
	}

	// Ensure no publication happened
	if wakes != 0 {
		t.Fatalf("expected 0 wakes on failed commit, got %d", wakes)
	}

	// Verify no record was written
	bytes, ok, _ := store.GetBytes("foreign/unauthorized/key")
	if ok || bytes != nil {
		t.Fatal("foreign key was written despite error")
	}
}

// Purpose: Durable persistence must survive Pebble close and reopen.
// Threats: Lost history index, lost active pointer, corrupted revision or summary counts on restart.
func TestEnvironmentOperationStore_DurableReloadAndHistoryRetention(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "env_ops.pebble")
	now := time.Now().UnixMilli()

	// Phase 1: Create store, admit ops, transition, close
	func() {
		store, err := Open(dbPath)
		if err != nil {
			t.Fatalf("open store phase 1: %v", err)
		}
		defer store.Close()

		ops := NewEnvironmentOperationStore(store)

		op1 := environments.EnvironmentOperation{
			OperationID:    "op-persist-1",
			AccountScopeID: "acc-durable",
			WorkspaceID:    "ws-durable",
			Action:         environments.OperationActionDeploy,
			DeploymentID:   "dep-durable-1",
			Status:         environments.OperationStatusQueued,
			CreatedAt:      now,
			Deadline:       now + 300000,
		}
		admitted1, err := ops.AdmitOperation(op1)
		if err != nil {
			t.Fatalf("admit op1: %v", err)
		}

		_, err = ops.TransitionOperation(OperationTransitionInput{
			AccountScopeID:   "acc-durable",
			WorkspaceID:      "ws-durable",
			OperationID:      admitted1.OperationID,
			ExpectedRevision: admitted1.Revision,
			TargetStatus:     environments.OperationStatusRunning,
		})
		if err != nil {
			t.Fatalf("transition op1 to running: %v", err)
		}

		op2 := environments.EnvironmentOperation{
			OperationID:    "op-persist-2",
			AccountScopeID: "acc-durable",
			WorkspaceID:    "ws-durable",
			Action:         environments.OperationActionExec,
			Status:         environments.OperationStatusQueued,
			CreatedAt:      now + 1000,
			Deadline:       now + 300000,
		}
		admitted2, err := ops.AdmitOperation(op2)
		if err != nil {
			t.Fatalf("admit op2: %v", err)
		}

		_, err = ops.TransitionOperation(OperationTransitionInput{
			AccountScopeID:   "acc-durable",
			WorkspaceID:      "ws-durable",
			OperationID:      admitted2.OperationID,
			ExpectedRevision: admitted2.Revision,
			TargetStatus:     environments.OperationStatusSucceeded,
		})
		if err != nil {
			t.Fatalf("transition op2 to succeeded: %v", err)
		}
	}()

	// Phase 2: Reopen store, verify operations, active deployment pointer, and summary
	func() {
		store, err := Open(dbPath)
		if err != nil {
			t.Fatalf("open store phase 2: %v", err)
		}
		defer store.Close()

		ops := NewEnvironmentOperationStore(store)

		// 1. Verify op1
		op1, found, err := ops.Get("acc-durable", "ws-durable", "op-persist-1")
		if err != nil || !found {
			t.Fatalf("op1 not found after restart: %v", err)
		}
		if op1.Status != environments.OperationStatusRunning || op1.Revision != 2 {
			t.Fatalf("op1 mismatch after restart: status=%s rev=%d", op1.Status, op1.Revision)
		}

		// 2. Verify active deployment pointer for dep-durable-1 points to op1
		activeOp, hasActive, err := ops.GetActiveOperationForDeployment("acc-durable", "ws-durable", "dep-durable-1")
		if err != nil || !hasActive {
			t.Fatalf("expected active op on dep-durable-1: hasActive=%v err=%v", hasActive, err)
		}
		if activeOp.OperationID != "op-persist-1" {
			t.Fatalf("active op mismatch: %s", activeOp.OperationID)
		}

		// 3. Verify op2
		op2, found, err := ops.Get("acc-durable", "ws-durable", "op-persist-2")
		if err != nil || !found {
			t.Fatalf("op2 not found after restart: %v", err)
		}
		if op2.Status != environments.OperationStatusSucceeded || op2.Revision != 2 {
			t.Fatalf("op2 mismatch after restart: status=%s rev=%d", op2.Status, op2.Revision)
		}

		// 4. Verify summary
		summary, err := ops.GetSummary("acc-durable", "ws-durable")
		if err != nil {
			t.Fatalf("get summary after restart: %v", err)
		}
		if summary.TotalOps != 2 || summary.RunningOps != 1 || summary.SucceededOps != 1 {
			t.Fatalf("summary counts mismatch after restart: %+v", summary)
		}
	}()
}

// Purpose: Cursor-paginated history must accept explicit IANA timezones and date ranges,
// validate cursors against query bounds, handle DST, and derive daily totals from full durable history.
// Threats: Aggregate totals derived from the visible page, cursor tampering, timezone shifts.
func TestEnvironmentOperationStore_CursorAndTimezoneDailyTotalsBeyondOnePage(t *testing.T) {
	store := openEphemeralStore(t)
	ops := NewEnvironmentOperationStore(store)

	// Choose explicit IANA timezone: America/New_York (UTC-4 in EDT)
	tz := "America/New_York"
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatalf("load location: %v", err)
	}

	// Day 1: 2026-09-20 at 10:00 AM EDT
	day1Time := time.Date(2026, 9, 20, 10, 0, 0, 0, loc)
	day1Millis := day1Time.UnixMilli()

	// Day 2: 2026-09-21 at 14:00 PM EDT
	day2Time := time.Date(2026, 9, 21, 14, 0, 0, 0, loc)
	day2Millis := day2Time.UnixMilli()

	// Create 5 operations: 3 on Day 1, 2 on Day 2
	opData := []struct {
		id     string
		millis int64
		status environments.OperationStatus
		action string
	}{
		{"op-page-1", day1Millis, environments.OperationStatusSucceeded, environments.OperationActionExec},
		{"op-page-2", day1Millis + 1000, environments.OperationStatusFailed, environments.OperationActionExec},
		{"op-page-3", day1Millis + 2000, environments.OperationStatusCancelled, environments.OperationActionDeploy},
		{"op-page-4", day2Millis, environments.OperationStatusSucceeded, environments.OperationActionExec},
		{"op-page-5", day2Millis + 1000, environments.OperationStatusTimedOut, environments.OperationActionExec},
	}

	for _, d := range opData {
		op := environments.EnvironmentOperation{
			OperationID:    d.id,
			AccountScopeID: "acc-page",
			WorkspaceID:    "ws-page",
			Action:         d.action,
			Status:         environments.OperationStatusQueued,
			CreatedAt:      d.millis,
			Deadline:       d.millis + 300000,
		}
		admitted, err := ops.AdmitOperation(op)
		if err != nil {
			t.Fatalf("admit %s: %v", d.id, err)
		}
		if d.status != environments.OperationStatusQueued {
			_, err = ops.TransitionOperation(OperationTransitionInput{
				AccountScopeID:   "acc-page",
				WorkspaceID:      "ws-page",
				OperationID:      admitted.OperationID,
				ExpectedRevision: admitted.Revision,
				TargetStatus:     d.status,
			})
			if err != nil {
				t.Fatalf("transition %s: %v", d.id, err)
			}
		}
	}

	// 1. Query Page 1 with Limit=2 across both days (StartDate: 2026-09-20, EndDate: 2026-09-21)
	query := environments.OperationHistoryQuery{
		AccountScopeID: "acc-page",
		WorkspaceID:    "ws-page",
		Timezone:       tz,
		StartDate:      "2026-09-20",
		EndDate:        "2026-09-21",
		Limit:          2,
	}

	page1, err := ops.QueryHistory(query)
	if err != nil {
		t.Fatalf("query page 1: %v", err)
	}

	if len(page1.Operations) != 2 {
		t.Fatalf("expected 2 operations on page 1, got %d", len(page1.Operations))
	}
	if !page1.HasMore {
		t.Fatal("expected has_more=true on page 1")
	}
	if page1.NextCursor == "" {
		t.Fatal("expected non-empty next_cursor on page 1")
	}

	// CRUCIAL REQUIREMENT: DailyTotals must contain the full aggregate counts across the full durable
	// date range (5 total ops), NOT just the 2 operations visible on page 1!
	if len(page1.DailyTotals) != 2 {
		t.Fatalf("expected 2 daily total entries, got %d", len(page1.DailyTotals))
	}

	// Verify Day 2 (2026-09-21): 2 operations (1 succeeded, 1 timed_out)
	// Verify Day 1 (2026-09-20): 3 operations (1 succeeded, 1 failed, 1 cancelled)
	var day1Counts, day2Counts environments.DailyOperationCounts
	for _, dt := range page1.DailyTotals {
		if dt.Date == "2026-09-20" {
			day1Counts = dt
		} else if dt.Date == "2026-09-21" {
			day2Counts = dt
		}
	}

	if day1Counts.TotalOps != 3 || day1Counts.Succeeded != 1 || day1Counts.Failed != 1 || day1Counts.Cancelled != 1 {
		t.Fatalf("day 1 counts mismatch: %+v", day1Counts)
	}
	if day2Counts.TotalOps != 2 || day2Counts.Succeeded != 1 || day2Counts.TimedOut != 1 {
		t.Fatalf("day 2 counts mismatch: %+v", day2Counts)
	}

	// 2. Fetch Page 2 using NextCursor
	query.Cursor = page1.NextCursor
	page2, err := ops.QueryHistory(query)
	if err != nil {
		t.Fatalf("query page 2: %v", err)
	}
	if len(page2.Operations) != 2 {
		t.Fatalf("expected 2 operations on page 2, got %d", len(page2.Operations))
	}
	if !page2.HasMore || page2.NextCursor == "" {
		t.Fatal("expected has_more=true on page 2")
	}

	// 3. Fetch Page 3 using Page 2's NextCursor
	query.Cursor = page2.NextCursor
	page3, err := ops.QueryHistory(query)
	if err != nil {
		t.Fatalf("query page 3: %v", err)
	}
	if len(page3.Operations) != 1 {
		t.Fatalf("expected 1 operation on page 3, got %d", len(page3.Operations))
	}
	if page3.HasMore || page3.NextCursor != "" {
		t.Fatal("expected has_more=false on final page")
	}

	// 4. Tampered / modified cursor parameters must be rejected
	tamperedQuery := query
	tamperedQuery.Cursor = page1.NextCursor
	tamperedQuery.Status = environments.OperationStatusFailed // Changed filter!
	_, err = ops.QueryHistory(tamperedQuery)
	if !errors.Is(err, environments.ErrCursorInvalid) {
		t.Fatalf("expected ErrCursorInvalid when query parameters differ from cursor, got: %v", err)
	}

	// 5. Date filter: only Day 1
	day1OnlyQuery := environments.OperationHistoryQuery{
		AccountScopeID: "acc-page",
		WorkspaceID:    "ws-page",
		Timezone:       tz,
		StartDate:      "2026-09-20",
		EndDate:        "2026-09-20",
		Limit:          10,
	}
	day1Page, err := ops.QueryHistory(day1OnlyQuery)
	if err != nil {
		t.Fatalf("query day 1 only: %v", err)
	}
	if len(day1Page.Operations) != 3 {
		t.Fatalf("expected 3 operations on day 1 only, got %d", len(day1Page.Operations))
	}
	if len(day1Page.DailyTotals) != 1 || day1Page.DailyTotals[0].Date != "2026-09-20" {
		t.Fatalf("expected exactly 1 daily total entry for 2026-09-20, got %+v", day1Page.DailyTotals)
	}
}

// Purpose: Deployment mutations must participate in summary observations and realtime outbox.
// Threat: Desynchronization between deployment instances and environment summary.
func TestEnvironmentOperationStore_DeploymentCountIntegration(t *testing.T) {
	store := openEphemeralStore(t)
	ds := NewDeploymentStore(store)

	wakes := 0
	var lastRecord V3RealtimeOutboxRecord
	store.SetEnvironmentPublisher(func(record V3RealtimeOutboxRecord) {
		wakes++
		lastRecord = record
	})

	now := time.Now().UnixMilli()
	dep := environments.Deployment{
		ID:             "dep-sync-1",
		AccountScopeID: "acc-sync",
		WorkspaceID:    "ws-sync",
		EnvironmentID:  "env-1",
		ConnectionID:   "conn-1",
		Name:           "Sync Instance",
		Status:         environments.DeploymentStatusRunning,
		Health:         environments.HealthStatusHealthy,
		CreatedAt:      now,
	}

	// 1. Save deployment
	_, err := ds.Save(dep)
	if err != nil {
		t.Fatalf("save deployment: %v", err)
	}

	if wakes != 1 {
		t.Fatalf("expected 1 wakeup on save deployment, got %d", wakes)
	}
	if lastRecord.Event.EventType != EnvironmentChangedEventType {
		t.Fatalf("expected event type %s, got %s", EnvironmentChangedEventType, lastRecord.Event.EventType)
	}

	// Verify summary active deployments count is 1
	summary, err := ds.Operations().GetSummary("acc-sync", "ws-sync")
	if err != nil {
		t.Fatalf("get summary: %v", err)
	}
	if summary.ActiveDeployments != 1 {
		t.Fatalf("expected ActiveDeployments=1, got %d", summary.ActiveDeployments)
	}

	// 2. Update deployment status to Stopped
	_, err = ds.UpdateStatus("acc-sync", "ws-sync", "dep-sync-1", environments.DeploymentStatusStopped, environments.HealthStatusUnknown, "")
	if err != nil {
		t.Fatalf("update status: %v", err)
	}
	if wakes != 2 {
		t.Fatalf("expected 2 wakeups after stopping deployment, got %d", wakes)
	}

	summary, err = ds.Operations().GetSummary("acc-sync", "ws-sync")
	if err != nil {
		t.Fatalf("get summary: %v", err)
	}
	if summary.ActiveDeployments != 0 {
		t.Fatalf("expected ActiveDeployments=0, got %d", summary.ActiveDeployments)
	}
}
