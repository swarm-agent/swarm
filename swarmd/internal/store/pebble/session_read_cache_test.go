package pebblestore

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

func cachedSession(t testing.TB, s *SessionStore, id string) SessionSnapshot {
	t.Helper()
	v, ok, err := s.GetSession(id)
	if err != nil || !ok {
		t.Fatalf("GetSession(%q): found=%v err=%v", id, ok, err)
	}
	return v
}

// Purpose: all direct record writers and the shared setSessionInBatch commit
// boundary must invalidate shared wrappers, never serve a pre-commit snapshot.
// Real temporary Pebble batches are the narrowest layer proving publication.
func TestSessionReadCacheWriters(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	other := NewSessionStore(s.store)
	v := SessionSnapshot{ID: "cache-writers", Title: "initial", UserID: "user-1", AccountScopeID: "account-1"}
	if err := s.CreateSession(v); err != nil {
		t.Fatal(err)
	}
	cachedSession(t, other, v.ID)
	for _, writer := range []struct {
		name  string
		write func(SessionSnapshot) error
	}{
		{"create", s.CreateSession}, {"update", s.UpdateSession},
		{"setSessionInBatch", func(v SessionSnapshot) error {
			b := s.store.NewBatch()
			defer b.Close()
			if err := s.setSessionInBatch(b, v); err != nil {
				return err
			}
			return s.store.commitSessionBatch(b, pebble.Sync)
		}},
	} {
		t.Run(writer.name, func(t *testing.T) {
			v.Title = writer.name
			if err := writer.write(v); err != nil {
				t.Fatal(err)
			}
			if got := cachedSession(t, other, v.ID); got.Title != v.Title {
				t.Fatalf("stale title: %q", got.Title)
			}
		})
	}
	if err := s.ArchiveSession(v.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := other.GetSession(v.ID); err != nil || ok {
		t.Fatalf("archived: %v %v", ok, err)
	}
	tomb, ok, err := s.GetV3SessionTombstone(v.ID)
	if err != nil || !ok {
		t.Fatalf("tombstone: %v %v", ok, err)
	}
	if err := s.ReactivateArchivedSessions([]string{v.ID}, map[string]int64{v.ID: tomb.UpdatedAt}); err != nil {
		t.Fatal(err)
	}
	if got := cachedSession(t, other, v.ID); got.Title != v.Title {
		t.Fatal("reactivation changed title")
	}
	if err := s.DeleteSession(v.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := other.GetSession(v.ID); err != nil || ok {
		t.Fatalf("deleted: %v %v", ok, err)
	}
}

// Purpose: ApplyV3SessionMutation must invalidate the cached record when an
// append updates counters; a real mutation proves the transaction wiring.
func TestSessionReadCacheMutation(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, s, "cache-mutation")
	before := cachedSession(t, s, "cache-mutation")
	_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: before.ID, UserID: before.UserID, AccountScopeID: before.AccountScopeID, Kind: V3SessionMutationAppendMessage, IdempotencyKey: "cache-append", PayloadHash: "cache-hash", Message: &MessageSnapshot{Role: "assistant", Content: "answer"}, NowUnixMs: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if got := cachedSession(t, s, before.ID); got.MessageCount != before.MessageCount+1 {
		t.Fatalf("stale message count: %d", got.MessageCount)
	}
}

// Purpose: GetSession's generation fence must reject a stale fill after a
// completed write. A channel hook makes the dangerous interleaving deterministic.
func TestSessionReadCacheNoStaleFill(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	v := SessionSnapshot{ID: "cache-race", Title: "old"}
	if err := s.CreateSession(v); err != nil {
		t.Fatal(err)
	}
	read, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	reader := NewSessionStore(s.store)
	reader.sessionCacheRead = func(string) { close(read); <-release }
	go func() { _, _, err := reader.GetSession(v.ID); done <- err }()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not reach hook")
	}
	v.Title = "new"
	err := s.UpdateSession(v)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not finish")
	}
	if got := cachedSession(t, s, v.ID); got.Title != "new" {
		t.Fatalf("stale fill: %q", got.Title)
	}
}

