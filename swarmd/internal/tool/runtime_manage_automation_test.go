package tool

import (
	"context"
	"testing"
	"encoding/json"
	"time"
	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the runtime decoder, rather than provider schema compliance, rejects
// forged authority and unbounded input before accessing services. This unit layer
// isolates dispatch ingress and does not claim execution approval integration.
func TestAutomationToolIngress(t *testing.T) {
	for _, args := range []map[string]any{
		{"action": "list", "role": "user"},
		{"action": "list", "account_id": "foreign"},
		{"action": "list", "limit": 51},
		{"action": "history", "before": -1},
		{"action": "update_context", "user_instructions": map[string]string{"grant": "all"}},
	} {
		if _, err := decodeAutomationToolRequest(args); err == nil { t.Fatal("accepted forged/unbounded request", args) }
	}
	if _, err := (&Runtime{}).executeManageAutomation(context.Background(), WorkspaceScope{}, map[string]any{"action": "run"}); err == nil { t.Fatal("missing adapter reported success") }
	d := manageAutomationDefinition()
	if d.Name != "manage_automation" || d.Parameters["additionalProperties"] != false { t.Fatal("schema lost strict envelope") }
	properties := d.Parameters["properties"].(map[string]any)
	for _, forbidden := range []string{"role", "principal", "account_id", "approval_reference", "user_instructions"} {
		if _, ok := properties[forbidden]; ok { t.Fatal("schema exposes authority", forbidden) }
	}
}

// Purpose: decoder and management consumer are the narrow layer that can prove
// agent proposals cannot mutate user definitions or smuggle grants across actions.
// Real domain reads with a no-write fake make rejection postconditions explicit.
func TestAutomationToolProposals(t *testing.T) {
	ctx, err := automation.BindRuntimeIdentity(context.Background(), identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}, "agent", "session")
	if err != nil { t.Fatal(err) }
	p, _ := automation.RuntimePrincipal(ctx)
	scope := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
	repo := &automationToolRepo{record: store.AutomationRecord{Scope: scope, AutomationID: "auto", ID: "auto", Kind: "definition", Revision: 2, Definition: &store.AutomationDefinition{Name: "original", Enabled: true}}}
	domain, err := automation.New(repo, automationToolPlans{}, automationToolAccess{}, time.Now)
	if err != nil { t.Fatal(err) }
	r := &Runtime{automations: domain}
	req := automationToolRequest{Action: "pause", ID: "auto", MutationID: "pause", ExpectedRevision: 2}
	out, err := r.automationManagement(ctx, p, scope, req)
	if err != nil { t.Fatal(err) }
	body := out["proposal"].(map[string]any)["body"].(map[string]any)
	if out["applied"] != false || body["action"] != "save" || body["expected_revision"] != uint64(2) || body["definition"].(store.AutomationDefinition).Enabled { t.Fatal("incorrect proposal", out) }
	if !repo.record.Definition.Enabled || repo.writes != 0 { t.Fatal("proposal mutated state") }
	req.ExpectedRevision = 1
	if _, err := r.automationManagement(ctx, p, scope, req); err == nil { t.Fatal("stale proposal accepted") }
	p.Role = "user"
	if _, err := r.automationManagement(ctx, p, scope, req); err == nil { t.Fatal("forged origin accepted") }
	if repo.writes != 0 { t.Fatal("rejection mutated state") }
}

