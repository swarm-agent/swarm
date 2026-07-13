package pebblestore

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

func TestExecutionEpochInitialAndConcurrentBoundaryAreAtomic(t *testing.T) {
	telemetryBefore := SnapshotExecutionEpochTelemetry()
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	created, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "epoch-session", UserID: "user", AccountScopeID: "account", IdempotencyKey: "create", RequestHash: "create-hash", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "epoch-session", WorkspacePath: "/workspace", WorkspaceName: "workspace"}, NowUnixMs: 100})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Event.EpochID == "" {
		t.Fatal("create event has no epoch")
	}
	initial, ok, err := sessions.GetActiveExecutionEpoch("epoch-session")
	if err != nil || !ok || initial.Ordinal != 1 {
		t.Fatalf("initial epoch ok=%v err=%v epoch=%+v", ok, err, initial)
	}

	const workers = 12
	results := make(chan BeginExecutionEpochResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := sessions.BeginExecutionEpoch(BeginExecutionEpochInput{SessionID: "epoch-session", UserID: "user", AccountScopeID: "account", ClientRequestID: "cp-1", PayloadHash: "same", PlanID: "plan-1", CheckpointID: "cp-1", Reason: "checkpoint", NowUnixMs: 200})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("begin epoch: %v", err)
	}
	var epochID string
	fresh := 0
	for result := range results {
		if epochID == "" {
			epochID = result.Epoch.EpochID
		}
		if result.Epoch.EpochID != epochID {
			t.Fatalf("different epochs: %q != %q", result.Epoch.EpochID, epochID)
		}
		if !result.Replayed {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh boundaries = %d, want 1", fresh)
	}
	active, ok, err := sessions.GetActiveExecutionEpoch("epoch-session")
	if err != nil || !ok || active.Ordinal != 2 || active.ParentEpochID != initial.EpochID {
		t.Fatalf("active epoch ok=%v err=%v epoch=%+v", ok, err, active)
	}
	projection, _, _ := sessions.GetV3SessionProjection("epoch-session")
	if projection.LastEventSeq != 2 {
		t.Fatalf("last root seq = %d, want 2", projection.LastEventSeq)
	}
	events, err := sessions.ListV3SessionEvents("epoch-session", 0, 10)
	if err != nil || len(events) != 2 || events[1].EventType != ExecutionEpochBoundaryEventType {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	telemetryAfter := SnapshotExecutionEpochTelemetry()
	if telemetryAfter.BoundaryCalls-telemetryBefore.BoundaryCalls != workers {
		t.Fatalf("boundary telemetry calls = %d, want %d", telemetryAfter.BoundaryCalls-telemetryBefore.BoundaryCalls, workers)
	}
	if telemetryAfter.PointReads <= telemetryBefore.PointReads || telemetryAfter.DecodeCalls <= telemetryBefore.DecodeCalls || telemetryAfter.BatchCommits <= telemetryBefore.BatchCommits {
		t.Fatalf("epoch telemetry did not observe point read/decode/batch: before=%+v after=%+v", telemetryBefore, telemetryAfter)
	}
}

func TestExecutionEpochTracksRangeSealsAndRepairsBoundedIndex(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	created, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "range-session", UserID: "user", AccountScopeID: "account", IdempotencyKey: "create", RequestHash: "create-hash", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "range-session", WorkspacePath: "/workspace", WorkspaceName: "workspace"}, NowUnixMs: 100})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "range-session", UserID: "user", AccountScopeID: "account", IdempotencyKey: "message", RequestHash: "message-hash", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{ID: "m-1", SessionID: "range-session", Role: "user", Content: "current"}, NowUnixMs: 110})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	active, ok, err := sessions.GetActiveExecutionEpoch("range-session")
	if err != nil || !ok || active.FirstRootSeq != created.Event.Seq || active.LastRootSeq != 2 {
		t.Fatalf("active range ok=%v err=%v epoch=%+v", ok, err, active)
	}
	sealed, err := sessions.SealExecutionEpoch(SealExecutionEpochInput{SessionID: "range-session", EpochID: active.EpochID, NowUnixMs: 120})
	if err != nil || sealed.Status != ExecutionEpochStatusSealed || sealed.LastRootSeq != 2 {
		t.Fatalf("seal err=%v epoch=%+v", err, sealed)
	}
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "range-session", EpochID: active.EpochID, UserID: "user", AccountScopeID: "account", IdempotencyKey: "late", RequestHash: "late-hash", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{ID: "m-late", SessionID: "range-session", Role: "assistant", Content: "late"}, NowUnixMs: 130}); err == nil {
		t.Fatal("sealed epoch accepted a late mutation")
	}

	began, err := sessions.BeginExecutionEpoch(BeginExecutionEpochInput{SessionID: "range-session", UserID: "user", AccountScopeID: "account", ClientRequestID: "cp-2", PayloadHash: "cp-2-hash", CheckpointID: "cp-2", NowUnixMs: 140})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := store.Delete(KeyExecutionEpochActive("range-session")); err != nil {
		t.Fatalf("delete active index: %v", err)
	}
	repaired, err := sessions.RepairActiveExecutionEpoch("range-session", began.Epoch.EpochID)
	if err != nil || repaired.EpochID != began.Epoch.EpochID || repaired.LastRootSeq != began.Event.Seq {
		t.Fatalf("repair err=%v epoch=%+v", err, repaired)
	}
}

