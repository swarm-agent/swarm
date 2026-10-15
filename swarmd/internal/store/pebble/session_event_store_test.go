package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestApplyV3SessionMutationAtomicCreateAndReplayIdempotency(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)

	result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-1",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "create-1",
		RequestHash:    "hash-create-1",
		Kind:           V3SessionMutationCreateSession,
		Session: &SessionSnapshot{
			ID:            "session-1",
			WorkspacePath: "/workspace",
			WorkspaceName: "workspace",
			Title:         "V3 Session",
		},
		NowUnixMs: 1000,
	})
	if err != nil {
		t.Fatalf("apply create: %v", err)
	}
	if result.PrimarySeq != 1 || result.Event.Seq != 1 {
		t.Fatalf("create seq result = %+v event=%+v", result, result.Event)
	}
	if result.RealtimeOutbox == nil || result.RealtimeOutbox.EndpointSeq != 1 || result.RealtimeOutbox.Event.Seq != result.Event.Seq || result.RealtimeOutbox.SessionID != result.SessionID {
		t.Fatalf("create realtime outbox = %+v result=%+v", result.RealtimeOutbox, result)
	}
	outbox, ok, err := sessions.GetV3RealtimeOutbox(result.RealtimeOutbox.EndpointSeq)
	if err != nil || !ok {
		t.Fatalf("get realtime outbox ok=%v err=%v", ok, err)
	}
	if outbox.Event.ID != result.Event.ID || outbox.EndpointCursor != "cursor-1" || outbox.Projection.ProjectionHighWatermarkSeq != 1 {
		t.Fatalf("persisted realtime outbox = %+v result event=%+v", outbox, result.Event)
	}
	if result.Projection.LastEventSeq != 1 || result.Projection.ProjectionHighWatermarkSeq != 1 {
		t.Fatalf("projection = %+v", result.Projection)
	}
	if result.Session == nil || result.Session.ID != "session-1" {
		t.Fatalf("result session = %+v", result.Session)
	}
	if _, ok, err := sessions.GetSession("session-1"); err != nil || !ok {
		t.Fatalf("session persisted ok=%v err=%v", ok, err)
	}
	events, err := sessions.ListV3SessionEvents("session-1", 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "session.created" {
		t.Fatalf("events = %+v", events)
	}
	idem, ok, err := sessions.GetV3SessionOperationIdempotencyRecord("account-1", "session-1", V3SessionMutationCreateSession, "create-1")
	if err != nil || !ok {
		t.Fatalf("idempotency ok=%v err=%v", ok, err)
	}
	if idem.Operation != V3SessionMutationCreateSession || idem.ClientRequestID != "create-1" || idem.PayloadHash != "hash-create-1" {
		t.Fatalf("idempotency operation/key/hash = %+v", idem)
	}
	if idem.Result.FirstSeq != 1 || idem.Result.LastSeq != 1 || len(idem.Result.EventIDs) != 1 || idem.Result.EventIDs[0] != result.Event.ID {
		t.Fatalf("idempotency result seq/events = %+v", idem.Result)
	}
	if idem.Result.PayloadHash != "hash-create-1" || idem.Result.ResponseVersion != V3SessionMutationResponseVersion || idem.Result.ResponseStatus != V3SessionMutationStatusCompleted || len(idem.Result.ResponseBody) == 0 {
		t.Fatalf("idempotency result contract = %+v", idem.Result)
	}

	replayed, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-1",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "create-1",
		RequestHash:    "hash-create-1",
		Kind:           V3SessionMutationCreateSession,
		Session: &SessionSnapshot{
			ID:            "session-1",
			WorkspacePath: "/workspace",
			WorkspaceName: "workspace",
			Title:         "V3 Session",
		},
		NowUnixMs: 2000,
	})
	if err != nil {
		t.Fatalf("replay create: %v", err)
	}
	if !replayed.Replayed || replayed.PrimarySeq != result.PrimarySeq || replayed.Event.ID != result.Event.ID {
		t.Fatalf("replayed = %+v original=%+v", replayed, result)
	}
	if replayed.RealtimeOutbox != nil {
		t.Fatalf("replayed idempotent mutation must not mint or publish a new realtime outbox row: %+v", replayed.RealtimeOutbox)
	}
	events, err = sessions.ListV3SessionEvents("session-1", 0, 10)
	if err != nil {
		t.Fatalf("list events after replay: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("duplicate idempotent create appended events: %+v", events)
	}

	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-1",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "create-2",
		RequestHash:    "hash-create-2",
		Kind:           V3SessionMutationCreateSession,
		Session: &SessionSnapshot{
			ID:            "session-1",
			WorkspacePath: "/workspace",
			WorkspaceName: "workspace",
			Title:         "Duplicate Create",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate create err = %v, want already exists", err)
	}
	events, err = sessions.ListV3SessionEvents("session-1", 0, 10)
	if err != nil {
		t.Fatalf("list events after duplicate create: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("duplicate create appended events: %+v", events)
	}

	conflict, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-1",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "create-1",
		RequestHash:    "different-hash",
		Kind:           V3SessionMutationCreateSession,
		Session: &SessionSnapshot{
			ID:            "session-1",
			WorkspacePath: "/workspace",
			WorkspaceName: "workspace",
			Title:         "Different",
		},
	})
	if !errors.Is(err, ErrV3IdempotencyConflict) {
		t.Fatalf("conflict err = %v", err)
	}
	if conflict.Conflict == nil || conflict.Conflict.Code != V3SessionMutationStatusConflict || conflict.Conflict.ExistingPayloadHash != "hash-create-1" || conflict.Conflict.IncomingPayloadHash != "different-hash" {
		t.Fatalf("conflict result = %+v", conflict)
	}
	events, err = sessions.ListV3SessionEvents("session-1", 0, 10)
	if err != nil {
		t.Fatalf("list events after conflict: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("conflicting idempotency mutation changed events: %+v", events)
	}
}

