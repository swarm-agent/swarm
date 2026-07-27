package pebblestore

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

func TestV3SessionMutationDistinctSessionCommitOverlapAndSameSessionSerialization(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "overlap-a")
	createV3SessionForTest(t, sessions, "overlap-b")

	aEntered := make(chan struct{})
	releaseA := make(chan struct{})
	bEntered := make(chan struct{})
	aSecondEntered := make(chan struct{}, 1)
	var aCalls int
	var hookMu sync.Mutex
	store.sessionMutations.beforeDurableCommit = func(sessionID string) {
		switch sessionID {
		case "overlap-a":
			hookMu.Lock()
			aCalls++
			call := aCalls
			hookMu.Unlock()
			if call == 1 {
				close(aEntered)
				<-releaseA
			} else {
				aSecondEntered <- struct{}{}
			}
		case "overlap-b":
			close(bEntered)
		}
	}

	apply := func(sessionID, key string) error {
		_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
			SessionID: sessionID, UserID: "user-1", AccountScopeID: "account-1",
			IdempotencyKey: key, PayloadHash: "hash-" + key,
			Kind:    V3SessionMutationAppendMessage,
			Message: &MessageSnapshot{Role: "user", Content: key}, NowUnixMs: 2000,
		})
		return err
	}

	aFirstDone := make(chan error, 1)
	go func() { aFirstDone <- apply("overlap-a", "overlap-a-1") }()
	<-aEntered

	aSecondDone := make(chan error, 1)
	go func() { aSecondDone <- apply("overlap-a", "overlap-a-2") }()
	bDone := make(chan error, 1)
	go func() { bDone <- apply("overlap-b", "overlap-b-1") }()

	select {
	case <-bEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("session B never reached the commit boundary while session A was blocked")
	}
	select {
	case err := <-bDone:
		if err != nil {
			t.Fatalf("session B mutation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session B did not finish independent commit work")
	}
	select {
	case <-aSecondEntered:
		t.Fatal("second session A mutation reached commit before the first released its session lock")
	default:
	}

	close(releaseA)
	if err := <-aFirstDone; err != nil {
		t.Fatalf("first session A mutation: %v", err)
	}
	if err := <-aSecondDone; err != nil {
		t.Fatalf("second session A mutation: %v", err)
	}

	events, err := sessions.ListV3SessionEvents("overlap-a", 0, 10)
	if err != nil {
		t.Fatalf("list session A events: %v", err)
	}
	for i, event := range events {
		if event.Seq != uint64(i+1) {
			t.Fatalf("session A event[%d] seq = %d, want %d", i, event.Seq, i+1)
		}
	}
	records, err := sessions.ListV3RealtimeOutboxAfter(0, 20)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	for i, record := range records {
		if record.EndpointSeq != uint64(i+1) {
			t.Fatalf("outbox[%d] endpoint = %d, want %d", i, record.EndpointSeq, i+1)
		}
	}
}

