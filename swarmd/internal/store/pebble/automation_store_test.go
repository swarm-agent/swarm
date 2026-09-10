package pebblestore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func automationFixture() AutomationMutation {
	return AutomationMutation{SubjectID: "writer", WrittenAt: 100000, Actor: "user", MutationID: "create", Record: AutomationRecord{
		Scope: AutomationScope{AccountID: "account-a", WorkspaceID: "workspace-a"}, AutomationID: "check", Kind: "definition", ID: "check",
		Definition: &AutomationDefinition{Name: "Check", Plans: []AutomationPlanBinding{{ID: "primary", Plan: AutomationPlanReference{SessionID: "session", PlanID: "plan", Revision: 1}}}, Schedule: AutomationSchedulePolicy{Kind: "manual", MissedPolicy: "skip", OverlapPolicy: "independent"}, Authorization: AutomationAuthorizationPolicy{Mode: "approval_required"}},
	}}
}

// Purpose: ApplyAutomationMutation/GetAutomationRecord own isolated, immutable
// definition revisions. This narrow real-store test prevents cross-scope reads,
// stale writes and restart replay from changing the head or historical result.
func TestAutomationStoreScopeRevisionRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m := automationFixture()
	if _, fresh, err := s.ApplyAutomationMutation(m); err != nil || !fresh {
		t.Fatalf("create: %v %v", fresh, err)
	}
	update := automationFixture()
	update.ExpectedRevision, update.MutationID = 1, "update"
	update.Record.Definition.Name = "Updated"
	if _, _, err := s.ApplyAutomationMutation(update); err != nil {
		t.Fatal(err)
	}
	stale := update
	stale.MutationID = "stale"
	if _, _, err := s.ApplyAutomationMutation(stale); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("stale: %v", err)
	}
	for _, scope := range []AutomationScope{{AccountID: "other", WorkspaceID: "workspace-a"}, {AccountID: "account-a", WorkspaceID: "other"}} {
		if _, found, err := s.GetAutomationRecord(scope, "check", "definition", "check", 0); err != nil || found {
			t.Fatalf("scope leak: %v %v", found, err)
		}
		q := AutomationSearch{Scope: scope, Limit: 10}
		rows, _, err := s.SearchAutomationRecords(q)
		if err != nil || len(rows) != 0 {
			t.Fatalf("search leak: %v %v", rows, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, fresh, err := s.ApplyAutomationMutation(m)
	if err != nil || fresh || got.Revision != 1 || got.SubjectID != "writer" || got.Actor != "user" || got.WrittenAt != 100000 {
		t.Fatalf("restart replay: %+v %v %v", got, fresh, err)
	}
	m.Record.Definition.Name = "Collision"
	if _, _, err := s.ApplyAutomationMutation(m); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("collision: %v", err)
	}
	head, _, err := s.GetAutomationRecord(m.Record.Scope, "check", "definition", "check", 0)
	if err != nil || head.Revision != 2 || head.Definition.Name != "Updated" {
		t.Fatalf("head changed: %+v %v", head, err)
	}
	old, _, err := s.GetAutomationRecord(m.Record.Scope, "check", "definition", "check", 1)
	if err != nil || old.Definition.Name != "Check" {
		t.Fatalf("history changed: %+v %v", old, err)
	}
}

// Purpose: atomic revision CAS, trigger uniqueness and terminal state guards at
// ApplyAutomationMutation must reject competing/invalid writes without partial
// head/revision/index state. Real Pebble is the narrowest transactional layer.
func TestAutomationStoreAtomicOccurrence(t *testing.T) {
	s := openTaskProgramTestStore(t)
	def := automationFixture()
	if _, _, err := s.ApplyAutomationMutation(def); err != nil {
		t.Fatal(err)
	}
	m := AutomationMutation{SubjectID: "writer", WrittenAt: 100000, Actor: "system", MutationID: "create", Record: AutomationRecord{Scope: def.Record.Scope, AutomationID: "check", Kind: "occurrence", ID: "first", Occurrence: &AutomationOccurrence{DefinitionRevision: 1, TriggerIdentity: "tick-1", State: "pending"}}}
	if _, _, err := s.ApplyAutomationMutation(m); err != nil {
		t.Fatal(err)
	}
	duplicate := m
	duplicate.Record.ID = "second"
	if _, _, err := s.ApplyAutomationMutation(duplicate); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("duplicate trigger: %v", err)
	}
	if _, found, err := s.GetAutomationRecord(def.Record.Scope, "check", "occurrence", "second", 0); err != nil || found {
		t.Fatalf("partial duplicate: %v %v", found, err)
	}
	m.ExpectedRevision = 1
	m.Record.Occurrence.State = "running"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			next := m
			next.MutationID = id
			_, _, err := s.ApplyAutomationMutation(next)
			results <- err
		}(id)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrAutomationConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("CAS: success=%d conflict=%d", success, conflict)
	}
	m.ExpectedRevision, m.MutationID, m.Record.Occurrence.State = 2, "complete", "completed"
	if _, _, err := s.ApplyAutomationMutation(m); err != nil {
		t.Fatal(err)
	}
	m.ExpectedRevision, m.MutationID, m.Record.Occurrence.State = 3, "restart", "running"
	if _, _, err := s.ApplyAutomationMutation(m); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("terminal restart: %v", err)
	}
	if _, found, err := s.GetAutomationRecord(def.Record.Scope, "check", "occurrence", "first", 4); err != nil || found {
		t.Fatalf("partial revision: %v %v", found, err)
	}
}

