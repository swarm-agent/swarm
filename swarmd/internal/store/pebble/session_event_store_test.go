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

func TestApplyV3SessionMutationFireworksUsageSummaryAccumulates(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-fireworks-usage")

	turns := []SessionTurnUsageSnapshot{
		{RunID: "v3run_fireworks_1", Provider: "fireworks", Model: "accounts/fireworks/models/glm-4.5", Source: "fireworks_api_usage", ContextWindow: 1000, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 40, TotalTokens: 120},
		{RunID: "v3run_fireworks_2", Provider: "fireworks", Model: "accounts/fireworks/models/glm-4.5", Source: "fireworks_api_usage", ContextWindow: 1000, InputTokens: 80, OutputTokens: 30, CacheReadTokens: 10, TotalTokens: 110},
	}

	var summary SessionUsageSummary
	for index, turn := range turns {
		result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
			SessionID:      "session-fireworks-usage",
			UserID:         "user-1",
			AccountScopeID: "account-1",
			IdempotencyKey: fmt.Sprintf("fireworks-usage-%d", index),
			PayloadHash:    fmt.Sprintf("hash-fireworks-usage-%d", index),
			Kind:           V3SessionMutationRecordUsage,
			EventType:      "run.usage.updated",
			TurnUsage:      &turn,
			NowUnixMs:      int64(3000 + index),
		})
		if err != nil {
			t.Fatalf("record fireworks usage %s: %v", turn.RunID, err)
		}
		if result.UsageSummary == nil {
			t.Fatalf("record fireworks usage %s missing usage summary", turn.RunID)
		}
		summary = *result.UsageSummary
	}

	if summary.TurnCount != 2 || summary.TotalTokens != 230 || summary.InputTokens != 180 || summary.OutputTokens != 50 || summary.CacheReadTokens != 50 {
		t.Fatalf("fireworks summary should accumulate token counts across turns, got %+v", summary)
	}
	if summary.RemainingTokens != 770 {
		t.Fatalf("fireworks remaining tokens = %d, want 770", summary.RemainingTokens)
	}

	replacement := SessionTurnUsageSnapshot{RunID: "v3run_fireworks_2", Provider: "fireworks", Model: "accounts/fireworks/models/glm-4.5", Source: "fireworks_api_usage", ContextWindow: 1000, InputTokens: 70, OutputTokens: 10, CacheReadTokens: 5, TotalTokens: 80}
	result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-fireworks-usage",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "fireworks-usage-replacement",
		PayloadHash:    "hash-fireworks-usage-replacement",
		Kind:           V3SessionMutationRecordUsage,
		EventType:      "run.usage.updated",
		TurnUsage:      &replacement,
		NowUnixMs:      3003,
	})
	if err != nil {
		t.Fatalf("replace fireworks usage: %v", err)
	}
	if result.UsageSummary == nil {
		t.Fatal("replace fireworks usage missing usage summary")
	}
	summary = *result.UsageSummary
	if summary.TurnCount != 2 || summary.TotalTokens != 200 || summary.InputTokens != 170 || summary.OutputTokens != 30 || summary.CacheReadTokens != 45 || summary.RemainingTokens != 800 {
		t.Fatalf("fireworks replacement should update accumulated totals by run id, got %+v", summary)
	}
}

func TestArchiveSessionCreatesTombstoneEventAndRealtimeOutbox(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-archive")

	if err := sessions.ArchiveSession("session-archive"); err != nil {
		t.Fatalf("archive session: %v", err)
	}
	if _, ok, err := sessions.GetSession("session-archive"); err != nil || ok {
		t.Fatalf("archived session visible ok=%v err=%v", ok, err)
	}

	events, err := sessions.ListV3SessionEvents("session-archive", 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 || events[1].EventType != "session.archived" {
		t.Fatalf("events = %+v, want create then archive", events)
	}
	var payload v3SessionEventReplayPayload
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatalf("decode archive payload: %v", err)
	}
	if payload.Kind != V3SessionMutationArchiveSession || payload.Tombstone == nil || payload.Tombstone.Kind != "archived" || !payload.Tombstone.Archived || payload.Tombstone.Deleted {
		t.Fatalf("archive payload = %+v tombstone=%+v", payload, payload.Tombstone)
	}

	tombstones, err := sessions.ListV3SessionTombstonesForAccount("account-1", 10)
	if err != nil {
		t.Fatalf("list tombstones: %v", err)
	}
	if len(tombstones) != 1 || tombstones[0].SessionID != "session-archive" || tombstones[0].Kind != "archived" || !tombstones[0].Archived || tombstones[0].Deleted {
		t.Fatalf("archive tombstones = %+v", tombstones)
	}

	outbox, err := sessions.ListV3RealtimeOutboxForSessionAfterSeq("session-archive", 1, 10)
	if err != nil {
		t.Fatalf("list archive outbox: %v", err)
	}
	if len(outbox) != 1 || outbox[0].Event.EventType != "session.archived" || outbox[0].Membership == nil || outbox[0].Membership.TombstoneKind != "archived" {
		t.Fatalf("archive outbox = %+v", outbox)
	}
}

