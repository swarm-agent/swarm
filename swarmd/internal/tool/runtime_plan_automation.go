package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/url"
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
	record, _, err := r.automations.EditParentDefinition(ctx, intent.Scope, intent.AutomationID, mutation, expected, intent.Definition)
	if err != nil {
		return "", err
	}
	intent.DefinitionRevision = record.Revision
	intent.Definition = *record.Definition
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{"applied": false, "automation": intent, "status": "requires_user_approval", "activation_status": "not saved or enabled; user save, reapproval and enabling required", "proposal": map[string]any{"method": "POST", "path": "/v3/automations", "body": map[string]any{"action": "save", "workspace_id": record.Scope.WorkspaceID, "id": record.AutomationID, "expected_revision": record.Revision, "mutation_id": "automation-edit-" + hex.EncodeToString(nonce[:]), "definition": record.Definition}}})
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

// ProposeParentAutomationInstructions verifies the same exact sidechat capability
// as a configuration edit, then returns data for explicit authenticated user save.
// It does not approve a plan, change live pins, or expose general parent reads.
func (r *Runtime) ProposeParentAutomationInstructions(ctx context.Context, scope WorkspaceScope, intent store.SessionPlanAutomationIntent, mutation string, expected uint64, document *store.SessionPlanDocument) (string, error) {
	if document == nil || document.Automation != nil || len(document.Checkpoints) == 0 || intent.Definition.SessionID == "" {
		return "", automation.ErrInvalid
	}
	if _, err := r.EditParentAutomation(ctx, scope, intent, mutation, expected); err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{
		"applied": false, "status": "requires_user_approval",
		"proposal": map[string]any{"method": "POST", "path": "/v3/sessions/" + url.PathEscape(intent.Definition.SessionID) + "/plans", "body": map[string]any{"document": document, "title": document.Title, "activate": false, "status": "draft", "approval_state": "pending"}},
		"instruction": "User must save and separately approve this new executable plan through the canonical plan interface. Then refresh the accepted automation review and propose its exact returned parent plan ID, revision and document digest as replacement pins. No instructions, automation configuration, approval or enabling have been applied.",
	})
	return string(out), err
}
