package automation

import (
	"context"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: EventAuthority and ExecutionService.Admit must bind registered source
// authority to the authenticated principal and exact target revision. Domain
// tests are the narrowest layer asserting denied intake causes no durable writes.
func TestRegisteredEventAdmission(t *testing.T) {
	s, _, _, _, p, scope, d := fixture(t)
	repo, err := store.Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer repo.Close()
	s.repo = repo
	d.Enabled = true
	d.Schedule.Kind, d.Schedule.TriggerSource = "event", "ci"
	d.Authorization = store.AutomationAuthorizationPolicy{Mode: "approved_policy", ApprovalReference: "approval", ExpiresAt: 200000}
	if _, _, err := s.SaveDefinition(context.Background(), p, scope, "automation", "save", 0, d); err != nil { t.Fatal(err) }
	a, err := NewEventAuthority([]EventRegistration{{p, scope, "automation", 1, "ci"}})
	if err != nil { t.Fatal(err) }
	ctx, err := BindRuntimeIdentity(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: p.AccountID, UserID: p.SubjectID}, "user", "")
	if err != nil { t.Fatal(err) }
	e, err := NewExecutionService(s, &executionRuntimeFake{}, a)
	if err != nil { t.Fatal(err) }
	trigger := Trigger{Kind: "event", Source: "ci", Identity: "build-1", ScheduledAt: 100000}
	for _, target := range []struct { scope store.AutomationScope; id string; revision uint64 }{
		{scope, "other", 1}, {scope, "automation", 2}, {store.AutomationScope{AccountID: "other", WorkspaceID: scope.WorkspaceID}, "automation", 1},
	} {
		if _, err := e.Admit(ctx, p, target.scope, target.id, target.revision, trigger); err == nil { t.Fatal("unregistered target accepted") }
	}
	if _, err := e.Admit(context.Background(), p, scope, "automation", 1, trigger); err == nil { t.Fatal("unbound identity accepted") }
	rows, _, err := repo.SearchAutomationRecords(store.AutomationSearch{Scope: scope, Kind: "occurrence", Limit: 10})
	if err != nil || len(rows) != 0 { t.Fatal("denied intake mutated storage") }
	first, err := e.Admit(ctx, p, scope, "automation", 1, trigger)
	if err != nil { t.Fatal(err) }
	replay, err := e.Admit(ctx, p, scope, "automation", 1, trigger)
	if err != nil || replay.ID != first.ID || replay.Revision != first.Revision { t.Fatalf("replay: %v", err) }
}