func TestV3SessionMutationStoresSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v3-restart.pebble")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-restart")
	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-restart",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "message-restart",
		RequestHash:    "hash-message-restart",
		Kind:           V3SessionMutationAppendMessage,
		Message: &MessageSnapshot{
			Role:    "user",
			Content: "survive restart",
		},
		NowUnixMs: 4000,
	})
	if err != nil {
		t.Fatalf("append before restart: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	sessions = NewSessionStore(reopened)
	events, err := sessions.ListV3SessionEvents("session-restart", 0, 10)
	if err != nil {
		t.Fatalf("list events after restart: %v", err)
	}
	if len(events) != 2 || events[1].Seq != 2 || events[1].EventType != "session.message.appended" {
		t.Fatalf("events after restart = %+v", events)
	}
	projection, ok, err := sessions.GetV3SessionProjection("session-restart")
	if err != nil || !ok {
		t.Fatalf("projection after restart ok=%v err=%v", ok, err)
	}
	if projection.LastEventSeq != 2 || projection.ProjectionHighWatermarkSeq != 2 {
		t.Fatalf("projection after restart = %+v", projection)
	}
	replayed, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-restart",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "message-restart",
		RequestHash:    "hash-message-restart",
		Kind:           V3SessionMutationAppendMessage,
		Message: &MessageSnapshot{
			Role:    "user",
			Content: "survive restart",
		},
		NowUnixMs: 5000,
	})
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if !replayed.Replayed || replayed.PrimarySeq != 2 || replayed.Message == nil || replayed.Message.Content != "survive restart" {
		t.Fatalf("replayed after restart = %+v", replayed)
	}
}

func TestApplyV3SessionMutationConcurrentIdempotentAppendAllocatesOneSeq(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-3")

	const workers = 16
	results := make(chan V3SessionMutationResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
				SessionID:      "session-3",
				UserID:         "user-1",
				AccountScopeID: "account-1",
				IdempotencyKey: "message-concurrent",
				RequestHash:    "same-request-hash",
				Kind:           V3SessionMutationAppendMessage,
				Message: &MessageSnapshot{
					Role:    "user",
					Content: "hello concurrently",
				},
				NowUnixMs: int64(3000 + i),
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent apply error: %v", err)
	}
	var first *V3SessionMutationResult
	count := 0
	for result := range results {
		count++
		if first == nil {
			copy := result
			first = &copy
			continue
		}
		if result.PrimarySeq != first.PrimarySeq || result.Event.ID != first.Event.ID {
			t.Fatalf("non-identical replay result: first=%+v result=%+v", first, result)
		}
	}
	if count != workers {
		t.Fatalf("results = %d, want %d", count, workers)
	}
	events, err := sessions.ListV3SessionEvents("session-3", 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want create + one message", events)
	}
	projection, ok, err := sessions.GetV3SessionProjection("session-3")
	if err != nil || !ok {
		t.Fatalf("projection ok=%v err=%v", ok, err)
	}
	if projection.LastEventSeq != 2 || projection.ProjectionHighWatermarkSeq != 2 {
		t.Fatalf("projection = %+v", projection)
	}
	updated, ok, err := sessions.GetSession("session-3")
	if err != nil || !ok {
		t.Fatalf("load session ok=%v err=%v", ok, err)
	}
	if updated.MessageCount != 1 {
		t.Fatalf("message count = %d, want 1", updated.MessageCount)
	}
}