// Purpose: locked context and append-only outcomes must survive rejected agent
// writes; scoped bounded pagination must neither duplicate rows nor accept a
// cursor from another namespace. These are observable storage postconditions.
func TestAutomationStoreContextAuditPagination(t *testing.T) {
	s := openTaskProgramTestStore(t)
	def := automationFixture()
	if _, _, err := s.ApplyAutomationMutation(def); err != nil {
		t.Fatal(err)
	}
	m := AutomationMutation{SubjectID: "writer", WrittenAt: 100000, Actor: "user", MutationID: "context", Record: AutomationRecord{Scope: def.Record.Scope, AutomationID: "check", Kind: "context", ID: "check", Context: &AutomationContext{UserLocked: map[string]string{"target": "approved"}}}}
	if _, _, err := s.ApplyAutomationMutation(m); err != nil {
		t.Fatal(err)
	}
	m.Actor, m.ExpectedRevision, m.MutationID = "agent", 1, "bad"
	m.Record.Context.UserLocked = map[string]string{"target": "different"}
	if _, _, err := s.ApplyAutomationMutation(m); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("locked: %v", err)
	}
	m.Record.Context.UserLocked = map[string]string{"target": "approved"}
	m.Record.Context.AgentOwned = map[string]string{"summary": "healthy"}
	m.MutationID = "good"
	if _, _, err := s.ApplyAutomationMutation(m); err != nil {
		t.Fatal(err)
	}
	audit := AutomationMutation{SubjectID: "writer", WrittenAt: 100000, Actor: "system", MutationID: "audit", Record: AutomationRecord{Scope: def.Record.Scope, AutomationID: "check", Kind: "audit", ID: "outcome", Outcome: &AutomationOutcome{Kind: "summary", Summary: "Healthy"}}}
	if _, _, err := s.ApplyAutomationMutation(audit); err != nil {
		t.Fatal(err)
	}
	audit.ExpectedRevision, audit.MutationID = 1, "rewrite"
	if _, _, err := s.ApplyAutomationMutation(audit); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("audit rewrite: %v", err)
	}
	q := AutomationSearch{Scope: def.Record.Scope, AutomationID: "check", Limit: 1}
	seen := map[string]bool{}
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("unbounded pagination")
		}
		rows, cursor, err := s.SearchAutomationRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if seen[r.Kind] {
				t.Fatal("duplicate row")
			}
			seen[r.Kind] = true
		}
		if cursor == "" {
			break
		}
		cross := q
		cross.Scope.AccountID, cross.Cursor = "other", cursor
		if _, _, err := s.SearchAutomationRecords(cross); !errors.Is(err, ErrAutomationInvalid) {
			t.Fatalf("cross-scope cursor: %v", err)
		}
		q.Cursor = cursor
	}
	if len(seen) != 3 {
		t.Fatalf("missing rows: %v", seen)
	}
	context, _, err := s.GetAutomationRecord(def.Record.Scope, "check", "context", "check", 0)
	if err != nil || context.Revision != 2 || context.Context.UserLocked["target"] != "approved" {
		t.Fatalf("context changed: %+v %v", context, err)
	}
}