func TestEventLogAppendBatchPersistsContiguousOrderedEvents(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	events, err := NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	envelopes, err := events.AppendBatch([]EventAppend{
		{Stream: "session:a", EventType: "session.archived", EntityID: "a", Payload: []byte(`{"id":"a"}`), Source: "v3"},
		{Stream: "session:b", EventType: "session.archived", EntityID: "b", Payload: []byte(`{"id":"b"}`), Source: "v3"},
	})
	if err != nil {
		t.Fatalf("append event batch: %v", err)
	}
	if len(envelopes) != 2 || envelopes[0].GlobalSeq+1 != envelopes[1].GlobalSeq || envelopes[0].EntityID != "a" || envelopes[1].EntityID != "b" {
		t.Fatalf("batch envelopes = %+v", envelopes)
	}
	persisted, err := events.ReadFrom(envelopes[0].GlobalSeq, 2)
	if err != nil {
		t.Fatalf("read event batch: %v", err)
	}
	if len(persisted) != 2 || persisted[0].GlobalSeq != envelopes[0].GlobalSeq || persisted[1].GlobalSeq != envelopes[1].GlobalSeq || events.CurrentSequence() != envelopes[1].GlobalSeq {
		t.Fatalf("persisted event batch = %+v current=%d", persisted, events.CurrentSequence())
	}
}

func TestApplyV3SessionMutationAppendMessageReactivatesArchivedSession(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-reactivate")
	if err := sessions.ArchiveSession("session-reactivate"); err != nil {
		t.Fatalf("archive session: %v", err)
	}
	if _, ok, err := sessions.GetSession("session-reactivate"); err != nil || ok {
		t.Fatalf("archived session visible before append ok=%v err=%v", ok, err)
	}

	result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-reactivate",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "message-reactivate",
		RequestHash:    "hash-message-reactivate",
		Kind:           V3SessionMutationAppendMessage,
		Message:        &MessageSnapshot{Role: "user", Content: "reactivate archived chat"},
		NowUnixMs:      2000,
	})
	if err != nil {
		t.Fatalf("append archived message: %v", err)
	}
	if result.Session == nil || result.Session.ID != "session-reactivate" || result.Session.MessageCount != 1 {
		t.Fatalf("append result session = %+v", result.Session)
	}
	if result.Event.EventType != "session.reactivated" {
		t.Fatalf("event type = %q, want session.reactivated", result.Event.EventType)
	}
	loaded, ok, err := sessions.GetSession("session-reactivate")
	if err != nil || !ok {
		t.Fatalf("reactivated session visible ok=%v err=%v", ok, err)
	}
	if loaded.MessageCount != 1 || loaded.LastMessageAt != 2000 {
		t.Fatalf("reactivated session = %+v", loaded)
	}
	tombstones, err := sessions.ListV3SessionTombstonesForAccount("account-1", 10)
	if err != nil {
		t.Fatalf("list tombstones: %v", err)
	}
	for _, tombstone := range tombstones {
		if tombstone.SessionID == "session-reactivate" {
			t.Fatalf("reactivated session still has tombstone: %+v", tombstone)
		}
	}
	search, err := sessions.SearchV3Sessions(V3SessionSearchOptions{AccountScopeID: "account-1", UserID: "user-1", Global: true, Query: "reactivate", Limit: 50})
	if err != nil {
		t.Fatalf("search reactivated: %v", err)
	}
	if len(search.Items) != 1 || search.Items[0].ID != "session-reactivate" || search.Items[0].Archived {
		t.Fatalf("reactivated search items = %+v", search.Items)
	}
}

