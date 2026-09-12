package tool

import (
	"context"
	"encoding/json"
	"reflect"

	"swarm/packages/swarmd/internal/automation"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func sessionPlanAutomationToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"description": "Exact saved paused automation revision, not inferred recurring prose. Preserve complete executable pins, schedule, timezone and policy. Approval remains an explicit user operation.",
		"properties": map[string]any{
			"scope": map[string]any{"type": "object", "properties": map[string]any{"account_id": map[string]any{"type": "string"}, "workspace_id": map[string]any{"type": "string"}}, "required": []string{"account_id", "workspace_id"}, "additionalProperties": false},
			"automation_id": map[string]any{"type": "string"},
			"definition_revision": map[string]any{"type": "integer", "minimum": 1},
			"existing": map[string]any{"type": "boolean"},
			"definition": automationDefinitionSchema(),
		},
		"required": []string{"scope", "automation_id", "definition_revision", "existing", "definition"},
		"additionalProperties": false,
	}
}

// EditParentAutomation binds the real sidechat identity, never a caller-selected
// parent. The domain verifies account, workspace, lineage, ownership and CAS.
// This is deliberately not the generic management or approval capability.
func (r *Runtime) EditParentAutomation(ctx context.Context, scope WorkspaceScope, intent store.SessionPlanAutomationIntent, mutation string, expected uint64) (string, error) {
	if r == nil || r.automations == nil || expected == 0 || expected != intent.DefinitionRevision || mutation == "" || intent.Definition.Enabled || intent.Definition.Authorization.Mode != "approval_required" || intent.Definition.Authorization.ApprovalReference != "" {
		return "", automation.ErrDenied
	}
	ctx, err := automation.BindRuntimeIdentity(ctx, scope.Principal, "agent", scope.SessionID)
	if err != nil {
		return "", err
	}
	record, fresh, err := r.automations.EditParentDefinition(ctx, intent.Scope, intent.AutomationID, mutation, expected, intent.Definition)
	if err != nil {
		return "", err
	}
	intent.DefinitionRevision = record.Revision
	intent.Definition = *record.Definition
	out, err := json.Marshal(map[string]any{"applied": true, "fresh": fresh, "automation": intent, "activation_status": "paused; fresh user approval and enabling required"})
	return string(out), err
}

// ReviewPlanAutomation routes typed recurring approval to the canonical explicit
// user API proposal, never to one-shot checkpoint execution or an agent grant.
func (r *Runtime) ReviewPlanAutomation(ctx context.Context, scope WorkspaceScope, intent store.SessionPlanAutomationIntent) (string, error) {
	if r == nil || r.automations == nil || intent.DefinitionRevision == 0 {
		return "", automation.ErrDenied
	}
	ctx, err := automation.BindRuntimeIdentity(ctx, scope.Principal, "agent", scope.SessionID)
	if err != nil { return "", err }
	p, err := automation.RuntimePrincipal(ctx)
	if err != nil { return "", err }
	review, err := r.automations.ReviewDefinition(ctx, p, intent.Scope, intent.AutomationID)
	if err != nil { return "", err }
	rows, _, err := r.automations.History(ctx, p, intent.Scope, intent.AutomationID, "definition", intent.AutomationID, 0, 1)
	if err != nil { return "", err }
	if len(rows) != 1 || rows[0].Revision != intent.DefinitionRevision || rows[0].Definition == nil || rows[0].Definition.SessionID != scope.SessionID || !reflect.DeepEqual(*rows[0].Definition, intent.Definition) {
		return "", automation.ErrDenied
	}
	out, err := r.automationManagement(ctx, p, intent.Scope, automationToolRequest{Action: "approve", ID: intent.AutomationID, ExpectedRevision: intent.DefinitionRevision})
	if err != nil { return "", err }
	out["automation"] = intent
	out["review_kind"] = "automation"
	out["review"] = review
	raw, err := json.Marshal(out)
	return string(raw), err
}
