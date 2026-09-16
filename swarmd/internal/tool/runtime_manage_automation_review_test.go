package tool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: automationManagement must propose a same-identity timing edit without
// mutating a saved automation or reusing its grant. Real preparation plus a
// no-write repository isolates this consumer's CAS, account and review contract.
func TestAutomationTimingEditReview(t *testing.T) {
	ctx, _ := automation.BindRuntimeIdentity(context.Background(), identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}, "agent", "manager")
	p, _ := automation.RuntimePrincipal(ctx)
	scope := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
	original := store.AutomationDefinition{SessionID: "canonical", Name: "daily", Enabled: true,
		Plans:         []store.AutomationPlanBinding{{ID: "primary", Plan: store.AutomationPlanReference{SessionID: "plan-session", PlanID: "plan", Revision: 1}}},
		Schedule:      store.AutomationSchedulePolicy{Kind: "cron", Expression: "0 9 * * *", Timezone: "UTC", MissedPolicy: "skip", OverlapPolicy: "serialize"},
		Authorization: store.AutomationAuthorizationPolicy{Mode: "approved_policy", ApprovalReference: "old-grant", ExpiresAt: 200000}}
	repo := &automationToolRepo{record: store.AutomationRecord{Scope: scope, AutomationID: "auto", ID: "auto", Kind: "definition", Revision: 2, Definition: &original}}
	domain, err := automation.New(repo, automationToolApprovedPlans{}, automationToolAccess{}, func() time.Time { return time.UnixMilli(100000) })
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{automations: domain}
	proposed := original
	proposed.SessionID, proposed.Plans, proposed.Enabled = "", nil, false
	proposed.Authorization.Mode, proposed.Authorization.ApprovalReference = "approval_required", ""
	proposed.Schedule.Expression = "0 18 * * *"
	req := automationToolRequest{Action: "save", ID: "auto", MutationID: "move-time", ExpectedRevision: 2, Definition: &proposed}
	out, err := runtime.automationManagement(ctx, p, scope, req)
	if err != nil {
		t.Fatal(err)
	}
	body := out["proposal"].(map[string]any)["body"].(map[string]any)
	d := body["definition"].(store.AutomationDefinition)
	if body["id"] != "auto" || d.SessionID != "canonical" || len(d.Plans) != 1 || d.Schedule.Expression != "0 18 * * *" || d.Schedule.OverlapPolicy != "serialize" || d.Enabled || d.Authorization.ApprovalReference != "" {
		t.Fatalf("edit changed identity/policy or lost timing: %+v", body)
	}
	if out["applied"] != false || out["review_kind"] != "automation" || out["previous_schedule"].(store.AutomationSchedulePolicy).Expression != "0 9 * * *" || out["activation_status"] == "" {
		t.Fatalf("missing truthful review: %+v", out)
	}
	req.ExpectedRevision = 1
	if _, err := runtime.automationManagement(ctx, p, scope, req); err == nil {
		t.Fatal("stale edit accepted")
	}
	req.ExpectedRevision = 2
	foreign := scope
	foreign.AccountID = "foreign"
	if _, err := runtime.automationManagement(ctx, p, foreign, req); err == nil {
		t.Fatal("cross-account edit accepted")
	}
	if repo.writes != 0 || original.Schedule.Expression != "0 9 * * *" || !original.Enabled {
		t.Fatal("proposal mutated saved state")
	}
}

// Purpose: decoder + canonical NormalizeSchedule must preserve exact wall time
// and fail closed on ambiguous timezone or unsupported syntax before any writes.
func TestAutomationTimingIngressFidelity(t *testing.T) {
	for _, tc := range []struct {
		expression, timezone string
		valid                bool
	}{
		{"0 18 * * *", "UTC", true}, {"0 18 * * *", "", false},
		{"0 9,18 * * *", "UTC", false}, {"@daily", "UTC", false},
	} {
		d := store.AutomationDefinition{Name: "daily", Schedule: store.AutomationSchedulePolicy{Kind: "cron", Expression: tc.expression, Timezone: tc.timezone}, Authorization: store.AutomationAuthorizationPolicy{Mode: "approval_required"}}
		data, _ := json.Marshal(d)
		var definition map[string]any
		if err := json.Unmarshal(data, &definition); err != nil {
			t.Fatal(err)
		}
		req, err := decodeAutomationToolRequest(map[string]any{"action": "save", "definition": definition})
		if (err == nil) != tc.valid {
			t.Fatalf("%q/%q: %v", tc.expression, tc.timezone, err)
		}
		if err == nil && req.Definition.Schedule.Expression != tc.expression {
			t.Fatal("timing approximated")
		}
	}
}
