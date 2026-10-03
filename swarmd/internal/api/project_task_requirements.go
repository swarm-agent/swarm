package api

import (
	"errors"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

// EditProjectTaskRequirements republishes only the patched definition through the
// existing atomic task-card publication boundary, including its approval receipt.
func (s *Server) EditProjectTaskRequirements(account, user, projectID, taskID string, patch sessionruntime.PlanDocumentPatch) (sessionruntime.ProjectTaskPlanSubmissionResult, error) {
	var empty sessionruntime.ProjectTaskPlanSubmissionResult
	s.projectTaskCreateMu.Lock()
	defer s.projectTaskCreateMu.Unlock()
	if patch.BaseRevisionID == "" {
		return empty, errors.New("base_revision_id is required")
	}
	ops := patch.Operations
	if len(ops) == 0 {
		ops = []sessionruntime.PlanDocumentPatch{patch}
	}
	for _, op := range ops {
		switch op.Operation {
		case "add_requirement", "edit_requirement", "remove_requirement", "reorder_requirements":
		default:
			return empty, errors.New("only targeted requirement edits are accepted")
		}
	}
	db := s.sessions.Store()
	task, found, err := db.GetProjectTask(account, projectID, taskID)
	if err != nil {
		return empty, err
	}
	if !found || task == nil || task.Archived || task.Status != "pending_approval" || task.PlanBinding == nil {
		return empty, errors.New("task is not awaiting plan review")
	}
	binding := task.PlanBinding
	owner, found, err := s.sessions.GetSession(binding.SessionID)
	if err != nil {
		return empty, err
	}
	if !found || owner.AccountScopeID != account || owner.UserID != user {
		return empty, errors.New("plan session ownership mismatch")
	}
	plan, found, err := s.sessions.GetPlan(binding.SessionID, binding.PlanID)
	if err != nil {
		return empty, err
	}
	if !found {
		return empty, errors.New("bound plan not found")
	}
	doc, err := sessionruntime.ApplyPlanDocumentPatch(plan.ID, plan.Title, plan.Document, patch)
	if err != nil {
		return empty, err
	}
	return s.planLifecycle.SubmitProjectTaskStructuredPlan(sessionruntime.ProjectTaskPlanSubmissionInput{
		AccountScopeID: account, UserID: user, ProjectID: projectID, TaskID: taskID,
		SessionID: binding.SessionID, Document: doc, PlanText: plan.Plan,
		ExpectedRevisionID: patch.BaseRevisionID,
	})
}
