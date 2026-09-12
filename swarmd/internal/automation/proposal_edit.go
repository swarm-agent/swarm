package automation

import (
	"context"
	"encoding/json"
	"strconv"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// EditParentDefinition is the only sidechat definition-edit capability. Transport
// must bind the actual agent session; parent IDs and user roles are never inputs.
// It returns a non-applied definition proposal at the current revision. Only an
// explicit user SaveDefinition CAS may persist it; approval remains separate.
func (s *Service) EditParentDefinition(ctx context.Context, scope store.AutomationScope, id, mutation string, expected uint64, d store.AutomationDefinition) (store.AutomationRecord, bool, error) {
	p, err := RuntimePrincipal(ctx)
	if err != nil || p.Role != "agent" || p.AccountID != scope.AccountID || expected == 0 {
		return store.AutomationRecord{}, false, ErrDenied
	}
	sessions, ok := s.plans.(interface {
		GetSession(string) (store.SessionSnapshot, bool, error)
	})
	if !ok {
		return store.AutomationRecord{}, false, ErrDenied
	}
	child, found, err := sessions.GetSession(p.SubjectID)
	if err != nil {
		return store.AutomationRecord{}, false, err
	}
	parentID, _ := child.Metadata["parent_session_id"].(string)
	if !found || child.Metadata["lineage_kind"] != "system_sidechat" || child.Metadata["plan_context_source"] != "automation_definition" || child.Metadata["automation_review_id"] != id || child.Metadata["automation_review_revision"] != strconv.FormatUint(expected, 10) || child.Metadata["automation_review_workspace_id"] != scope.WorkspaceID || child.AccountScopeID != p.AccountID || child.Metadata["system_sidechat"] != true || child.Metadata["system_sidechat_kind"] != "plan" || parentID == "" {
		return store.AutomationRecord{}, false, ErrDenied
	}
	parent, found, err := sessions.GetSession(parentID)
	if err != nil {
		return store.AutomationRecord{}, false, err
	}
	if !found || parent.AccountScopeID != p.AccountID || parent.UserID != child.UserID {
		return store.AutomationRecord{}, false, ErrDenied
	}
	owned := false
	for _, grant := range parent.WorkspaceGrants {
		owned = owned || (grant.WorkspaceID == scope.WorkspaceID && grant.Kind == store.WorkspaceGrantPrimary)
	}
	if !owned {
		return store.AutomationRecord{}, false, ErrDenied
	}
	old, found, err := s.repo.GetAutomationRecord(scope, id, "definition", id, 0)
	if err != nil {
		return store.AutomationRecord{}, false, err
	}
	if !found || old.Scope != scope || old.AutomationID != id || old.Revision != expected || old.Definition == nil || old.Definition.SessionID != parentID {
		return store.AutomationRecord{}, false, ErrDenied
	}
	if d.SessionID != "" && d.SessionID != parentID {
		return store.AutomationRecord{}, false, ErrDenied
	}
	d.SessionID = parentID
	if d.Plans == nil {
		d.Plans = old.Definition.Plans
	}
	if err := store.ValidateAutomationBindings(d.Plans); err != nil {
		return store.AutomationRecord{}, false, err
	}
	d.Plans = append([]store.AutomationPlanBinding(nil), d.Plans...)
	for _, binding := range d.Plans {
		ref := binding.Plan
		if ref.SessionID != parentID || ref.DocumentSHA256 == "" || ref.Revision == 0 || ref.Revision > uint64(^uint(0)>>1) {
			return store.AutomationRecord{}, false, ErrDenied
		}
		// The sidechat capability does not grant general parent access. Resolve
		// just this exact canonical, separately approved instruction revision.
		plan, found, err := s.plans.GetPlanRevision(ref.SessionID, ref.PlanID, int(ref.Revision))
		if err != nil {
			return store.AutomationRecord{}, false, err
		}
		if !found || plan.AccountScopeID != scope.AccountID || plan.SessionID != parentID || plan.ID != ref.PlanID || ref.Revision == 0 || uint64(plan.Version) != ref.Revision || plan.ApprovalState != "approved" || plan.Document == nil {
			return store.AutomationRecord{}, false, ErrDenied
		}
		data, err := json.Marshal(plan.Document)
		if err != nil || executionDocumentDigest(data) != ref.DocumentSHA256 {
			return store.AutomationRecord{}, false, ErrDenied
		}
	}
	d.Enabled = false
	d.Authorization.Mode = "approval_required"
	d.Authorization.ApprovalReference = ""
	d.Schedule, err = NormalizeSchedule(d.Schedule)
	if err != nil || d.Authorization.ExpiresAt <= s.now().UnixMilli() {
		return store.AutomationRecord{}, false, ErrInvalid
	}
	return store.AutomationRecord{Scope: scope, AutomationID: id, ID: id, Kind: "definition", Revision: expected, Definition: &d}, false, nil
}
