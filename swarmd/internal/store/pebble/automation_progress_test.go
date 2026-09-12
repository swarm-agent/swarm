package pebblestore

import (
	"errors"
	"testing"
)

// Purpose: progress classification must not change across occurrence revisions.
// ApplyAutomationMutation is the canonical CAS boundary; real Pebble verifies
// rejected trigger-kind edits leave both the head and immutable history intact.
func TestAutomationProgressTriggerImmutable(t *testing.T) {
	s := openTaskProgramTestStore(t)
	def := automationFixture()
	if _, _, err := s.ApplyAutomationMutation(def); err != nil {
		t.Fatal(err)
	}
	m := AutomationMutation{SubjectID: "writer", Actor: "system", WrittenAt: 100000, MutationID: "admit", Record: AutomationRecord{Scope: def.Record.Scope, AutomationID: "check", Kind: "occurrence", ID: "run", Occurrence: &AutomationOccurrence{DefinitionRevision: 1, TriggerIdentity: "trigger", TriggerKind: "manual", State: "pending"}}}
	if _, _, err := s.ApplyAutomationMutation(m); err != nil {
		t.Fatal(err)
	}
	m.ExpectedRevision = 1
	m.MutationID = "forge"
	m.Record.Occurrence.TriggerKind = "schedule"
	m.Record.Occurrence.State = "running"
	if _, _, err := s.ApplyAutomationMutation(m); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("kind mutation: %v", err)
	}
	for _, revision := range []uint64{0, 1} {
		got, found, err := s.GetAutomationRecord(def.Record.Scope, "check", "occurrence", "run", revision)
		if err != nil || !found || got.Revision != 1 || got.Occurrence.TriggerKind != "manual" || got.Occurrence.State != "pending" {
			t.Fatalf("partial mutation: %+v %v", got, err)
		}
	}
}