func createV3SessionForTest(t *testing.T, sessions *SessionStore, sessionID string) {
	t.Helper()
	createV3SessionForStoreTest(t, sessions, sessionID, "user-1", "account-1")
}

func createV3SessionForStoreTest(t *testing.T, sessions *SessionStore, sessionID, userID, accountScopeID string) V3SessionMutationResult {
	t.Helper()
	result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      sessionID,
		UserID:         userID,
		AccountScopeID: accountScopeID,
		IdempotencyKey: fmt.Sprintf("create-%s", sessionID),
		RequestHash:    fmt.Sprintf("hash-create-%s", sessionID),
		Kind:           V3SessionMutationCreateSession,
		Session: &SessionSnapshot{
			ID:            sessionID,
			WorkspacePath: "/workspace",
			WorkspaceName: "workspace",
			Title:         sessionID,
		},
		NowUnixMs: 1000,
	})
	if err != nil {
		t.Fatalf("create test v3 session: %v", err)
	}
	return result
}

func appendV3SessionMessageForStoreTest(t *testing.T, sessions *SessionStore, sessionID, idempotencyKey, content, userID, accountScopeID string) V3SessionMutationResult {
	t.Helper()
	result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      sessionID,
		UserID:         userID,
		AccountScopeID: accountScopeID,
		IdempotencyKey: idempotencyKey,
		RequestHash:    fmt.Sprintf("hash-%s", idempotencyKey),
		Kind:           V3SessionMutationAppendMessage,
		Message:        &MessageSnapshot{Role: "user", Content: content},
		NowUnixMs:      2000,
	})
	if err != nil {
		t.Fatalf("append test v3 session message: %v", err)
	}
	if result.RealtimeOutbox == nil {
		t.Fatalf("append result missing realtime outbox: %+v", result)
	}
	return result
}

func openV3SessionEventTestStore(t testing.TB) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "v3-sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestApplyV3SessionMutationRealtimeOutboxIsAtomicAndOrdered(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-outbox-a")
	createV3SessionForTest(t, sessions, "session-outbox-b")

	appendResult, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-outbox-a",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "message-outbox-a",
		PayloadHash:    "hash-message-outbox-a",
		Kind:           V3SessionMutationAppendMessage,
		Message:        &MessageSnapshot{Role: "user", Content: "outbox message"},
		NowUnixMs:      2000,
	})
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
	if appendResult.RealtimeOutbox == nil {
		t.Fatalf("append mutation missing realtime outbox row: %+v", appendResult)
	}

	records, err := sessions.ListV3RealtimeOutboxAfter(0, 10)
	if err != nil {
		t.Fatalf("list realtime outbox: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("realtime outbox rows = %d %+v, want create/create/append", len(records), records)
	}
	for i, record := range records {
		wantEndpointSeq := uint64(i + 1)
		if record.EndpointSeq != wantEndpointSeq || record.EndpointCursor != V3RealtimeOutboxCursor(wantEndpointSeq) {
			t.Fatalf("outbox[%d] endpoint = %d/%q, want %d/%q", i, record.EndpointSeq, record.EndpointCursor, wantEndpointSeq, V3RealtimeOutboxCursor(wantEndpointSeq))
		}
		storedEvent, ok, err := sessions.GetV3SessionEvent(record.SessionID, record.Event.Seq)
		if err != nil || !ok {
			t.Fatalf("outbox[%d] event lookup ok=%v err=%v", i, ok, err)
		}
		if storedEvent.ID != record.Event.ID || storedEvent.EventType != record.Event.EventType {
			t.Fatalf("outbox[%d] event = %+v stored=%+v", i, record.Event, storedEvent)
		}
	}
	sessionRecords, err := sessions.ListV3RealtimeOutboxForSessionAfterSeq("session-outbox-a", 1, 10)
	if err != nil {
		t.Fatalf("list session realtime outbox: %v", err)
	}
	if len(sessionRecords) != 1 || sessionRecords[0].Event.ID != appendResult.Event.ID {
		t.Fatalf("session outbox after seq 1 = %+v, want append event %q", sessionRecords, appendResult.Event.ID)
	}
}