// Purpose: GetSession must not expose cached maps, slices, or pointers. Mutating
// both miss and hit results proves isolation at the public read boundary.
func TestSessionReadCacheCopies(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	available := true
	v := SessionSnapshot{ID: "cache-copy", Metadata: map[string]any{"nested": map[string]any{"value": "original"}}, WorkspaceGrants: []WorkspaceGrant{{Available: &available}}, ModelProfile: &SessionModelProfileSnapshot{Source: "original", Plan: &ModelProfileSelection{}}, Lifecycle: &SessionLifecycleSnapshot{Active: true}}
	if err := s.CreateSession(v); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got := cachedSession(t, s, v.ID)
		if got.Metadata["nested"].(map[string]any)["value"] != "original" || !*got.WorkspaceGrants[0].Available || got.ModelProfile.Source != "original" || !got.Lifecycle.Active {
			t.Fatal("cached snapshot mutated")
		}
		got.Metadata["nested"].(map[string]any)["value"] = "changed"
		*got.WorkspaceGrants[0].Available = false
		got.WorkspaceGrants = nil
		got.ModelProfile.Source = "changed"
		got.ModelProfile.Plan = nil
		got.Lifecycle.Active = false
	}
}

// Purpose: concurrent shared-wrapper reads and writes must never cross session
// identities or regress a completed same-session write. Real commits and 64
// goroutines exercise cache synchronization; the operator runs the race detector.
func TestSessionReadCacheConcurrency(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	for i := 0; i < 65; i++ {
		if err := s.CreateSession(SessionSnapshot{ID: fmt.Sprintf("parallel-%d", i), Title: "0"}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	var shared sync.Mutex
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			local := NewSessionStore(s.store)
			for n := 1; n <= 8; n++ {
				// This read overlaps other goroutines' shared-session commits.
				got, ok, err := local.GetSession("parallel-64")
				if err != nil || !ok || got.ID != "parallel-64" || got.Title == "" {
					errs <- fmt.Errorf("overlapping shared read: %+v %v %v", got, ok, err)
					return
				}
				for _, id := range []string{fmt.Sprintf("parallel-%d", i), "parallel-64"} {
					if id == "parallel-64" {
						shared.Lock()
					}
					title := fmt.Sprintf("%d-%d", i, n)
					err := local.UpdateSession(SessionSnapshot{ID: id, Title: title})
					got, ok, readErr := local.GetSession(id)
					if id == "parallel-64" {
						shared.Unlock()
					}
					if err != nil || readErr != nil || !ok || got.ID != id || got.Title != title {
						errs <- fmt.Errorf("%s: write=%v read=%v found=%v title=%q want=%q", id, err, readErr, ok, got.Title, title)
						return
					}
				}
			}
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent operations timed out")
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// Purpose: bounded LRU state must reject fills from evicted generations, without
// retaining a generation map for every historical session. Pure cache operations
// isolate that invariant independently of Pebble.
func TestSessionReadCacheBound(t *testing.T) {
	var c sessionReadCache
	old, generation, _ := c.begin("old")
	for i := 0; i < sessionReadCacheLimit; i++ {
		c.begin(fmt.Sprint(i))
	}
	c.fill(old, generation, SessionSnapshot{Title: "stale"})
	_, _, value := c.begin("old")
	if value != nil || len(c.entries) != sessionReadCacheLimit {
		t.Fatal("eviction resurrected a stale value or exceeded bound")
	}
}

// Purpose: compare the real GetSession hot path to the same stored record read
// uncached, including identical normalization and lifecycle lookup. No benchmark
// numbers are asserted or fabricated; execution is left to the operator.
func BenchmarkGetSessionCached(b *testing.B) {
	s := NewSessionStore(openV3SessionEventTestStore(b))
	v := SessionSnapshot{ID: "benchmark-cache", Title: "session", Metadata: map[string]any{"nested": map[string]any{"value": "record"}}}
	if err := s.CreateSession(v); err != nil {
		b.Fatal(err)
	}
	cachedSession(b, s, v.ID)
	for _, cached := range []bool{true, false} {
		b.Run(fmt.Sprintf("cached=%v", cached), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var ok bool
				var err error
				if cached {
					_, ok, err = s.GetSession(v.ID)
				} else {
					_, ok, err = s.getSessionFromReader(s.store.db, v.ID)
				}
				if err != nil || !ok {
					b.Fatalf("read: %v %v", ok, err)
				}
			}
		})
	}
}

// Purpose: plan acceptance has a separately observed commit boundary; it must
// invalidate session snapshots just like ordinary V3 mutations. Calling the real
// acceptance service on a temporary store proves that independent wiring.
func TestSessionReadCachePlanAcceptance(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, s, "cache-plan")
	v := cachedSession(t, s, "cache-plan")
	v.Mode = "plan"
	if err := s.UpdateSession(v); err != nil {
		t.Fatal(err)
	}
	cachedSession(t, s, v.ID)
	v.Mode = "execute"
	_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: v.ID, UserID: v.UserID, AccountScopeID: v.AccountScopeID, Kind: V3SessionMutationAcceptPlan, ClientRequestID: "accept-cache-plan", PayloadHash: "accept-cache-hash", NowUnixMs: 2000, PlanAcceptance: &V3PlanAcceptanceMutation{Session: v, Plan: SessionPlanSnapshot{ID: "plan", SessionID: v.ID, Status: "approved"}, PlanEventPayload: []byte(`{}`), ModeEventPayload: []byte(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := cachedSession(t, s, v.ID); got.Mode != "auto" {
		t.Fatalf("stale mode: %q", got.Mode)
	}
}

// Purpose: BeginExecutionEpoch writes the session only when a boundary includes
// messages, whereas clear-context alone leaves the record unchanged. Exercise
// both real paths so cached records do not hide epoch counter writes.
func TestSessionReadCacheExecutionEpochAndClearContext(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, s, "cache-epoch")
	before := cachedSession(t, s, "cache-epoch")
	_, err := s.BeginExecutionEpoch(BeginExecutionEpochInput{SessionID: before.ID, UserID: before.UserID, AccountScopeID: before.AccountScopeID, ClientRequestID: "cache-epoch-boundary", PayloadHash: "cache-epoch-hash", Reason: "context_compaction_test", SourceMessageID: "source", SkipRunIntent: true, TriggerMessage: &MessageSnapshot{Role: "user", Content: "continue"}, NowUnixMs: 2000})
	if err != nil {
		t.Fatal(err)
	}
	after := cachedSession(t, s, before.ID)
	if after.MessageCount != before.MessageCount+1 {
		t.Fatalf("stale epoch count: %d", after.MessageCount)
	}
	after.Metadata = map[string]any{"swarm_v3_project_id": "cache-project"}
	if err := s.UpdateSession(after); err != nil {
		t.Fatal(err)
	}
	cachedSession(t, s, after.ID)
	seq, err := s.readV3SessionSequence(after.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: after.ID, UserID: after.UserID, AccountScopeID: after.AccountScopeID, Kind: V3SessionMutationClearContext, ClientRequestID: "cache-clear", PayloadHash: "cache-clear-hash", ExpectedLastEventSeq: &seq})
	if err != nil {
		t.Fatal(err)
	}
	if got := cachedSession(t, s, after.ID); got.MessageCount != after.MessageCount {
		t.Fatal("clear context changed session counter")
	}
}

