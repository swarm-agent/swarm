package pebblestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func planRuntimeTestCommand(expected uint64, requestID, hash, eventType, status string) PlanExecutionCommand {
	return PlanExecutionCommand{
		SessionID:            "session/With Case",
		PlanID:               "plan/With Case",
		AccountScopeID:       "account-1",
		ExpectedExecutionSeq: expected,
		ClientRequestID:      requestID,
		PayloadHash:          hash,
		DefinitionRevision:   1,
		EventType:            eventType,
		CheckpointID:         "cp/One",
		NextSummary:          PlanExecutionSummary{Status: status, ActiveCheckpointID: "cp/One"},
		CheckpointChange:     &CheckpointExecution{CheckpointID: "cp/One", Status: status},
		SubtaskChanges:       []SubtaskExecution{{CheckpointID: "cp/One", SubtaskID: "task/One", Status: status}},
		NextAction:           "none",
		NowUnixMs:            int64(100 + expected),
	}
}

func TestPlanRuntimeKeyspaceIsPrefixSafeAndOrdered(t *testing.T) {
	first := KeyPlanExecutionEvent("session/With Case", "plan/With Case", 2)
	second := KeyPlanExecutionEvent("session/With Case", "plan/With Case", 10)
	if first >= second {
		t.Fatalf("fixed-width keys do not sort: %q >= %q", first, second)
	}
	if strings.Contains(first, "session/With Case") || strings.Contains(first, "plan/With Case") {
		t.Fatalf("composite IDs were not encoded: %q", first)
	}
	if !strings.HasPrefix(first, PlanExecutionEventPrefix("session/With Case", "plan/With Case")) {
		t.Fatalf("event key %q is outside its prefix", first)
	}
}

func TestAppendPlanExecutionIsAtomicOrderedAndIdempotent(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session/With Case")
	before := SnapshotPlanRuntimeTelemetry()

	first := planRuntimeTestCommand(0, "request-1", "hash-1", "plan.execution_activated", "in_progress")
	result, err := sessions.AppendPlanExecution(first)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if result.ExecutionSeq != 1 || result.Replayed {
		t.Fatalf("result = %+v", result)
	}
	summary, ok, err := sessions.GetPlanExecutionSummary(first.SessionID, first.PlanID)
	if err != nil || !ok || summary.ExecutionSeq != 1 {
		t.Fatalf("summary ok=%v err=%v value=%+v", ok, err, summary)
	}
	checkpoint, ok, err := sessions.GetPlanCheckpointExecution(first.SessionID, first.PlanID, "cp/One")
	if err != nil || !ok || checkpoint.ExecutionSeq != 1 {
		t.Fatalf("checkpoint ok=%v err=%v value=%+v", ok, err, checkpoint)
	}
	subtask, ok, err := sessions.GetPlanSubtaskExecution(first.SessionID, first.PlanID, "cp/One", "task/One")
	if err != nil || !ok || subtask.ExecutionSeq != 1 {
		t.Fatalf("subtask ok=%v err=%v value=%+v", ok, err, subtask)
	}

	replayed, err := sessions.AppendPlanExecution(first)
	if err != nil || !replayed.Replayed || replayed.ExecutionSeq != 1 {
		t.Fatalf("replay err=%v result=%+v", err, replayed)
	}
	conflictingPayload := first
	conflictingPayload.PayloadHash = "different"
	if _, err := sessions.AppendPlanExecution(conflictingPayload); !errors.Is(err, ErrPlanRuntimeIdempotencyConflict) {
		t.Fatalf("payload reuse err = %v", err)
	}
	stale := planRuntimeTestCommand(0, "request-2", "hash-2", "plan.subtask_focused", "in_progress")
	if _, err := sessions.AppendPlanExecution(stale); !errors.Is(err, ErrPlanRuntimeExecutionConflict) {
		t.Fatalf("stale sequence err = %v", err)
	}

	page, err := sessions.ListPlanExecutionEventsAfter(first.SessionID, first.PlanID, 0, 10)
	if err != nil || len(page.Events) != 1 || page.Events[0].ExecutionSeq != 1 {
		t.Fatalf("events err=%v page=%+v", err, page)
	}
	after := SnapshotPlanRuntimeTelemetry()
	if after.Mutations-before.Mutations != 1 || after.Replays-before.Replays != 1 || after.Conflicts-before.Conflicts != 2 {
		t.Fatalf("telemetry before=%+v after=%+v", before, after)
	}
	if after.EventBytes == before.EventBytes || after.ProjectionBytes == before.ProjectionBytes || after.OutboxBytes == before.OutboxBytes || after.LogicalBytes == before.LogicalBytes || after.KeysPerCommitTotal == before.KeysPerCommitTotal {
		t.Fatalf("write amplification telemetry was not recorded: before=%+v after=%+v", before, after)
	}
}

