package pebblestore

import "testing"

func TestHydrateV3SessionSnapshotReadsOnePebbleSnapshot(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-hydrate-snapshot")

	snapshot := store.db.NewSnapshot()
	defer snapshot.Close()
	if cursor, err := readV3RealtimeOutboxSequenceFromReader(snapshot); err != nil || cursor != 1 {
		t.Fatalf("snapshot realtime outbox seq = %d err=%v, want 1", cursor, err)
	}

	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-hydrate-snapshot",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "message-after-read-snapshot",
		PayloadHash:    "hash-message-after-read-snapshot",
		Kind:           V3SessionMutationAppendMessage,
		Message:        &MessageSnapshot{Role: "user", Content: "after read snapshot"},
		NowUnixMs:      2000,
	})
	if err != nil {
		t.Fatalf("append message after read snapshot: %v", err)
	}

	hydrated, ok, err := sessions.hydrateV3SessionSnapshotFromReader(snapshot, "session-hydrate-snapshot", 500, 500)
	if err != nil || !ok {
		t.Fatalf("hydrate from read snapshot ok=%t err=%v", ok, err)
	}
	if hydrated.Projection.LastEventSeq != 1 || hydrated.Session.MessageCount != 0 || len(hydrated.Messages) != 0 || len(hydrated.Events) != 1 {
		t.Fatalf("hydration observed writes after read snapshot: session=%+v projection=%+v messages=%+v events=%+v", hydrated.Session, hydrated.Projection, hydrated.Messages, hydrated.Events)
	}
	if hydrated.SnapshotEndpointCursor != V3RealtimeOutboxCursor(1) {
		t.Fatalf("snapshot endpoint cursor = %q, want %q", hydrated.SnapshotEndpointCursor, V3RealtimeOutboxCursor(1))
	}

	fresh, ok, err := sessions.HydrateV3SessionSnapshot("session-hydrate-snapshot", 500, 500)
	if err != nil || !ok {
		t.Fatalf("fresh hydrate ok=%t err=%v", ok, err)
	}
	if fresh.Projection.LastEventSeq != 2 || fresh.Session.MessageCount != 1 || len(fresh.Messages) != 1 || len(fresh.Events) != 2 {
		t.Fatalf("fresh hydration = session=%+v projection=%+v messages=%+v events=%+v", fresh.Session, fresh.Projection, fresh.Messages, fresh.Events)
	}
	if fresh.SnapshotEndpointCursor != V3RealtimeOutboxCursor(2) {
		t.Fatalf("fresh snapshot endpoint cursor = %q, want %q", fresh.SnapshotEndpointCursor, V3RealtimeOutboxCursor(2))
	}
}

func TestCurrentV3RealtimeOutboxCursor(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	cursor, err := sessions.CurrentV3RealtimeOutboxCursor()
	if err != nil {
		t.Fatalf("empty current realtime outbox cursor: %v", err)
	}
	if cursor != V3RealtimeOutboxCursor(0) {
		t.Fatalf("empty current realtime outbox cursor = %q, want %q", cursor, V3RealtimeOutboxCursor(0))
	}
	createV3SessionForTest(t, sessions, "session-current-realtime-cursor")
	cursor, err = sessions.CurrentV3RealtimeOutboxCursor()
	if err != nil {
		t.Fatalf("current realtime outbox cursor: %v", err)
	}
	if cursor != V3RealtimeOutboxCursor(1) {
		t.Fatalf("current realtime outbox cursor = %q, want %q", cursor, V3RealtimeOutboxCursor(1))
	}
}