func TestSealExecutionEpochRemovesActiveIndex(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "seal-index-session")
	active, ok, err := sessions.GetActiveExecutionEpoch("seal-index-session")
	if err != nil || !ok {
		t.Fatalf("get active: ok=%v err=%v", ok, err)
	}
	sealed, err := sessions.SealExecutionEpoch(SealExecutionEpochInput{SessionID: active.SessionID, EpochID: active.EpochID})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if sealedActive, ok, err := sessions.GetActiveExecutionEpoch(active.SessionID); err != nil || ok {
		t.Fatalf("sealed epoch remained active: ok=%v err=%v epoch=%+v", ok, err, sealedActive)
	}
	began, err := sessions.BeginExecutionEpoch(BeginExecutionEpochInput{SessionID: active.SessionID, UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "after-seal", PayloadHash: "after-seal-hash"})
	if err != nil || began.Epoch.ParentEpochID != sealed.EpochID || began.Epoch.Ordinal != sealed.Ordinal+1 {
		t.Fatalf("begin after seal: err=%v result=%+v", err, began)
	}
}

func TestBeginExecutionEpochRejectsIndexCollisionsWithoutOverwrite(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "collision-session")
	if err := store.PutBytes(KeyExecutionEpochOrdinal("collision-session", 2), []byte("foreign-epoch")); err != nil {
		t.Fatalf("seed ordinal collision: %v", err)
	}
	_, err := sessions.BeginExecutionEpoch(BeginExecutionEpochInput{SessionID: "collision-session", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "collision", PayloadHash: "collision-hash", EpochID: "successor"})
	if err == nil {
		t.Fatal("expected ordinal collision")
	}
	active, ok, getErr := sessions.GetActiveExecutionEpoch("collision-session")
	if getErr != nil || !ok || active.Ordinal != 1 || active.Status != ExecutionEpochStatusActive {
		t.Fatalf("active changed after collision: ok=%v err=%v epoch=%+v", ok, getErr, active)
	}
	if raw, ok, getErr := store.GetBytes(KeyExecutionEpochOrdinal("collision-session", 2)); getErr != nil || !ok || string(raw) != "foreign-epoch" {
		t.Fatalf("ordinal collision overwritten: ok=%v err=%v value=%q", ok, getErr, raw)
	}
}