func TestAppendPlanExecutionPreCommitFailureLeavesNoPartialState(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session/With Case")
	beforeProjection, ok, err := sessions.GetV3SessionProjection("session/With Case")
	if err != nil || !ok {
		t.Fatalf("projection before fault: ok=%v err=%v", ok, err)
	}
	beforeCursor, err := sessions.CurrentV3RealtimeOutboxCursor()
	if err != nil {
		t.Fatalf("cursor before fault: %v", err)
	}
	store.sessionMutations.beforePlanRuntimeCommit = func(_, _ string) error { return errors.New("injected") }
	command := planRuntimeTestCommand(0, "request-fail", "hash-fail", "plan.execution_activated", "in_progress")
	if _, err := sessions.AppendPlanExecution(command); err == nil {
		t.Fatal("append unexpectedly succeeded")
	}
	store.sessionMutations.beforePlanRuntimeCommit = nil
	if _, ok, err := sessions.GetPlanExecutionSummary(command.SessionID, command.PlanID); err != nil || ok {
		t.Fatalf("summary after failed commit ok=%v err=%v", ok, err)
	}
	if _, ok, err := sessions.GetPlanExecutionEvent(command.SessionID, command.PlanID, 1); err != nil || ok {
		t.Fatalf("event after failed commit ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.GetBytes(KeyPlanExecutionOutbox(command.SessionID, command.PlanID, 1)); err != nil || ok {
		t.Fatalf("outbox after failed commit ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.GetBytes(KeyPlanExecutionIdempotency(command.AccountScopeID, command.SessionID, command.PlanID, command.ClientRequestID)); err != nil || ok {
		t.Fatalf("idempotency after failed commit ok=%v err=%v", ok, err)
	}
	if _, ok, err := sessions.GetPlanCheckpointExecution(command.SessionID, command.PlanID, "cp/One"); err != nil || ok {
		t.Fatalf("checkpoint projection after failed commit ok=%v err=%v", ok, err)
	}
	if _, ok, err := sessions.GetPlanSubtaskExecution(command.SessionID, command.PlanID, "cp/One", "task/One"); err != nil || ok {
		t.Fatalf("subtask projection after failed commit ok=%v err=%v", ok, err)
	}
	afterProjection, ok, err := sessions.GetV3SessionProjection(command.SessionID)
	if err != nil || !ok || afterProjection.LastEventSeq != beforeProjection.LastEventSeq || afterProjection.ProjectionHighWatermarkSeq != beforeProjection.ProjectionHighWatermarkSeq {
		t.Fatalf("root high-water changed after failed commit: before=%+v after=%+v ok=%v err=%v", beforeProjection, afterProjection, ok, err)
	}
	if events, err := sessions.ListV3SessionEvents(command.SessionID, beforeProjection.LastEventSeq, 10); err != nil || len(events) != 0 {
		t.Fatalf("root event became visible after failed commit: events=%+v err=%v", events, err)
	}
	if afterCursor, err := sessions.CurrentV3RealtimeOutboxCursor(); err != nil || afterCursor != beforeCursor {
		t.Fatalf("global outbox cursor changed after failed commit: before=%q after=%q err=%v", beforeCursor, afterCursor, err)
	}
}

func TestPlanExecutionOutboxReplayAndHydrationUseCompactProjectionState(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session")
	definition := PlanDefinitionWrite{
		Definition:  PlanDefinition{SessionID: "session", PlanID: "plan", DefinitionRevision: 1, Title: "Plan", CheckpointOrder: []string{"cp-stable"}},
		Checkpoints: []CheckpointDefinition{{CheckpointID: "cp-stable", Order: 1, Title: "Checkpoint", SubtaskOrder: []string{"st-stable"}}},
		Subtasks:    []SubtaskDefinition{{CheckpointID: "cp-stable", SubtaskID: "st-stable", Order: 1, Title: "Subtask"}},
	}
	if _, err := sessions.PutPlanDefinition(definition); err != nil {
		t.Fatal(err)
	}
	command := planRuntimeTestCommand(0, "delta-1", "hash-1", "plan.subtasks_completed", "in_progress")
	command.SessionID, command.PlanID = "session", "plan"
	command.CheckpointID = "cp-stable"
	command.CheckpointChange = &CheckpointExecution{CheckpointID: "cp-stable", Status: "in_progress"}
	command.SubtaskChanges = []SubtaskExecution{{CheckpointID: "cp-stable", SubtaskID: "st-stable", Status: "completed"}}
	if _, err := sessions.AppendPlanExecution(command); err != nil {
		t.Fatal(err)
	}

	page, err := sessions.ListPlanExecutionOutboxAfter("session", "plan", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].Protocol != PlanExecutionRealtimeProtocol || page.Records[0].Kind != PlanExecutionRealtimeKindDelta {
		t.Fatalf("unexpected compact outbox page: %+v", page)
	}
	raw, err := json.Marshal(page.Records[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("checkpoint_definitions")) || bytes.Contains(raw, []byte("active_plan")) {
		t.Fatalf("routine outbox embedded full plan state: %s", raw)
	}

	hydrated, ok, err := sessions.HydratePlanRuntime("session", "plan", 1)
	if err != nil || !ok {
		t.Fatalf("hydrate: ok=%v err=%v", ok, err)
	}
	if hydrated.Summary.ExecutionSeq != 1 || hydrated.CheckpointExecutions["cp-stable"].Status != "in_progress" || hydrated.SubtaskExecutions["cp-stable\x00st-stable"].Status != "completed" {
		t.Fatalf("unexpected materialized hydration: %+v", hydrated)
	}
}

func TestPlanExecutionRecoveryUsesCompatibleSnapshotAndTail(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session/With Case")
	first := planRuntimeTestCommand(0, "request-1", "hash-1", "plan.execution_activated", "in_progress")
	if _, err := sessions.AppendPlanExecution(first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	snapshot, err := sessions.MaterializePlanExecutionSnapshot(first.SessionID, first.PlanID, 1)
	if err != nil || snapshot.ExecutionSeq != 1 || snapshot.ContentHash == "" {
		t.Fatalf("snapshot err=%v value=%+v", err, snapshot)
	}
	second := planRuntimeTestCommand(1, "request-2", "hash-2", "plan.subtasks_completed", "completed")
	second.SubtaskChanges[0].CompletedAt = 200
	second.NextSummary.ActiveCheckpointID = ""
	if _, err := sessions.AppendPlanExecution(second); err != nil {
		t.Fatalf("append second: %v", err)
	}

	recovered, err := sessions.RecoverPlanExecution(first.SessionID, first.PlanID)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.SnapshotSeq != 1 || recovered.TailEvents != 1 || recovered.Summary.ExecutionSeq != 2 || recovered.Summary.Status != "completed" {
		t.Fatalf("recovery = %+v", recovered)
	}
	if recovered.Subtasks["cp/One\x00task/One"].Status != "completed" {
		t.Fatalf("subtask recovery = %+v", recovered.Subtasks)
	}
}

func TestPlanRuntimeDoesNotInterfereWithExecutionEpoch(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "epoch-independent")
	before, ok, err := sessions.GetActiveExecutionEpoch("epoch-independent")
	if err != nil || !ok {
		t.Fatalf("active epoch before ok=%v err=%v", ok, err)
	}
	command := planRuntimeTestCommand(0, "request-1", "hash-1", "plan.execution_activated", "in_progress")
	command.SessionID = "epoch-independent"
	if _, err := sessions.AppendPlanExecution(command); err != nil {
		t.Fatalf("append: %v", err)
	}
	after, ok, err := sessions.GetActiveExecutionEpoch("epoch-independent")
	if err != nil || !ok || after.EpochID != before.EpochID || after.LastRootSeq != before.LastRootSeq+1 {
		t.Fatalf("epoch identity changed or boundary did not advance exactly once: before=%+v after=%+v ok=%v err=%v", before, after, ok, err)
	}
	_, messages, err := sessions.ListExecutionEpochMessages("epoch-independent", before.EpochID, 10)
	if err != nil {
		t.Fatalf("load named epoch: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("passive progress leaked into provider message context: %+v", messages)
	}
}

func TestPlanRuntimeProgressDoesNotRewriteDefinitionOrLegacyPlan(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "definition-independent")
	definition, err := sessions.PutPlanDefinition(PlanDefinitionWrite{
		Definition:  PlanDefinition{SessionID: "definition-independent", PlanID: "plan", DefinitionRevision: 1, Title: strings.Repeat("definition bytes ", 256), CheckpointOrder: []string{"cp/One"}},
		Checkpoints: []CheckpointDefinition{{CheckpointID: "cp/One", SubtaskOrder: []string{"task/One"}}},
		Subtasks:    []SubtaskDefinition{{CheckpointID: "cp/One", SubtaskID: "task/One"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	definitionRaw, ok, err := store.GetBytes(KeyPlanDefinition(definition.SessionID, definition.PlanID, 1))
	if err != nil || !ok {
		t.Fatalf("definition before progress: ok=%v err=%v", ok, err)
	}
	for seq := uint64(0); seq < 8; seq++ {
		command := planRuntimeTestCommand(seq, fmt.Sprintf("request-%d", seq), fmt.Sprintf("hash-%d", seq), "plan.subtasks_completed", "in_progress")
		command.SessionID, command.PlanID = definition.SessionID, definition.PlanID
		if _, err := sessions.AppendPlanExecution(command); err != nil {
			t.Fatalf("append %d: %v", seq, err)
		}
	}
	afterRaw, ok, err := store.GetBytes(KeyPlanDefinition(definition.SessionID, definition.PlanID, 1))
	if err != nil || !ok || !bytes.Equal(definitionRaw, afterRaw) {
		t.Fatalf("immutable definition changed: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.GetBytes(KeyPlanDefinition(definition.SessionID, definition.PlanID, 2)); err != nil || ok {
		t.Fatalf("execution created definition revision 2: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.GetBytes(KeySessionPlan(definition.SessionID, definition.PlanID)); err != nil || ok {
		t.Fatalf("execution wrote legacy plan authority: ok=%v err=%v", ok, err)
	}
	events, err := sessions.ListV3SessionEvents(definition.SessionID, 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventType == "session.plan.saved" {
			t.Fatalf("execution emitted duplicate legacy event: %+v", event)
		}
	}
}

func TestPlanExecutionOutboxDetectsCursorGap(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "outbox-gap")
	for seq := uint64(0); seq < 3; seq++ {
		command := planRuntimeTestCommand(seq, fmt.Sprintf("request-%d", seq), fmt.Sprintf("hash-%d", seq), "plan.subtasks_completed", "in_progress")
		command.SessionID = "outbox-gap"
		if _, err := sessions.AppendPlanExecution(command); err != nil {
			t.Fatalf("append %d: %v", seq, err)
		}
	}
	if err := store.Delete(KeyPlanExecutionOutbox("outbox-gap", "plan/With Case", 2)); err != nil {
		t.Fatalf("delete middle outbox: %v", err)
	}
	if _, err := sessions.ListPlanExecutionOutboxAfter("outbox-gap", "plan/With Case", 0, 10); err == nil || !strings.Contains(err.Error(), "gap after sequence 1") {
		t.Fatalf("expected deterministic cursor gap, got %v", err)
	}
}

func TestPlanRuntimeRecoveryTailWorkIgnoresPreSnapshotHistory(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "bounded-tail")
	for seq := uint64(0); seq < 40; seq++ {
		command := planRuntimeTestCommand(seq, fmt.Sprintf("history-%d", seq), fmt.Sprintf("history-hash-%d", seq), "plan.subtasks_completed", "in_progress")
		command.SessionID = "bounded-tail"
		if _, err := sessions.AppendPlanExecution(command); err != nil {
			t.Fatalf("history append %d: %v", seq, err)
		}
	}
	if _, err := sessions.MaterializePlanExecutionSnapshot("bounded-tail", "plan/With Case", 40); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	for seq := uint64(40); seq < 43; seq++ {
		command := planRuntimeTestCommand(seq, fmt.Sprintf("tail-%d", seq), fmt.Sprintf("tail-hash-%d", seq), "plan.subtasks_completed", "in_progress")
		command.SessionID = "bounded-tail"
		if _, err := sessions.AppendPlanExecution(command); err != nil {
			t.Fatalf("tail append %d: %v", seq, err)
		}
	}
	recovered, err := sessions.RecoverPlanExecution("bounded-tail", "plan/With Case")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.SnapshotSeq != 40 || recovered.TailEvents != 3 || recovered.Summary.ExecutionSeq != 43 {
		t.Fatalf("recovery replayed work outside the snapshot tail: %+v", recovered)
	}
}

func createPlanRuntimeBenchmarkSession(b *testing.B, sessions *SessionStore, sessionID string) {
	b.Helper()
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: sessionID, UserID: "user-1", AccountScopeID: "account-1",
		IdempotencyKey: "create-" + sessionID, RequestHash: "hash-create-" + sessionID,
		Kind:      V3SessionMutationCreateSession,
		Session:   &SessionSnapshot{ID: sessionID, WorkspacePath: "/workspace", WorkspaceName: "workspace", Title: sessionID},
		NowUnixMs: 1000,
	})
	if err != nil {
		b.Fatalf("create benchmark session: %v", err)
	}
}

func BenchmarkPlanRuntimeConstantDeltaScaling(b *testing.B) {
	for _, scale := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("unrelated_definition_and_history_%d", scale), func(b *testing.B) {
			store := openV3SessionEventTestStore(b)
			sessions := NewSessionStore(store)
			createPlanRuntimeBenchmarkSession(b, sessions, "bench-session")
			checkpointOrder := make([]string, scale)
			checkpoints := make([]CheckpointDefinition, scale)
			subtasks := make([]SubtaskDefinition, scale)
			for i := 0; i < scale; i++ {
				checkpointID := fmt.Sprintf("cp-%06d", i)
				subtaskID := fmt.Sprintf("task-%06d", i)
				checkpointOrder[i] = checkpointID
				checkpoints[i] = CheckpointDefinition{CheckpointID: checkpointID, Order: i + 1, Title: strings.Repeat("definition-only ", 16), SubtaskOrder: []string{subtaskID}}
				subtasks[i] = SubtaskDefinition{CheckpointID: checkpointID, SubtaskID: subtaskID, Order: 1, Title: strings.Repeat("unrelated ", 16)}
			}
			if _, err := sessions.PutPlanDefinition(PlanDefinitionWrite{Definition: PlanDefinition{SessionID: "bench-session", PlanID: "bench-plan", DefinitionRevision: 1, Title: strings.Repeat("large definition ", scale), CheckpointOrder: checkpointOrder}, Checkpoints: checkpoints, Subtasks: subtasks}); err != nil {
				b.Fatal(err)
			}
			// Scale unrelated session history without putting it in the plan delta.
			for i := 0; i < scale; i++ {
				if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "bench-session", UserID: "user-1", AccountScopeID: "account-1", IdempotencyKey: fmt.Sprintf("history-%d", i), RequestHash: fmt.Sprintf("history-hash-%d", i), Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{ID: fmt.Sprintf("message-%d", i), SessionID: "bench-session", Role: "user", Content: "unrelated history"}}); err != nil {
					b.Fatal(err)
				}
			}
			var expected uint64
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				before := SnapshotPlanRuntimeTelemetry()
				command := planRuntimeTestCommand(expected, fmt.Sprintf("bench-%d-%d", scale, i), fmt.Sprintf("bench-hash-%d-%d", scale, i), "plan.subtasks_completed", "in_progress")
				command.SessionID, command.PlanID = "bench-session", "bench-plan"
				command.CheckpointID = checkpointOrder[0]
				command.CheckpointChange = &CheckpointExecution{CheckpointID: checkpointOrder[0], Status: "in_progress"}
				command.SubtaskChanges = []SubtaskExecution{{CheckpointID: checkpointOrder[0], SubtaskID: subtasks[0].SubtaskID, Status: "completed"}}
				if _, err := sessions.AppendPlanExecution(command); err != nil {
					b.Fatal(err)
				}
				after := SnapshotPlanRuntimeTelemetry()
				b.ReportMetric(float64(after.EventBytes-before.EventBytes), "event-bytes/op")
				b.ReportMetric(float64(after.OutboxBytes-before.OutboxBytes), "outbox-bytes/op")
				b.ReportMetric(float64(after.ResultBytes-before.ResultBytes), "result-bytes/op")
				b.ReportMetric(float64(after.LogicalBytes-before.LogicalBytes), "logical-bytes/op")
				b.ReportMetric(float64(after.KeysPerCommitTotal-before.KeysPerCommitTotal), "keys/op")
				expected++
			}
		})
	}
}

