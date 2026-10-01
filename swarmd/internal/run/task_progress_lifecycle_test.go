package run

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func newTestTaskProgressService(t *testing.T) (*Service, *sessionruntime.Service, *pebblestore.Store, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "state.pebble"))
	if err != nil {
		t.Fatalf("failed to open test pebble store: %v", err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("failed to open event log: %v", err)
	}
	sessionStore := pebblestore.NewSessionStore(store)
	sessionSvc := sessionruntime.NewService(sessionStore, events)
	runSvc := NewService(sessionSvc, nil, nil, tool.NewRuntime(1), nil, nil, nil, events)
	cleanup := func() {
		_ = store.Close()
	}
	return runSvc, sessionSvc, store, cleanup
}

func TestTaskProgressLifecycleSignalingDone(t *testing.T) {
	runSvc, sessionSvc, _, cleanup := newTestTaskProgressService(t)
	defer cleanup()

	sessionID := "test-coder-session-done"
	now := time.Now().UnixMilli()
	created, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          "test-user",
		AccountScopeID:  "test-account",
		ClientRequestID: "init",
		IdempotencyKey:  "init",
		PayloadHash:     "init",
		RequestHash:     "init",
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session: &pebblestore.SessionSnapshot{
			ID:             sessionID,
			UserID:         "test-user",
			AccountScopeID: "test-account",
			WorkspacePath:  "/tmp/test",
			WorkspaceName:  "test",
			Title:          "Coder Task",
			Mode:           "auto",
			Metadata:       map[string]any{"lifecycle_signal": "in_progress"},
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		NowUnixMs: now,
	})
	if err != nil || created.Session == nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// 1. Set todos
	setCall := tool.Call{
		CallID: "call-1",
		Name:   "task_progress",
		Arguments: mustJSON(t, map[string]any{
			"action": "set_todos",
			"todos":  []string{"Inspect code", "Implement fix", "Author tests"},
		}),
	}
	output, err := runSvc.executeTaskProgressTool(sessionID, setCall, sessionSvc.ApplySessionMutation, planLifecycleRunContext{RunID: "run-1"})
	if err != nil {
		t.Fatalf("set_todos failed: %v", err)
	}
	var setResp map[string]any
	if err := json.Unmarshal([]byte(output), &setResp); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if setResp["status"] != "ok" {
		t.Fatalf("status = %v, want ok", setResp["status"])
	}
	todos := setResp["todos"].([]any)
	if len(todos) != 3 {
		t.Fatalf("expected 3 todos, got %d", len(todos))
	}

	// 2. Mark in progress
	ipCall := tool.Call{
		CallID: "call-2",
		Name:   "task_progress",
		Arguments: mustJSON(t, map[string]any{
			"action": "in_progress",
			"title":  "Implement fix",
		}),
	}
	output, err = runSvc.executeTaskProgressTool(sessionID, ipCall, sessionSvc.ApplySessionMutation, planLifecycleRunContext{RunID: "run-1"})
	if err != nil {
		t.Fatalf("in_progress failed: %v", err)
	}
	var ipResp map[string]any
	if err := json.Unmarshal([]byte(output), &ipResp); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if ipResp["active_todo"] != "Implement fix" {
		t.Fatalf("active_todo = %v, want 'Implement fix'", ipResp["active_todo"])
	}

	// 3. Mark a todo complete
	compCall := tool.Call{
		CallID: "call-3",
		Name:   "task_progress",
		Arguments: mustJSON(t, map[string]any{
			"action": "complete_todo",
			"title":  "Inspect code",
		}),
	}
	output, err = runSvc.executeTaskProgressTool(sessionID, compCall, sessionSvc.ApplySessionMutation, planLifecycleRunContext{RunID: "run-1"})
	if err != nil {
		t.Fatalf("complete_todo failed: %v", err)
	}
	var compResp map[string]any
	if err := json.Unmarshal([]byte(output), &compResp); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	summary := compResp["summary"].(map[string]any)
	if summary["completed_count"].(float64) != 1 {
		t.Fatalf("completed_count = %v, want 1", summary["completed_count"])
	}
	if summary["open_count"].(float64) != 2 {
		t.Fatalf("open_count = %v, want 2", summary["open_count"])
	}

	// 4. Signal done
	doneCall := tool.Call{
		CallID: "call-4",
		Name:   "task_progress",
		Arguments: mustJSON(t, map[string]any{
			"action":  "done",
			"summary": "Fix implemented and tests authored",
		}),
	}
	output, err = runSvc.executeTaskProgressTool(sessionID, doneCall, sessionSvc.ApplySessionMutation, planLifecycleRunContext{RunID: "run-1"})
	if err != nil {
		t.Fatalf("done failed: %v", err)
	}
	var doneResp map[string]any
	if err := json.Unmarshal([]byte(output), &doneResp); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if doneResp["lifecycle_state"] != "needs_review" {
		t.Fatalf("lifecycle_state = %v, want 'needs_review'", doneResp["lifecycle_state"])
	}

	// Verify session metadata
	updated, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("GetSession failed: %v", err)
	}
	if updated.Metadata["lifecycle_signal"] != "needs_review" {
		t.Fatalf("metadata lifecycle_signal = %v, want 'needs_review'", updated.Metadata["lifecycle_signal"])
	}
	if updated.Metadata["lifecycle_summary"] != "Fix implemented and tests authored" {
		t.Fatalf("metadata lifecycle_summary = %v", updated.Metadata["lifecycle_summary"])
	}

	// Verify session lifecycle in store
	lifecycle, ok, err := sessionSvc.GetLifecycle(sessionID)
	if err != nil || !ok {
		t.Fatalf("GetLifecycle failed: %v", err)
	}
	if lifecycle.Phase != "needs_review" {
		t.Fatalf("lifecycle.Phase = %v, want 'needs_review'", lifecycle.Phase)
	}
}

