package pebblestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestV3RealtimeOutboxNewWritesUseOneCanonicalRecordAndThreeCompactReferences(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	before := CurrentV3SessionWriteCounters()
	result := createV3SessionForStoreTest(t, sessions, "session-compact-outbox", "user-1", "account-1")

	canonical, ok, err := store.GetBytes(KeyV3RealtimeOutbox(result.RealtimeOutbox.EndpointSeq))
	if err != nil || !ok {
		t.Fatalf("canonical outbox ok=%v err=%v", ok, err)
	}
	var full V3RealtimeOutboxRecord
	if err := json.Unmarshal(canonical, &full); err != nil || full.Event.ID != result.Event.ID {
		t.Fatalf("canonical outbox decode=%v record=%+v", err, full)
	}
	keys := []string{
		KeyV3RealtimeOutboxBySessionEndpoint(result.SessionID, result.RealtimeOutbox.EndpointSeq),
		KeyV3RealtimeOutboxBySessionSeq(result.SessionID, result.Event.Seq),
		KeyV3RealtimeOutboxByAuthScope("account-1", "user-1", result.RealtimeOutbox.EndpointSeq),
	}
	newBytes := len(canonical)
	oldBytes := len(canonical) * 4
	for _, key := range keys {
		raw, found, getErr := store.GetBytes(key)
		if getErr != nil || !found {
			t.Fatalf("reference %q ok=%v err=%v", key, found, getErr)
		}
		var ref v3RealtimeOutboxReference
		if err := json.Unmarshal(raw, &ref); err != nil || ref.Version != 1 || ref.EndpointSeq != result.RealtimeOutbox.EndpointSeq {
			t.Fatalf("reference %q decode=%v value=%+v", key, err, ref)
		}
		if len(raw) >= len(canonical) {
			t.Fatalf("reference %q bytes=%d, canonical=%d", key, len(raw), len(canonical))
		}
		newBytes += len(raw)
	}
	if newBytes >= oldBytes {
		t.Fatalf("compact logical values=%d, old four-full values=%d", newBytes, oldBytes)
	}
	t.Logf("outbox value bytes: old_four_full=%d new_one_full_three_references=%d reduction=%.1f%%", oldBytes, newBytes, 100*(1-float64(newBytes)/float64(oldBytes)))
	after := CurrentV3SessionWriteCounters()
	if after.SuccessfulFreshMutations != before.SuccessfulFreshMutations+1 || after.SuccessfulBatchOperations != before.SuccessfulBatchOperations || after.EstimatedLogicalBytes <= before.EstimatedLogicalBytes {
		t.Fatalf("counters before=%+v after=%+v", before, after)
	}
}

func TestV3SearchOrdinaryAppendWritesOnlyNewMessageTokens(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	const sessionID = "session-bounded-search-append"
	createV3SessionForStoreTest(t, sessions, sessionID, "user-1", "account-1")

	for i := 0; i < 40; i++ {
		appendV3SessionMessageForStoreTest(t, sessions, sessionID, fmt.Sprintf("old-%d", i), fmt.Sprintf("historicaltoken%d", i), "user-1", "account-1")
	}
	before := SnapshotV3PlanAcceptanceTelemetry()
	appendV3SessionMessageForStoreTest(t, sessions, sessionID, "bounded-new", "freshone freshtwo freshone", "user-1", "account-1")
	delta := DeltaV3PlanAcceptanceTelemetry(SnapshotV3PlanAcceptanceTelemetry(), before)

	if delta.SearchPostingsSet != 2 {
		t.Fatalf("ordinary append set %d search postings, want exactly two unique new-message tokens", delta.SearchPostingsSet)
	}
	if delta.SearchPostingsRead != 0 || delta.SearchPostingsDeleted != 0 || delta.SearchFullRebuilds != 0 || delta.SearchAllTokenRekeys != 0 || delta.MessageRowsScanned != 0 {
		t.Fatalf("ordinary append touched prior search history: %+v", delta)
	}
	for _, token := range []string{"freshone", "freshtwo"} {
		if _, ok, err := store.GetBytes(keyV3SessionSearchPosting(sessionID, "message", token)); err != nil || !ok {
			t.Fatalf("new token %q posting ok=%v err=%v", token, ok, err)
		}
	}
}

