package pebblestore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func automationFixture() AutomationMutation {
	return AutomationMutation{Actor: "user", MutationID: "create", Record: AutomationRecord{
		Scope: AutomationScope{AccountID: "account-a", WorkspaceID: "workspace-a"}, AutomationID: "check", Kind: "definition", ID: "check",
		Definition: &AutomationDefinition{Name: "Check", Plan: AutomationPlanReference{SessionID: "session", PlanID: "plan", Revision: 1}, Schedule: AutomationSchedulePolicy{Kind: "manual", MissedPolicy: "skip", OverlapPolicy: "independent"}, Authorization: AutomationAuthorizationPolicy{Mode: "approval_required"}},
	}}
}

// Purpose: ApplyAutomationMutation/GetAutomationRecord own isolated, immutable
// definition revisions. This narrow real-store test prevents cross-scope reads,
// stale writes and restart replay from changing the head or historical result.
func TestAutomationStoreScopeRevisionRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil { t.Fatal(err) }
	m := automationFixture()
	if _, fresh, err := s.ApplyAutomationMutation(m); err != nil || !fresh { t.Fatalf("create: %v %v", fresh, err) }
	update := automationFixture()
	update.ExpectedRevision, update.MutationID = 1, "update"
	update.Record.Definition.Name = "Updated"
	if _, _, err := s.ApplyAutomationMutation(update); err != nil { t.Fatal(err) }
	stale := update
	stale.MutationID = "stale"
	if _, _, err := s.ApplyAutomationMutation(stale); !errors.Is(err, ErrAutomationConflict) { t.Fatalf("stale: %v", err) }
	for _, scope := range []AutomationScope{{AccountID: "other", WorkspaceID: "workspace-a"}, {AccountID: "account-a", WorkspaceID: "other"}} {
		if _, found, err := s.GetAutomationRecord(scope, "check", "definition", "check", 0); err != nil || found { t.Fatalf("scope leak: %v %v", found, err) }
		q := AutomationSearch{Scope: scope, Limit: 10}
		rows, _, err := s.SearchAutomationRecords(q)
		if err != nil || len(rows) != 0 { t.Fatalf("search leak: %v %v", rows, err) }
	}
	if err := s.Close(); err != nil { t.Fatal(err) }
	s, err = Open(path)
	if err != nil { t.Fatal(err) }
	defer s.Close()
	got, fresh, err := s.ApplyAutomationMutation(m)
	if err != nil || fresh || got.Revision != 1 { t.Fatalf("restart replay: %+v %v %v", got, fresh, err) }
	m.Record.Definition.Name = "Collision"
	if _, _, err := s.ApplyAutomationMutation(m); !errors.Is(err, ErrAutomationConflict) { t.Fatalf("collision: %v", err) }
	head, _, err := s.GetAutomationRecord(m.Record.Scope, "check", "definition", "check", 0)
	if err != nil || head.Revision != 2 || head.Definition.Name != "Updated" { t.Fatalf("head changed: %+v %v", head, err) }
	old, _, err := s.GetAutomationRecord(m.Record.Scope, "check", "definition", "check", 1)
	if err != nil || old.Definition.Name != "Check" { t.Fatalf("history changed: %+v %v", old, err) }
}

// Purpose: atomic revision CAS, trigger uniqueness and terminal state guards at
// ApplyAutomationMutation must reject competing/invalid writes without partial
// head/revision/index state. Real Pebble is the narrowest transactional layer.
func TestAutomationStoreAtomicOccurrence(t *testing.T) {
	s := openTaskProgramTestStore(t)
	def := automationFixture()
	if _, _, err := s.ApplyAutomationMutation(def); err != nil { t.Fatal(err) }
	m := AutomationMutation{Actor: "system", MutationID: "create", Record: AutomationRecord{Scope: def.Record.Scope, AutomationID: "check", Kind: "occurrence", ID: "first", Occurrence: &AutomationOccurrence{DefinitionRevision: 1, TriggerIdentity: "tick-1", State: "pending"}}}
	if _, _, err := s.ApplyAutomationMutation(m); err != nil { t.Fatal(err) }
	duplicate := m
	duplicate.Record.ID = "second"
	if _, _, err := s.ApplyAutomationMutation(duplicate); !errors.Is(err, ErrAutomationConflict) { t.Fatalf("duplicate trigger: %v", err) }
	if _, found, err := s.GetAutomationRecord(def.Record.Scope, "check", "occurrence", "second", 0); err != nil || found { t.Fatalf("partial duplicate: %v %v", found, err) }
	m.ExpectedRevision = 1
	m.Record.Occurrence.State = "running"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); next := m; next.MutationID = id; _, _, err := s.ApplyAutomationMutation(next); results <- err }(id)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results { if err == nil { success++ } else if errors.Is(err, ErrAutomationConflict) { conflict++ } else { t.Fatal(err) } }
	if success != 1 || conflict != 1 { t.Fatalf("CAS: success=%d conflict=%d", success, conflict) }
	m.ExpectedRevision, m.MutationID, m.Record.Occurrence.State = 2, "complete", "completed"
	if _, _, err := s.ApplyAutomationMutation(m); err != nil { t.Fatal(err) }
	m.ExpectedRevision, m.MutationID, m.Record.Occurrence.State = 3, "restart", "running"
	if _, _, err := s.ApplyAutomationMutation(m); !errors.Is(err, ErrAutomationConflict) { t.Fatalf("terminal restart: %v", err) }
	if _, found, err := s.GetAutomationRecord(def.Record.Scope, "check", "occurrence", "first", 4); err != nil || found { t.Fatalf("partial revision: %v %v", found, err) }
}