func TestArchiveSessionsBatchCreatesOrderedTombstonesEventsAndOutbox(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-archive-a")
	createV3SessionForTest(t, sessions, "session-archive-b")

	if err := sessions.ArchiveSessions([]string{"session-archive-a", "session-archive-b"}); err != nil {
		t.Fatalf("archive sessions: %v", err)
	}
	for _, sessionID := range []string{"session-archive-a", "session-archive-b"} {
		if _, ok, err := sessions.GetSession(sessionID); err != nil || ok {
			t.Fatalf("archived session %s visible ok=%v err=%v", sessionID, ok, err)
		}
	}

	tombstones, err := sessions.ListV3SessionTombstonesForAccount("account-1", 10)
	if err != nil {
		t.Fatalf("list tombstones: %v", err)
	}
	if len(tombstones) != 2 {
		t.Fatalf("tombstones = %+v, want 2", tombstones)
	}
	outbox, err := sessions.ListV3RealtimeOutboxAfter(0, 10)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	archiveOutbox := make([]V3RealtimeOutboxRecord, 0, 2)
	for _, record := range outbox {
		if record.Event.EventType == "session.archived" {
			archiveOutbox = append(archiveOutbox, record)
		}
	}
	if len(archiveOutbox) != 2 {
		t.Fatalf("archive outbox = %+v, want 2", archiveOutbox)
	}
	if archiveOutbox[0].EndpointSeq+1 != archiveOutbox[1].EndpointSeq {
		t.Fatalf("archive outbox endpoint seqs not contiguous: %+v", archiveOutbox)
	}
	if archiveOutbox[0].SessionID != "session-archive-a" || archiveOutbox[1].SessionID != "session-archive-b" {
		t.Fatalf("archive outbox order = %+v", archiveOutbox)
	}
}

func TestReactivateArchivedSessionsRestoresBatchWithoutAppendingMessage(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	ids := []string{"session-unarchive-a", "session-unarchive-b"}
	for _, id := range ids {
		createV3SessionForTest(t, sessions, id)
	}
	if err := sessions.ArchiveSessions(ids); err != nil {
		t.Fatalf("archive sessions: %v", err)
	}
	versions := map[string]int64{}
	for _, id := range ids {
		tombstone, ok, err := sessions.GetV3SessionTombstone(id)
		if err != nil || !ok {
			t.Fatalf("load tombstone %s ok=%v err=%v", id, ok, err)
		}
		versions[id] = tombstone.UpdatedAt
	}
	if err := sessions.ReactivateArchivedSessions(ids, versions); err != nil {
		t.Fatalf("reactivate archived sessions: %v", err)
	}
	for _, id := range ids {
		session, ok, err := sessions.GetSession(id)
		if err != nil || !ok || session.MessageCount != 0 {
			t.Fatalf("restored session %s = %+v ok=%v err=%v", id, session, ok, err)
		}
		if _, found, err := sessions.GetV3SessionTombstone(id); err != nil || found {
			t.Fatalf("restored tombstone %s found=%v err=%v", id, found, err)
		}
		events, err := sessions.ListV3SessionEvents(id, 0, 10)
		if err != nil || len(events) != 3 || events[2].EventType != "session.reactivated" {
			t.Fatalf("restored events %s = %+v err=%v", id, events, err)
		}
	}
	search, err := sessions.SearchV3Sessions(V3SessionSearchOptions{AccountScopeID: "account-1", UserID: "user-1", Global: true, Query: "unarchive", Limit: 10})
	if err != nil || len(search.Items) != 2 || search.Items[0].Archived || search.Items[1].Archived {
		t.Fatalf("restored search = %+v err=%v", search.Items, err)
	}
	outbox, err := sessions.ListV3RealtimeOutboxAfter(0, 20)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	reactivated := 0
	for _, record := range outbox {
		if record.Event.EventType == "session.reactivated" {
			reactivated++
			if record.Membership == nil || record.Membership.TombstoneKind != "" {
				t.Fatalf("reactivated membership = %+v", record.Membership)
			}
		}
	}
	if reactivated != 2 {
		t.Fatalf("reactivated outbox count = %d", reactivated)
	}
}