func TestSessionStorageMeasurementIsPayloadSafeAndExact(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	secret := "private-message-payload"
	if err := store.PutBytes("v3/session_message/session-1/0001", []byte(secret)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBytes("v3/session_event/session-1/0001", []byte("private-event-payload")); err != nil {
		t.Fatal(err)
	}

	measurement, err := store.MeasureSessionStorageNamespaces(context.Background(), []SessionStorageNamespace{
		{Name: "messages", Prefix: "v3/session_message/"},
		{Name: "events", Prefix: "v3/session_event/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if measurement.TotalCount != 2 || len(measurement.Namespaces) != 2 {
		t.Fatalf("measurement = %+v", measurement)
	}
	for _, namespace := range measurement.Namespaces {
		if namespace.LogicalBytes != namespace.KeyBytes+namespace.ValueBytes || namespace.Count != 1 {
			t.Fatalf("namespace aggregate = %+v", namespace)
		}
	}
	encoded, err := json.Marshal(measurement)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "private-event-payload") || strings.Contains(string(encoded), "session-1") {
		t.Fatalf("measurement exposed key or payload: %s", encoded)
	}
}

func TestV3RetentionPolicyAndMaintenanceBoundaryContracts(t *testing.T) {
	policy := DefaultV3SessionRetentionPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatalf("default retention policy: %v", err)
	}
	invalid := policy
	invalid.CompletedIdempotencyRetention = 0
	if err := invalid.Validate(); err == nil {
		t.Fatal("zero completed idempotency retention unexpectedly accepted")
	}
	if policy.RealtimeReplayRetention != 30*24*time.Hour || policy.RealtimeMinimumRecords != 100_000 || policy.BatchRecords != 500 {
		t.Fatalf("default retention policy changed without updating invariant test: %+v", policy)
	}

	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	state := V3SessionMaintenanceState{
		OldestRetainedRealtimeEndpointSeq: 42,
		RealtimePrunedThroughEndpointSeq:  41,
		CompletedIdempotencyCutoffUnixMs:  1_700_000_000_000,
		CompletedIdempotencyResumeKey:     "v3/session_idempotency/resume",
		UpdatedAtUnixMs:                   1_700_000_000_001,
	}
	if err := sessions.PutV3SessionMaintenanceState(state); err != nil {
		t.Fatal(err)
	}
	boundary, err := sessions.OldestRetainedV3RealtimeEndpointSeq()
	if err != nil || boundary != 42 {
		t.Fatalf("durable retention boundary=%d err=%v", boundary, err)
	}
	loaded, ok, err := sessions.GetV3SessionMaintenanceState()
	if err != nil || !ok || loaded.Version != v3SessionMaintenanceVersion || loaded.CompletedIdempotencyResumeKey != state.CompletedIdempotencyResumeKey {
		t.Fatalf("maintenance state ok=%v err=%v value=%+v", ok, err, loaded)
	}
}

func TestV3SessionRetentionIsBoundedResumableAndAtomic(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	const sessionID = "session-retention"
	createV3SessionForStoreTest(t, sessions, sessionID, "user-1", "account-1")
	for i := 0; i < 5; i++ {
		appendV3SessionMessageForStoreTest(t, sessions, sessionID, fmt.Sprintf("retention-%d", i), "payload", "user-1", "account-1")
	}

	policy := V3SessionRetentionPolicy{RealtimeReplayRetention: time.Hour, CompletedIdempotencyRetention: time.Hour, RealtimeMinimumRecords: 2, BatchRecords: 2}
	now := time.UnixMilli(10_000_000)
	first, err := sessions.RunV3SessionRetentionPass(context.Background(), now, policy)
	if err != nil || first.RealtimeRecordsDeleted != 2 || !first.MoreRealtimeWork || first.OldestRetainedEndpointSeq != 3 {
		t.Fatalf("first retention pass=%+v err=%v", first, err)
	}
	assertV3RealtimeOutboxDeletedWithIndexes(t, store, sessions, sessionID, 1, 1)
	assertV3RealtimeOutboxDeletedWithIndexes(t, store, sessions, sessionID, 2, 2)

	second, err := sessions.RunV3SessionRetentionPass(context.Background(), now.Add(time.Second), policy)
	if err != nil || second.RealtimeRecordsDeleted != 2 || second.MoreRealtimeWork || second.OldestRetainedEndpointSeq != 5 {
		t.Fatalf("second retention pass=%+v err=%v", second, err)
	}
	for seq := uint64(5); seq <= 6; seq++ {
		if _, ok, err := sessions.GetV3RealtimeOutbox(seq); err != nil || !ok {
			t.Fatalf("retained outbox %d ok=%v err=%v", seq, ok, err)
		}
	}

	store.sessionMutations.beforeSessionMaintenanceCommit = func() error { return errors.New("simulated maintenance crash") }
	_, err = sessions.RunV3SessionRetentionPass(context.Background(), now.Add(2*time.Second), policy)
	if err == nil || !strings.Contains(err.Error(), "simulated maintenance crash") {
		t.Fatalf("maintenance failure err=%v", err)
	}
	state, ok, stateErr := sessions.GetV3SessionMaintenanceState()
	if stateErr != nil || !ok || state.RealtimePrunedThroughEndpointSeq != 4 {
		t.Fatalf("maintenance state after interrupted pass ok=%v err=%v state=%+v", ok, stateErr, state)
	}
}

func TestV3SessionRetentionPreservesRetryWindowAndIncompleteIdempotency(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	const sessionID = "session-retention-idempotency"
	created := createV3SessionForStoreTest(t, sessions, sessionID, "user-1", "account-1")
	key := KeyV3SessionOperationIdempotency("account-1", sessionID, V3SessionMutationCreateSession, "create-"+sessionID)
	var record V3SessionIdempotencyRecord
	if ok, err := store.GetJSON(key, &record); err != nil || !ok {
		t.Fatalf("load idempotency ok=%v err=%v", ok, err)
	}
	record.CreatedAt = 9_500_000
	record.CompletedAt = 9_500_000
	if err := store.PutJSON(key, record); err != nil {
		t.Fatal(err)
	}
	inflightKey := KeyV3SessionOperationIdempotency("account-1", sessionID, "custom.inflight", "request")
	inflight := record
	inflight.Status = "in_progress"
	inflight.CompletedAt = 0
	if err := store.PutJSON(inflightKey, inflight); err != nil {
		t.Fatal(err)
	}
	policy := V3SessionRetentionPolicy{RealtimeReplayRetention: time.Hour, CompletedIdempotencyRetention: time.Hour, RealtimeMinimumRecords: 1, BatchRecords: 10}
	result, err := sessions.RunV3SessionRetentionPass(context.Background(), time.UnixMilli(10_000_000), policy)
	if err != nil || result.IdempotencyRecordsDeleted != 0 {
		t.Fatalf("inside retry-window maintenance=%+v err=%v", result, err)
	}
	replayed, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: sessionID, UserID: "user-1", AccountScopeID: "account-1", IdempotencyKey: "create-" + sessionID, RequestHash: "hash-create-" + sessionID, Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: sessionID, WorkspacePath: "/workspace", WorkspaceName: "workspace", Title: sessionID}, NowUnixMs: 10_000_001})
	if err != nil || !replayed.Replayed || replayed.Event.ID != created.Event.ID {
		t.Fatalf("retry-window replay=%+v err=%v", replayed, err)
	}
	if _, ok, err := store.GetBytes(inflightKey); err != nil || !ok {
		t.Fatalf("in-flight idempotency ok=%v err=%v", ok, err)
	}
}