type automationToolRepo struct { record store.AutomationRecord; writes int }
func (f *automationToolRepo) ApplyAutomationMutation(store.AutomationMutation) (store.AutomationRecord, bool, error) { f.writes++; return store.AutomationRecord{}, false, automation.ErrDenied }
func (f *automationToolRepo) GetAutomationRecord(_ store.AutomationScope, _, kind, _ string, _ uint64) (store.AutomationRecord, bool, error) { return f.record, kind == "definition", nil }
func (*automationToolRepo) SearchAutomationRecords(store.AutomationSearch) ([]store.AutomationRecord, string, error) { return nil, "", nil }
type automationToolPlans struct{}
func (automationToolPlans) GetPlanRevision(string, string, int) (store.SessionPlanSnapshot, bool, error) { return store.SessionPlanSnapshot{}, false, nil }
type automationToolAccess struct{}
func (automationToolAccess) Workspace(ctx context.Context, p automation.Principal, s store.AutomationScope, _ string) error { actual, err := automation.RuntimePrincipal(ctx); if err != nil || actual != p || p.AccountID != s.AccountID { return automation.ErrDenied }; return nil }
func (automationToolAccess) PlanSession(context.Context, automation.Principal, store.AutomationScope, string) error { return nil }
func (automationToolAccess) OccurrenceSession(context.Context, automation.Principal, store.AutomationScope, string) error { return nil }
func (automationToolAccess) Execution(context.Context, automation.Principal, store.AutomationScope, store.AutomationDefinition, string) error { return automation.ErrDenied }

// Purpose: strict recursive decoding prevents modified grants, user-lock writes,
// and definition payloads attached to unrelated actions before any service call.
func TestAutomationToolDefinitionIsolation(t *testing.T) {
	d := store.AutomationDefinition{Name: "draft", Plans: []store.AutomationPlanBinding{{ID: "first", Plan: store.AutomationPlanReference{SessionID: "session", PlanID: "plan", Revision: 1}}}, Schedule: store.AutomationSchedulePolicy{Kind: "manual", MissedPolicy: "skip", OverlapPolicy: "independent"}, Authorization: store.AutomationAuthorizationPolicy{Mode: "approval_required"}}
	for _, scenario := range []string{"valid", "grant", "enabled", "cross-action", "locked", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			data, _ := json.Marshal(d)
			var definition map[string]any
			if err := json.Unmarshal(data, &definition); err != nil { t.Fatal(err) }
			args := map[string]any{"action": "save", "definition": definition}
			switch scenario {
			case "grant": definition["authorization"].(map[string]any)["approval_reference"] = "forged"
			case "enabled": definition["enabled"] = true
			case "cross-action": args["action"] = "run"
			case "locked": args["user_instructions"] = map[string]string{"locked": "overwrite"}
			case "unknown": definition["principal"] = "user"
			}
			_, err := decodeAutomationToolRequest(args)
			if (err == nil) != (scenario == "valid") { t.Fatalf("unexpected decode result: %v", err) }
		})
	}
}