func TestBeginExecutionEpochAllowsDistinctRunsAfterSameCheckpoint(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "repeated-followup-session")

	first, err := sessions.BeginExecutionEpoch(BeginExecutionEpochInput{
		SessionID: "repeated-followup-session", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "followup-message-1", PayloadHash: "followup-hash-1",
		Reason: "post_checkpoint_followup", PlanID: "plan-1", CheckpointID: "followup-1",
		AttemptID: "followup-1:attempt-1", RunID: "run-followup-message-1",
		TriggerMessage: &MessageSnapshot{ID: "followup-message-1", Role: "user", Content: "first reply"}, NowUnixMs: 200,
	})
	if err != nil {
		t.Fatalf("begin first follow-up epoch: %v", err)
	}
	second, err := sessions.BeginExecutionEpoch(BeginExecutionEpochInput{
		SessionID: "repeated-followup-session", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "followup-message-2", PayloadHash: "followup-hash-2",
		Reason: "post_checkpoint_followup", PlanID: "plan-1", CheckpointID: "followup-1",
		AttemptID: "followup-1:attempt-1", RunID: "run-followup-message-2",
		TriggerMessage: &MessageSnapshot{ID: "followup-message-2", Role: "user", Content: "second reply"}, NowUnixMs: 300,
	})
	if err != nil {
		t.Fatalf("begin second follow-up epoch: %v", err)
	}
	if second.Replayed || second.Epoch.EpochID == first.Epoch.EpochID || second.Epoch.Ordinal != first.Epoch.Ordinal+1 {
		t.Fatalf("second follow-up did not create a distinct successor: first=%+v second=%+v", first.Epoch, second.Epoch)
	}
	if second.Epoch.Boundary.AttemptID != "followup-1:attempt-1" || second.Epoch.Boundary.RunID != "run-followup-message-2" {
		t.Fatalf("second boundary identity = %+v", second.Epoch.Boundary)
	}
}

func TestBeginExecutionEpochMigratesLegacyPostCheckpointBoundary(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "legacy-followup-session")

	first, err := sessions.BeginExecutionEpoch(BeginExecutionEpochInput{
		SessionID: "legacy-followup-session", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "legacy-followup-message-1", PayloadHash: "legacy-followup-hash-1",
		Reason: "post_checkpoint_followup", PlanID: "plan-1", CheckpointID: "followup-1",
		RunID: "run-legacy-followup-message-1", TriggerMessage: &MessageSnapshot{ID: "legacy-followup-message-1", Role: "user", Content: "first reply"}, NowUnixMs: 200,
	})
	if err != nil {
		t.Fatalf("begin first follow-up epoch: %v", err)
	}
	newKey := KeyExecutionEpochBoundary(first.Epoch.SessionID, first.Epoch.Boundary.PlanID, first.Epoch.Boundary.CheckpointID, first.Epoch.Boundary.AttemptID, first.Epoch.Boundary.Reason, first.Epoch.Boundary.RunID)
	legacyKey := executionEpochBoundaryLegacyKey(first.Epoch.SessionID, first.Epoch.Boundary.PlanID, first.Epoch.Boundary.CheckpointID, first.Epoch.Boundary.AttemptID, first.Epoch.Boundary.Reason)
	batch := store.db.NewBatch()
	defer batch.Close()
	if err := batch.Delete([]byte(newKey), nil); err != nil {
		t.Fatalf("delete new boundary index: %v", err)
	}
	if err := batch.Set([]byte(legacyKey), []byte(first.Epoch.EpochID), nil); err != nil {
		t.Fatalf("seed legacy boundary index: %v", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatalf("commit legacy boundary fixture: %v", err)
	}

	second, err := sessions.BeginExecutionEpoch(BeginExecutionEpochInput{
		SessionID: "legacy-followup-session", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "legacy-followup-message-2", PayloadHash: "legacy-followup-hash-2",
		Reason: "post_checkpoint_followup", PlanID: "plan-1", CheckpointID: "followup-1",
		RunID:          "run-legacy-followup-message-2",
		TriggerMessage: &MessageSnapshot{ID: "legacy-followup-message-2", Role: "user", Content: "second reply"}, NowUnixMs: 300,
	})
	if err != nil {
		t.Fatalf("begin successor after legacy boundary: %v", err)
	}
	if second.Epoch.EpochID == first.Epoch.EpochID || second.Epoch.ParentEpochID != first.Epoch.EpochID {
		t.Fatalf("legacy successor identity: first=%+v second=%+v", first.Epoch, second.Epoch)
	}
}

