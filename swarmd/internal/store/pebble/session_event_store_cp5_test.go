package pebblestore

import (
	"strings"
	"testing"
)

func TestReplayV3SessionEventsProjectsOrderedPrimaryState(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-replay")

	first, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-replay",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "message-replay-1",
		PayloadHash:    "hash-message-replay-1",
		Kind:           V3SessionMutationAppendMessage,
		Message: &MessageSnapshot{
			Role:    "user",
			Content: "first replay message",
		},
		RunIntent: &V3SessionRunIntent{Status: V3RunIntentPendingExecutor},
		NowUnixMs: 2000,
	})
	if err != nil {
		t.Fatalf("append first message: %v", err)
	}
	second, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-replay",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "message-replay-2",
		PayloadHash:    "hash-message-replay-2",
		Kind:           V3SessionMutationAppendMessage,
		Message: &MessageSnapshot{
			Role:    "user",
			Content: "second replay message",
		},
		RunIntent: &V3SessionRunIntent{Status: V3RunIntentDispatchBlocked, BlockedReason: "cp5 blocked"},
		NowUnixMs: 3000,
	})
	if err != nil {
		t.Fatalf("append second message: %v", err)
	}

	replay, err := sessions.ReplayV3SessionEvents("session-replay", 0, 10)
	if err != nil {
		t.Fatalf("replay events: %v", err)
	}
	if replay.Session == nil || replay.Session.ID != "session-replay" || replay.Session.MessageCount != 2 {
		t.Fatalf("replay session = %+v", replay.Session)
	}
	if replay.Projection.LastEventSeq != 3 || replay.Projection.ProjectionHighWatermarkSeq != 3 || replay.HighWatermarkSeq != 3 || replay.NextSeq != 3 {
		t.Fatalf("replay projection/watermark = %+v high=%d next=%d", replay.Projection, replay.HighWatermarkSeq, replay.NextSeq)
	}
	if len(replay.Events) != 3 || replay.Events[0].Seq != 1 || replay.Events[1].Seq != 2 || replay.Events[2].Seq != 3 {
		t.Fatalf("replay events = %+v", replay.Events)
	}
	if len(replay.Messages) != 2 || replay.Messages[0].ID != first.Message.ID || replay.Messages[1].ID != second.Message.ID {
		t.Fatalf("replay messages = %+v", replay.Messages)
	}
	if len(replay.RunIntents) != 2 || replay.RunIntents[0].RunID != first.RunIntent.RunID || replay.RunIntents[1].Status != V3RunIntentDispatchBlocked {
		t.Fatalf("replay run intents = %+v", replay.RunIntents)
	}
}

func TestReplayV3SessionEventsSupportsCursorLimitAndDetectsGaps(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-cursor")
	for i := 0; i < 3; i++ {
		suffix := string(rune('a' + i))
		_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
			SessionID:      "session-cursor",
			UserID:         "user-1",
			AccountScopeID: "account-1",
			IdempotencyKey: "message-cursor-" + suffix,
			PayloadHash:    "hash-message-cursor-" + suffix,
			Kind:           V3SessionMutationAppendMessage,
			Message: &MessageSnapshot{
				Role:    "user",
				Content: "cursor message " + suffix,
			},
		})
		if err != nil {
			t.Fatalf("append cursor message %d: %v", i, err)
		}
	}

	page, err := sessions.ReplayV3SessionEvents("session-cursor", 1, 2)
	if err != nil {
		t.Fatalf("replay page: %v", err)
	}
	if len(page.Events) != 2 || page.Events[0].Seq != 2 || page.Events[1].Seq != 3 || page.HighWatermarkSeq != 3 || page.NextSeq != 3 {
		t.Fatalf("page replay = %+v", page)
	}

	if err := store.Delete(KeyV3SessionEvent("session-cursor", 3)); err != nil {
		t.Fatalf("delete event to create gap: %v", err)
	}
	_, err = sessions.ReplayV3SessionEvents("session-cursor", 1, 10)
	if err == nil || !strings.Contains(err.Error(), "sequence gap") {
		t.Fatalf("gap replay err = %v, want sequence gap", err)
	}
}
