package pebblestore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Purpose: ClaimMemoryJob/FinishMemoryBatch must retain unread bytes across failed
// and restarted batches, atomically publish multiple facts, and never overwrite
// explicit rules. Temporary Pebble plus canonical messages is the narrowest layer.
func TestMemoryIncrementalFragmentsAtomicBatch(t *testing.T) {
	db, s, _ := memoryJobFixture(t)
	text := strings.Repeat("界", 9000)
	_, err := NewSessionStore(db).ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "allowed", UserID: "user", AccountScopeID: "a", IdempotencyKey: "large", RequestHash: "large", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{Role: "user", Content: text}, RunIntent: &V3SessionRunIntent{Status: V3RunIntentDispatchBlocked, BlockedReason: "fixture"}, NowUnixMs: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	var observed strings.Builder
	for n := 0; n < 8; n++ {
		id := "job"
		if n > 0 {
			id = fmt.Sprintf("batch-%d", n)
			if _, err = s.QueueMemoryJob("a", "user", id, false); err != nil {
				t.Fatal(err)
			}
		}
		j, in, err := s.ClaimMemoryJob(context.Background(), "a", "user", id)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			before, _ := s.GetForAccount("a")
			good := MemoryEntry{Kind: "learned", WorkspaceID: "w", Content: "first fact", Sources: j.Sources[:1]}
			bad := good
			bad.Kind = "rule"
			if _, err = s.FinishMemoryBatch(context.Background(), "a", "user", id, []MemoryEntry{good, bad}, 1); err == nil {
				t.Fatal("invalid second entry accepted")
			}
			after, _ := s.GetForAccount("a")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("partial entries or progress")
			}
			if _, err = s.UpdateMemoryJob("a", "user", id, "failed", 0, 0); err != nil {
				t.Fatal(err)
			}
			s = NewMemoryStore(db)
			if _, err = s.QueueMemoryJob("a", "user", "retry", false); err != nil {
				t.Fatal(err)
			}
			id = "retry"
			retry, again, err := s.ClaimMemoryJob(context.Background(), "a", "user", id)
			if err != nil || !reflect.DeepEqual(in, again) {
				t.Fatal("failed fragment skipped", err)
			}
			j = retry
		}
		for _, input := range in {
			if input.Content != "Project uses Go" {
				observed.WriteString(input.Content)
			}
		}
		entries := []MemoryEntry{}
		if len(j.Sources) > 0 {
			for _, fact := range []string{"first fact", "second fact"} {
				entries = append(entries, MemoryEntry{Kind: "learned", WorkspaceID: "w", Content: fact, Sources: j.Sources[:1]})
			}
		}
		done, err := s.FinishMemoryBatch(context.Background(), "a", "user", id, entries, 9000)
		if err != nil || done.Status != "completed" {
			t.Fatal(done, err)
		}
		s = NewMemoryStore(db)
		if !done.ScanLimited {
			break
		}
	}
	if observed.String() != text {
		t.Fatalf("fragment gap/duplicate: got %d want %d", observed.Len(), len(text))
	}
	d, err := s.GetForAccount("a")
	if err != nil || len(d.Entries) != 2 || len(d.JobOffsets) != 0 {
		t.Fatal("missing facts or fragment progress", d, err)
	}
	// Job history is bounded retention, never a lifetime run-count admission cap.
	for n := 0; n < 70; n++ {
		id := fmt.Sprintf("empty-%d", n)
		if _, err = s.QueueMemoryJob("a", "user", id, false); err != nil {
			t.Fatal(err)
		}
		if _, _, err = s.ClaimMemoryJob(context.Background(), "a", "user", id); err != nil {
			t.Fatal(err)
		}
		if _, err = s.FinishMemoryBatch(context.Background(), "a", "user", id, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
}
