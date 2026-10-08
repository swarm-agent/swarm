package pebblestore

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

func dailyRebuildStore(t *testing.T) *SessionStore {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "usage.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewSessionStore(db)
}

func seedDailyReceipt(t *testing.T, s *SessionStore, key string, rec any) {
	t.Helper()
	value, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.db.Set([]byte(key), value, pebble.NoSync); err != nil {
		t.Fatal(err)
	}
}

// Purpose: GetTodayUsageTotal's cold-cache path must count every receipt in the
// UTC day without leaking another account or truncating at the old list limits.
// Direct legacy records in a temporary Pebble store exercise the actual rebuild
// boundary without creating accumulators via the normal receipt writers.
func TestDailyUsageRebuildCompleteAndScoped(t *testing.T) {
	s := dailyRebuildStore(t)
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	start := now.Add(-12 * time.Hour).UnixMilli()
	for i := 0; i < 10001; i++ {
		id := fmt.Sprintf("r-%05d", i)
		seedDailyReceipt(t, s, KeySessionTurnUsage("session", id), SessionTurnUsageSnapshot{
			SessionID: "session", RunID: id, AccountScopeID: "account", CreatedAt: start,
			EstimatedCostUSD: 1, TotalTokens: 2,
		})
	}
	for i := 0; i < 2001; i++ {
		id := fmt.Sprintf("m-%05d", i)
		seedDailyReceipt(t, s, KeySessionMediaUsage("account", id), SessionMediaUsageRecord{
			ID: id, AccountScopeID: "account", CreatedAt: start, CostUSD: 1,
		})
	}
	for _, tc := range []struct {
		id, account string
		ts          int64
	}{
		{"old", "account", start - 1},
		{"future", "account", start + 86400000},
		{"foreign", "other", start},
		{"unscoped", "", start},
	} {
		seedDailyReceipt(t, s, KeySessionTurnUsage("session", tc.id), SessionTurnUsageSnapshot{
			SessionID: "session", RunID: tc.id, AccountScopeID: tc.account,
			CreatedAt: tc.ts, UpdatedAt: now.UnixMilli(), EstimatedCostUSD: 100, TotalTokens: 100,
		})
		seedDailyReceipt(t, s, KeySessionMediaUsage("account", tc.id), SessionMediaUsageRecord{
			ID: tc.id, AccountScopeID: tc.account, CreatedAt: tc.ts, CostUSD: 100,
		})
	}
	seedDailyReceipt(t, s, KeySessionTurnUsage("session", "fallback-time"), SessionTurnUsageSnapshot{
		SessionID: "session", RunID: "fallback-time", AccountScopeID: "account",
		UpdatedAt: start + 86399999, EstimatedCostUSD: 3, TotalTokens: 5,
	})
	for i := 0; i < 2; i++ {
		cost, tokens, err := s.getUsageTotalForDay(" account ", now)
		if err != nil || cost != 12005 || tokens != 20007 {
			t.Fatalf("cost=%v tokens=%v err=%v", cost, tokens, err)
		}
	}
	acc, found, err := s.GetDailyUsageAccumulator("account", "2026-05-04")
	if err != nil || !found || acc.TurnCount != 10002 || acc.MediaCalls != 2001 || !acc.PricingCoverageIncomplete {
		t.Fatalf("acc=%+v found=%v err=%v", acc, found, err)
	}
}

// Purpose: a corrupt cache or receipt must fail closed and never publish partial
// totals. The real Pebble rebuild is the narrowest layer owning that guarantee.
func TestDailyUsageRebuildFailureDoesNotCache(t *testing.T) {
	for _, prefix := range []string{"turn", "media", "cache"} {
		t.Run(prefix, func(t *testing.T) {
			s := dailyRebuildStore(t)
			now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
			cacheKey := KeyDailyUsageAccumulator("account", "2026-05-04")
			key := KeySessionTurnUsage("session", "bad")
			if prefix == "media" {
				key = KeySessionMediaUsage("account", "bad")
			} else if prefix == "cache" {
				key = cacheKey
			}
			if err := s.store.db.Set([]byte(key), []byte("{"), pebble.NoSync); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.getUsageTotalForDay("account", now); err == nil {
				t.Fatal("expected corruption error")
			}
			var acc DailyUsageAccumulator
			found, err := s.store.GetJSON(cacheKey, &acc)
			if prefix == "cache" {
				if err == nil {
					t.Fatal("corrupt cache was replaced")
				}
			} else if err != nil || found {
				t.Fatalf("partial cache: found=%v err=%v", found, err)
			}
		})
	}
}

