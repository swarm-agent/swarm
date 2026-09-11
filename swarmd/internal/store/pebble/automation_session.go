package pebblestore

import (
	"reflect"
)

// guardAutomationSessionMutation runs under the canonical session mutation lock.
// A pending occurrence reserves the conversation before plan installation. User
// turns are rejected (not discarded or silently queued) until its plan terminates.
func (s *SessionStore) guardAutomationSessionMutation(in *V3SessionMutationInput) error {
	current, found, err := s.GetSession(in.SessionID)
	if err != nil {
		return err
	}
	if in.Kind == V3SessionMutationCreateSession && in.Session != nil && in.Session.Automation != nil {
		return ErrAutomationInvalid
	}
	if in.Kind == V3SessionMutationCreateSession && in.Session != nil {
		purpose, _ := in.Session.Metadata[SessionPurposeMetadataKey].(string)
		workspaceID := SessionAutomationManagementWorkspace(*in.Session)
		if purpose != "" && (purpose != SessionPurposeAutomationManagement || workspaceID == "") {
			return ErrAutomationInvalid
		}
		if workspaceID != "" {
			valid := false
			for _, grant := range in.Session.WorkspaceGrants {
				valid = valid || (grant.WorkspaceID == workspaceID && grant.Kind == WorkspaceGrantPrimary)
			}
			if !valid {
				return ErrAutomationInvalid
			}
		}
	}
	if !found {
		if in.AutomationBinding != nil {
			return ErrAutomationInvalid
		}
		return nil
	}
	// Purpose is immutable after creation, including lower-level metadata writers.
	candidate := in.Session
	if in.PlanAcceptance != nil {
		candidate = &in.PlanAcceptance.Session
	}
	if candidate != nil {
		if workspaceID := SessionAutomationManagementWorkspace(current); workspaceID != "" {
			valid := false
			for _, grant := range candidate.WorkspaceGrants {
				valid = valid || (grant.WorkspaceID == workspaceID && grant.Kind == WorkspaceGrantPrimary)
			}
			if !valid || candidate.WorkspacePath != current.WorkspacePath {
				return ErrAutomationConflict
			}
		}
		for _, key := range []string{SessionPurposeMetadataKey, SessionPurposeWorkspaceMetadataKey} {
			if !reflect.DeepEqual(current.Metadata[key], candidate.Metadata[key]) {
				return ErrAutomationConflict
			}
		}
	}
	binding := current.Automation
	if in.PlanAcceptance != nil {
		in.Session = &in.PlanAcceptance.Session
	}
	if in.AutomationBinding != nil {
		next := in.AutomationBinding
		if in.Kind != V3SessionMutationUpdateMetadata || current.AccountScopeID != in.AccountScopeID || current.UserID != in.UserID || !current.WorktreeEnabled || next.AutomationID == "" || next.WorkspaceID == "" {
			return ErrAutomationInvalid
		}
		if binding != nil && (binding.AutomationID != next.AutomationID || binding.WorkspaceID != next.WorkspaceID) {
			return ErrAutomationConflict
		}
		if active, ok, err := s.GetV3SessionActiveRunIntent(in.SessionID); err != nil {
			return err
		} else if ok && (active.Status == V3RunIntentRunning || active.Status == V3RunIntentPendingExecutor) {
			return ErrAutomationConflict
		}
		if next.ExecutionKey != "" && (binding == nil || binding.ExecutionKey == "") {
			active, ok, err := s.GetActivePlan(in.SessionID)
			if err != nil {
				return err
			}
			if ok {
				plan, found, err := s.GetPlan(in.SessionID, active.PlanID)
				if err != nil {
					return err
				}
				if found && plan.Document != nil {
					for _, cp := range plan.Document.Checkpoints {
						if cp.Status == "in_progress" || cp.Status == "blocked" || cp.Status == "needs_review" {
							return ErrAutomationConflict
						}
					}
				}
			}
		}
		if binding != nil && binding.ExecutionKey != "" && binding.ExecutionKey != next.ExecutionKey {
			if err := s.automationSessionIdle(current); err != nil {
				return err
			}
		}
		if binding != nil && binding.ExecutionKey == next.ExecutionKey && !reflect.DeepEqual(binding, next) {
			return ErrAutomationConflict
		}
		// Validate the definition and current grant at the serialized publication
		// boundary as well as in the explicit-user adapter.
		scope := AutomationScope{AccountID: in.AccountScopeID, WorkspaceID: next.WorkspaceID}
		def, ok, err := s.store.GetAutomationRecord(scope, next.AutomationID, "definition", next.AutomationID, 0)
		if err != nil {
			return err
		}
		if !ok || def.Revision != in.AutomationDefinitionRevision || def.Definition == nil || def.Definition.SessionID != in.SessionID || !reflect.DeepEqual(def.Definition.Authorization, next.Policy) {
			return ErrAutomationConflict
		}
		if next.ExecutionKey != "" {
			grant, ok, err := s.store.GetAutomationApproval(scope, next.Policy.ApprovalReference)
			if err != nil {
				return err
			}
			if !ok || grant.AutomationID != next.AutomationID || grant.SubjectID != current.UserID || grant.RevokedAt != 0 || grant.ExpiresAt <= in.NowUnixMs || !def.Definition.Enabled {
				return ErrAutomationConflict
			}
		}
		copy := current
		copy.Automation = next
		if next.ExecutionKey != "" {
			copy.Mode = "auto"
		}
		in.Session = &copy
		return nil
	}
	if binding != nil && in.Session != nil && (current.WorkspacePath != in.Session.WorkspacePath || current.WorktreeRootPath != in.Session.WorktreeRootPath || current.WorktreeBranch != in.Session.WorktreeBranch || !in.Session.WorktreeEnabled || !reflect.DeepEqual(current.WorkspaceGrants, in.Session.WorkspaceGrants)) {
		return ErrAutomationConflict
	}
	if binding == nil || binding.ExecutionKey == "" {
		return nil
	}
	// Only occurrence-attributed checkpoint intents may enter a reserved session.
	if in.RunIntent != nil && (in.RunIntent.Status == V3RunIntentPendingExecutor || in.RunIntent.Status == V3RunIntentRunning) && in.RunIntent.PlanID != binding.PlanID {
		if err := s.automationSessionIdle(current); err != nil {
			return err
		}
	}
	differentPlan := in.PlanSave != nil && in.PlanSave.Activate && in.PlanSave.Plan.ID != binding.PlanID
	if in.PlanAcceptance != nil && in.PlanAcceptance.Plan.ID != binding.PlanID {
		differentPlan = true
	}
	userMessage := in.Message != nil && in.Message.Role == "user"
	if differentPlan || userMessage {
		if err := s.automationSessionIdle(current); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionStore) automationSessionIdle(current SessionSnapshot) error {
	b := current.Automation
	occurrence, ok, err := s.store.GetAutomationRecord(AutomationScope{AccountID: current.AccountScopeID, WorkspaceID: b.WorkspaceID}, b.AutomationID, "occurrence", b.OccurrenceID, 0)
	if err != nil {
		return err
	}
	if active, found, err := s.GetV3SessionActiveRunIntent(current.ID); err != nil {
		return err
	} else if found && (active.Status == V3RunIntentRunning || active.Status == V3RunIntentPendingExecutor) {
		return ErrAutomationConflict
	}
	if ok && occurrence.Occurrence != nil && occurrence.Occurrence.State == "cancelled" {
		return nil
	}
	plan, ok, err := s.GetPlan(current.ID, b.PlanID)
	if err != nil {
		return err
	}
	if !ok || plan.Document == nil || len(plan.Document.Checkpoints) == 0 {
		return ErrAutomationConflict
	}
	for _, cp := range plan.Document.Checkpoints {
		if cp.Status == "failed" {
			return nil
		}
	}
	for _, cp := range plan.Document.Checkpoints {
		if cp.Status != "completed" {
			return ErrAutomationConflict
		}
	}
	return nil
}
