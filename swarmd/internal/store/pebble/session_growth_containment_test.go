package pebblestore

import (
	"encoding/json"
	"testing"
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
