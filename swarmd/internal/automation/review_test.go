package automation

import (
	"context"
	"errors"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: SaveDefinition must clear execution authorization when policy bytes
// change, without changing historical pins; ReviewDefinition must disclose the
// paused review identity without leaking it across accounts. This domain/store
// test is the narrowest layer exercising real revision CAS and its postconditions.
func TestDefinitionEditRequiresReview(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	plans := &conversationPlans{plan: store.SessionPlanSnapshot{ID: "plan", SessionID: "chat", AccountScopeID: "account", Version: 1, ApprovalState: "approved", Document: &store.SessionPlanDocument{}}}
	svc, err := New(db, plans, conversationAccess{}, func() time.Time { return time.UnixMilli(1000) })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p := Principal{AccountID: "account", SubjectID: "owner", Role: "user"}
	scope := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
	d := store.AutomationDefinition{Name: "review", SessionID: "chat", Plans: []store.AutomationPlanBinding{{ID: "primary", Plan: store.AutomationPlanReference{SessionID: "chat", PlanID: "plan", Revision: 1}}}, Schedule: store.AutomationSchedulePolicy{Kind: "manual"}, Authorization: store.AutomationAuthorizationPolicy{Mode: "approval_required", ExpiresAt: 100000}}
	first, _, err := svc.SaveDefinition(ctx, p, scope, "automation", "create", 0, d)
	if err != nil {
		t.Fatal(err)
	}
	d = *first.Definition
	d.Schedule = store.AutomationSchedulePolicy{Kind: "interval", IntervalSeconds: 3600}
	d.Enabled = true
	d.Authorization.Mode = "approved_policy"
	d.Authorization.ApprovalReference = "old-grant"
	edited, _, err := svc.SaveDefinition(ctx, p, scope, "automation", "edit", first.Revision, d)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Definition.Enabled || edited.Definition.Authorization.Mode != "approval_required" || edited.Definition.Authorization.ApprovalReference != "" {
		t.Fatalf("edit retained execution authorization: %+v", edited)
	}
	if first.Definition.Schedule.Kind != "manual" || first.Definition.Enabled {
		t.Fatal("historical definition mutated")
	}
	if _, _, err := svc.SaveDefinition(ctx, p, scope, "automation", "stale", first.Revision, d); !errors.Is(err, store.ErrAutomationConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	review, err := svc.ReviewDefinition(ctx, p, scope, "automation")
	if err != nil || review.State != "pending_approval" || review.DefinitionRevision != edited.Revision || review.PolicySHA256 == "" || review.Schedule.IntervalSeconds != 3600 {
		t.Fatalf("review: %+v %v", review, err)
	}
	foreign := p
	foreign.AccountID = "foreign"
	if _, err := svc.ReviewDefinition(ctx, foreign, scope, "automation"); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-account review: %v", err)
	}
	head, _, err := db.GetAutomationRecord(scope, "automation", "definition", "automation", 0)
	if err != nil || head.Revision != edited.Revision || head.Definition.Enabled {
		t.Fatalf("rejection changed head: %+v %v", head, err)
	}
	if _, err := svc.ReviewDefinition(ctx, p, scope, "ordinary-plan"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ordinary plan became automation: %v", err)
	}
}
