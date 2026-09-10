package pebblestore

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: automation head/history/receipt and content-free V3 invalidation
// are one commit. Threats: rejected writes leaking signals, retries duplicating
// events, restart losing updates, and account-crossing participants. This real
// temporary-store test exercises the canonical participant rather than mocks.
func TestAutomationRealtimeAtomicRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil { t.Fatal(err) }
	defer func() { s.Close() }()
	m := automationFixture()
	wakes := 0
	s.SetAutomationPublisher(func(record V3RealtimeOutboxRecord) {
		wakes++
		// Requirement: post-commit callbacks may reenter domain authorities.
		// TryLock proves release without leaving a hung goroutine on regression.
		if !s.automationsMu.TryLock() { t.Fatal("publisher holds automation lock") }
		s.automationsMu.Unlock()
		if _, fresh, err := s.ApplyAutomationMutation(m); err != nil || fresh { t.Fatalf("callback retry: %v %v", fresh, err) }
		if _, found, err := NewSessionStore(s).GetSession(record.SessionID); err != nil || found { t.Fatalf("synthetic session exposed: %v %v", found, err) }
	})
	check := func(want int) {
		t.Helper()
		rows, err := NewSessionStore(s).ListV3RealtimeOutboxAfter(0, 100)
		if err != nil || len(rows) != want { t.Fatalf("outbox count %d want %d: %v", len(rows), want, err) }
		for _, r := range rows {
			if r.AccountScopeID != m.Record.Scope.AccountID || r.Event.EventType != AutomationChangedEventType || string(r.Event.Payload) != "{}" { t.Fatalf("invalid signal: %+v", r) }
		}
	}
	if _, _, err := s.ApplyAutomationMutation(m); err != nil { t.Fatal(err) }
	check(1)
	if _, fresh, err := s.ApplyAutomationMutation(m); err != nil || fresh { t.Fatalf("replay: %v %v", fresh, err) }
	stale := automationFixture()
	stale.MutationID = "stale"
	if _, _, err := s.ApplyAutomationMutation(stale); err == nil { t.Fatal("stale write accepted") }
	key, _ := automationKey(m.Record)
	participant := &automationRealtimeMutation{scope: m.Record.Scope, writes: map[string]json.RawMessage{key + ":head": json.RawMessage(`{}`), "zz:foreign:key": json.RawMessage(`{}`)}}
	if err := s.commitAutomationRealtime(participant); err == nil { t.Fatal("foreign participant accepted") }
	if participant.outbox != nil { t.Fatal("failed participant retained publication") }
	var foreign any
	if found, err := s.GetJSON("zz:foreign:key", &foreign); err != nil || found { t.Fatalf("foreign write persisted: %v %v", found, err) }
	got, found, err := s.GetAutomationRecord(m.Record.Scope, "check", "definition", "check", 0)
	if err != nil || !found || got.Revision != 1 || got.Definition == nil { t.Fatalf("partial head: %+v %v", got, err) }
	check(1)
	g := AutomationApproval{Scope: m.Record.Scope, ID: "grant", AutomationID: "check", DefinitionRevision: 1, PolicySHA256: strings.Repeat("a", 64), SubjectID: "writer", WrittenAt: 100, ExpiresAt: 200}
	if _, err := s.CreateAutomationApproval(g); err != nil { t.Fatal(err) }
	if _, err := s.RevokeAutomationApproval(g.Scope, g.ID, "writer", 1, 150); err != nil { t.Fatal(err) }
	if _, err := s.RevokeAutomationApproval(g.Scope, g.ID, "writer", 1, 150); err == nil { t.Fatal("stale revocation accepted") }
	check(3)
	if wakes != 3 { t.Fatalf("wakes %d", wakes) }
	occurrence := AutomationMutation{SubjectID: "writer", Actor: "user", WrittenAt: 100, MutationID: "occurrence", Record: AutomationRecord{Scope: m.Record.Scope, AutomationID: "check", Kind: "occurrence", ID: "occ", Occurrence: &AutomationOccurrence{DefinitionRevision: 1, TriggerIdentity: "manual", State: "pending"}}}
	if _, _, err := s.ApplyAutomationMutation(occurrence); err != nil { t.Fatal(err) }
	admitted, err := s.AdmitAutomationCancellation(m.Record.Scope, "check", "occ", 1, "cancel", "writer", 110)
	if err != nil { t.Fatal(err) }
	if _, err := s.FinishAutomationCancellation(admitted, "writer", 120); err != nil { t.Fatal(err) }
	check(6)
	if wakes != 6 { t.Fatalf("cancel wakes %d", wakes) }
	if err := s.Close(); err != nil { t.Fatal(err) }
	s, err = Open(path)
	if err != nil { t.Fatal(err) }
	check(6)
	if _, fresh, err := s.ApplyAutomationMutation(m); err != nil || fresh { t.Fatalf("restart replay: %v %v", fresh, err) }
	check(6)
	grant, ok, err := s.GetAutomationApproval(g.Scope, g.ID)
	if err != nil || !ok || grant.RevokedAt != 150 { t.Fatalf("revocation lost: %+v %v", grant, err) }
}
