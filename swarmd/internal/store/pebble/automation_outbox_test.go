package pebblestore

import "testing"

// Purpose: Claim/AckAutomationDelivery must persist bounded independent retries,
// reject foreign or nonterminal references and retain ack across reopen without
// rewriting execution. Real Pebble is the narrowest durable boundary.
func TestAutomationDeliveryRestartIsolation(t *testing.T) {
	path := t.TempDir()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	m := automationFixture()
	if _, _, err := s.ApplyAutomationMutation(m); err != nil {
		t.Fatal(err)
	}
	r := AutomationRecord{Scope: m.Record.Scope, AutomationID: "check", Kind: "occurrence", ID: "occurrence", Occurrence: &AutomationOccurrence{DefinitionRevision: 1, TriggerIdentity: "build", ScheduledAt: 1, State: "pending"}}
	for i, state := range []string{"pending", "running", "completed"} {
		r.Occurrence.State = state
		if state != "pending" {
			r.Occurrence.SessionID = "execution"
		}
		if _, _, err := s.ApplyAutomationMutation(AutomationMutation{Record: r, ExpectedRevision: uint64(i), MutationID: state, Actor: "system", SubjectID: "writer", WrittenAt: 100000}); err != nil {
			t.Fatal(err)
		}
	}
	ref := AutomationDeliveryReference{r.Scope, "check", r.ID, 3}
	foreign := ref
	foreign.Scope.AccountID = "other"
	if _, _, ok, err := s.ClaimAutomationDelivery(foreign, 1); err == nil || ok {
		t.Fatal("foreign delivery admitted")
	}
	nonterminal := ref
	nonterminal.Revision = 1
	if _, _, ok, err := s.ClaimAutomationDelivery(nonterminal, 1); err == nil || ok {
		t.Fatal("nonterminal delivery admitted")
	}
	_, attempt, ok, err := s.ClaimAutomationDelivery(ref, 1)
	if err != nil || !ok {
		t.Fatalf("claim: %v", err)
	}
	if _, _, ok, err := s.ClaimAutomationDelivery(ref, 2); err != nil || ok {
		t.Fatal("lease replay admitted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AckAutomationDelivery(attempt); err != nil {
		t.Fatal(err)
	}
	if _, d, ok, err := s.ClaimAutomationDelivery(ref, 1000000); err != nil || ok || !d.Acked {
		t.Fatal("ack lost")
	}
	got, found, err := s.GetAutomationRecord(ref.Scope, "check", "occurrence", r.ID, 0)
	if err != nil || !found || got.Revision != 3 || got.Occurrence.State != "completed" {
		t.Fatal("delivery rewrote execution")
	}
}
