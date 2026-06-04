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

func TestApplyV3SessionMutationAppendMessageAndDispatchBlockedRunIntent(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-2")

	payload, err := json.Marshal(map[string]any{"message": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-2",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "message-1",
		RequestHash:    "hash-message-1",
		Kind:           V3SessionMutationAppendMessage,
		EventPayload:   payload,
		Message: &MessageSnapshot{
			Role:    "user",
			Content: "hello",
		},
		RunIntent: &V3SessionRunIntent{
			Status:        V3RunIntentDispatchBlocked,
			BlockedReason: "no dispatch authority",
		},
		NowUnixMs: 2000,
	})
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
	if result.PrimarySeq != 2 || result.Message == nil || result.Message.GlobalSeq != 2 {
		t.Fatalf("message result = %+v message=%+v", result, result.Message)
	}
	if result.RunIntent == nil || result.RunIntent.Status != V3RunIntentDispatchBlocked {
		t.Fatalf("run intent = %+v", result.RunIntent)
	}
	updated, ok, err := sessions.GetSession("session-2")
	if err != nil || !ok {
		t.Fatalf("load updated session ok=%v err=%v", ok, err)
	}
	if updated.MessageCount != 1 || updated.LastMessageAt != 2000 {
		t.Fatalf("updated session = %+v", updated)
	}
	projection, ok, err := sessions.GetV3SessionProjection("session-2")
	if err != nil || !ok {
		t.Fatalf("projection ok=%v err=%v", ok, err)
	}
	if projection.LastEventSeq != 2 || projection.ProjectionHighWatermarkSeq != 2 {
		t.Fatalf("projection = %+v", projection)
	}
	messages, err := sessions.ListV3SessionMessages("session-2", 0, 10)
	if err != nil {
		t.Fatalf("list v3 messages: %v", err)
	}
	if len(messages) != 1 || messages[0].ID != result.Message.ID {
		t.Fatalf("messages = %+v", messages)
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
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      sessionID,
		UserID:         "user-1",
		AccountScopeID: "account-1",
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
}

func openV3SessionEventTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "v3-sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
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