func TestTaskProgressLifecycleSignalingBlocked(t *testing.T) {
	runSvc, sessionSvc, _, cleanup := newTestTaskProgressService(t)
	defer cleanup()

	sessionID := "test-coder-session-blocked"
	now := time.Now().UnixMilli()
	created, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          "test-user",
		AccountScopeID:  "test-account",
		ClientRequestID: "init",
		IdempotencyKey:  "init",
		PayloadHash:     "init",
		RequestHash:     "init",
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session: &pebblestore.SessionSnapshot{
			ID:             sessionID,
			UserID:         "test-user",
			AccountScopeID: "test-account",
			WorkspacePath:  "/tmp/test",
			WorkspaceName:  "test",
			Title:          "Coder Task",
			Mode:           "auto",
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		NowUnixMs: now,
	})
	if err != nil || created.Session == nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Signal blocked
	blockedCall := tool.Call{
		CallID: "call-b",
		Name:   "task_progress",
		Arguments: mustJSON(t, map[string]any{
			"action":       "blocked",
			"reason":       "Missing required database migrations",
			"blocker_code": "MIGRATION_MISSING",
		}),
	}
	output, err := runSvc.executeTaskProgressTool(sessionID, blockedCall, sessionSvc.ApplySessionMutation, planLifecycleRunContext{RunID: "run-b"})
	if err != nil {
		t.Fatalf("blocked call failed: %v", err)
	}
	var blockedResp map[string]any
	if err := json.Unmarshal([]byte(output), &blockedResp); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if blockedResp["lifecycle_state"] != "blocked" {
		t.Fatalf("lifecycle_state = %v, want 'blocked'", blockedResp["lifecycle_state"])
	}
	if blockedResp["blocker_reason"] != "Missing required database migrations" {
		t.Fatalf("blocker_reason = %v", blockedResp["blocker_reason"])
	}

	// Verify session metadata
	updated, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("GetSession failed: %v", err)
	}
	if updated.Metadata["lifecycle_signal"] != "blocked" {
		t.Fatalf("metadata lifecycle_signal = %v, want 'blocked'", updated.Metadata["lifecycle_signal"])
	}
	if updated.Metadata["blocker_reason"] != "Missing required database migrations" {
		t.Fatalf("metadata blocker_reason = %v", updated.Metadata["blocker_reason"])
	}

	// Verify session lifecycle in store
	lifecycle, ok, err := sessionSvc.GetLifecycle(sessionID)
	if err != nil || !ok {
		t.Fatalf("GetLifecycle failed: %v", err)
	}
	if lifecycle.Phase != "blocked" {
		t.Fatalf("lifecycle.Phase = %v, want 'blocked'", lifecycle.Phase)
	}
	if lifecycle.StopReason != "Missing required database migrations" {
		t.Fatalf("lifecycle.StopReason = %v", lifecycle.StopReason)
	}
}