// Purpose: staged but abandoned session writes must not publish cache values,
// and separately stored lifecycle updates must remain visible on cache hits.
// Real batch abandonment and UpsertSessionLifecycle exercise both boundaries.
func TestSessionReadCacheAbortedWriteAndLifecycle(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	v := SessionSnapshot{ID: "cache-abort", Title: "committed"}
	if err := s.CreateSession(v); err != nil {
		t.Fatal(err)
	}
	cachedSession(t, s, v.ID)
	batch := s.store.NewBatch()
	v.Title = "aborted"
	if err := s.withMutationSessionCache().setSessionInBatch(batch, v); err != nil {
		t.Fatal(err)
	}
	if err := batch.Close(); err != nil {
		t.Fatal(err)
	}
	if got := cachedSession(t, s, v.ID); got.Title != "committed" {
		t.Fatal("uncommitted record escaped")
	}
	if err := s.UpsertSessionLifecycle(SessionLifecycleSnapshot{SessionID: v.ID, Active: true}); err != nil {
		t.Fatal(err)
	}
	if got := cachedSession(t, s, v.ID); got.Lifecycle == nil || !got.Lifecycle.Active {
		t.Fatal("lifecycle read was cached")
	}
}

// Purpose: classification and fresh plain mutation must share one decode while
// session locks remain held. The per-instance decode observer counts real record
// decodes, not helper calls, at the ApplyV3SessionMutation boundary.
func TestSessionReadCacheClassificationSharesMutationCache(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, s, "cache-classify")
	decodes := 0
	s.sessionDecoded = func(string) { decodes++ }
	_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "cache-classify", UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationAppendMessage, IdempotencyKey: "classify-message", PayloadHash: "classify-hash", Message: &MessageSnapshot{Role: "assistant", Content: "answer"}, NowUnixMs: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if decodes != 1 {
		t.Fatalf("record decodes=%d, want 1", decodes)
	}
}
