package pebblestore

import (
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// Purpose: ApplyV3SessionMutation must clear only provider context, retain the
// durable conversation, and serialize against run/plan writes. Store tests are
// the narrowest proof of epoch ranges, idempotency, restart and rejection with
// no partial changes; these are deterministic regressions, not live benchmarks.
func TestSessionContextClearPreservesHistoryAndReplaysAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.pebble")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s := NewSessionStore(db)
	if err := s.PutProject("account", &ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	session := SessionSnapshot{ID: "conversation", AccountScopeID: "account", UserID: "user", Metadata: map[string]any{
		"swarm_v3_project_id": "project", "project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator",
	}}
	apply := func(key, kind string) V3SessionMutationInput {
		return V3SessionMutationInput{SessionID: session.ID, UserID: "user", AccountScopeID: "account", ClientRequestID: key, PayloadHash: key, Kind: kind}
	}
	create := apply("create", V3SessionMutationCreateSession)
	create.Session = &session
	if _, err := s.ApplyV3SessionMutation(create); err != nil {
		t.Fatal(err)
	}
	message := apply("message", V3SessionMutationAppendMessage)
	message.Message = &MessageSnapshot{ID: "old-message", SessionID: session.ID, Role: "user", Content: "Retained history"}
	if _, err := s.ApplyV3SessionMutation(message); err != nil {
		t.Fatal(err)
	}
	usage := apply("usage", V3SessionMutationRecordUsage)
	usage.TurnUsage = &SessionTurnUsageSnapshot{SessionID: session.ID, UserID: "user", AccountScopeID: "account", RunID: "prior", InputTokens: 100, OutputTokens: 20, ContextWindow: 1000}
	if _, err := s.ApplyV3SessionMutation(usage); err != nil {
		t.Fatal(err)
	}
	before, _, _ := s.GetActiveExecutionEpoch(session.ID)
	original, _, _ := s.GetSession(session.ID)
	projection, _, _ := s.GetV3SessionProjection(session.ID)
	clear := apply("clear", V3SessionMutationClearContext)
	clear.ExpectedLastEventSeq = &projection.LastEventSeq
	results := make(chan V3SessionMutationResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.ApplyV3SessionMutation(clear)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	fresh := 0
	for result := range results {
		if !result.Replayed {
			fresh++
		}
		if result.RealtimeOutbox == nil || result.RealtimeOutbox.EndpointSeq == 0 {
			t.Fatal("missing durable realtime receipt")
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh clears = %d", fresh)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s = NewSessionStore(db)
	replay, err := s.ApplyV3SessionMutation(clear)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	after, _, _ := s.GetActiveExecutionEpoch(session.ID)
	if after.Ordinal != before.Ordinal+1 || after.Boundary.Reason != ExecutionEpochReasonContextCleared {
		t.Fatalf("epoch: %+v", after)
	}
	_, history, err := s.ListExecutionEpochMessages(session.ID, before.EpochID, 0)
	if err != nil || len(history) != 1 || history[0].Content != "Retained history" {
		t.Fatalf("history lost: %+v %v", history, err)
	}
	_, context, err := s.ListExecutionEpochMessages(session.ID, after.EpochID, 0)
	if err != nil || len(context) != 0 {
		t.Fatalf("context not empty: %+v %v", context, err)
	}
	usageAfter, _, err := s.GetUsageSummary(session.ID)
	if err != nil || usageAfter.InputTokens != 0 || usageAfter.RemainingTokens != 1000 || usageAfter.TurnCount != 1 {
		t.Fatalf("usage reset: %+v %v", usageAfter, err)
	}
	got, _, _ := s.GetSession(session.ID)
	if !reflect.DeepEqual(got, original) {
		t.Fatal("session was replaced or mutated")
	}
	for _, field := range []string{"user", "account", "stale", "payload"} {
		bad := clear
		switch field {
		case "user":
			bad.UserID = "other"
		case "account":
			bad.AccountScopeID = "other"
		case "stale":
			bad.ClientRequestID = "stale"
		case "payload":
			bad.PayloadHash = "different"
		}
		if _, err := s.ApplyV3SessionMutation(bad); err == nil {
			t.Fatalf("accepted %s", field)
		}
		current, _, _ := s.GetActiveExecutionEpoch(session.ID)
		if !reflect.DeepEqual(current, after) {
			t.Fatalf("partial %s mutation", field)
		}
	}
	plan := SessionPlanSnapshot{ID: "plan", SessionID: session.ID, UserID: "user", AccountScopeID: "account", Document: &SessionPlanDocument{Status: "approved"}}
	if err := s.PutPlan(plan); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActivePlan(session.ID, plan.ID, 1); err != nil {
		t.Fatal(err)
	}
	projection, _, _ = s.GetV3SessionProjection(session.ID)
	blockedClear := clear
	blockedClear.ClientRequestID = "with-plan"
	blockedClear.ExpectedLastEventSeq = &projection.LastEventSeq
	if _, err := s.ApplyV3SessionMutation(blockedClear); err != ErrSessionContextClearConflict {
		t.Fatalf("plan rejection: %v", err)
	}
	unchanged, _, _ := s.GetActiveExecutionEpoch(session.ID)
	if unchanged.EpochID != after.EpochID {
		t.Fatal("active plan was cleared")
	}
	plan.Document.Status = "completed"
	if err := s.PutPlan(plan); err != nil {
		t.Fatal(err)
	}
	run := apply("run", V3SessionMutationRecordRunIntent)
	run.RunIntent = &V3SessionRunIntent{SessionID: session.ID, RunID: "running", Status: V3RunIntentPendingExecutor}
	if _, err := s.ApplyV3SessionMutation(run); err != nil {
		t.Fatal(err)
	}
	projection, _, _ = s.GetV3SessionProjection(session.ID)
	clear.ClientRequestID = "while-running"
	clear.ExpectedLastEventSeq = &projection.LastEventSeq
	if _, err := s.ApplyV3SessionMutation(clear); err != ErrSessionContextClearConflict {
		t.Fatalf("active run rejection: %v", err)
	}
	current, _, _ := s.GetActiveExecutionEpoch(session.ID)
	if current.Ordinal != after.Ordinal {
		t.Fatal("active run context changed")
	}
}
