package automation

import (
	"context"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: PrepareConversationDefinition must pin approved canonical bytes and
// preserve execution ownership without granting approval or writing state. This
// domain test isolates foreign identity, changed bytes and cross-session edits.
func TestConversationDefinition(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	plans := &conversationPlans{plan: store.SessionPlanSnapshot{ID: "plan", SessionID: "chat", AccountScopeID: "account", Version: 1, ApprovalState: "approved", Document: &store.SessionPlanDocument{}}}
	svc, err := New(db, plans, conversationAccess{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := BindRuntimeIdentity(context.Background(), identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}, "agent", "chat")
	p, _ := RuntimePrincipal(ctx)
	scope := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
	draft := store.AutomationDefinition{Name: "daily", Schedule: store.AutomationSchedulePolicy{Kind: "manual"}, Authorization: store.AutomationAuthorizationPolicy{Mode: "approval_required"}}
	got, err := svc.PrepareConversationDefinition(ctx, p, scope, nil, draft)
	if err != nil || got.SessionID != "chat" || len(got.Plans) != 1 || got.Plans[0].Plan.DocumentSHA256 == "" || got.Enabled || got.Authorization.Mode != "approval_required" {
		t.Fatalf("bad proposal: %+v %v", got, err)
	}
	existing := got
	existing.SessionID = "canonical"
	revised, err := svc.PrepareConversationDefinition(ctx, p, scope, &existing, draft)
	if err != nil || revised.SessionID != "canonical" {
		t.Fatalf("management moved session: %+v %v", revised, err)
	}
	for _, scenario := range []string{"foreign-account", "foreign-session", "unapproved", "changed-bytes", "enabled"} {
		t.Run(scenario, func(t *testing.T) {
			d := got
			principal := p
			switch scenario {
			case "foreign-account":
				principal.AccountID = "foreign"
			case "foreign-session":
				d.SessionID = "foreign"
			case "unapproved":
				plans.plan.ApprovalState = "pending"
				defer func() { plans.plan.ApprovalState = "approved" }()
			case "changed-bytes":
				d.Plans = append([]store.AutomationPlanBinding(nil), got.Plans...)
				d.Plans[0].Plan.DocumentSHA256 = "wrong"
			case "enabled":
				d.Enabled = true
			}
			if _, err := svc.PrepareConversationDefinition(ctx, principal, scope, nil, d); err == nil {
				t.Fatal("accepted invalid proposal")
			}
		})
	}
	rows, _, err := db.SearchAutomationRecords(store.AutomationSearch{Scope: scope, Kind: "definition", Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("proposal wrote state: %v %v", rows, err)
	}
}

type conversationPlans struct{ plan store.SessionPlanSnapshot }

func (p *conversationPlans) GetActivePlan(string) (store.SessionPlanSnapshot, bool, error) {
	return p.plan, true, nil
}
func (p *conversationPlans) GetPlanRevision(string, string, int) (store.SessionPlanSnapshot, bool, error) {
	return p.plan, true, nil
}

type conversationAccess struct{}

func (conversationAccess) Workspace(_ context.Context, p Principal, s store.AutomationScope, _ string) error {
	if p.AccountID != s.AccountID {
		return ErrDenied
	}
	return nil
}
func (conversationAccess) PlanSession(_ context.Context, _ Principal, _ store.AutomationScope, id string) error {
	if id != "chat" && id != "canonical" {
		return ErrDenied
	}
	return nil
}
func (conversationAccess) OccurrenceSession(context.Context, Principal, store.AutomationScope, string) error {
	return ErrDenied
}
func (conversationAccess) Execution(context.Context, Principal, store.AutomationScope, store.AutomationDefinition, string) error {
	return ErrDenied
}
