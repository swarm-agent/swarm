package automation

import (
	"context"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// DefinitionReview is evidence for explicit user review, not an execution grant.
// Consumers must submit the exact revision/digest to the existing approve route,
// then explicitly apply its enable proposal. Ordinary plans have no such identity.
type DefinitionReview struct {
	AutomationID       string                              `json:"automation_id"`
	WorkspaceID        string                              `json:"workspace_id"`
	SessionID          string                              `json:"session_id,omitempty"`
	DefinitionRevision uint64                              `json:"definition_revision"`
	PolicySHA256       string                              `json:"policy_sha256"`
	State              string                              `json:"state"`
	Schedule           store.AutomationSchedulePolicy      `json:"schedule"`
	Authorization      store.AutomationAuthorizationPolicy `json:"authorization"`
	Plans              []store.AutomationPlanBinding       `json:"plans"`
	PauseSemantics     string                              `json:"pause_semantics"`
}

func (s *Service) ReviewDefinition(ctx context.Context, p Principal, scope store.AutomationScope, id string) (DefinitionReview, error) {
	var out DefinitionReview
	if err := s.authorize(ctx, p, scope, "read"); err != nil {
		return out, err
	}
	r, found, err := s.repo.GetAutomationRecord(scope, id, "definition", id, 0)
	if err != nil {
		return out, err
	}
	if !found || r.Definition == nil {
		return out, ErrNotFound
	}
	d := r.Definition
	digest, err := ApprovalPolicyDigest(*d)
	if err != nil {
		return out, err
	}
	state := "pending_approval"
	if d.Authorization.Mode == "approved_policy" {
		state = "paused"
		if d.Enabled {
			state = "enabled"
		}
	}
	if d.Authorization.ExpiresAt <= s.now().UnixMilli() {
		state = "expired"
	}
	return DefinitionReview{
		AutomationID: id, WorkspaceID: scope.WorkspaceID, SessionID: d.SessionID,
		DefinitionRevision: r.Revision, PolicySHA256: digest, State: state,
		Schedule: d.Schedule, Authorization: d.Authorization, Plans: d.Plans,
		PauseSemantics: "Pausing stops new admission; it does not cancel already-admitted work or change its pinned definition revision.",
	}, nil
}
