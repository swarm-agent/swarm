package automation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: approved local events require the exact current definition and live
// approving-user grant. Domain admission with real storage is the narrowest
// boundary proving foreign/stale/revoked intake creates no occurrence.
func TestApprovedEventAdmission(t *testing.T) {
	_, _, ownership, plans, user, scope, d := fixture(t)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	clock := func() time.Time { return time.UnixMilli(100000) }
	a, err := NewPolicyApproval(db, plans, ownership, RuntimeApprovalIdentity(), clock)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(db, plans, a, clock)
	if err != nil {
		t.Fatal(err)
	}
	bind := func(p Principal) context.Context {
		ctx, err := BindRuntimeIdentity(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: p.AccountID, UserID: p.SubjectID}, "user", "")
		if err != nil {
			t.Fatal(err)
		}
		return ctx
	}
	ctx := bind(user)
	data, _ := json.Marshal(plans.plan.Document)
	d.Plans[0].Plan.DocumentSHA256 = executionDocumentDigest(data)
	d.Schedule.Kind, d.Schedule.TriggerSource = "event", "ci"
	d.Schedule.MissedPolicy, d.Schedule.OverlapPolicy = "skip", "independent"
	d.Authorization.ExpiresAt = 200000
	_, _, err = db.ApplyAutomationMutation(store.AutomationMutation{Actor: "user", SubjectID: user.SubjectID, WrittenAt: 100000, MutationID: "create", Record: store.AutomationRecord{Scope: scope, AutomationID: "check", ID: "check", Kind: "definition", Definition: &d}})
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := ApprovalPolicyDigest(d)
	grant, err := a.ApproveUser(ctx, ApprovalRequest{Scope: scope, AutomationID: "check", DefinitionRevision: 1, PolicySHA256: digest})
	if err != nil {
		t.Fatal(err)
	}
	d.Enabled, d.Authorization.Mode, d.Authorization.ApprovalReference = true, "approved_policy", grant.ID
	_, _, err = db.ApplyAutomationMutation(store.AutomationMutation{Actor: "user", SubjectID: user.SubjectID, WrittenAt: 100000, MutationID: "enable", ExpectedRevision: 1, Record: store.AutomationRecord{Scope: scope, AutomationID: "check", ID: "check", Kind: "definition", Definition: &d}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewExecutionService(s, &executionRuntimeFake{}, NewApprovedEventAuthority(a))
	if err != nil {
		t.Fatal(err)
	}
	trigger := Trigger{Kind: "event", Source: "ci", Identity: "build", ScheduledAt: 100000}
	foreign := user
	foreign.SubjectID = "foreign"
	for _, tc := range []struct {
		p        Principal
		revision uint64
		source   string
	}{
		{foreign, 2, "ci"}, {user, 1, "ci"}, {user, 2, "other"},
	} {
		bad := trigger
		bad.Source = tc.source
		if _, err := e.Admit(bind(tc.p), tc.p, scope, "check", tc.revision, bad); err == nil {
			t.Fatal("unauthorized event accepted")
		}
	}
	rows, _, err := db.SearchAutomationRecords(store.AutomationSearch{Scope: scope, Kind: "occurrence", Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatal("denied admission wrote occurrence")
	}
	first, err := e.Admit(ctx, user, scope, "check", 2, trigger)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := e.Admit(ctx, user, scope, "check", 2, trigger)
	if err != nil || replay.ID != first.ID || replay.Revision != first.Revision {
		t.Fatal("event replay not idempotent", err)
	}
	if _, err := a.RevokeUser(ctx, scope, grant.ID, grant.Revision); err != nil {
		t.Fatal(err)
	}
	trigger.Identity = "after-revoke"
	if _, err := e.Admit(ctx, user, scope, "check", 2, trigger); err == nil {
		t.Fatal("revoked event accepted")
	}
	rows, _, err = db.SearchAutomationRecords(store.AutomationSearch{Scope: scope, Kind: "occurrence", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != first.ID {
		t.Fatal("revoked event mutated occurrences")
	}
}
