package pebblestore

import (
	"fmt"
	"sync"
	"testing"
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

func TestBeginExecutionEpochAcceptanceUsesFixedOperationsForLegacyAndIndexedPaths(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	if err := sessions.CreateSession(SessionSnapshot{ID: "fixed-work-session", UserID: "user", AccountScopeID: "account", WorkspacePath: "/workspace", WorkspaceName: "workspace"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	seedExecutionEpochBenchmarkHistory(t, store, "fixed-work-session", 1_000)

	assertOperations := func(name string, wantDecodes uint64, input BeginExecutionEpochInput) {
		t.Helper()
		before := SnapshotExecutionEpochTelemetry()
		if _, err := sessions.BeginExecutionEpoch(input); err != nil {
			t.Fatalf("%s begin epoch: %v", name, err)
		}
		after := SnapshotExecutionEpochTelemetry()
		if got := after.PointReads - before.PointReads; got != 1 {
			t.Fatalf("%s point reads = %d, want 1", name, got)
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

	assertOperations("legacy", 0, BeginExecutionEpochInput{SessionID: "fixed-work-session", UserID: "user", AccountScopeID: "account", ClientRequestID: "legacy-boundary", PayloadHash: "legacy-hash", Reason: "legacy"})
	assertOperations("indexed", 1, BeginExecutionEpochInput{SessionID: "fixed-work-session", UserID: "user", AccountScopeID: "account", ClientRequestID: "indexed-boundary", PayloadHash: "indexed-hash", Reason: "indexed"})
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