func BenchmarkPlanRuntimeRecoveryFixedTail(b *testing.B) {
	for _, history := range []int{16, 256, 1024} {
		b.Run(fmt.Sprintf("prior_events_%d_tail_4", history), func(b *testing.B) {
			store := openV3SessionEventTestStore(b)
			sessions := NewSessionStore(store)
			createPlanRuntimeBenchmarkSession(b, sessions, "recovery-bench")
			for seq := 0; seq < history; seq++ {
				command := planRuntimeTestCommand(uint64(seq), fmt.Sprintf("history-%d", seq), fmt.Sprintf("hash-%d", seq), "plan.subtasks_completed", "in_progress")
				command.SessionID = "recovery-bench"
				if _, err := sessions.AppendPlanExecution(command); err != nil {
					b.Fatal(err)
				}
			}
			if _, err := sessions.MaterializePlanExecutionSnapshot("recovery-bench", "plan/With Case", uint64(history)); err != nil {
				b.Fatal(err)
			}
			for seq := history; seq < history+4; seq++ {
				command := planRuntimeTestCommand(uint64(seq), fmt.Sprintf("tail-%d", seq), fmt.Sprintf("tail-hash-%d", seq), "plan.subtasks_completed", "in_progress")
				command.SessionID = "recovery-bench"
				if _, err := sessions.AppendPlanExecution(command); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				recovered, err := sessions.RecoverPlanExecution("recovery-bench", "plan/With Case")
				if err != nil || recovered.TailEvents != 4 {
					b.Fatalf("recovery err=%v value=%+v", err, recovered)
				}
				b.ReportMetric(float64(recovered.TailEvents), "tail-events/op")
				b.ReportMetric(float64(recovered.TailBytes), "tail-bytes/op")
			}
		})
	}
}
