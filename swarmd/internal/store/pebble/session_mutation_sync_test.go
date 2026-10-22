package pebblestore

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
)

// openStrictMutationStore supplies a test-only VFS without changing production
// Open's OS directory permission checks. The store and mutation pipeline are real.
func openStrictMutationStore(t testing.TB, fs vfs.FS) *Store {
	t.Helper()
	opts := storeOptions(false)
	opts.FS = fs
	db, err := pebble.Open("mutation-db", opts)
	if err != nil {
		opts.Cache.Unref()
		t.Fatal(err)
	}
	return &Store{db: db, cache: opts.Cache, path: "mutation-db", sessionMutations: newSessionMutationCoordinator()}
}

// Purpose: ApplyV3SessionMutation must not acknowledge volatile accounting.
// StrictMem discards unsynced WAL data, the narrowest real storage layer proving
// receipts, daily accumulators and account rollups survive an abrupt crash.
func TestSessionMutationPostUnlockSyncDurability(t *testing.T) {
	fs := vfs.NewStrictMem()
	store := openStrictMutationStore(t, fs)
	s := NewSessionStore(store)
	const count, receipts = 8, 3
	const now int64 = 1767225600000
	for i := 0; i < count; i++ {
		createV3SessionForTest(t, s, fmt.Sprintf("session-%d", i))
	}
	// Do not let the separately synced outbox head mask a missing WAL barrier.
	store.sessionMutations.publishOutboxHead = func(*Store, uint64) error { return nil }
	done := make(chan error, count)
	for i := 0; i < count; i++ {
		go func(i int) {
			for j := 0; j < receipts; j++ {
				if _, err := s.ApplyV3SessionMutation(lockTestUsage(fmt.Sprintf("session-%d", i), fmt.Sprintf("receipt-%d", j), now)); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}(i)
	}
	for i := 0; i < count; i++ {
		awaitLockMutation(t, done)
	}
	// Closing must not accidentally make any additional writes durable.
	fs.SetIgnoreSyncs(true)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	fs.ResetToSyncedState()
	fs.SetIgnoreSyncs(false)
	store = openStrictMutationStore(t, fs)
	defer store.Close()
	s = NewSessionStore(store)
	date := time.UnixMilli(now).UTC().Format("2006-01-02")
	acc, ok, err := s.GetDailyUsageAccumulator("account-1", date)
	if err != nil || !ok || acc.TotalTokens != count*receipts*15 || acc.TotalCostUSD != count*receipts*0.125 || acc.TurnCount != count*receipts {
		t.Fatalf("daily after crash: %+v found=%v err=%v", acc, ok, err)
	}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("session-%d", i)
		roll, ok, err := s.GetAccountUsageRollup("account-1", date, id, "test", "test")
		if err != nil || !ok || roll.TotalTokens != receipts*15 || roll.TokenCostUSD != receipts*0.125 || roll.Turns != receipts {
			t.Fatalf("rollup after crash: %+v found=%v err=%v", roll, ok, err)
		}
		for j := 0; j < receipts; j++ {
			usage, ok, err := s.GetTurnUsage(id, fmt.Sprintf("receipt-%d", j))
			if err != nil || !ok || usage.TotalTokens != 15 || usage.EstimatedCostUSD != 0.125 {
				t.Fatalf("receipt after crash: %+v found=%v err=%v", usage, ok, err)
			}
		}
	}
}

// Purpose: the post-unlock WAL barrier must neither retain the shared account
// lock nor publish/return success early. Real mutations and coordinator state
// prove admission and failure behavior without timing disk performance.
func TestSessionMutationPostUnlockSyncOrdering(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			s := NewSessionStore(openV3SessionEventTestStore(t))
			createV3SessionForTest(t, s, "first")
			createV3SessionForTest(t, s, "second")
			c := s.store.sessionMutations
			baseline := c.publishedHead
			var signals atomic.Int32
			s.store.SetSchedulingObserver(func(signal SchedulingSignal) {
				if signal.Kind == SchedulingSignalBudget {
					signals.Add(1)
				}
			})
			entered := make(chan string, 2)
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			injected := errors.New("injected WAL sync failure")
			c.beforePostUnlockSync = func(id string) error {
				entered <- id
				<-release
				if fail {
					return injected
				}
				return nil
			}
			done := make(chan error, 2)
			start := func(id string) {
				go func() { _, err := s.ApplyV3SessionMutation(lockTestUsage(id, "receipt", 1767225600000)); done <- err }()
				select {
				case got := <-entered:
					if got != id {
						t.Fatalf("sync for %s, want %s", got, id)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("account lock retained through sync")
				}
			}
			start("first")
			start("second") // Reaches its own committed batch while first is in sync.
			c.outboxMu.Lock()
			head, committed := c.publishedHead, len(c.committed)
			c.outboxMu.Unlock()
			if signals.Load() != 0 {
				t.Fatal("scheduling published before sync")
			}
			if head != baseline || committed != 0 {
				t.Fatal("outbox committed before sync")
			}
			select {
			case err := <-done:
				t.Fatalf("returned before sync: %v", err)
			default:
			}
			once.Do(func() { close(release) })
			for i := 0; i < 2; i++ {
				select {
				case err := <-done:
					if fail && !errors.Is(err, injected) || !fail && err != nil {
						t.Fatalf("unexpected result: %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("mutation did not return")
				}
			}
			c.outboxMu.Lock()
			head = c.publishedHead
			committed = len(c.committed)
			c.outboxMu.Unlock()
			if fail && signals.Load() != 0 {
				t.Fatal("sync failure published scheduling")
			}
			if !fail && signals.Load() != 2 {
				t.Fatalf("budget signals: %d", signals.Load())
			}
			if fail && (head != baseline || committed != 0) {
				t.Fatal("sync failure published outbox")
			}
			if !fail && head != baseline+2 {
				t.Fatalf("published head %d, want %d", head, baseline+2)
			}
		})
	}
}

// Purpose: measure real same-account TurnUsage mutation throughput through
// ApplyV3SessionMutation with 64 independent sessions, not simulated agent work.
func BenchmarkConcurrentUsageMutationsSameAccount(b *testing.B) {
	s := NewSessionStore(openV3SessionEventTestStore(b))
	const workers = 64
	for i := 0; i < workers; i++ {
		id := fmt.Sprintf("session-%d", i)
		_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationCreateSession, IdempotencyKey: "create-" + id, RequestHash: "create-" + id, Session: &SessionSnapshot{ID: id, WorkspacePath: "/workspace", Title: id}, NowUnixMs: 1000})
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := worker; i < b.N; i += workers {
				if _, err := s.ApplyV3SessionMutation(lockTestUsage(fmt.Sprintf("session-%d", worker), fmt.Sprintf("receipt-%d", i), 1767225600000)); err != nil {
					b.Error(err)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
}