// Purpose: a failed Pebble commit must not publish a head, revision or replay
// receipt. Read-only storage supplies a deterministic commit failure without
// substituting a mock transaction for the production batch boundary.
func TestAutomationStoreCommitFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	m := automationFixture()
	if _, fresh, err := s.ApplyAutomationMutation(m); err == nil || fresh {
		t.Fatalf("read-only commit: %v %v", fresh, err)
	}
	if _, found, err := s.GetAutomationRecord(m.Record.Scope, "check", "definition", "check", 0); err != nil || found {
		t.Fatalf("partial head: %v %v", found, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if r, fresh, err := s.ApplyAutomationMutation(m); err != nil || !fresh || r.Revision != 1 {
		t.Fatalf("receipt leaked: %+v %v %v", r, fresh, err)
	}
}

// Purpose: real Pebble dispatch reservations and scheduler cursors must survive
// restart, reject competing occurrences/stale cursors, and preserve the winner.
// This is the narrowest layer proving the durable claim, not provider execution.
func TestAutomationDispatchClaimAndCursorRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m := automationFixture()
	m.Record.Definition.Schedule.OverlapPolicy = "serialize"
	if _, _, err := s.ApplyAutomationMutation(m); err != nil {
		t.Fatal(err)
	}
	scope := m.Record.Scope
	for _, id := range []string{"first", "second"} {
		_, _, err := s.ApplyAutomationMutation(AutomationMutation{Record: AutomationRecord{Scope: scope, AutomationID: "check", Kind: "occurrence", ID: id, Occurrence: &AutomationOccurrence{DefinitionRevision: 1, TriggerIdentity: id, State: "pending", ScheduledAt: 100000}}, MutationID: id, Actor: "system", SubjectID: "scheduler", WrittenAt: 100000})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ClaimAutomationDispatch(scope, "check", "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceAutomationCursor(scope, "check", 1, 0, 100000); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ClaimAutomationDispatch(scope, "check", "second"); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("competing claim: %v", err)
	}
	if err := s.ClaimAutomationDispatch(scope, "check", "first"); err != nil {
		t.Fatalf("owner recovery: %v", err)
	}
	if err := s.AdvanceAutomationCursor(scope, "check", 1, 0, 200000); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("stale cursor: %v", err)
	}
	if current, err := s.GetAutomationCursor(scope, "check", 1); err != nil || current != 100000 {
		t.Fatalf("cursor changed: %d %v", current, err)
	}
	if _, found, err := s.GetAutomationRecord(scope, "check", "occurrence", "second", 0); err != nil || !found {
		t.Fatal("competing admission lost")
	}
}