// Purpose: concurrent wrappers must wait for the account mutation and recheck
// the cache, not scan corrupt history or overwrite committed deltas. Holding the
// production account lock gives a deterministic race boundary without sleeps.
func TestDailyUsageRebuildSerializesWithWriters(t *testing.T) {
	s := dailyRebuildStore(t)
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	if err := s.store.db.Set([]byte(KeySessionTurnUsage("session", "bad")), []byte("{"), pebble.NoSync); err != nil {
		t.Fatal(err)
	}
	unlock := s.store.sessionMutations.lockSessions("account:account")
	results := make(chan error, 16)
	for i := 0; i < cap(results); i++ {
		go func() {
			cost, tokens, err := NewSessionStore(s.store).getUsageTotalForDay("account", now)
			if err == nil && (cost != 7 || tokens != 11) {
				err = fmt.Errorf("incorrect totals: %v/%v", cost, tokens)
			}
			results <- err
		}()
	}
	err := s.PutDailyUsageAccumulator(DailyUsageAccumulator{AccountScopeID: "account", Date: "2026-05-04", TotalCostUSD: 7, TotalTokens: 11})
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for i := 0; i < cap(results); i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatal("concurrent reads did not finish")
		}
	}
}

// Purpose: rebuilding must not decode/retain provider history object graphs.
// Measure actual Go allocations of the real temporary-store fallback (not a
// benchmark or provider workload). The encoded fixture is ~5 MiB; decoding its
// 409600 history maps would exceed this deliberately generous 24 MiB budget.
func TestDailyUsageRebuildMemory(t *testing.T) {
	s := dailyRebuildStore(t)
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	history := "[" + strings.Repeat(`{"value":1},`, 2047) + `{"value":1}]`
	payload := []byte(fmt.Sprintf(`{"session_id":"session","run_id":"run","account_scope_id":"account","created_at":%d,"total_tokens":2,"estimated_cost_usd":1,"api_usage_history":%s}`, now.UnixMilli(), history))
	for i := 0; i < 200; i++ {
		if err := s.store.db.Set([]byte(KeySessionTurnUsage("session", fmt.Sprint(i))), payload, pebble.NoSync); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.db.Flush(); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	cost, tokens, err := s.getUsageTotalForDay("account", now)
	runtime.ReadMemStats(&after)
	if err != nil || cost != 200 || tokens != 400 {
		t.Fatalf("cost=%v tokens=%v err=%v", cost, tokens, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 24<<20 {
		t.Fatalf("rebuild allocated %d bytes; provider history must remain undecoded", allocated)
	}
}

// Purpose: empty days must be durably cached and reused, while legacy baseline
// cost and UpdatedAt fallback remain compatible. A temp-store read after restart
// proves the cache is durable rather than a process-local shortcut.
func TestDailyUsageRebuildEmptyAndLegacy(t *testing.T) {
	s := dailyRebuildStore(t)
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	if _, _, err := s.getUsageTotalForDay(" ", now); err == nil {
		t.Fatal("blank account accepted")
	}
	cost, tokens, err := s.getUsageTotalForDay("empty", now)
	if err != nil || cost != 0 || tokens != 0 {
		t.Fatalf("empty totals: %v/%v %v", cost, tokens, err)
	}
	if _, found, err := s.GetDailyUsageAccumulator("empty", "2026-05-04"); err != nil || !found {
		t.Fatalf("empty day not cached: %v/%v", found, err)
	}
	seedDailyReceipt(t, s, KeySessionTurnUsage("legacy", "run"), SessionTurnUsageSnapshot{
		SessionID: "legacy", RunID: "run", Provider: "openai", Model: "gpt-4o",
		InputTokens: 1000000, TotalTokens: 1000000, UpdatedAt: now.UnixMilli(),
	})
	cost, tokens, err = s.getUsageTotalForDay("default", now)
	if err != nil || cost != 2.5 || tokens != 1000000 {
		t.Fatalf("legacy totals: %v/%v %v", cost, tokens, err)
	}
	// Close and reopen the same isolated store; no live database is involved.
	path := s.store.path
	if err := s.store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cost, tokens, err = NewSessionStore(db).getUsageTotalForDay("default", now)
	if err != nil || cost != 2.5 || tokens != 1000000 {
		t.Fatalf("restarted totals: %v/%v %v", cost, tokens, err)
	}
}

// Purpose: cold-cache readers and atomic daily increments must not lose updates.
// Real concurrent store operations across wrappers exercise the shared account
// lock; exact final totals detect stale rebuild publication after a writer.
func TestDailyUsageRebuildConcurrentIncrements(t *testing.T) {
	s := dailyRebuildStore(t)
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	results := make(chan error, 32)
	for i := 0; i < 16; i++ {
		go func() {
			<-start
			_, err := NewSessionStore(s.store).IncrementDailyUsage("account", "2026-05-04", 1, 2)
			results <- err
		}()
		go func() {
			<-start
			_, _, err := NewSessionStore(s.store).getUsageTotalForDay("account", now)
			results <- err
		}()
	}
	close(start)
	deadline := time.After(10 * time.Second)
	for i := 0; i < cap(results); i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatal("concurrent operations did not finish")
		}
	}
	cost, tokens, err := s.getUsageTotalForDay("account", now)
	if err != nil || cost != 16 || tokens != 32 {
		t.Fatalf("lost increments: %v/%v %v", cost, tokens, err)
	}
}