func assertV3RealtimeOutboxDeletedWithIndexes(t *testing.T, store *Store, sessions *SessionStore, sessionID string, endpointSeq, eventSeq uint64) {
	t.Helper()
	keys := []string{KeyV3RealtimeOutbox(endpointSeq), KeyV3RealtimeOutboxBySessionEndpoint(sessionID, endpointSeq), KeyV3RealtimeOutboxBySessionSeq(sessionID, eventSeq), KeyV3RealtimeOutboxByAuthScope("account-1", "user-1", endpointSeq)}
	for _, key := range keys {
		if _, ok, err := store.GetBytes(key); err != nil || ok {
			t.Fatalf("pruned key %q ok=%v err=%v", key, ok, err)
		}
	}
	if _, ok, err := sessions.GetV3RealtimeOutbox(endpointSeq); err != nil || ok {
		t.Fatalf("pruned canonical endpoint %d ok=%v err=%v", endpointSeq, ok, err)
	}
}

func TestV3SearchOrdinaryAppendMigratesMetadataWithoutScanningHistory(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	const sessionID = "session-search-metadata-migration"
	createV3SessionForStoreTest(t, sessions, sessionID, "user-1", "account-1")
	session, ok, err := sessions.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("load session ok=%v err=%v", ok, err)
	}
	metadataPostingCount := len(v3SessionSearchMetadataTokens(session))
	if err := store.PutJSON(keyV3SessionSearchMeta(sessionID), v3SessionSearchSessionMeta{SessionID: sessionID, Keys: []string{"v3/session_search/account/legacy"}}); err != nil {
		t.Fatal(err)
	}
	before := SnapshotV3PlanAcceptanceTelemetry()
	appendV3SessionMessageForStoreTest(t, sessions, sessionID, "migration-append", "migrationfresh", "user-1", "account-1")
	delta := DeltaV3PlanAcceptanceTelemetry(SnapshotV3PlanAcceptanceTelemetry(), before)
	if delta.MessageRowsScanned != 0 || delta.SearchPostingsRead != 0 || delta.SearchPostingsDeleted != 0 || delta.SearchFullRebuilds != 0 || delta.SearchAllTokenRekeys != 0 {
		t.Fatalf("ordinary migration append touched historical rows: %+v", delta)
	}
	if want := uint64(metadataPostingCount + 1); delta.SearchPostingsSet != want {
		t.Fatalf("ordinary migration append set %d postings, want %d bounded metadata plus new token", delta.SearchPostingsSet, want)
	}
	var meta v3SessionSearchSessionMeta
	if ok, err := store.GetJSON(keyV3SessionSearchMeta(sessionID), &meta); err != nil || !ok || meta.Version != v3SessionSearchIndexVersion {
		t.Fatalf("migrated metadata ok=%v err=%v value=%+v", ok, err, meta)
	}
}

