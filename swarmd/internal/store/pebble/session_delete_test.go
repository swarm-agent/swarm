package pebblestore

import "testing"

func TestDeleteSessionsPurgesContentAndRetainsDurableRemoval(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	session := SessionSnapshot{ID: "delete-purge", UserID: "user-1", AccountScopeID: "acct-1", WorkspacePath: t.TempDir(), Title: "Delete me", CreatedAt: 1000, UpdatedAt: 1000}
	createSearchTestSession(t, sessions, session)
	appendSearchTestMessage(t, sessions, session.ID, session.UserID, session.AccountScopeID, "private content", 2000)

	if err := sessions.DeleteSessions([]string{session.ID}); err != nil {
		t.Fatalf("delete sessions: %v", err)
	}
	if _, ok, err := sessions.GetSession(session.ID); err != nil || ok {
		t.Fatalf("deleted session still hydratable: ok=%t err=%v", ok, err)
	}
	result, err := sessions.SearchV3Sessions(V3SessionSearchOptions{AccountScopeID: session.AccountScopeID, UserID: session.UserID, Global: true, Query: "private", Limit: 10})
	if err != nil {
		t.Fatalf("search deleted content: %v", err)
	}
	if len(result.Items) != 0 || result.Summary.LogicalContentBytes != 0 {
		t.Fatalf("deleted content remained searchable/counted: %+v", result)
	}
	if err := store.IteratePrefix(V3SessionMessagePrefix(session.ID), 10, func(string, []byte) error {
		t.Fatal("deleted V3 message record remains")
		return nil
	}); err != nil {
		t.Fatalf("scan deleted messages: %v", err)
	}
	tombstone, ok, err := sessions.GetV3SessionTombstone(session.ID)
	if err != nil || !ok || !tombstone.Deleted {
		t.Fatalf("durable deletion tombstone missing: ok=%t tombstone=%+v err=%v", ok, tombstone, err)
	}
	projection, ok, err := sessions.GetV3SessionProjection(session.ID)
	if err != nil || !ok || projection.LastEventSeq == 0 {
		t.Fatalf("durable deletion projection missing: ok=%t projection=%+v err=%v", ok, projection, err)
	}
	outbox, ok, err := sessions.LastV3RealtimeOutboxForSessionAtOrBeforeEndpoint(session.ID, ^uint64(0))
	if err != nil || !ok || outbox.Event.EventType != "session.deleted" {
		t.Fatalf("durable deletion outbox missing: ok=%t outbox=%+v err=%v", ok, outbox, err)
	}
}