func TestBeginExecutionEpochFaultIsAllOrNothingAndTriggerIsAtomic(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "fault-session")
	injected := errors.New("injected epoch commit failure")
	store.sessionMutations.beforeExecutionEpochCommit = func(string) error { return injected }
	input := BeginExecutionEpochInput{SessionID: "fault-session", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "fault", PayloadHash: "fault-hash", PlanID: "plan", CheckpointID: "cp", RunID: "run", TriggerMessage: &MessageSnapshot{ID: "trigger", Role: "user", Content: "start"}, NowUnixMs: 200}
	if _, err := sessions.BeginExecutionEpoch(input); !errors.Is(err, injected) {
		t.Fatalf("fault error = %v", err)
	}
	active, ok, err := sessions.GetActiveExecutionEpoch(input.SessionID)
	if err != nil || !ok || active.Ordinal != 1 || active.Status != ExecutionEpochStatusActive {
		t.Fatalf("partial active after fault: ok=%v err=%v epoch=%+v", ok, err, active)
	}
	if events, err := sessions.ListV3SessionEvents(input.SessionID, 0, 10); err != nil || len(events) != 1 {
		t.Fatalf("partial events after fault: err=%v events=%+v", err, events)
	}
	store.sessionMutations.beforeExecutionEpochCommit = nil
	result, err := sessions.BeginExecutionEpoch(input)
	if err != nil {
		t.Fatalf("retry transition: %v", err)
	}
	if result.TriggerEvent == nil || result.TriggerOutbox == nil || result.Epoch.FirstRootSeq != 2 || result.Epoch.LastRootSeq != 3 {
		t.Fatalf("compound result = %+v", result)
	}
	if events, err := sessions.ListV3SessionEvents(input.SessionID, 0, 10); err != nil || len(events) != 3 || events[1].EventType != ExecutionEpochBoundaryEventType || events[2].EventType != "session.message.appended" {
		t.Fatalf("compound events: err=%v events=%+v", err, events)
	}
}

func TestBeginExecutionEpochReplayReturnsCompoundTrigger(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "trigger-replay-session")
	input := BeginExecutionEpochInput{SessionID: "trigger-replay-session", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "trigger-replay", PayloadHash: "trigger-replay-hash", Reason: "post_checkpoint_followup", TriggerMessage: &MessageSnapshot{ID: "trigger-replay-message", Role: "user", Content: "continue"}, NowUnixMs: 200}
	first, err := sessions.BeginExecutionEpoch(input)
	if err != nil || first.TriggerEvent == nil || first.TriggerOutbox == nil {
		t.Fatalf("first compound transition: err=%v result=%+v", err, first)
	}
	replayed, err := sessions.BeginExecutionEpoch(input)
	if err != nil || !replayed.Replayed || replayed.TriggerEvent == nil || replayed.TriggerOutbox == nil {
		t.Fatalf("replayed compound transition: err=%v result=%+v", err, replayed)
	}
	if replayed.Event.Seq != first.Event.Seq || replayed.TriggerEvent.Seq != first.TriggerEvent.Seq || replayed.Projection.LastEventSeq != first.TriggerEvent.Seq {
		t.Fatalf("replayed compound rows disagree: first=%+v replayed=%+v", first, replayed)
	}
}

func TestBeginExecutionEpochReplayRepublishesDurableOutboxHead(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "publish-session")
	failed := false
	store.sessionMutations.publishOutboxHead = func(store *Store, target uint64) error {
		if !failed {
			failed = true
			return errors.New("injected head failure")
		}
		return store.db.Set([]byte(KeyV3RealtimeOutboxSequence()), uint64ToBytes(target), pebble.Sync)
	}
	input := BeginExecutionEpochInput{SessionID: "publish-session", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "publish", PayloadHash: "publish-hash", NowUnixMs: 200}
	if _, err := sessions.BeginExecutionEpoch(input); err == nil {
		t.Fatal("expected post-commit head publication failure")
	}
	result, err := sessions.BeginExecutionEpoch(input)
	if err != nil || !result.Replayed || result.Outbox.EndpointSeq == 0 {
		t.Fatalf("replay failed: err=%v result=%+v", err, result)
	}
	cursor, err := sessions.CurrentV3RealtimeOutboxCursor()
	if err != nil || cursor != result.Outbox.EndpointCursor {
		t.Fatalf("cursor=%q want=%q err=%v", cursor, result.Outbox.EndpointCursor, err)
	}
}