func TestV3RealtimeOutboxIndexesResolveHistoricalFullValues(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	record := V3RealtimeOutboxRecord{EndpointSeq: 7, EndpointCursor: V3RealtimeOutboxCursor(7), SessionID: "historical-session", UserID: "user-1", AccountScopeID: "account-1", Event: V3SessionEvent{ID: "historical-event", SessionID: "historical-session", Seq: 3, EventType: "session.message.appended"}, Projection: V3SessionProjection{SessionID: "historical-session", LastEventSeq: 3}}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	batch := store.NewBatch()
	defer batch.Close()
	for _, key := range []string{KeyV3RealtimeOutbox(7), KeyV3RealtimeOutboxBySessionEndpoint(record.SessionID, 7), KeyV3RealtimeOutboxBySessionSeq(record.SessionID, 3), KeyV3RealtimeOutboxByAuthScope(record.AccountScopeID, record.UserID, 7)} {
		if err := batch.Set([]byte(key), raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Commit(nil); err != nil {
		t.Fatal(err)
	}

	bySession, err := sessions.ListV3RealtimeOutboxForSessionAfterEndpoint(record.SessionID, 0, 10)
	if err != nil || len(bySession) != 1 || bySession[0].Event.ID != record.Event.ID {
		t.Fatalf("session historical=%+v err=%v", bySession, err)
	}
	bySeq, err := sessions.ListV3RealtimeOutboxForSessionAfterSeq(record.SessionID, 0, 10)
	if err != nil || len(bySeq) != 1 || bySeq[0].Event.ID != record.Event.ID {
		t.Fatalf("seq historical=%+v err=%v", bySeq, err)
	}
	byAuth, err := sessions.ListV3RealtimeOutboxForAuthScopeAfter(record.AccountScopeID, record.UserID, 0, 10)
	if err != nil || len(byAuth) != 1 || byAuth[0].Event.ID != record.Event.ID {
		t.Fatalf("auth historical=%+v err=%v", byAuth, err)
	}
}
