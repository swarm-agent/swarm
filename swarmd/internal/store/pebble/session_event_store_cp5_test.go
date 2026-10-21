package pebblestore

import (
	"strings"
	"testing"
)

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

func TestApplyV3SessionMutationExpectedLastEventSeqConflicts(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-settings-conflict")

	expected := uint64(0)
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:            "session-settings-conflict",
		UserID:               "user-1",
		AccountScopeID:       "account-1",
		IdempotencyKey:       "settings-conflict",
		PayloadHash:          "hash-settings-conflict",
		Kind:                 V3SessionMutationUpdateSettings,
		Session:              &SessionSnapshot{ID: "session-settings-conflict", Mode: "plan"},
		ExpectedLastEventSeq: &expected,
	})
	if err == nil {
		t.Fatal("expected projection conflict")
	}
	conflict, ok := err.(*V3ProjectionConflictError)
	if !ok {
		t.Fatalf("err = %T %v, want V3ProjectionConflictError", err, err)
	}
	if conflict.SessionID != "session-settings-conflict" || conflict.Expected != 0 || conflict.Actual != 1 {
		t.Fatalf("conflict = %+v", conflict)
	}

	expected = 1
	result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:            "session-settings-conflict",
		UserID:               "user-1",
		AccountScopeID:       "account-1",
		IdempotencyKey:       "settings-ok",
		PayloadHash:          "hash-settings-ok",
		Kind:                 V3SessionMutationUpdateSettings,
		Session:              &SessionSnapshot{ID: "session-settings-conflict", Mode: "plan"},
		ExpectedLastEventSeq: &expected,
	})
	if err != nil {
		t.Fatalf("settings update: %v", err)
	}
	if result.Event.EventType != "session.settings.updated" || result.Projection.LastEventSeq != 2 {
		t.Fatalf("settings mutation result = %+v", result)
	}
}