func TestReactivateArchivedSessionsRejectsStaleBatchAtomically(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	ids := []string{"session-unarchive-stale-a", "session-unarchive-stale-b"}
	for _, id := range ids {
		createV3SessionForTest(t, sessions, id)
	}
	if err := sessions.ArchiveSessions(ids); err != nil {
		t.Fatal(err)
	}
	versions := map[string]int64{}
	for _, id := range ids {
		tombstone, ok, err := sessions.GetV3SessionTombstone(id)
		if err != nil || !ok {
			t.Fatalf("tombstone %s ok=%v err=%v", id, ok, err)
		}
		versions[id] = tombstone.UpdatedAt
	}
	versions[ids[1]]--
	if err := sessions.ReactivateArchivedSessions(ids, versions); err == nil {
		t.Fatal("expected stale batch error")
	}
	for _, id := range ids {
		if _, ok, err := sessions.GetSession(id); err != nil || ok {
			t.Fatalf("stale batch restored %s ok=%v err=%v", id, ok, err)
		}
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

func TestV3RealtimeOutboxIndexesReplaySessionsInEndpointOrder(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-index-a")
	createV3SessionForTest(t, sessions, "session-index-b")

	a1 := appendV3SessionMessageForStoreTest(t, sessions, "session-index-a", "index-a-1", "a one", "user-1", "account-1")
	b1 := appendV3SessionMessageForStoreTest(t, sessions, "session-index-b", "index-b-1", "b one", "user-1", "account-1")
	a2 := appendV3SessionMessageForStoreTest(t, sessions, "session-index-a", "index-a-2", "a two", "user-1", "account-1")

	all, err := sessions.ListV3RealtimeOutboxForSessionsAfterEndpoint([]string{"session-index-b", "session-index-a"}, a1.RealtimeOutbox.EndpointSeq-1, 10)
	if err != nil {
		t.Fatalf("list sessions after endpoint: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("indexed session replay rows = %d %+v, want a1,b1,a2", len(all), all)
	}
	want := []uint64{a1.RealtimeOutbox.EndpointSeq, b1.RealtimeOutbox.EndpointSeq, a2.RealtimeOutbox.EndpointSeq}
	for i, record := range all {
		if record.EndpointSeq != want[i] {
			t.Fatalf("indexed replay[%d].EndpointSeq = %d, want %d; rows=%+v", i, record.EndpointSeq, want[i], all)
		}
	}

	lastA, ok, err := sessions.LastV3RealtimeOutboxForSessionAtOrBeforeEndpoint("session-index-a", b1.RealtimeOutbox.EndpointSeq)
	if err != nil || !ok {
		t.Fatalf("last session a before endpoint ok=%v err=%v", ok, err)
	}
	if lastA.Event.ID != a1.Event.ID || lastA.Event.Seq != a1.Event.Seq {
		t.Fatalf("last session a before b endpoint = %+v, want %+v", lastA, a1.Event)
	}
}

func TestV3RealtimeOutboxAuthScopeIndexReturnsOnlyAuthorizedRowsInEndpointOrder(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForStoreTest(t, sessions, "session-auth-a", "user-a", "account-a")
	createV3SessionForStoreTest(t, sessions, "session-auth-b", "user-b", "account-b")
	createV3SessionForStoreTest(t, sessions, "session-auth-empty-legacy", "", "account-a")

	a1 := appendV3SessionMessageForStoreTest(t, sessions, "session-auth-a", "auth-a-1", "a one", "user-a", "account-a")
	b1 := appendV3SessionMessageForStoreTest(t, sessions, "session-auth-b", "auth-b-1", "b one", "user-b", "account-b")
	legacy := appendV3SessionMessageForStoreTest(t, sessions, "session-auth-empty-legacy", "auth-empty-legacy-1", "legacy", "", "account-a")
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
		if record.Event.ID == b1.Event.ID || record.Event.ID == legacy.Event.ID || record.AccountScopeID == "account-b" || record.UserID == "user-b" || record.UserID == "" {
			t.Fatalf("auth-scoped replay leaked unauthorized or legacy row: %+v", record)
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

func TestApplyV3SessionMutationFreshWritesOneEventProjectionAndOutboxRow(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)

	create, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-outbox-atomic",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "create-session-outbox-atomic",
		PayloadHash:    "hash-create-session-outbox-atomic",
		Kind:           V3SessionMutationCreateSession,
		Session: &SessionSnapshot{
			ID:            "session-outbox-atomic",
			WorkspacePath: "/workspace",
			WorkspaceName: "workspace",
			Title:         "session-outbox-atomic",
		},
		NowUnixMs: 1000,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	appendResult, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-outbox-atomic",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "append-session-outbox-atomic",
		PayloadHash:    "hash-append-session-outbox-atomic",
		Kind:           V3SessionMutationAppendMessage,
		Message:        &MessageSnapshot{Role: "user", Content: "atomic append"},
		NowUnixMs:      2000,
	})
	if err != nil {
		t.Fatalf("append message: %v", err)
	}

	fresh := []V3SessionMutationResult{create, appendResult}
	for i, result := range fresh {
		wantSeq := uint64(i + 1)
		if result.Replayed || result.Event.Seq != wantSeq || result.PrimarySeq != wantSeq || result.RealtimeOutbox == nil {
			t.Fatalf("fresh result[%d] = %+v, want seq %d and realtime outbox", i, result, wantSeq)
		}
		if result.RealtimeOutbox.EndpointSeq != wantSeq || result.RealtimeOutbox.Event.ID != result.Event.ID || result.RealtimeOutbox.Projection.ProjectionHighWatermarkSeq != wantSeq {
			t.Fatalf("fresh result[%d] outbox = %+v, event=%+v", i, result.RealtimeOutbox, result.Event)
		}
		storedEvent, ok, err := sessions.GetV3SessionEvent(result.SessionID, wantSeq)
		if err != nil || !ok {
			t.Fatalf("fresh result[%d] event lookup ok=%v err=%v", i, ok, err)
		}
		if storedEvent.ID != result.Event.ID || storedEvent.Seq != wantSeq {
			t.Fatalf("fresh result[%d] stored event = %+v, want %+v", i, storedEvent, result.Event)
		}
		projection, ok, err := sessions.GetV3SessionProjection(result.SessionID)
		if err != nil || !ok {
			t.Fatalf("fresh result[%d] projection lookup ok=%v err=%v", i, ok, err)
		}
		if projection.ProjectionHighWatermarkSeq < wantSeq || result.Projection.ProjectionHighWatermarkSeq != wantSeq {
			t.Fatalf("fresh result[%d] projection = stored %+v result %+v, want high watermark at least %d", i, projection, result.Projection, wantSeq)
		}
		storedOutbox, ok, err := sessions.GetV3RealtimeOutbox(result.RealtimeOutbox.EndpointSeq)
		if err != nil || !ok {
			t.Fatalf("fresh result[%d] outbox lookup ok=%v err=%v", i, ok, err)
		}
		if storedOutbox.EndpointCursor != V3RealtimeOutboxCursor(wantSeq) || storedOutbox.Event.ID != result.Event.ID {
			t.Fatalf("fresh result[%d] stored outbox = %+v, want event %q cursor %q", i, storedOutbox, result.Event.ID, V3RealtimeOutboxCursor(wantSeq))
		}
	}

	beforeReplay, err := sessions.ListV3RealtimeOutboxAfter(0, 10)
	if err != nil {
		t.Fatalf("list outbox before replay: %v", err)
	}
	replayed, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-outbox-atomic",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "append-session-outbox-atomic",
		PayloadHash:    "hash-append-session-outbox-atomic",
		Kind:           V3SessionMutationAppendMessage,
		Message:        &MessageSnapshot{Role: "user", Content: "atomic append"},
		NowUnixMs:      3000,
	})
	if err != nil {
		t.Fatalf("replay append: %v", err)
	}
	if !replayed.Replayed || replayed.RealtimeOutbox != nil {
		t.Fatalf("idempotent replay = %+v, want replay without new realtime outbox", replayed)
	}
	afterReplay, err := sessions.ListV3RealtimeOutboxAfter(0, 10)
	if err != nil {
		t.Fatalf("list outbox after replay: %v", err)
	}
	if len(afterReplay) != len(beforeReplay) {
		t.Fatalf("idempotent replay changed outbox rows: before=%+v after=%+v", beforeReplay, afterReplay)
	}
	for i := range beforeReplay {
		if beforeReplay[i].EndpointSeq != afterReplay[i].EndpointSeq || beforeReplay[i].Event.ID != afterReplay[i].Event.ID {
			t.Fatalf("idempotent replay changed outbox[%d]: before=%+v after=%+v", i, beforeReplay[i], afterReplay[i])
		}
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
	createV3SessionForTest(t, sessions, "session-recovery-index-2")

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
		SessionID:      "session-recovery-index-2",
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
