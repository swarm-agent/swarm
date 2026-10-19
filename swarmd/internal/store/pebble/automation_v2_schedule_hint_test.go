package pebblestore

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func openHintTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "hint.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func hintTestRecord(account, automation, session string, nextDue int64) AutomationV2Record {
	r := AutomationV2Record{AutomationID: automation, Enabled: true, NextDueAt: nextDue, AcceptedAt: 1}
	r.AccountID, r.SessionID, r.UserID = account, session, "user-hint"
	return r
}

// Requirement: the V2 schedule hint must report the due time admission will
// actually use. Every accepted automation is indexed twice, by automation ID
// and by session ID; admission (prepareAutomationV2Execution) re-reads the
// session-ID entry, and the daily run cap path rewrites only that entry to the
// next local day. Threat: the hint taking the raw minimum across both entries
// keeps reporting the stale automation-ID due time all day, so an idle daemon
// wakes and sweeps every second. Owner: SessionStore.AutomationV2ScheduleHint.
// The fixture writes the exact two-entry state the cap path leaves behind; the
// store layer is the narrowest one that owns the index layout.
func TestAutomationV2ScheduleHintUsesAuthoritativeEntryAfterDailyCap(t *testing.T) {
	s := openHintTestStore(t)
	now := time.Now().UnixMilli()
	stale := now - int64(time.Hour/time.Millisecond)
	tomorrow := now + int64(20*time.Hour/time.Millisecond)
	byAutomation := hintTestRecord("acct-hint", "auto-capped", "sess-capped", stale)
	bySession := byAutomation
	bySession.NextDueAt = tomorrow
	if err := s.PutJSON(automationV2Key("accepted", "acct-hint", "auto-capped"), byAutomation); err != nil {
		t.Fatal(err)
	}
	if err := s.PutJSON(automationV2Key("accepted", "acct-hint", "sess-capped"), bySession); err != nil {
		t.Fatal(err)
	}
	hint, err := NewSessionStore(s).AutomationV2ScheduleHint(now)
	if err != nil {
		t.Fatal(err)
	}
	if hint.EarliestDueAt != tomorrow {
		t.Fatalf("EarliestDueAt = %d, want the capped session entry's next-day due %d (stale automation entry %d must not count)", hint.EarliestDueAt, tomorrow, stale)
	}
	if hint.AcceptedKeys != 2 {
		t.Fatalf("AcceptedKeys = %d, want 2 raw index entries for sweep paging", hint.AcceptedKeys)
	}
	if hint.LatestDueAt != 0 || hint.UpcomingDueAt != tomorrow || hint.Overflow || hint.Pending {
		t.Fatalf("hint = %+v, want nothing due now and the capped record upcoming tomorrow", hint)
	}
	// Negative control: an un-capped automation whose entries agree is still
	// reported as due, so de-duplication does not hide real due work.
	dueNow := hintTestRecord("acct-hint", "auto-due", "sess-due", stale)
	for _, key := range []string{automationV2Key("accepted", "acct-hint", "auto-due"), automationV2Key("accepted", "acct-hint", "sess-due")} {
		if err := s.PutJSON(key, dueNow); err != nil {
			t.Fatal(err)
		}
	}
	if hint, err = NewSessionStore(s).AutomationV2ScheduleHint(now); err != nil {
		t.Fatal(err)
	}
	if hint.EarliestDueAt != stale || hint.LatestDueAt != stale || hint.UpcomingDueAt != tomorrow || hint.AcceptedKeys != 4 {
		t.Fatalf("hint with a due automation = %+v, want earliest/latest due %d and upcoming %d over 4 keys", hint, stale, tomorrow)
	}
}

// Requirement: a catalog larger than the bounded hint scan must still be
// swept. Threat: the hint failing hard at its bound, so V2 automations are
// never swept again and an error is logged every second. Owner:
// SessionStore.AutomationV2ScheduleHint; the scheduler degrades to paging.
func TestAutomationV2ScheduleHintOverflowIsNotAnError(t *testing.T) {
	s := openHintTestStore(t)
	batch := s.NewBatch()
	for i := 0; i <= automationV2ScheduleHintLimit; i++ {
		r := hintTestRecord("acct-big", fmt.Sprintf("auto-%05d", i), fmt.Sprintf("sess-%05d", i), 0)
		raw := []byte(fmt.Sprintf(`{"automation_id":%q,"account_id":"acct-big","session_id":%q}`, r.AutomationID, r.SessionID))
		if err := batch.Set([]byte(automationV2Key("accepted", "acct-big", r.SessionID)), raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Commit(nil); err != nil {
		t.Fatal(err)
	}
	hint, err := NewSessionStore(s).AutomationV2ScheduleHint(time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("over-limit catalog hint returned %v, want an overflow hint the scheduler can page through", err)
	}
	if !hint.Overflow || hint.EarliestDueAt != 0 || hint.LatestDueAt != 0 || hint.UpcomingDueAt != 0 {
		t.Fatalf("over-limit hint = %+v, want Overflow with no claimed due times", hint)
	}
}

// Requirement: migrating legacy V2 automations into workers must wake the
// worker scheduler for each created worker, as every other worker write does.
// Threat: MigrateLegacyAutomationsV2 commits its own batch without a
// scheduling signal, so migrated workers are not scheduled until the next
// safety rescan and slots due before then are skipped. Owner:
// WorkerStore.MigrateLegacyAutomationsV2 and Store.SetSchedulingObserver.
func TestMigrateLegacyAutomationsV2SignalsEachCreatedWorker(t *testing.T) {
	s := openHintTestStore(t)
	const account = "acct-migrate"
	for _, id := range []string{"auto_mig_one", "auto_mig_two"} {
		r := hintTestRecord(account, id, "sess-"+id, 0)
		if err := s.PutJSON(automationV2Key("accepted", account, r.SessionID), r); err != nil {
			t.Fatal(err)
		}
	}
	var signals []SchedulingSignal
	s.SetSchedulingObserver(func(sig SchedulingSignal) { signals = append(signals, sig) })
	ws := NewWorkerStore(s)
	summary, err := ws.MigrateLegacyAutomationsV2(account)
	if err != nil || summary.FailedCount != 0 {
		t.Fatalf("migrate: %+v, %v", summary, err)
	}
	got := map[string]bool{}
	for _, sig := range signals {
		if sig.Kind != SchedulingSignalWorker || sig.AccountScopeID != account {
			t.Fatalf("unexpected signal %+v", sig)
		}
		got[sig.WorkerID] = true
	}
	if len(got) != 2 || !got["auto_mig_one"] || !got["auto_mig_two"] {
		t.Fatalf("migration signalled workers %v, want auto_mig_one and auto_mig_two", got)
	}
	// A repeat migration creates nothing and must not signal.
	signals = nil
	if summary, err = ws.MigrateLegacyAutomationsV2(account); err != nil || summary.SkippedCount != 2 {
		t.Fatalf("repeat migrate: %+v, %v", summary, err)
	}
	if len(signals) != 0 {
		t.Fatalf("repeat migration signalled %+v, want none", signals)
	}
}
