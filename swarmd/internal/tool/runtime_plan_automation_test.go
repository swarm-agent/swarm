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

type parentEditPlans struct { automationToolPlans; sessions map[string]store.SessionSnapshot }
func (p parentEditPlans) GetSession(id string) (store.SessionSnapshot, bool, error) { s, ok := p.sessions[id]; return s, ok, nil }
type parentEditRepo struct { automationToolRepo }
func (r *parentEditRepo) ApplyAutomationMutation(m store.AutomationMutation) (store.AutomationRecord, bool, error) {
	r.writes++
	r.record = m.Record
	r.record.Revision = m.ExpectedRevision + 1
	return r.record, true, nil
}

// Purpose: the restricted runtime adapter must bind actual child identity and
// delegate exact-parent/CAS checks to EditParentDefinition, never expose approval.
// A real domain with isolated session/repository fakes proves rejected requests
// do not write and accepted edits keep identity/pins while pausing reapproval.
func TestRestrictedParentAutomationEdit(t *testing.T) {
	for _, scenario := range []string{"valid", "foreign", "stale", "ordinary", "self-approve"} {
		t.Run(scenario, func(t *testing.T) {
			canonical := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
			definition := store.AutomationDefinition{SessionID: "parent", Name: "daily", Plans: []store.AutomationPlanBinding{{ID: "work"}}, Schedule: store.AutomationSchedulePolicy{Kind: "cron", Expression: "0 9 * * *", Timezone: "UTC"}, Authorization: store.AutomationAuthorizationPolicy{Mode: "approval_required", ExpiresAt: 200000}}
			repo := &parentEditRepo{automationToolRepo{record: store.AutomationRecord{Scope: canonical, AutomationID: "auto", ID: "auto", Kind: "definition", Revision: 2, Definition: &definition}}}
			child := store.SessionSnapshot{AccountScopeID: "account", UserID: "owner", Metadata: map[string]any{"system_sidechat": true, "system_sidechat_kind": "plan", "parent_session_id": "parent"}}
			if scenario == "ordinary" { child.Metadata = nil }
			plans := parentEditPlans{sessions: map[string]store.SessionSnapshot{"child": child, "parent": {AccountScopeID: "account", UserID: "owner", WorkspaceGrants: []store.WorkspaceGrant{{WorkspaceID: "workspace", Kind: store.WorkspaceGrantPrimary}}}}}
			domain, err := automation.New(repo, plans, automationToolAccess{}, func() time.Time { return time.UnixMilli(100000) })
			if err != nil { t.Fatal(err) }
			runtime := &Runtime{automations: domain}
			intent := store.SessionPlanAutomationIntent{Scope: canonical, AutomationID: "auto", DefinitionRevision: 2, Existing: true, Definition: definition}
			intent.Definition.Schedule.Expression = "0 18 * * *"
			if scenario == "foreign" { intent.Scope.AccountID = "foreign" }
			if scenario == "stale" { intent.DefinitionRevision = 1 }
			if scenario == "self-approve" { intent.Definition.Authorization.Mode = "approved_policy" }
			scope := WorkspaceScope{SessionID: "child", Principal: identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}}
			out, err := runtime.EditParentAutomation(context.Background(), scope, intent, "edit", intent.DefinitionRevision)
			if scenario != "valid" {
				if err == nil || repo.writes != 0 || definition.Schedule.Expression != "0 9 * * *" { t.Fatal("rejection wrote state", err) }
				return
			}
			if err != nil { t.Fatal(err) }
			var result map[string]any
			if err := json.Unmarshal([]byte(out), &result); err != nil { t.Fatal(err) }
			if repo.writes != 1 || repo.record.AutomationID != "auto" || repo.record.Revision != 3 || repo.record.Definition.Enabled || repo.record.Definition.SessionID != "parent" || len(repo.record.Definition.Plans) != 1 || repo.record.Definition.Schedule.Expression != "0 18 * * *" || result["activation_status"] == "" { t.Fatal("lost identity, timing or paused status", out) }
		})
	}
}

// Purpose: typed approval routing must return an explicit-user canonical proposal
// for the exact saved intent, with no writes or grants. Foreign session, stale
// revision and altered timing must fail rather than approving another definition.
func TestTypedAutomationApprovalRouting(t *testing.T) {
	canonical := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
	d := store.AutomationDefinition{SessionID: "parent", Name: "daily", Schedule: store.AutomationSchedulePolicy{Kind: "cron", Expression: "0 18 * * *", Timezone: "UTC"}, Authorization: store.AutomationAuthorizationPolicy{Mode: "approval_required", ExpiresAt: 200000}}
	repo := &automationToolRepo{record: store.AutomationRecord{Scope: canonical, AutomationID: "auto", ID: "auto", Kind: "definition", Revision: 2, Definition: &d}}
	domain, err := automation.New(repo, automationToolPlans{}, automationToolAccess{}, func() time.Time { return time.UnixMilli(100000) })
	if err != nil { t.Fatal(err) }
	runtime := &Runtime{automations: domain}
	for _, scenario := range []string{"valid", "foreign", "stale", "timing"} {
		intent := store.SessionPlanAutomationIntent{Scope: canonical, AutomationID: "auto", DefinitionRevision: 2, Definition: d}
		scope := WorkspaceScope{SessionID: "parent", Principal: identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}}
		if scenario == "foreign" { scope.SessionID = "other" }
		if scenario == "stale" { intent.DefinitionRevision = 1 }
		if scenario == "timing" { intent.Definition.Schedule.Expression = "0 9 * * *" }
		out, err := runtime.ReviewPlanAutomation(context.Background(), scope, intent)
		if scenario == "valid" {
			if err != nil { t.Fatal(err) }
			var result map[string]any
			if err := json.Unmarshal([]byte(out), &result); err != nil { t.Fatal(err) }
			proposal := result["proposal"].(map[string]any)
			if result["applied"] != false || proposal["path"] != "/v3/automations/approve" || result["review_kind"] != "automation" { t.Fatal(out) }
		} else if err == nil { t.Fatal("accepted mismatched intent", scenario) }
		if repo.writes != 0 || d.Enabled || d.Authorization.ApprovalReference != "" { t.Fatal("routing minted authority") }
	}
}
