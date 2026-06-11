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

func TestApplyV3SessionMutationUsageSummaryUsesLatestProviderTotal(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-codex-usage")

	// Exact run.usage.updated sequence from session 5eec75c3eccbb18c3b564e0595593034.
	// The frontend usage summary must expose the normalized provider snapshot, not
	// a cumulative sum of earlier provider snapshots.
	turns := []SessionTurnUsageSnapshot{
		{RunID: "v3run_29e56b20903b242c097a7587864be713", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 62318, OutputTokens: 326, ThinkingTokens: 132, CacheReadTokens: 42496, TotalTokens: 62644},
		{RunID: "v3run_d094c6c72e8f6e1f4dc208fcf4761432", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 236524, OutputTokens: 349, CacheReadTokens: 233472, TotalTokens: 236873},
		{RunID: "v3run_1355cbbfd88e2202707210e104721b4a", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 245356, OutputTokens: 407, ThinkingTokens: 153, CacheReadTokens: 244736, TotalTokens: 245763},
		{RunID: "v3run_4dffeb36e1b4a3a8b5427fe84af0df07", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 246838, OutputTokens: 6, CacheReadTokens: 246272, TotalTokens: 246844},
		{RunID: "v3run_1c156b38c995f0d31179b89fa34a7ef5", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 248409, OutputTokens: 408, CacheReadTokens: 247296, TotalTokens: 248817},
		{RunID: "v3run_08a297a10aa085dcc48999c205746870", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 252016, OutputTokens: 224, CacheReadTokens: 249344, TotalTokens: 252240},
		{RunID: "v3run_6fc277f9f80963b2e215b32605387ee2", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 252648, OutputTokens: 96, CacheReadTokens: 248320, TotalTokens: 252744},
	}

	var summary SessionUsageSummary
	for index, turn := range turns {
		result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
			SessionID:      "session-codex-usage",
			UserID:         "user-1",
			AccountScopeID: "account-1",
			IdempotencyKey: fmt.Sprintf("usage-%d", index),
			PayloadHash:    fmt.Sprintf("hash-usage-%d", index),
			Kind:           V3SessionMutationRecordUsage,
			EventType:      "run.usage.updated",
			TurnUsage:      &turn,
			NowUnixMs:      int64(2000 + index),
		})
		if err != nil {
			t.Fatalf("record usage %s: %v", turn.RunID, err)
		}
		if result.UsageSummary == nil {
			t.Fatalf("record usage %s missing usage summary", turn.RunID)
		}
		summary = *result.UsageSummary
	}

	if summary.TotalTokens != 252744 {
		t.Fatalf("summary total tokens = %d, want latest normalized codex total 252744", summary.TotalTokens)
	}
	if summary.RemainingTokens != 19256 {
		t.Fatalf("summary remaining tokens = %d, want 272000 - 252744 = 19256", summary.RemainingTokens)
	}
	if summary.InputTokens != 252648 || summary.OutputTokens != 96 || summary.CacheReadTokens != 248320 {
		t.Fatalf("summary token fields = input %d output %d cache_read %d, want latest provider snapshot", summary.InputTokens, summary.OutputTokens, summary.CacheReadTokens)
	}
	if summary.TurnCount != len(turns) {
		t.Fatalf("summary turn count = %d, want %d", summary.TurnCount, len(turns))
	}
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

func TestV3SessionRunIntentStatusIndexesSupportRecoveryDiscovery(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-recovery-index")

	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-recovery-index",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "pending-run-1",
		RequestHash:    "hash-pending-run-1",
		Kind:           V3SessionMutationAppendMessage,
		Message:        &MessageSnapshot{Role: "user", Content: "pending please"},
		RunIntent:      &V3SessionRunIntent{RunID: "run-pending", Status: V3RunIntentPendingExecutor},
		NowUnixMs:      2000,
	})
	if err != nil {
		t.Fatalf("record pending run: %v", err)
	}
	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-recovery-index",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "claim-run-1",
		RequestHash:    "hash-claim-run-1",
		Kind:           V3SessionMutationRecordRunIntent,
		RunIntent:      &V3SessionRunIntent{RunID: "run-pending", Status: V3RunIntentRunning},
		NowUnixMs:      3000,
	})
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-recovery-index",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "pending-run-2",
		RequestHash:    "hash-pending-run-2",
		Kind:           V3SessionMutationAppendMessage,
		Message:        &MessageSnapshot{Role: "user", Content: "still pending"},
		RunIntent:      &V3SessionRunIntent{RunID: "run-still-pending", Status: V3RunIntentPendingExecutor},
		NowUnixMs:      4000,
	})
	if err != nil {
		t.Fatalf("record second pending run: %v", err)
	}

	pending, err := sessions.ListV3SessionRunIntentsByStatus(V3RunIntentPendingExecutor, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || pending[0].RunID != "run-still-pending" {
		t.Fatalf("pending intents = %+v", pending)
	}
	running, err := sessions.ListV3SessionRunIntentsByStatus(V3RunIntentRunning, 10)
	if err != nil {
		t.Fatalf("list running: %v", err)
	}
	if len(running) != 1 || running[0].RunID != "run-pending" {
		t.Fatalf("running intents = %+v", running)
	}
	recoverable, err := sessions.ListV3SessionRecoverableRunIntents(3500, 10)
	if err != nil {
		t.Fatalf("list recoverable: %v", err)
	}
	if len(recoverable) != 2 || recoverable[0].RunID != "run-still-pending" || recoverable[1].RunID != "run-pending" {
		t.Fatalf("recoverable intents = %+v", recoverable)
	}
}