func TestV3RealtimeOutboxAuthScopeIndexReturnsOnlyAuthorizedRowsInEndpointOrder(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForStoreTest(t, sessions, "session-auth-a", "user-a", "account-a")
	createV3SessionForStoreTest(t, sessions, "session-auth-b", "user-b", "account-b")

	a1 := appendV3SessionMessageForStoreTest(t, sessions, "session-auth-a", "auth-a-1", "a one", "user-a", "account-a")
	b1 := appendV3SessionMessageForStoreTest(t, sessions, "session-auth-b", "auth-b-1", "b one", "user-b", "account-b")
	a2 := appendV3SessionMessageForStoreTest(t, sessions, "session-auth-a", "auth-a-2", "a two", "user-a", "account-a")

	records, err := sessions.ListV3RealtimeOutboxForAuthScopeAfter("account-a", "user-a", a1.RealtimeOutbox.EndpointSeq-1, 10)
	if err != nil {
		t.Fatalf("list auth-scoped outbox: %v", err)
	}
	wantIDs := []string{a1.Event.ID, a2.Event.ID}
	if len(records) != len(wantIDs) {
		t.Fatalf("auth-scoped rows = %d %+v, want %d", len(records), records, len(wantIDs))
	}
	for i, record := range records {
		if record.Event.ID != wantIDs[i] {
			t.Fatalf("auth-scoped row[%d] = event %q session %q, want event %q; all=%+v", i, record.Event.ID, record.SessionID, wantIDs[i], records)
		}
		if record.Event.ID == b1.Event.ID || record.AccountScopeID == "account-b" || record.UserID == "user-b" || record.UserID == "" {
			t.Fatalf("auth-scoped replay leaked an unauthorized or identity-free row: %+v", record)
		}
	}
	if records[len(records)-1].EndpointSeq != a2.RealtimeOutbox.EndpointSeq {
		t.Fatalf("authorized replay did not advance to latest visible endpoint: got %d want %d", records[len(records)-1].EndpointSeq, a2.RealtimeOutbox.EndpointSeq)
	}
}

func TestV3RealtimeOutboxSessionSeqIndexDoesNotMissRowsPastOldGlobalScanCap(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-scan-cap-target")

	const fillerRows = 100000
	batch := store.NewBatch()
	for endpointSeq := uint64(2); endpointSeq <= fillerRows+1; endpointSeq++ {
		record := V3RealtimeOutboxRecord{
			EndpointSeq:    endpointSeq,
			EndpointCursor: V3RealtimeOutboxCursor(endpointSeq),
			SessionID:      fmt.Sprintf("session-scan-cap-filler-%06d", endpointSeq),
			UserID:         "user-1",
			AccountScopeID: "account-1",
			Event:          V3SessionEvent{ID: fmt.Sprintf("filler-%06d", endpointSeq), SessionID: fmt.Sprintf("session-scan-cap-filler-%06d", endpointSeq), Seq: 1, EventType: "session.message.appended", Payload: json.RawMessage(`{"kind":"message"}`)},
			Projection:     V3SessionProjection{SessionID: fmt.Sprintf("session-scan-cap-filler-%06d", endpointSeq), LastEventSeq: 1, ProjectionHighWatermarkSeq: 1},
			CreatedAt:      int64(endpointSeq),
		}
		payload, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal filler outbox: %v", err)
		}
		if err := batch.Set([]byte(KeyV3RealtimeOutbox(endpointSeq)), payload, nil); err != nil {
			t.Fatalf("set filler outbox: %v", err)
		}
		if endpointSeq%5000 == 0 {
			if err := batch.Commit(nil); err != nil {
				t.Fatalf("commit filler batch: %v", err)
			}
			if err := batch.Close(); err != nil {
				t.Fatalf("close filler batch: %v", err)
			}
			batch = store.NewBatch()
		}
	}
	targetEndpointSeq := uint64(fillerRows + 2)
	target := V3RealtimeOutboxRecord{
		EndpointSeq:    targetEndpointSeq,
		EndpointCursor: V3RealtimeOutboxCursor(targetEndpointSeq),
		SessionID:      "session-scan-cap-target",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		Event:          V3SessionEvent{ID: "target-past-scan-cap", SessionID: "session-scan-cap-target", Seq: 2, EventType: "session.message.appended", Payload: json.RawMessage(`{"kind":"message"}`)},
		Projection:     V3SessionProjection{SessionID: "session-scan-cap-target", LastEventSeq: 2, ProjectionHighWatermarkSeq: 2},
		CreatedAt:      int64(targetEndpointSeq),
	}
	targetPayload, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("marshal target outbox: %v", err)
	}
	for _, key := range []string{
		KeyV3RealtimeOutbox(targetEndpointSeq),
		KeyV3RealtimeOutboxBySessionEndpoint(target.SessionID, targetEndpointSeq),
		KeyV3RealtimeOutboxBySessionSeq(target.SessionID, target.Event.Seq),
		KeyV3RealtimeOutboxByAuthScope(target.AccountScopeID, target.UserID, targetEndpointSeq),
	} {
		if err := batch.Set([]byte(key), targetPayload, nil); err != nil {
			t.Fatalf("set target index %q: %v", key, err)
		}
	}
	if err := batch.Set([]byte(KeyV3RealtimeOutboxSequence()), uint64ToBytes(targetEndpointSeq), nil); err != nil {
		t.Fatalf("set outbox sequence: %v", err)
	}
	if err := batch.Commit(nil); err != nil {
		t.Fatalf("commit target batch: %v", err)
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("close target batch: %v", err)
	}

	records, err := sessions.ListV3RealtimeOutboxForSessionAfterSeq("session-scan-cap-target", 1, 1)
	if err != nil {
		t.Fatalf("list target session outbox: %v", err)
	}
	if len(records) != 1 || records[0].EndpointSeq != targetEndpointSeq || records[0].Event.ID != target.Event.ID {
		t.Fatalf("session indexed replay past scan cap = %+v, want target endpoint %d", records, targetEndpointSeq)
	}
}