func TestBeginExecutionEpochAcceptanceUsesFixedOperationsForLegacyAndIndexedPaths(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	if err := sessions.CreateSession(SessionSnapshot{ID: "fixed-work-session", UserID: "user", AccountScopeID: "account", WorkspacePath: "/workspace", WorkspaceName: "workspace"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	seedExecutionEpochBenchmarkHistory(t, store, "fixed-work-session", 1_000)

	assertOperations := func(name string, wantPointReads, wantDecodes uint64, input BeginExecutionEpochInput) {
		t.Helper()
		before := SnapshotExecutionEpochTelemetry()
		if _, err := sessions.BeginExecutionEpoch(input); err != nil {
			t.Fatalf("%s begin epoch: %v", name, err)
		}
		after := SnapshotExecutionEpochTelemetry()
		if got := after.PointReads - before.PointReads; got != wantPointReads {
			t.Fatalf("%s point reads = %d, want %d", name, got, wantPointReads)
		}
		if got := after.DecodeCalls - before.DecodeCalls; got != wantDecodes {
			t.Fatalf("%s decodes = %d, want %d", name, got, wantDecodes)
		}
		if got := after.IteratorReads - before.IteratorReads; got != 0 {
			t.Fatalf("%s iterators = %d, want 0", name, got)
		}
		if got := after.BatchCommits - before.BatchCommits; got != 1 {
			t.Fatalf("%s batch commits = %d, want 1", name, got)
		}
	}

	assertOperations("legacy", 3, 0, BeginExecutionEpochInput{SessionID: "fixed-work-session", UserID: "user", AccountScopeID: "account", ClientRequestID: "legacy-boundary", PayloadHash: "legacy-hash", Reason: "legacy"})
	assertOperations("indexed", 2, 1, BeginExecutionEpochInput{SessionID: "fixed-work-session", UserID: "user", AccountScopeID: "account", ClientRequestID: "indexed-boundary", PayloadHash: "indexed-hash", Reason: "indexed"})
}

func TestExecutionEpochMessageRangeIsSnapshotBoundedAndLifecycleStateIsFixedSize(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "bounded-recovery-session")
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "bounded-recovery-session", UserID: "user", AccountScopeID: "account", IdempotencyKey: "m1", RequestHash: "m1", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{ID: "m1", Role: "user", Content: "inside"}, NowUnixMs: 2}); err != nil {
		t.Fatalf("append inside: %v", err)
	}
	first, ok, err := sessions.GetActiveExecutionEpoch("bounded-recovery-session")
	if err != nil || !ok {
		t.Fatalf("get first epoch: ok=%v err=%v", ok, err)
	}
	if _, err := sessions.BeginExecutionEpoch(BeginExecutionEpochInput{SessionID: "bounded-recovery-session", UserID: "user", AccountScopeID: "account", ClientRequestID: "next", PayloadHash: "next", Reason: "checkpoint"}); err != nil {
		t.Fatalf("begin next epoch: %v", err)
	}
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "bounded-recovery-session", UserID: "user", AccountScopeID: "account", IdempotencyKey: "m2", RequestHash: "m2", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{ID: "m2", Role: "user", Content: "outside"}, NowUnixMs: 3}); err != nil {
		t.Fatalf("append outside: %v", err)
	}
	sealed, messages, err := sessions.ListExecutionEpochMessages("bounded-recovery-session", first.EpochID, 500)
	if err != nil {
		t.Fatalf("read sealed range: %v", err)
	}
	if sealed.Status != ExecutionEpochStatusSealed || sealed.LastRootSeq != 2 || len(messages) != 1 || messages[0].ID != "m1" {
		t.Fatalf("sealed range leaked later messages: epoch=%+v messages=%+v", sealed, messages)
	}

	state := ExecutionProviderLifecycleState{SessionID: first.SessionID, EpochID: first.EpochID, Provider: "codex", Model: "gpt-5", ConfigurationHash: "config", ProviderLineageID: "lineage", ContextBranchID: "branch", ProviderCacheKey: "cache", SessionAffinityKey: "affinity", TransportAffinityKey: "transport", BoundaryReason: "epoch_fresh_context", UpdatedAt: 4}
	if err := sessions.PutExecutionProviderLifecycleState(state); err != nil {
		t.Fatalf("put provider lifecycle: %v", err)
	}
	got, ok, err := sessions.GetExecutionProviderLifecycleState(first.SessionID, first.EpochID)
	if err != nil || !ok || got.Version != ExecutionProviderLifecycleStateVersion || got.ProviderLineageID != "lineage" {
		t.Fatalf("get provider lifecycle: ok=%v err=%v state=%+v", ok, err, got)
	}
	raw, ok, err := store.GetBytes(KeyExecutionProviderLifecycleState(first.SessionID, first.EpochID))
	if err != nil || !ok || len(raw) > 4096 {
		t.Fatalf("provider lifecycle is not fixed-size: ok=%v err=%v bytes=%d", ok, err, len(raw))
	}
}

