package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type PlanAcceptanceCommitInput struct {
	Session               pebblestore.SessionSnapshot
	PlanID                string
	Title                 string
	Plan                  string
	Document              *pebblestore.SessionPlanDocument
	ApplySessionMutation  func(SessionMutationInput) (SessionMutationResult, error)
	ModeEventFields       map[string]any
	BuildLifecycleMessage func(pebblestore.SessionPlanSnapshot, PlanExecutionSummary) *pebblestore.MessageSnapshot
}

type PlanAcceptanceCommitResult struct {
	Session  pebblestore.SessionSnapshot
	Plan     pebblestore.SessionPlanSnapshot
	Mutation SessionMutationResult
}

// CommitV3PlanAcceptance prepares and commits native V3 plan acceptance through
// one idempotent store transaction. It deliberately bypasses legacy plan, mode,
// global-event, and message persistence.
func (s *Service) CommitV3PlanAcceptance(input PlanAcceptanceCommitInput) (PlanAcceptanceCommitResult, error) {
	if s == nil || s.store == nil || input.ApplySessionMutation == nil {
		return PlanAcceptanceCommitResult{}, errors.New("v3 plan acceptance mutation boundary is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	session := input.Session
	if current, ok, err := s.store.GetSession(session.ID); err != nil {
		return PlanAcceptanceCommitResult{}, err
	} else if !ok {
		return PlanAcceptanceCommitResult{}, fmt.Errorf("session %q not found", session.ID)
	} else {
		session = current
	}
	if NormalizeMode(session.Mode) != ModePlan {
		return PlanAcceptanceCommitResult{}, fmt.Errorf("v3 plan acceptance requires session mode %q, got %q", ModePlan, NormalizeMode(session.Mode))
	}
	now := time.Now().UnixMilli()
	planID := strings.TrimSpace(input.PlanID)
	if planID == "" && input.Document != nil {
		planID = strings.TrimSpace(input.Document.ID)
	}
	if planID == "" {
		planID = s.newPlanID(now)
	}
	title := strings.TrimSpace(input.Title)
	if title == "" && input.Document != nil {
		title = strings.TrimSpace(input.Document.Title)
	}
	if title == "" {
		title = "Plan"
	}
	planText := strings.TrimSpace(input.Plan)
	if planText == "" {
		planText = "# " + title
	}
	existing, found, err := s.store.GetPlan(session.ID, planID)
	if err != nil {
		return PlanAcceptanceCommitResult{}, err
	}
	document, err := NormalizePlanDocumentForSave(planID, title, input.Document, func() *pebblestore.SessionPlanDocument {
		if found {
			return existing.Document
		}
		return nil
	}())
	if err != nil {
		return PlanAcceptanceCommitResult{}, err
	}
	version := 1
	createdAt := now
	var archived *pebblestore.SessionPlanSnapshot
	if found {
		version = existing.Version + 1
		if existing.Version <= 0 {
			version = 2
		}
		createdAt = existing.CreatedAt
		copy := existing
		copy.Active = false
		archived = &copy
	}
	document.ID = planID
	document.Title = title
	document.Status = "approved"
	document.RevisionID = fmt.Sprintf("%s:v%d", planID, version)
	plan := pebblestore.SessionPlanSnapshot{ID: planID, SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, Title: title, Plan: planText, Status: "approved", ApprovalState: "approved", Active: true, CreatedAt: createdAt, UpdatedAt: now, UpdateSummary: "exit plan mode submission", UpdateScope: "plan", UpdateKind: "exit_plan_mode", RevisionKind: PlanRevisionKindDefinition, Version: version, Document: document}
	if found {
		plan.ParentRevision = existing.Version
		plan.PriorTitle = existing.Title
		plan.PriorPlan = existing.Plan
		plan.DiffLines = BuildPlanDiffLines(existing.Plan, planText)
	}
	updatedSession := session
	updatedSession.Mode = ModeAuto
	updatedSession.UpdatedAt = now
	planPayload, err := json.Marshal(map[string]any{"session_id": session.ID, "plan_id": planID, "title": title, "status": "approved", "approval_state": "approved", "activate": true, "has_active_plan": true, "active_plan": plan, "updated_at": now, "updated": found, "version": version, "parent_revision": plan.ParentRevision, "update_summary": plan.UpdateSummary, "update_scope": plan.UpdateScope, "update_kind": plan.UpdateKind, "revision_kind": plan.RevisionKind})
	if err != nil {
		return PlanAcceptanceCommitResult{}, err
	}
	modeFields := map[string]any{"session_id": session.ID, "mode": ModeAuto, "updated_at": now}
	for key, value := range input.ModeEventFields {
		modeFields[key] = value
	}
	modePayload, err := json.Marshal(modeFields)
	if err != nil {
		return PlanAcceptanceCommitResult{}, err
	}
	var lifecycleMessage *pebblestore.MessageSnapshot
	if input.BuildLifecycleMessage != nil {
		lifecycleMessage = input.BuildLifecycleMessage(plan, SummarizePlanExecution(plan.Document))
	}
	acceptance := &pebblestore.V3PlanAcceptanceMutation{Plan: plan, ArchivedRevision: archived, Session: updatedSession, PlanEventPayload: planPayload, ModeEventPayload: modePayload, ModeMessage: lifecycleMessage}
	hashInput, _ := json.Marshal(acceptance)
	sum := sha256.Sum256(hashInput)
	payloadHash := hex.EncodeToString(sum[:])
	clientRequestID := fmt.Sprintf("plan-acceptance:%s:%s:v%d", session.ID, planID, version)
	mutation, err := input.ApplySessionMutation(SessionMutationInput{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash, Kind: SessionMutationAcceptPlan, EventType: "session.plan.saved", PlanAcceptance: acceptance, NowUnixMs: now})
	if err != nil {
		return PlanAcceptanceCommitResult{}, err
	}
	if mutation.Plan != nil {
		plan = *mutation.Plan
	}
	if mutation.Session != nil {
		updatedSession = *mutation.Session
	}
	return PlanAcceptanceCommitResult{Session: updatedSession, Plan: plan, Mutation: mutation}, nil
}
