package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
)

// Purpose: scheduler continuations must be synced, isolated and CAS guarded.
// Real Pebble reopen is the narrowest proof that process restart and stale workers
// cannot reset progress; no schedule or provider is executed by this test.
func TestAutomationSchedulerPositionRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil { t.Fatal(err) }
	key := AutomationRecoveryPositionKey(AutomationScope{AccountID: "a", WorkspaceID: "w"}, "d")
	if err := s.SaveAutomationSchedulerPosition(key, "", "opaque"); err != nil { t.Fatal(err) }
	if err := s.Close(); err != nil { t.Fatal(err) }
	s, err = Open(path)
	if err != nil { t.Fatal(err) }
	defer s.Close()
	var got string
	if err := s.GetAutomationSchedulerPosition(key, &got); err != nil || got != "opaque" { t.Fatal(got, err) }
	if err := s.SaveAutomationSchedulerPosition(key, "", "stale"); !errors.Is(err, ErrAutomationConflict) { t.Fatal(err) }
	if err := s.GetAutomationSchedulerPosition(key, &got); err != nil || got != "opaque" { t.Fatal(got, err) }
	got = ""
	foreign := AutomationRecoveryPositionKey(AutomationScope{AccountID: "other", WorkspaceID: "w"}, "d")
	if err := s.GetAutomationSchedulerPosition(foreign, &got); err != nil || got != "" { t.Fatal("scope leak", got, err) }
}

// Purpose: bounded SearchAutomationRecords pages must make durable progress past
// more than 1000 historical records, including zero-result revision pages. The
// real search/storage boundary proves restart continuation rather than a fake list.
func TestAutomationSchedulerHistoryPagination(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil { t.Fatal(err) }
	defer s.Close()
	m := automationFixture()
	if _, _, err := s.ApplyAutomationMutation(m); err != nil { t.Fatal(err) }
	for i := 0; i < 1050; i++ {
		id := fmt.Sprintf("occ-%04d", i)
		r := AutomationRecord{Scope: m.Record.Scope, AutomationID: "check", Kind: "occurrence", ID: id, Occurrence: &AutomationOccurrence{DefinitionRevision: 1, TriggerIdentity: id, ScheduledAt: 100000, State: "pending"}}
		if _, _, err := s.ApplyAutomationMutation(AutomationMutation{Record: r, MutationID: id, Actor: "system", SubjectID: "writer", WrittenAt: 100000}); err != nil { t.Fatal(err) }
	}
	key := AutomationRecoveryPositionKey(m.Record.Scope, "check")
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		var cursor string
		if err := s.GetAutomationSchedulerPosition(key, &cursor); err != nil { t.Fatal(err) }
		rows, next, err := s.SearchAutomationRecords(AutomationSearch{Scope: m.Record.Scope, AutomationID: "check", Kind: "occurrence", Cursor: cursor, Limit: 50})
		if err != nil { t.Fatal(err) }
		for _, r := range rows { if seen[r.ID] { t.Fatal("duplicate", r.ID) }; seen[r.ID] = true }
		if err := s.SaveAutomationSchedulerPosition(key, cursor, next); err != nil { t.Fatal(err) }
		if next == "" { break }
	}
	if len(seen) != 1050 { t.Fatalf("history starved: %d", len(seen)) }
	if _, _, _, err := s.SchedulerCatalogNext("account", "foreign-prefix"); !errors.Is(err, ErrAutomationInvalid) { t.Fatal("foreign catalog cursor accepted", err) }
}

// Purpose: catalog continuation must exceed the old 128-account cap and tolerate
// deleted seek anchors without losing the following account. Direct canonical
// catalog fixtures isolate the iterator boundary from identity provisioning.
func TestAutomationSchedulerCatalogPagination(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer s.Close()
	for i := 0; i < 140; i++ {
		id := fmt.Sprintf("a-%03d", i)
		data, err := json.Marshal(AccountScopeRecord{ID: id})
		if err != nil { t.Fatal(err) }
		if err := s.db.Set([]byte(AccountScopePrefix()+id), data, pebble.Sync); err != nil { t.Fatal(err) }
	}
	var cursor string
	for i := 0; i < 140; i++ {
		key, account, _, err := s.SchedulerCatalogNext("", cursor)
		if err != nil || account.ID != fmt.Sprintf("a-%03d", i) { t.Fatal(i, account, err) }
		cursor = key
		if err := s.db.Delete([]byte(key), pebble.Sync); err != nil { t.Fatal(err) }
	}
	key, _, _, err := s.SchedulerCatalogNext("", cursor)
	if err != nil || key != "" { t.Fatal(key, err) }
}