func TestSessionSearchAttentionWithoutPlanReadsLifecycle(t *testing.T) {
	_, sessionSvc, _, cleanup := newTestTaskProgressService(t)
	defer cleanup()

	sessionID := "test-session-attention"
	now := time.Now().UnixMilli()
	_, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          "test-user",
		AccountScopeID:  "test-account",
		ClientRequestID: "init",
		IdempotencyKey:  "init",
		PayloadHash:     "init",
		RequestHash:     "init",
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session: &pebblestore.SessionSnapshot{
			ID:             sessionID,
			UserID:         "test-user",
			AccountScopeID: "test-account",
			WorkspacePath:  "/tmp/test",
			WorkspaceName:  "test",
			Title:          "Attention Test",
			Mode:           "auto",
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		NowUnixMs: now,
	})
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Set lifecycle to needs_review
	err = sessionSvc.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{
		SessionID:      sessionID,
		UserID:         "test-user",
		AccountScopeID: "test-account",
		RunID:          "run-1",
		Active:         false,
		Phase:          "needs_review",
		UpdatedAt:      now,
	})
	if err != nil {
		t.Fatalf("UpsertLifecycle failed: %v", err)
	}

	result, err := sessionSvc.SearchSessions(pebblestore.V3SessionSearchOptions{
		AccountScopeID: "test-account",
		UserID:         "test-user",
		Global:         true,
	})
	if err != nil {
		t.Fatalf("SearchSessions failed: %v", err)
	}
	if len(result.Items) == 0 {
		t.Fatal("expected search items, got none")
	}
	item := result.Items[0]
	if item.Attention.State != "needs_review" {
		t.Fatalf("item.Attention.State = %v, want 'needs_review'", item.Attention.State)
	}

	// Update to blocked
	_ = sessionSvc.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{
		SessionID:      sessionID,
		UserID:         "test-user",
		AccountScopeID: "test-account",
		RunID:          "run-1",
		Active:         false,
		Phase:          "blocked",
		UpdatedAt:      now,
	})
	result, _ = sessionSvc.SearchSessions(pebblestore.V3SessionSearchOptions{
		AccountScopeID: "test-account",
		UserID:         "test-user",
		Global:         true,
	})
	if result.Items[0].Attention.State != "blocked" {
		t.Fatalf("item.Attention.State = %v, want 'blocked'", result.Items[0].Attention.State)
	}

	// Update to running
	_ = sessionSvc.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{
		SessionID:      sessionID,
		UserID:         "test-user",
		AccountScopeID: "test-account",
		RunID:          "run-1",
		Active:         true,
		Phase:          "running",
		UpdatedAt:      now,
	})
	result, _ = sessionSvc.SearchSessions(pebblestore.V3SessionSearchOptions{
		AccountScopeID: "test-account",
		UserID:         "test-user",
		Global:         true,
	})
	if result.Items[0].Attention.State != "in_progress" {
		t.Fatalf("item.Attention.State = %v, want 'in_progress'", result.Items[0].Attention.State)
	}
}