func TestApplyV3SessionMutationConcurrentDistinctAppendsAllocateContiguousSeq(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-distinct")

	const workers = 24
	results := make(chan V3SessionMutationResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
				SessionID:      "session-distinct",
				UserID:         "user-1",
				AccountScopeID: "account-1",
				IdempotencyKey: fmt.Sprintf("message-distinct-%02d", i),
				PayloadHash:    fmt.Sprintf("hash-message-distinct-%02d", i),
				Kind:           V3SessionMutationAppendMessage,
				Message: &MessageSnapshot{
					Role:    "user",
					Content: fmt.Sprintf("distinct message %02d", i),
				},
				NowUnixMs: int64(6000 + i),
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent distinct apply error: %v", err)
	}

	seen := map[uint64]bool{1: true}
	count := 0
	for result := range results {
		count++
		if result.FirstSeq != result.PrimarySeq || result.LastSeq != result.PrimarySeq || len(result.EventIDs) != 1 || result.EventIDs[0] != result.Event.ID {
			t.Fatalf("result sequence contract = %+v", result)
		}
		if seen[result.PrimarySeq] {
			t.Fatalf("duplicate primary seq allocated: %d", result.PrimarySeq)
		}
		seen[result.PrimarySeq] = true
	}
	if count != workers {
		t.Fatalf("results = %d, want %d", count, workers)
	}
	for seq := uint64(1); seq <= workers+1; seq++ {
		if !seen[seq] {
			t.Fatalf("missing primary seq %d in seen set %#v", seq, seen)
		}
	}

	events, err := sessions.ListV3SessionEvents("session-distinct", 0, workers+2)
	if err != nil {
		t.Fatalf("list distinct events: %v", err)
	}
	if len(events) != workers+1 {
		t.Fatalf("events = %d, want create + %d messages: %+v", len(events), workers, events)
	}
	for i, event := range events {
		wantSeq := uint64(i + 1)
		if event.Seq != wantSeq {
			t.Fatalf("event[%d].Seq = %d, want %d", i, event.Seq, wantSeq)
		}
	}
	projection, ok, err := sessions.GetV3SessionProjection("session-distinct")
	if err != nil || !ok {
		t.Fatalf("projection ok=%v err=%v", ok, err)
	}
	if projection.LastEventSeq != workers+1 || projection.ProjectionHighWatermarkSeq != workers+1 {
		t.Fatalf("projection = %+v, want high watermark %d", projection, workers+1)
	}
	updated, ok, err := sessions.GetSession("session-distinct")
	if err != nil || !ok {
		t.Fatalf("load session ok=%v err=%v", ok, err)
	}
	if updated.MessageCount != workers {
		t.Fatalf("message count = %d, want %d", updated.MessageCount, workers)
	}
}
