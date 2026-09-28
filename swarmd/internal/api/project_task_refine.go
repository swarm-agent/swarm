package api

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// refineBoundProjectTask invalidates acceptance BEFORE requesting new planning.
// It never substitutes a router summary for an executable plan. If later steps
// fail, the old plan remains rejected, so a partial failure cannot execute it.
func (s *Server) refineBoundProjectTask(p identity.Principal, task *pebblestore.ProjectTaskRecord, guards tool.ProjectTaskApprovalGuards, feedback string) (*pebblestore.ProjectTaskRecord, error) {
	if task.PlanBinding == nil || task.SessionID == "" {
		return nil, errors.New("bound task required")
	}
	b := task.PlanBinding
	if guards.SessionID != task.SessionID || guards.PlanID != b.PlanID || guards.DefinitionRevision <= 0 || guards.DefinitionRevision != b.DefinitionRevision {
		return nil, errors.New("exact current session, plan and definition revision required")
	}
	if strings.TrimSpace(feedback) == "" {
		return nil, errors.New("plan revision feedback required")
	}
	if task.Status != "pending_approval" {
		return nil, errors.New("only an unapproved plan may be refined here")
	}
	db := s.sessions.Store()
	sess, found, err := db.GetSession(task.SessionID)
	if err != nil {
		return nil, err
	}
	if !found || sess.AccountScopeID != p.AccountScopeID || sess.Mode != sessionruntime.ModePlan {
		return nil, errors.New("bound planning session unavailable")
	}
	plan, found, err := db.GetPlan(task.SessionID, b.PlanID)
	if err != nil {
		return nil, err
	}
	if !found || plan.AccountScopeID != p.AccountScopeID || plan.SessionID != task.SessionID || plan.Version != guards.DefinitionRevision {
		return nil, errors.New("bound plan revision is stale")
	}
	if plan.ApprovalState == "approved" {
		return nil, errors.New("approved plan requires the execution revision workflow")
	}
	doc, err := cloneSessionsV3PlanDocument(plan.Document)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, errors.New("executable plan missing")
	}
	archived := plan
	archived.Active = false
	plan.ParentRevision = plan.Version
	plan.Version++
	plan.Status = "rejected"
	plan.ApprovalState = "rejected"
	plan.AcceptedDefinitionReceipt = ""
	plan.UpdatedAt = time.Now().UnixMilli()
	doc.Status = "rejected"
	plan.Document = doc
	key := fmt.Sprintf("project-task:refine:%s:%d", task.ID, guards.DefinitionRevision)
	if _, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: task.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessionruntime.SessionMutationSavePlan, PlanSave: &pebblestore.V3PlanSaveMutation{Plan: plan, ArchivedRevision: &archived, ExpectedParentVersion: archived.Version}}); err != nil {
		return nil, err
	}
	updated, err := db.UpdateProjectTask(p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
		if t.PlanBinding == nil || t.PlanBinding.PlanID != b.PlanID || t.PlanBinding.DefinitionRevision != guards.DefinitionRevision {
			return errors.New("task changed during refinement")
		}
		t.Status = "planning"
		t.Revision++
		t.FeedbackHistory = append(t.FeedbackHistory, feedback)
		t.ActionNeeded = "Revising executable plan; approval unavailable until resubmission"
		t.PlanDocument = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Canonical message ingestion records a durable run intent and source message.
	_, job, err := s.acceptSessionsV3Message(p, task.SessionID, sessionsV3MessageRequest{ClientRequestID: key + ":message", IdempotencyKey: key + ":message", Role: "user", Content: feedback, Metadata: map[string]any{"role": "project_plan_refine", "project_id": task.ProjectID, "task_id": task.ID, "plan_id": b.PlanID}})
	if err != nil {
		return nil, err
	}
	if job != nil {
		s.v3SessionExecutor.EnqueueRun(*job)
	}
	return updated, nil
}