// Purpose: ClaimAutomationDispatch must serialize shared targets across definitions
// and workspaces without leaking reservations across accounts. Real Pebble is the
// narrowest layer proving restart durability and all-or-nothing multi-target claims.
func TestAutomationSharedTargetClaims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	create := func(id, workspace, account string, targets ...string) AutomationScope {
		m := automationFixture()
		m.Record.AutomationID, m.Record.ID = id, id
		m.Record.Scope = AutomationScope{AccountID: account, WorkspaceID: workspace}
		m.Record.Definition.Authorization.TargetIDs = targets
		if _, _, err := s.ApplyAutomationMutation(m); err != nil {
			t.Fatal(err)
		}
		_, _, err := s.ApplyAutomationMutation(AutomationMutation{Record: AutomationRecord{Scope: m.Record.Scope, AutomationID: id, Kind: "occurrence", ID: "run", Occurrence: &AutomationOccurrence{DefinitionRevision: 1, TriggerIdentity: "trigger", State: "pending", ScheduledAt: 100000}}, MutationID: "admit", Actor: "system", SubjectID: "scheduler", WrittenAt: 100000})
		if err != nil {
			t.Fatal(err)
		}
		return m.Record.Scope
	}
	a := create("a", "one", "account", "shared")
	b := create("b", "two", "account", "free", "shared")
	c := create("c", "two", "account", "free")
	other := create("a", "one", "other-account", "shared")
	if err := s.ClaimAutomationDispatch(a, "a", "run"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimAutomationDispatch(b, "b", "run"); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("shared target accepted: %v", err)
	}
	if err := s.ClaimAutomationDispatch(c, "c", "run"); err != nil {
		t.Fatalf("failed claim partially reserved free target: %v", err)
	}
	if err := s.ClaimAutomationDispatch(other, "a", "run"); err != nil {
		t.Fatalf("cross-account reservation: %v", err)
	}
	if err := s.ClaimAutomationDispatch(a, "a", "run"); err != nil {
		t.Fatalf("owner recovery: %v", err)
	}
	row, found, err := s.GetAutomationRecord(b, "b", "occurrence", "run", 0)
	if err != nil || !found || row.Revision != 1 || row.Occurrence.State != "pending" {
		t.Fatalf("loser mutated: %+v %v", row, err)
	}
}

// Purpose: the persistence CAS, not an HTTP read, must choose exactly one
// cancellation admission under concurrent requests. The real store proves the
// losing request writes neither a revision nor a usable cancellation receipt.
func TestAutomationCancellationConcurrentCAS(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m := automationFixture()
	if _, _, err := s.ApplyAutomationMutation(m); err != nil {
		t.Fatal(err)
	}
	r := AutomationRecord{Scope: m.Record.Scope, AutomationID: "check", Kind: "occurrence", ID: "occurrence", Occurrence: &AutomationOccurrence{DefinitionRevision: 1, TriggerIdentity: "trigger", ScheduledAt: 100000, State: "pending"}}
	if _, _, err := s.ApplyAutomationMutation(AutomationMutation{Record: r, MutationID: "admit", Actor: "user", SubjectID: "writer", WrittenAt: 100000}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, id := range []string{"cancel-a", "cancel-b"} {
		go func(id string) {
			<-start
			_, err := s.AdmitAutomationCancellation(r.Scope, "check", r.ID, 1, id, "writer", 100001)
			results <- result{id, err}
		}(id)
	}
	close(start)
	wins := 0
	loser := ""
	for i := 0; i < 2; i++ {
		var got result
		select {
		case got = <-results:
		case <-time.After(5 * time.Second):
			t.Fatal("cancellation admission deadlocked")
		}
		if got.err == nil {
			wins++
		} else if errors.Is(got.err, ErrAutomationConflict) {
			loser = got.id
		} else {
			t.Fatal(got.err)
		}
	}
	if wins != 1 {
		t.Fatalf("admissions: %d", wins)
	}
	head, found, err := s.GetAutomationRecord(r.Scope, "check", "occurrence", r.ID, 0)
	if err != nil || !found || head.Revision != 2 || head.Occurrence.State != "cancelling" {
		t.Fatalf("head: %+v %v", head, err)
	}
	if _, err := s.AdmitAutomationCancellation(r.Scope, "check", r.ID, 1, loser, "writer", 100002); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("loser receipt: %v", err)
	}
	if _, found, err := s.GetAutomationRecord(r.Scope, "check", "occurrence", r.ID, 3); err != nil || found {
		t.Fatalf("partial revision: %v %v", found, err)
	}
}
