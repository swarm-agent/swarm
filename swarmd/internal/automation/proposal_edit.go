package automation

import (
	"context"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// EditParentDefinition is the only sidechat definition-edit capability. Transport
// must bind the actual agent session; parent IDs and user roles are never inputs.
// It cannot create, enable, approve, move sessions or replace executable pins.
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
	if !found || child.AccountScopeID != p.AccountID || child.Metadata["system_sidechat"] != true || child.Metadata["system_sidechat_kind"] != "plan" || parentID == "" {
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
	if !found || old.Revision != expected || old.Definition == nil || old.Definition.SessionID != parentID {
		return store.AutomationRecord{}, false, ErrDenied
	}
	// Only configuration changes are accepted; executable instructions remain
	// immutable plan pins until a separately reviewed plan revision is supplied.
	d.SessionID = parentID
	d.Plans = old.Definition.Plans
	d.Enabled = false
	d.Authorization.Mode = "approval_required"
	d.Authorization.ApprovalReference = ""
	d.Schedule, err = NormalizeSchedule(d.Schedule)
	if err != nil || d.Authorization.ExpiresAt <= s.now().UnixMilli() {
		return store.AutomationRecord{}, false, ErrInvalid
	}
	return s.repo.ApplyAutomationMutation(store.AutomationMutation{Actor: "agent", SubjectID: p.SubjectID, WrittenAt: s.now().UnixMilli(), MutationID: mutation, ExpectedRevision: expected, Record: store.AutomationRecord{Scope: scope, AutomationID: id, ID: id, Kind: "definition", Definition: &d}})
}
