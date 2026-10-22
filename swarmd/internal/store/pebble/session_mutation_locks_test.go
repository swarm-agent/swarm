package pebblestore

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func lockTestMessage(id, request string) V3SessionMutationInput {
	return V3SessionMutationInput{SessionID: id, UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationAppendMessage, IdempotencyKey: request, RequestHash: request, Message: &MessageSnapshot{ID: request, Role: "assistant", Content: "progress"}}
}
func lockTestUsage(id, request string, now int64) V3SessionMutationInput {
	in := lockTestMessage(id, request)
	in.NowUnixMs = now
	in.TurnUsage = &SessionTurnUsageSnapshot{RunID: request, Provider: "test", Model: "test", InputTokens: 10, OutputTokens: 5, TotalTokens: 15, EstimatedCostUSD: 0.125, CostProvenance: "provider", PriceStatus: "known", CreatedAt: now}
	return in
}
func awaitLockMutation(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("mutation timed out")
	}
}

// Purpose: ApplyV3SessionMutation must serialize shared accounting without losing
// or duplicating receipts when unrelated sessions append concurrently. Real
// temporary Pebble batches are the narrowest layer proving all persisted totals.
func TestSessionMutationConcurrentExactUsage(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	const count, receipts = 100, 3
	now := time.Now().UTC().UnixMilli()
	date := time.UnixMilli(now).UTC().Format("2006-01-02")
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("session-%d", i)
		createV3SessionForTest(t, s, id)
		snapshot, _, err := s.GetSession(id)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Metadata = map[string]any{SessionEndUserMetadataKey: EndUserMetadata("test-app", "shared-user", nil)}
		if err := s.UpdateSession(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, count)
	for i := 0; i < count; i++ {
		go func(i int) {
			id := fmt.Sprintf("session-%d", i)
			for j := 0; j < receipts; j++ {
				if _, err := s.ApplyV3SessionMutation(lockTestMessage(id, fmt.Sprintf("plain-%d", j))); err != nil {
					done <- err
					return
				}
				in := lockTestUsage(id, fmt.Sprintf("receipt-%d", j), now)
				if _, err := s.ApplyV3SessionMutation(in); err != nil {
					done <- err
					return
				}
				// Exact replay must not double-count the same receipt.
				if _, err := s.ApplyV3SessionMutation(in); err != nil {
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
	acc, ok, err := s.GetDailyUsageAccumulator("account-1", date)
	if err != nil || !ok || acc.TotalTokens != count*receipts*15 || acc.TotalCostUSD != count*receipts*0.125 || acc.TurnCount != count*receipts {
		t.Fatalf("daily: %+v found=%v err=%v", acc, ok, err)
	}
	var tokens int64
	var cost float64
	var turns int
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("session-%d", i)
		roll, ok, err := s.GetAccountUsageRollup("account-1", date, id, "test", "test")
		if err != nil || !ok || roll.TotalTokens != receipts*15 || roll.TokenCostUSD != receipts*0.125 || roll.Turns != receipts {
			t.Fatalf("rollup: %+v found=%v err=%v", roll, ok, err)
		}
		tokens += roll.TotalTokens
		cost += roll.TokenCostUSD
		turns += roll.Turns
		usage, ok, err := s.GetTurnUsage(id, "receipt-2")
		if err != nil || !ok || len(usage.ScopeTotals) == 0 {
			t.Fatalf("receipt: %+v %v", usage, err)
		}
		for _, scope := range usage.ScopeTotals {
			total, ok, err := s.GetUsageScope("account-1", scope.Kind, scope.ProjectID, scope.ID)
			if err != nil || !ok || total.TotalTokens != count*receipts*15 || total.ProviderCostUSD != count*receipts*0.125 || total.ReceiptCount != count*receipts {
				t.Fatalf("scope: %+v found=%v err=%v", total, ok, err)
			}
		}
	}
	if tokens != acc.TotalTokens || cost != acc.TotalCostUSD || turns != acc.TurnCount {
		t.Fatal("aggregate rollups differ from daily accumulator")
	}
	todayCost, todayTokens, err := s.GetTodayUsageTotal("account-1")
	if err != nil || todayCost != acc.TotalCostUSD || todayTokens != acc.TotalTokens {
		t.Fatalf("today: %v %v %v", todayCost, todayTokens, err)
	}
}

// Purpose: the real commit seam proves plain writes do not queue behind another
// session, while accounting still does. This store-level test exercises actual
// keyed-lock ownership rather than timing a synthetic workload.
func TestSessionMutationAccountLockParallelism(t *testing.T) {
	for _, usage := range []bool{false, true} {
		t.Run(fmt.Sprint(usage), func(t *testing.T) {
			s := NewSessionStore(openV3SessionEventTestStore(t))
			createV3SessionForTest(t, s, "first")
			createV3SessionForTest(t, s, "second")
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			s.store.sessionMutations.beforeDurableCommit = func(id string) {
				if id == "first" {
					close(entered)
					<-release
				}
			}
			first, second := make(chan error, 1), make(chan error, 1)
			makeInput := func(id string) V3SessionMutationInput {
				if usage {
					return lockTestUsage(id, "write", time.Now().UnixMilli())
				}
				return lockTestMessage(id, "write")
			}
			go func() { _, err := s.ApplyV3SessionMutation(makeInput("first")); first <- err }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("hook not reached")
			}
			started := make(chan struct{})
			go func() { close(started); _, err := s.ApplyV3SessionMutation(makeInput("second")); second <- err }()
			<-started
			if usage {
				select {
				case err := <-second:
					t.Fatalf("usage escaped account lock: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
			} else {
				awaitLockMutation(t, second)
			}
			once.Do(func() { close(release) })
			awaitLockMutation(t, first)
			if usage {
				awaitLockMutation(t, second)
			}
		})
	}
}

// Purpose: projectsMu must not gate ordinary run starts, but task identity gained
// through a canonical mutation must escalate the next write. The precommit seam
// observes lock admission even when a deliberately minimal task fixture fails
// later domain validation; the plain branch additionally commits successfully.
func TestSessionMutationProjectLockAndEscalation(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, s, "plain")
	createV3SessionForTest(t, s, "task")
	snap, _, err := s.GetSession("task")
	if err != nil {
		t.Fatal(err)
	}
	snap.Metadata = map[string]any{"task_id": "task-1", "task_attempt_id": "attempt-1"}
	in := lockTestMessage("task", "metadata")
	in.Kind = V3SessionMutationUpdateSettings
	in.Message = nil
	in.Session = &snap
	if _, err := s.ApplyV3SessionMutation(in); err != nil {
		t.Fatal(err)
	}
	unlock := s.store.sessionMutations.lockSessions("task")
	plain, err := s.isPlainSessionMutation(lockTestMessage("task", "next"))
	unlock()
	if err != nil || plain {
		t.Fatalf("task metadata did not escalate: plain=%v err=%v", plain, err)
	}
	makeRun := func(id string) V3SessionMutationInput {
		in := lockTestMessage(id, "start")
		in.Message.Role = "user"
		in.RunIntent = &V3SessionRunIntent{RunID: "run-" + id, Status: V3RunIntentPendingExecutor}
		return in
	}
	s.store.projectsMu.Lock()
	var once sync.Once
	release := func() { once.Do(func() { s.store.projectsMu.Unlock() }) }
	defer release()
	done := make(chan error, 1)
	go func() { _, err := s.ApplyV3SessionMutation(makeRun("plain")); done <- err }()
	awaitLockMutation(t, done)
	started := make(chan struct{})
	go func() { close(started); _, err := s.ApplyV3SessionMutation(makeRun("task")); done <- err }()
	<-started
	select {
	case err := <-done:
		t.Fatalf("task escaped project lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("missing task authority was accepted")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("task remained blocked")
	}
	messages, err := s.ListV3SessionMessages("task", 0, 10)
	if err != nil || len(messages) != 0 {
		t.Fatalf("rejected task start persisted messages: %v %v", messages, err)
	}
}

// Purpose: shared usage writes without lock evidence must reject before changing
// a batch, preventing future unclassified mutations from corrupting totals.
func TestSessionMutationAccountWriteGuard(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	batch := s.store.NewBatch()
	defer batch.Close()
	guards := []func() error{
		func() error { return s.setUsageDays(batch, SessionTurnUsageSnapshot{}, SessionTurnUsageSnapshot{}) },
		func() error { return s.setMediaUsageDays(batch, SessionMediaUsageRecord{}) },
		func() error { return s.setUsageBinding(batch, "account-1", "s", nil) },
		func() error { return s.setAccountWorkerBudgetHolds(batch, DailyUsageAccumulator{}) },
		func() error { return s.setDailyUsageAccumulatorPayload(batch, DailyUsageAccumulator{}, nil) },
		func() error {
			return s.updateAccountUsageRollupInBatch(batch, "account-1", "2026-01-01", "s", "test", "test", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
		},
	}
	for i, guard := range guards {
		if err := guard(); err == nil || batch.Count() != 0 {
			t.Fatalf("guard %d: err=%v count=%d", i, err, batch.Count())
		}
	}
	if err := s.setUsageScopeTotals(batch, "account-1", []UsageScopeTotal{{Kind: "session", ID: "s", TotalTokens: 1}}); err == nil {
		t.Fatal("unguarded write accepted")
	}
	if batch.Count() != 0 {
		t.Fatal("rejected write changed batch")
	}
	if err := s.bindTaskUsageInBatch(batch, "account-1", ProjectTaskRecord{}); err == nil {
		t.Fatal("unguarded task binding accepted")
	}
	if batch.Count() != 0 {
		t.Fatal("rejected task binding changed batch")
	}
}

// Benchmark actual Pebble append mutations, not provider throughput. Each of 64
// goroutines owns its own session; b.N is the total number of plain messages.
func BenchmarkConcurrentSessionMutationsSameAccount(b *testing.B) {
	s := NewSessionStore(openV3SessionEventTestStore(b))
	for i := 0; i < 64; i++ {
		id := fmt.Sprintf("bench-%d", i)
		if err := s.CreateSession(SessionSnapshot{ID: id, UserID: "user-1", AccountScopeID: "account-1"}); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := i; j < b.N; j += 64 {
				if _, err := s.ApplyV3SessionMutation(lockTestMessage(fmt.Sprintf("bench-%d", i), fmt.Sprintf("msg-%d", j))); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		b.Fatal(err)
	}
}