func TestV3RealtimeOutboxRecoveryPublishesContiguousCommittedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox-recovery.pebble")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	future := V3RealtimeOutboxRecord{
		EndpointSeq: 2, EndpointCursor: V3RealtimeOutboxCursor(2), SessionID: "recovered-future",
		UserID: "user-1", AccountScopeID: "account-1",
		Event:      V3SessionEvent{ID: "future-event", SessionID: "recovered-future", Seq: 1, EventType: "session.created", Payload: json.RawMessage(`{"kind":"create_session"}`)},
		Projection: V3SessionProjection{SessionID: "recovered-future", LastEventSeq: 1, ProjectionHighWatermarkSeq: 1}, CreatedAt: 1000,
	}
	payload, err := json.Marshal(future)
	if err != nil {
		t.Fatalf("marshal future row: %v", err)
	}
	if err := store.db.Set([]byte(KeyV3RealtimeOutbox(2)), payload, pebble.Sync); err != nil {
		t.Fatalf("write future durable row: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()
	sessions := NewSessionStore(store)
	created := createV3SessionForStoreTest(t, sessions, "recovered-first", "user-1", "account-1")
	if created.RealtimeOutbox == nil || created.RealtimeOutbox.EndpointSeq != 1 {
		t.Fatalf("first post-reopen endpoint = %+v, want reused gap 1", created.RealtimeOutbox)
	}
	cursor, err := sessions.CurrentV3RealtimeOutboxCursor()
	if err != nil {
		t.Fatalf("current cursor: %v", err)
	}
	if cursor != V3RealtimeOutboxCursor(2) {
		t.Fatalf("published cursor = %q, want %q", cursor, V3RealtimeOutboxCursor(2))
	}
	records, err := sessions.ListV3RealtimeOutboxAfter(0, 10)
	if err != nil {
		t.Fatalf("list recovered outbox: %v", err)
	}
	if len(records) != 2 || records[0].EndpointSeq != 1 || records[1].EndpointSeq != 2 {
		t.Fatalf("recovered replay = %+v, want contiguous endpoints 1,2", records)
	}
}

func TestV3SessionMutationConcurrentCrossSessionPublicationIsContiguous(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	const sessionCount = 12
	for i := 0; i < sessionCount; i++ {
		createV3SessionForTest(t, sessions, fmt.Sprintf("cross-%02d", i))
	}

	var wg sync.WaitGroup
	errCh := make(chan error, sessionCount*2)
	endpointCh := make(chan uint64, sessionCount*2)
	for i := 0; i < sessionCount; i++ {
		for mutation := 0; mutation < 2; mutation++ {
			wg.Add(1)
			go func(i, mutation int) {
				defer wg.Done()
				id := fmt.Sprintf("cross-%02d", i)
				key := fmt.Sprintf("append-%02d-%d", i, mutation)
				result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, UserID: "user-1", AccountScopeID: "account-1", IdempotencyKey: key, PayloadHash: "hash-" + key, Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{Role: "user", Content: key}, NowUnixMs: int64(2000 + mutation)})
				if err != nil {
					errCh <- err
					return
				}
				endpointCh <- result.RealtimeOutbox.EndpointSeq
			}(i, mutation)
		}
	}
	wg.Wait()
	close(errCh)
	close(endpointCh)
	for err := range errCh {
		t.Fatalf("cross-session mutation: %v", err)
	}
	endpoints := make([]int, 0, sessionCount*2)
	for endpoint := range endpointCh {
		endpoints = append(endpoints, int(endpoint))
	}
	sort.Ints(endpoints)
	for i, endpoint := range endpoints {
		want := sessionCount + i + 1
		if endpoint != want {
			t.Fatalf("endpoint[%d] = %d, want %d; all=%v", i, endpoint, want, endpoints)
		}
	}
	rows, err := sessions.ListV3RealtimeOutboxAfter(0, sessionCount*4)
	if err != nil {
		t.Fatalf("list cross-session outbox: %v", err)
	}
	if len(rows) != sessionCount*3 {
		t.Fatalf("outbox rows = %d, want %d", len(rows), sessionCount*3)
	}
	for i, row := range rows {
		if row.EndpointSeq != uint64(i+1) {
			t.Fatalf("outbox[%d] endpoint = %d, want %d", i, row.EndpointSeq, i+1)
		}
	}
	for i := 0; i < sessionCount; i++ {
		id := fmt.Sprintf("cross-%02d", i)
		events, err := sessions.ListV3SessionEvents(id, 0, 10)
		if err != nil {
			t.Fatalf("list %s events: %v", id, err)
		}
		if len(events) != 3 || events[0].Seq != 1 || events[1].Seq != 2 || events[2].Seq != 3 {
			t.Fatalf("%s events = %+v, want seq 1,2,3", id, events)
		}
	}
}
