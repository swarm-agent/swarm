package permission

import (
	"encoding/json"
	"reflect"

	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// CoordinateAutomationAcceptance serializes review edits/resolution with conversion.
// The lock order is permission -> automation -> session. Cancellation consumes the
// review without releasing an approved tool invocation into one-shot execution.
func (s *Service) CoordinateAutomationAcceptance(g store.AutomationApproval, in store.V3SessionMutationInput, commit func(store.AutomationApproval, store.V3SessionMutationInput) (store.AutomationApproval, error)) (store.AutomationApproval, error) {
	s.mu.Lock()
	locked := true
	defer func() { if locked { s.mu.Unlock() } }()
	if commit == nil || in.AutomationProposal == nil || s.sessions == nil {
		return store.AutomationApproval{}, store.ErrAutomationInvalid
	}
	snapshot, found, err := s.sessions.GetSession(in.SessionID)
	if err != nil { return store.AutomationApproval{}, err }
	if !found || snapshot.UserID != g.SubjectID || snapshot.AccountScopeID != g.Scope.AccountID {
		return store.AutomationApproval{}, store.ErrAutomationConflict
	}
	plans, ok := s.sessions.(sessionPlanLookup)
	if !ok { return store.AutomationApproval{}, store.ErrAutomationInvalid }
	plan, found, err := plans.GetPlan(in.SessionID, in.AutomationProposal.PlanID)
	if err != nil { return store.AutomationApproval{}, err }
	if !found || plan.Document == nil { return store.AutomationApproval{}, store.ErrAutomationConflict }
	pending, err := s.store.ListPendingPermissions(in.SessionID, 2001)
	if err != nil { return store.AutomationApproval{}, err }
	if len(pending) > 2000 { return store.AutomationApproval{}, store.ErrAutomationConflict }
	var matched *store.PermissionRecord
	for _, record := range pending {
		if !isPendingPlanProposalRecord(record) { continue }
		var payload struct {
			PlanID string `json:"plan_id"`
			Document *store.SessionPlanDocument `json:"document"`
		}
		if err := json.Unmarshal([]byte(record.ToolArguments), &payload); err != nil { return store.AutomationApproval{}, err }
		if payload.PlanID != plan.ID { continue }
		if payload.Document == nil || payload.Document.Automation == nil { return store.AutomationApproval{}, store.ErrAutomationConflict }
		canonical, err := sessionruntime.NormalizePlanDocumentForSave(plan.ID, plan.Title, payload.Document, nil)
		if err != nil { return store.AutomationApproval{}, err }
		want, got := *plan.Document, *canonical
		want.RevisionID, got.RevisionID = "", ""
		want.Status, got.Status = "", ""
		if !reflect.DeepEqual(want, got) || matched != nil { return store.AutomationApproval{}, store.ErrAutomationConflict }
		copy := record
		matched = &copy
	}
	if matched == nil {
		// No pending review is valid for direct saved proposals and receipt retries.
		return commit(g, in)
	}
	updated := *matched
	updated.Status, updated.Decision = store.PermissionStatusCancelled, DecisionCancel
	updated.Reason = "review consumed by explicit automation conversion; no one-shot execution"
	updated.UpdatedAt, updated.ResolvedAt, updated.CompletedAt = g.WrittenAt, g.WrittenAt, g.WrittenAt
	updated.ExecutionStatus = store.PermissionExecCancelled
	updated.Output = permissionResolutionSummary(updated.ToolName, updated.Status, updated.Reason)
	updated.Error = permissionResolutionError(updated.Status)
	updated.DurationMS = permissionDurationMS(updated)
	summary, err := s.summaryForMutationLocked(in.SessionID, matched, updated, g.WrittenAt)
	if err != nil { return store.AutomationApproval{}, err }
	in.AutomationPermission = &store.AutomationPermissionResolution{Previous: *matched, Record: updated, Summary: summary}
	grant, err := commit(g, in)
	if err != nil { return store.AutomationApproval{}, err }
	s.notifyWaitersLocked(updated)
	publishPermission, publishSummary := s.permissionRealtimePublish, s.summaryRealtimePublish
	s.mu.Unlock()
	locked = false
	// Durable rows are already committed. Propagate delivery failures explicitly;
	// reconnect repairs from the canonical permission and summary records.
	if publishPermission != nil {
		if err := publishPermission(in.SessionID, updated); err != nil { return grant, err }
	}
	if publishSummary != nil {
		if err := publishSummary(in.SessionID, summary); err != nil { return grant, err }
	}
	return grant, nil
}