// Purpose: locked context and append-only outcomes must survive rejected agent
// writes; scoped bounded pagination must neither duplicate rows nor accept a
// cursor from another namespace. These are observable storage postconditions.
func TestAutomationStoreContextAuditPagination(t *testing.T) {
	s := openTaskProgramTestStore(t)
	def := automationFixture()
	if _, _, err := s.ApplyAutomationMutation(def); err != nil { t.Fatal(err) }
	m := AutomationMutation{Actor: "user", MutationID: "context", Record: AutomationRecord{Scope: def.Record.Scope, AutomationID: "check", Kind: "context", ID: "check", Context: &AutomationContext{UserLocked: map[string]string{"target": "approved"}}}}
	if _, _, err := s.ApplyAutomationMutation(m); err != nil { t.Fatal(err) }
	m.Actor, m.ExpectedRevision, m.MutationID = "agent", 1, "bad"
	m.Record.Context.UserLocked = map[string]string{"target": "different"}
	if _, _, err := s.ApplyAutomationMutation(m); !errors.Is(err, ErrAutomationConflict) { t.Fatalf("locked: %v", err) }
	m.Record.Context.UserLocked = map[string]string{"target": "approved"}
	m.Record.Context.AgentOwned = map[string]string{"summary": "healthy"}
	m.MutationID = "good"
	if _, _, err := s.ApplyAutomationMutation(m); err != nil { t.Fatal(err) }
	audit := AutomationMutation{Actor: "system", MutationID: "audit", Record: AutomationRecord{Scope: def.Record.Scope, AutomationID: "check", Kind: "audit", ID: "outcome", Outcome: &AutomationOutcome{Kind: "summary", Summary: "Healthy"}}}
	if _, _, err := s.ApplyAutomationMutation(audit); err != nil { t.Fatal(err) }
	audit.ExpectedRevision, audit.MutationID = 1, "rewrite"
	if _, _, err := s.ApplyAutomationMutation(audit); !errors.Is(err, ErrAutomationConflict) { t.Fatalf("audit rewrite: %v", err) }
	q := AutomationSearch{Scope: def.Record.Scope, AutomationID: "check", Limit: 1}
	seen := map[string]bool{}
	for pages := 0; ; pages++ {
		if pages > 20 { t.Fatal("unbounded pagination") }
		rows, cursor, err := s.SearchAutomationRecords(q)
		if err != nil { t.Fatal(err) }
		for _, r := range rows { if seen[r.Kind] { t.Fatal("duplicate row") }; seen[r.Kind] = true }
		if cursor == "" { break }
		cross := q
		cross.Scope.AccountID, cross.Cursor = "other", cursor
		if _, _, err := s.SearchAutomationRecords(cross); !errors.Is(err, ErrAutomationInvalid) { t.Fatalf("cross-scope cursor: %v", err) }
		q.Cursor = cursor
	}
	if len(seen) != 3 { t.Fatalf("missing rows: %v", seen) }
	context, _, err := s.GetAutomationRecord(def.Record.Scope, "check", "context", "check", 0)
	if err != nil || context.Revision != 2 || context.Context.UserLocked["target"] != "approved" { t.Fatalf("context changed: %+v %v", context, err) }
}

// Purpose: a failed Pebble commit must not publish a head, revision or replay
// receipt. Read-only storage supplies a deterministic commit failure without
// substituting a mock transaction for the production batch boundary.
func TestAutomationStoreCommitFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil { t.Fatal(err) }
	if err := s.Close(); err != nil { t.Fatal(err) }
	s, err = OpenReadOnly(path)
	if err != nil { t.Fatal(err) }
	m := automationFixture()
	if _, fresh, err := s.ApplyAutomationMutation(m); err == nil || fresh { t.Fatalf("read-only commit: %v %v", fresh, err) }
	if _, found, err := s.GetAutomationRecord(m.Record.Scope, "check", "definition", "check", 0); err != nil || found { t.Fatalf("partial head: %v %v", found, err) }
	if err := s.Close(); err != nil { t.Fatal(err) }
	s, err = Open(path)
	if err != nil { t.Fatal(err) }
	defer s.Close()
	if r, fresh, err := s.ApplyAutomationMutation(m); err != nil || !fresh || r.Revision != 1 { t.Fatalf("receipt leaked: %+v %v %v", r, fresh, err) }
}