func TestExecutionEpochTelemetryUsesOnlyAnonymousAggregates(t *testing.T) {
	telemetryType := reflect.TypeOf(ExecutionEpochTelemetry{})
	for i := 0; i < telemetryType.NumField(); i++ {
		field := telemetryType.Field(i)
		if field.Type.Kind() == reflect.String || field.Type.Kind() == reflect.Slice || field.Type.Kind() == reflect.Map {
			t.Fatalf("telemetry field %s can carry identifying or transcript data: %s", field.Name, field.Type)
		}
	}
	before := SnapshotExecutionEpochTelemetry()
	ObserveExecutionEpochQueueWait(time.Now().Add(-time.Millisecond))
	ObserveExecutionEpochSocketDial(time.Now().Add(-time.Millisecond))
	ObserveExecutionEpochSocketReuse()
	ObserveExecutionEpochSocketReset()
	ObserveExecutionEpochProviderSend(time.Now().Add(-time.Millisecond))
	ObserveExecutionEpochFirstEvent(time.Now().Add(-time.Millisecond))
	after := SnapshotExecutionEpochTelemetry()
	if after.QueueWaits != before.QueueWaits+1 || after.SocketDials != before.SocketDials+1 || after.SocketReuses != before.SocketReuses+1 || after.SocketResets != before.SocketResets+1 || after.ProviderSends != before.ProviderSends+1 || after.FirstEvents != before.FirstEvents+1 {
		t.Fatalf("anonymous telemetry counters did not advance: before=%+v after=%+v", before, after)
	}
}

func TestBeginExecutionEpochLazilyDescribesLegacyPrefixWithoutHistoryRead(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	if err := sessions.CreateSession(SessionSnapshot{ID: "legacy-session", UserID: "user", AccountScopeID: "account", WorkspacePath: "/workspace", WorkspaceName: "workspace"}); err != nil {
		t.Fatalf("create legacy session: %v", err)
	}
	for i := 1; i <= 3; i++ {
		_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "legacy-session", UserID: "user", AccountScopeID: "account", IdempotencyKey: fmt.Sprintf("message-%d", i), RequestHash: fmt.Sprintf("hash-%d", i), Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{ID: fmt.Sprintf("m-%d", i), SessionID: "legacy-session", Role: "user", Content: "legacy"}, NowUnixMs: int64(i)})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	result, err := sessions.BeginExecutionEpoch(BeginExecutionEpochInput{SessionID: "legacy-session", UserID: "user", AccountScopeID: "account", ClientRequestID: "boundary", PayloadHash: "boundary-hash", Reason: "legacy transition", NowUnixMs: 10})
	if err != nil {
		t.Fatalf("begin epoch: %v", err)
	}
	if result.Predecessor.Boundary.LegacyPrefix == nil || result.Predecessor.Boundary.LegacyPrefix.LastRootSeq != 3 {
		t.Fatalf("legacy predecessor = %+v", result.Predecessor)
	}
	if result.Event.Seq != 4 || result.Epoch.FirstRootSeq != 4 {
		t.Fatalf("boundary event/epoch = %+v / %+v", result.Event, result.Epoch)
	}
}