// Purpose: an approved pending occurrence reaches the real ExecutionService with
// agent identity; stale occurrences and changed policy must never call Ensure.
// A temporary store, fixed clock and fake canonical runtime isolate this boundary.
func TestAutomationToolDispatch(t *testing.T) {
	db, err := store.Open(t.TempDir()); if err != nil { t.Fatal(err) }; defer db.Close()
	verified := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
	userCtx, _ := automation.BindRuntimeIdentity(context.Background(), verified, "user", "")
	agentCtx, _ := automation.BindRuntimeIdentity(context.Background(), verified, "agent", "agent-session")
	user, _ := automation.RuntimePrincipal(userCtx); agent, _ := automation.RuntimePrincipal(agentCtx)
	scope := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
	clock := func() time.Time { return time.UnixMilli(100000) }
	policy, err := automation.NewPolicyApproval(db, automationToolApprovedPlans{}, automationToolOwnership{}, automation.RuntimeApprovalIdentity(), clock)
	if err != nil { t.Fatal(err) }
	domain, err := automation.New(db, automationToolApprovedPlans{}, policy, clock); if err != nil { t.Fatal(err) }
	d := store.AutomationDefinition{Name: "check", Plans: []store.AutomationPlanBinding{{ID: "primary", Plan: store.AutomationPlanReference{SessionID: "plan-session", PlanID: "plan", Revision: 1}}}, Schedule: store.AutomationSchedulePolicy{Kind: "manual"}, Authorization: store.AutomationAuthorizationPolicy{Mode: "approval_required", ExpiresAt: 200000}}
	record, _, err := domain.SaveDefinition(userCtx, user, scope, "auto", "create", 0, d); if err != nil { t.Fatal(err) }
	d = *record.Definition
	digest, _ := automation.ApprovalPolicyDigest(d)
	grant, err := policy.ApproveUser(userCtx, automation.ApprovalRequest{Scope: scope, AutomationID: "auto", DefinitionRevision: record.Revision, PolicySHA256: digest}); if err != nil { t.Fatal(err) }
	d.Enabled, d.Authorization.Mode, d.Authorization.ApprovalReference = true, "approved_policy", grant.ID
	record, _, err = domain.SaveDefinition(userCtx, user, scope, "auto", "enable", record.Revision, d); if err != nil { t.Fatal(err) }
	host := &automationToolHost{}
	execution, err := automation.NewExecutionService(domain, host, automation.RuntimeTriggers{}); if err != nil { t.Fatal(err) }
	occ, err := execution.Admit(userCtx, user, scope, "auto", record.Revision, automation.Trigger{Kind: "manual", Identity: "manual", ScheduledAt: 100000}); if err != nil { t.Fatal(err) }
	r := &Runtime{automations: domain}; r.ConfigureAutomationExecution(execution, policy)
	req := automationToolRequest{Action: "run", ID: "auto", MutationID: "dispatch", ExpectedRevision: record.Revision, OccurrenceID: occ.ID, OccurrenceRevision: occ.Revision + 1}
	if _, err := r.automationManagement(agentCtx, agent, scope, req); err == nil || host.calls != 0 { t.Fatal("stale occurrence dispatched") }
	req.OccurrenceRevision = occ.Revision
	out, err := r.automationManagement(agentCtx, agent, scope, req)
	if err != nil { t.Fatal(err) }
	if out["status"] != "dispatched" || host.calls != 1 || host.principal != agent || out["record"].(store.AutomationRecord).Occurrence.SessionID != "execution-session" { t.Fatal("dispatch lost identity or session", out) }
	// Editing a pinned tool list cannot reuse the old grant, even with a current CAS.
	d.Authorization.AllowedTools = []string{"different-tool"}
	changed, _, err := db.ApplyAutomationMutation(store.AutomationMutation{Actor: "user", SubjectID: "owner", WrittenAt: 100000, MutationID: "alter-policy", ExpectedRevision: record.Revision, Record: store.AutomationRecord{Scope: scope, AutomationID: "auto", ID: "auto", Kind: "definition", Definition: &d}}); if err != nil { t.Fatal(err) }
	req.ExpectedRevision = changed.Revision
	if _, err := r.automationManagement(agentCtx, agent, scope, req); err == nil || host.calls != 1 { t.Fatal("altered grant dispatched") }
}

type automationToolApprovedPlans struct{}
func (automationToolApprovedPlans) GetPlanRevision(string, string, int) (store.SessionPlanSnapshot, bool, error) { return store.SessionPlanSnapshot{ID: "plan", SessionID: "plan-session", AccountScopeID: "account", Version: 1, ApprovalState: "approved", Document: &store.SessionPlanDocument{}}, true, nil }
type automationToolOwnership struct{}
func (automationToolOwnership) Workspace(context.Context, automation.Principal, store.AutomationScope, string) error { return nil }
func (automationToolOwnership) PlanSession(context.Context, automation.Principal, store.AutomationScope, string) error { return nil }
func (automationToolOwnership) OccurrenceSession(context.Context, automation.Principal, store.AutomationScope, string) error { return nil }
type automationToolHost struct { calls int; principal automation.Principal }
func (h *automationToolHost) Ensure(_ context.Context, p automation.Principal, _, _ store.AutomationRecord) (string, error) { h.calls++; h.principal = p; return "execution-session", nil }
