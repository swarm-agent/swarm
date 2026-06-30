package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// PlanLifecycleService owns user-facing typed plan lifecycle transitions. HTTP
// handlers and tools should call these explicit methods instead of embedding
// lifecycle state changes inline: follow-up requests append one checkpoint,
// revision requests create approval-gated replacements for the current plan,
// and new-plan requests create approval-gated separate proposals.
type PlanLifecycleService struct {
	sessions             *Service
	globalFollowupPolicy func(accountScopeID string) (string, error)
}

func NewPlanLifecycleService(sessions *Service) *PlanLifecycleService {
	return &PlanLifecycleService{sessions: sessions}
}

func (s *PlanLifecycleService) SetGlobalFollowupCheckpointPolicyResolver(resolver func(accountScopeID string) (string, error)) {
	if s == nil {
		return
	}
	s.globalFollowupPolicy = resolver
}

type PlanLifecyclePlanInput struct {
	SessionID             string
	PlanID                string
	Title                 string
	Plan                  string
	Document              *pebblestore.SessionPlanDocument
	AgentCanSubmit        bool
	ExecutionGranularity  string
	ContinuationPolicy    string
	ContinueAutomatically *bool
}

type PlanLifecycleFollowupCheckpointInput struct {
	SessionID           string
	PlanID              string
	ChangeRequest       string
	UserRequest         string
	Title               string
	Tasks               []string
	AcceptanceCriteria  []string
	SourceMessageID     string
	GlobalDefaultPolicy string
	ApprovalConfirmed   bool
	RunID               string
	RunSessionID        string
	ParentSessionID     string
	StartedAt           int64
	AttemptID           string
}

type PlanLifecycleProposalInput struct {
	SessionID string
	PlanID    string
	Title     string
	Plan      string
	Document  *pebblestore.SessionPlanDocument
	Reason    string
}

type PlanLifecycleFollowupPolicyInput struct {
	SessionID                string
	PlanID                   string
	FollowupCheckpointPolicy string
	Reason                   string
}

type PlanLifecycleExecutionInput struct {
	SessionID               string
	PlanID                  string
	CheckpointID            string
	ExecutionGranularity    string
	ContinuationPolicy      string
	ContinueAutomatically   *bool
	Result                  string
	Notes                   string
	ReviewedAt              int64
	RunID                   string
	RunSessionID            string
	ParentSessionID         string
	AttemptID               string
	StartedAt               int64
	RequestedCheckpointKind string
}

type PlanLifecycleResult struct {
	Session      pebblestore.SessionSnapshot
	Plan         pebblestore.SessionPlanSnapshot
	PlanEvent    *pebblestore.EventEnvelope
	ModeEvent    *pebblestore.EventEnvelope
	Summary      PlanExecutionSummary
	CheckpointID string
	AttemptID    string
	Action       string
	Message      string
}

func (s *PlanLifecycleService) EnterPlanMode(sessionID string) (PlanLifecycleResult, error) {
	if err := s.requireConfigured(); err != nil {
		return PlanLifecycleResult{}, err
	}
	session, err := s.requireSession(sessionID)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	if NormalizeMode(session.Mode) != ModeAuto {
		return PlanLifecycleResult{}, fmt.Errorf("enter plan mode requires session mode %q, got %q", ModeAuto, NormalizeMode(session.Mode))
	}
	updated, event, err := s.sessions.SetMode(session.ID, ModePlan)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	return PlanLifecycleResult{Session: updated, ModeEvent: event, Action: "enter_plan_mode", Message: "entered plan mode"}, nil
}

func (s *PlanLifecycleService) SubmitPlanForApproval(input PlanLifecyclePlanInput) (PlanLifecycleResult, error) {
	if err := s.requireConfigured(); err != nil {
		return PlanLifecycleResult{}, err
	}
	if !input.AgentCanSubmit {
		return PlanLifecycleResult{}, errors.New("submit plan for approval is disabled for this agent")
	}
	session, err := s.requireSession(input.SessionID)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	if err := requireSessionMode(session, ModePlan, "submit plan for approval"); err != nil {
		return PlanLifecycleResult{}, err
	}
	planID := strings.TrimSpace(input.PlanID)
	title := strings.TrimSpace(input.Title)
	planText := strings.TrimSpace(input.Plan)
	document := clonePlanLifecycleDocument(input.Document)
	if planID == "" {
		if document != nil && strings.TrimSpace(document.ID) != "" {
			planID = strings.TrimSpace(document.ID)
		} else if active, ok, err := s.sessions.GetActivePlan(session.ID); err != nil {
			return PlanLifecycleResult{}, err
		} else if ok {
			planID = strings.TrimSpace(active.ID)
			if title == "" {
				title = strings.TrimSpace(active.Title)
			}
			if planText == "" {
				planText = strings.TrimSpace(active.Plan)
			}
			if document == nil {
				document = clonePlanLifecycleDocument(active.Document)
			}
		}
	} else if existing, ok, err := s.sessions.GetPlan(session.ID, planID); err != nil {
		return PlanLifecycleResult{}, err
	} else if ok {
		if title == "" {
			title = strings.TrimSpace(existing.Title)
		}
		if planText == "" {
			planText = strings.TrimSpace(existing.Plan)
		}
		if document == nil {
			document = clonePlanLifecycleDocument(existing.Document)
		}
	}
	if title == "" && document != nil {
		title = strings.TrimSpace(document.Title)
	}
	if title == "" {
		return PlanLifecycleResult{}, errors.New("submit plan for approval requires title or document.title")
	}
	if planText == "" && document != nil {
		planText = strings.TrimSpace(firstNonBlank(document.DisplayText, document.RenderedText))
	}
	if planText == "" && document == nil {
		return PlanLifecycleResult{}, errors.New("submit plan for approval requires plan or document")
	}
	if planText == "" {
		planText = "# " + title
	}
	if document != nil {
		document.ID = strings.TrimSpace(firstNonBlank(planID, document.ID))
		document.Title = strings.TrimSpace(firstNonBlank(title, document.Title))
		continuation := strings.TrimSpace(input.ContinuationPolicy)
		if input.ContinueAutomatically != nil {
			if *input.ContinueAutomatically {
				continuation = PlanAcceptanceContinuationAutomatic
			} else {
				continuation = PlanAcceptanceContinuationReviewEachCheckpoint
			}
		}
		if _, err := ApplyPlanAcceptanceExecutionPolicy(document, PlanAcceptanceExecutionOptions{ExecutionGranularity: input.ExecutionGranularity, ContinuationPolicy: continuation}); err != nil {
			return PlanLifecycleResult{}, err
		}
		document.Status = "approved"
	}
	saved, planEvent, err := s.sessions.SavePlanWithMetadata(session.ID, planID, title, planText, "approved", "approved", true, PlanSaveMetadata{UpdateSummary: "exit plan mode submission", UpdateScope: "plan", UpdateKind: "exit_plan_mode", Document: document})
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	updated, modeEvent, err := s.sessions.SetMode(session.ID, ModeAuto)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	summary := SummarizePlanExecution(saved.Document)
	checkpointID := ""
	if summary.AutoAdvanceAllowed && !summary.PlanComplete && !summary.ReviewRequired && !summary.Blocked && !summary.Failed {
		checkpointID = strings.TrimSpace(summary.NextCheckpointID)
	}
	return PlanLifecycleResult{Session: updated, Plan: saved, PlanEvent: planEvent, ModeEvent: modeEvent, Summary: summary, CheckpointID: checkpointID, Action: "submit_plan_for_approval", Message: "structured plan saved, approved; mode switched to auto"}, nil
}

func (s *PlanLifecycleService) RequestFollowupCheckpoint(input PlanLifecycleFollowupCheckpointInput) (PlanLifecycleResult, error) {
	state, err := s.loadApprovedPlan(input.SessionID, input.PlanID, "request_followup_checkpoint")
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	request := strings.TrimSpace(input.ChangeRequest)
	if request == "" {
		return PlanLifecycleResult{}, errors.New("request_followup_checkpoint requires change_request")
	}
	if summary := SummarizePlanExecution(state.doc); summary.Blocked || summary.Failed {
		return PlanLifecycleResult{}, fmt.Errorf("request_followup_checkpoint requires an unblocked plan; current stop reason is %q", summary.StopReason)
	}
	policy, err := s.resolveFollowupCheckpointPolicy(state, input.GlobalDefaultPolicy)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	if policy == PlanFollowupCheckpointPolicyRequireApproval && !input.ApprovalConfirmed {
		return PlanLifecycleResult{}, errors.New("request_followup_checkpoint requires user approval by resolved follow-up checkpoint policy")
	}
	checkpointID := nextFollowupCheckpointID(state.doc)
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = fmt.Sprintf("Follow-up: %s", truncatePlanLifecycleTitle(request, 72))
	}
	tasks := trimStringSlice(input.Tasks)
	if len(tasks) == 0 {
		tasks = []string{request}
	}
	checkpoint := pebblestore.SessionPlanCheckpoint{
		ID:                 checkpointID,
		Title:              title,
		Status:             PlanCheckpointStatusPending,
		Objective:          request,
		Tasks:              tasks,
		AcceptanceCriteria: trimStringSlice(input.AcceptanceCriteria),
		SourceMessageID:    strings.TrimSpace(input.SourceMessageID),
		Order:              len(state.doc.Checkpoints) + 1,
	}
	state.doc.Checkpoints = append(state.doc.Checkpoints, checkpoint)
	normalizeCheckpointOrder(state.doc)
	state.doc.ActiveCheckpointID = checkpointID
	if state.doc.ExecutionState == nil {
		state.doc.ExecutionState = &pebblestore.SessionPlanExecutionState{}
	}
	state.doc.ExecutionState.Status = PlanExecutionStateIdle
	state.doc.ExecutionState.ActiveAttemptID = ""
	state.doc.ExecutionState.CurrentRunID = ""
	state.doc.ExecutionState.CurrentSessionID = ""
	state.doc.ExecutionPolicy.Shape = PlanExecutionShapeCheckpointed
	normalizePlanExecutionPolicy(&state.doc.ExecutionPolicy, len(state.doc.Checkpoints))
	if policy == PlanFollowupCheckpointPolicyAutoStart {
		return s.applyCheckpointStartAndSave(state, PlanLifecycleExecutionInput{SessionID: input.SessionID, PlanID: state.plan.ID, CheckpointID: checkpointID, RunID: input.RunID, RunSessionID: input.RunSessionID, ParentSessionID: input.ParentSessionID, AttemptID: input.AttemptID, StartedAt: input.StartedAt}, checkpointID, "request_followup_checkpoint", "Appended follow-up checkpoint and prepared fresh-context checkpoint start", state.plan.Status, state.plan.ApprovalState)
	}
	return s.saveLifecyclePlan(state, checkpointID, "request_followup_checkpoint", "Appended follow-up checkpoint")
}

func (s *PlanLifecycleService) RequestPlanRevision(input PlanLifecycleProposalInput) (PlanLifecycleResult, error) {
	state, err := s.loadPlanForLifecycle(input.SessionID, input.PlanID, "request_plan_revision")
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	doc := clonePlanLifecycleDocument(input.Document)
	planText := strings.TrimSpace(input.Plan)
	if doc == nil {
		doc = state.doc
	}
	if planText == "" {
		planText = state.plan.Plan
	}
	title := strings.TrimSpace(firstNonBlank(input.Title, doc.Title, state.plan.Title))
	if title == "" {
		title = "Plan revision proposal"
	}
	doc.ID = state.plan.ID
	doc.Title = title
	doc.Status = "pending_approval"
	saved, event, err := s.sessions.SavePlanWithMetadata(state.session.ID, state.plan.ID, title, planText, "pending_approval", "pending", true, PlanSaveMetadata{UpdateSummary: firstNonBlank(strings.TrimSpace(input.Reason), "Plan revision proposal pending approval"), UpdateScope: "plan", UpdateKind: "request_plan_revision", Document: doc})
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	return PlanLifecycleResult{Session: state.session, Plan: saved, PlanEvent: event, Summary: SummarizePlanExecution(saved.Document), Action: "request_plan_revision", Message: "plan revision proposal saved for approval"}, nil
}

func (s *PlanLifecycleService) RequestNewPlan(input PlanLifecycleProposalInput) (PlanLifecycleResult, error) {
	if err := s.requireConfigured(); err != nil {
		return PlanLifecycleResult{}, err
	}
	session, err := s.requireSession(input.SessionID)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	doc := clonePlanLifecycleDocument(input.Document)
	title := strings.TrimSpace(firstNonBlank(input.Title, planLifecycleDocumentTitle(doc), "New plan proposal"))
	planText := strings.TrimSpace(input.Plan)
	if planText == "" && doc != nil {
		planText = strings.TrimSpace(firstNonBlank(doc.DisplayText, doc.RenderedText))
	}
	if planText == "" {
		planText = "# " + title
	}
	if doc != nil {
		doc.Title = title
		doc.Status = "pending_approval"
	}
	saved, event, err := s.sessions.SavePlanWithMetadata(session.ID, "", title, planText, "pending_approval", "pending", false, PlanSaveMetadata{UpdateSummary: firstNonBlank(strings.TrimSpace(input.Reason), "New plan proposal pending approval"), UpdateScope: "plan", UpdateKind: "request_new_plan", Document: doc})
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	return PlanLifecycleResult{Session: session, Plan: saved, PlanEvent: event, Summary: SummarizePlanExecution(saved.Document), Action: "request_new_plan", Message: "new plan proposal saved for approval"}, nil
}

func (s *PlanLifecycleService) SetFollowupCheckpointPolicy(input PlanLifecycleFollowupPolicyInput) (PlanLifecycleResult, error) {
	state, err := s.loadPlanForLifecycle(input.SessionID, input.PlanID, "set_followup_checkpoint_policy")
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	policy := normalizePlanFollowupCheckpointPolicy(input.FollowupCheckpointPolicy)
	switch policy {
	case "", PlanFollowupCheckpointPolicyRequireApproval, PlanFollowupCheckpointPolicyAutoStart:
	default:
		return PlanLifecycleResult{}, fmt.Errorf("followup checkpoint policy %q is not supported", input.FollowupCheckpointPolicy)
	}
	state.doc.ExecutionPolicy.FollowupCheckpointPolicy = policy
	normalizePlanExecutionPolicy(&state.doc.ExecutionPolicy, len(state.doc.Checkpoints))
	return s.saveLifecyclePlan(state, "", "set_followup_checkpoint_policy", firstNonBlank(strings.TrimSpace(input.Reason), "Updated follow-up checkpoint policy"))
}

func (s *PlanLifecycleService) ApprovePlan(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	return s.approvePlanWithPolicy(input, "approve_plan", PlanAcceptanceExecutionOptions{ExecutionGranularity: input.ExecutionGranularity, ContinuationPolicy: input.ContinuationPolicy})
}

func (s *PlanLifecycleService) StartPlanAutomatic(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	input.ContinuationPolicy = PlanAcceptanceContinuationAutomatic
	return s.approvePlanWithPolicy(input, "approve_and_start", PlanAcceptanceExecutionOptions{ExecutionGranularity: input.ExecutionGranularity, ContinuationPolicy: input.ContinuationPolicy})
}

func (s *PlanLifecycleService) StartPlanCheckpointed(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	if strings.TrimSpace(input.ContinuationPolicy) == "" {
		input.ContinuationPolicy = PlanAcceptanceContinuationReviewEachCheckpoint
	}
	return s.approvePlanWithPolicy(input, "approve_and_start", PlanAcceptanceExecutionOptions{ExecutionGranularity: input.ExecutionGranularity, ContinuationPolicy: input.ContinuationPolicy})
}

func (s *PlanLifecycleService) ApproveAndStartPlanAutomatic(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	input.ContinuationPolicy = PlanAcceptanceContinuationAutomatic
	return s.approveAndStartCheckpoint(input, "start_plan_automatic", PlanAcceptanceExecutionOptions{ExecutionGranularity: input.ExecutionGranularity, ContinuationPolicy: input.ContinuationPolicy})
}

func (s *PlanLifecycleService) ApproveAndStartPlanCheckpointed(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	if strings.TrimSpace(input.ContinuationPolicy) == "" {
		input.ContinuationPolicy = PlanAcceptanceContinuationReviewEachCheckpoint
	}
	return s.approveAndStartCheckpoint(input, "start_plan_checkpointed", PlanAcceptanceExecutionOptions{ExecutionGranularity: input.ExecutionGranularity, ContinuationPolicy: input.ContinuationPolicy})
}

func (s *PlanLifecycleService) RestartCheckpointRun(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	return s.resetAndStartCheckpoint(input, false, "restart_checkpoint")
}

func (s *PlanLifecycleService) RewindCheckpointRun(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	return s.resetAndStartCheckpoint(input, true, "rewind_to_checkpoint")
}

func (s *PlanLifecycleService) PausePlanRun(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	return s.updateExecutionState(input, "pause_plan_run", PlanExecutionStateIdle, "Paused plan run")
}

func (s *PlanLifecycleService) StopPlanRun(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	return s.updateExecutionState(input, "stop_plan_run", PlanExecutionStateIdle, "Stopped plan run")
}

func (s *PlanLifecycleService) ResumeAutomatic(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	return s.resumeWithMode(input, PlanExecutionPolicyModeAutomatic, "resume_automatic", "Updated plan automatic continuation policy")
}

func (s *PlanLifecycleService) ResumeCheckpointed(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	return s.resumeWithMode(input, PlanExecutionPolicyModeReviewEachCheckpoint, "resume_checkpointed", "Updated plan checkpoint-by-checkpoint continuation policy")
}

func (s *PlanLifecycleService) StartCheckpoint(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	return s.startCheckpoint(input, "start_checkpoint")
}

func (s *PlanLifecycleService) ContinueCheckpoint(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	return s.startCheckpoint(input, "continue_checkpoint")
}

func (s *PlanLifecycleService) AcceptCheckpoint(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	state, err := s.loadApprovedPlan(input.SessionID, input.PlanID, "accept checkpoint")
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	checkpointID := strings.TrimSpace(firstNonBlank(input.CheckpointID, state.doc.ActiveCheckpointID))
	if checkpointID == "" {
		return PlanLifecycleResult{}, errors.New("accept checkpoint requires checkpoint_id")
	}
	if err := requireCheckpointReviewable(state.doc, checkpointID); err != nil {
		return PlanLifecycleResult{}, err
	}
	reviewedAt := input.ReviewedAt
	if reviewedAt <= 0 {
		reviewedAt = time.Now().UnixMilli()
	}
	if _, err := ApplyPlanCheckpointReviewAcceptance(state.doc, PlanCheckpointReviewAcceptanceOptions{CheckpointID: checkpointID, Result: input.Result, Notes: input.Notes, ReviewedAt: reviewedAt}); err != nil {
		return PlanLifecycleResult{}, err
	}
	return s.saveLifecyclePlan(state, checkpointID, "accept_checkpoint", "Accepted checkpoint review")
}

func (s *PlanLifecycleService) RestartCheckpointFromZero(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	state, err := s.loadApprovedPlan(input.SessionID, input.PlanID, "restart checkpoint")
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	checkpointID := strings.TrimSpace(firstNonBlank(input.CheckpointID, state.doc.ActiveCheckpointID))
	if err := requireCheckpointResettable(state.doc, checkpointID); err != nil {
		return PlanLifecycleResult{}, err
	}
	if _, err := ApplyPlanCheckpointReset(state.doc, PlanCheckpointResetOptions{CheckpointID: checkpointID}); err != nil {
		return PlanLifecycleResult{}, err
	}
	return s.saveLifecyclePlan(state, checkpointID, "restart_checkpoint", "Restarted checkpoint from zero and prepared fresh-context checkpoint start")
}

func (s *PlanLifecycleService) RewindToCheckpoint(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	state, err := s.loadApprovedPlan(input.SessionID, input.PlanID, "rewind to checkpoint")
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	checkpointID := strings.TrimSpace(firstNonBlank(input.CheckpointID, state.doc.ActiveCheckpointID))
	if err := requireCheckpointResettable(state.doc, checkpointID); err != nil {
		return PlanLifecycleResult{}, err
	}
	if _, err := ApplyPlanCheckpointReset(state.doc, PlanCheckpointResetOptions{CheckpointID: checkpointID, Rewind: true}); err != nil {
		return PlanLifecycleResult{}, err
	}
	return s.saveLifecyclePlan(state, checkpointID, "rewind_to_checkpoint", "Rewound plan execution to checkpoint and prepared fresh-context checkpoint start")
}

type planLifecycleState struct {
	session pebblestore.SessionSnapshot
	plan    pebblestore.SessionPlanSnapshot
	doc     *pebblestore.SessionPlanDocument
}

func (s *PlanLifecycleService) approvePlanWithPolicy(input PlanLifecycleExecutionInput, action string, options PlanAcceptanceExecutionOptions) (PlanLifecycleResult, error) {
	state, err := s.loadPlanForLifecycle(input.SessionID, input.PlanID, "approve plan")
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	if err := requireSessionMode(state.session, ModeAuto, "approve plan"); err != nil {
		return PlanLifecycleResult{}, err
	}
	if input.ContinueAutomatically != nil {
		if *input.ContinueAutomatically {
			options.ContinuationPolicy = PlanAcceptanceContinuationAutomatic
		} else {
			options.ContinuationPolicy = PlanAcceptanceContinuationReviewEachCheckpoint
		}
	}
	if _, err := ApplyPlanAcceptanceExecutionPolicy(state.doc, options); err != nil {
		return PlanLifecycleResult{}, err
	}
	state.doc.Status = "approved"
	return s.saveLifecyclePlanWithStatus(state, "", action, "Approved plan and prepared fresh-context checkpoint start", "approved", "approved")
}

func (s *PlanLifecycleService) approveAndStartCheckpoint(input PlanLifecycleExecutionInput, action string, options PlanAcceptanceExecutionOptions) (PlanLifecycleResult, error) {
	state, err := s.loadPlanForLifecycle(input.SessionID, input.PlanID, action)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	if err := requireSessionMode(state.session, ModeAuto, action); err != nil {
		return PlanLifecycleResult{}, err
	}
	if input.ContinueAutomatically != nil {
		if *input.ContinueAutomatically {
			options.ContinuationPolicy = PlanAcceptanceContinuationAutomatic
		} else {
			options.ContinuationPolicy = PlanAcceptanceContinuationReviewEachCheckpoint
		}
	}
	if _, err := ApplyPlanAcceptanceExecutionPolicy(state.doc, options); err != nil {
		return PlanLifecycleResult{}, err
	}
	state.doc.Status = "approved"
	checkpointID := strings.TrimSpace(input.CheckpointID)
	if checkpointID == "" {
		summary := SummarizePlanExecution(state.doc)
		checkpointID = strings.TrimSpace(summary.NextCheckpointID)
	}
	if err := requireCheckpointRunnable(state.doc, checkpointID); err != nil {
		return PlanLifecycleResult{}, err
	}
	return s.applyCheckpointStartAndSave(state, input, checkpointID, action, "Approved plan and prepared fresh-context checkpoint start", "approved", "approved")
}

func (s *PlanLifecycleService) resetAndStartCheckpoint(input PlanLifecycleExecutionInput, rewind bool, action string) (PlanLifecycleResult, error) {
	state, err := s.loadApprovedPlan(input.SessionID, input.PlanID, action)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	checkpointID := strings.TrimSpace(firstNonBlank(input.CheckpointID, state.doc.ActiveCheckpointID))
	if err := requireCheckpointResettable(state.doc, checkpointID); err != nil {
		return PlanLifecycleResult{}, err
	}
	if _, err := ApplyPlanCheckpointReset(state.doc, PlanCheckpointResetOptions{CheckpointID: checkpointID, Rewind: rewind}); err != nil {
		return PlanLifecycleResult{}, err
	}
	return s.applyCheckpointStartAndSave(state, input, checkpointID, action, "Prepared fresh-context checkpoint start", state.plan.Status, state.plan.ApprovalState)
}

func (s *PlanLifecycleService) startCheckpoint(input PlanLifecycleExecutionInput, action string) (PlanLifecycleResult, error) {
	state, err := s.loadApprovedPlan(input.SessionID, input.PlanID, action)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	checkpointID := strings.TrimSpace(firstNonBlank(input.CheckpointID, state.doc.ActiveCheckpointID))
	if checkpointID == "" {
		summary := SummarizePlanExecution(state.doc)
		checkpointID = strings.TrimSpace(summary.NextCheckpointID)
	}
	if err := requireCheckpointRunnable(state.doc, checkpointID); err != nil {
		return PlanLifecycleResult{}, err
	}
	return s.applyCheckpointStartAndSave(state, input, checkpointID, action, "Prepared fresh-context checkpoint start", state.plan.Status, state.plan.ApprovalState)
}

func (s *PlanLifecycleService) applyCheckpointStartAndSave(state planLifecycleState, input PlanLifecycleExecutionInput, checkpointID, action, summary, status, approvalState string) (PlanLifecycleResult, error) {
	startedAt := input.StartedAt
	if startedAt <= 0 {
		startedAt = time.Now().UnixMilli()
	}
	decision, err := ApplyPlanCheckpointStart(state.doc, PlanCheckpointStartOptions{CheckpointID: checkpointID, PlanID: state.plan.ID, AttemptID: input.AttemptID, RunID: input.RunID, SessionID: input.RunSessionID, ParentSessionID: firstNonBlank(input.ParentSessionID, state.session.ID), StartedAt: startedAt})
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	result, err := s.saveLifecyclePlanWithStatus(state, decision.CheckpointID, action, summary, status, approvalState)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	result.CheckpointID = decision.CheckpointID
	result.AttemptID = decision.AttemptID
	return result, nil
}

func (s *PlanLifecycleService) resumeWithMode(input PlanLifecycleExecutionInput, mode, action, summary string) (PlanLifecycleResult, error) {
	state, err := s.loadApprovedPlan(input.SessionID, input.PlanID, action)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	if state.doc.ExecutionPolicy.Shape == "" {
		return PlanLifecycleResult{}, errors.New("resume requires an accepted plan execution shape")
	}
	if summaryState := SummarizePlanExecution(state.doc); summaryState.Blocked || summaryState.Failed || summaryState.PlanComplete {
		return PlanLifecycleResult{}, fmt.Errorf("resume requires a runnable plan; current stop reason is %q", summaryState.StopReason)
	}
	state.doc.ExecutionPolicy.Mode = mode
	if err := ValidatePlanDocument(state.doc); err != nil {
		return PlanLifecycleResult{}, err
	}
	return s.saveLifecyclePlan(state, "", action, summary)
}

func (s *PlanLifecycleService) updateExecutionState(input PlanLifecycleExecutionInput, action, status, summary string) (PlanLifecycleResult, error) {
	state, err := s.loadApprovedPlan(input.SessionID, input.PlanID, action)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	if state.doc.ExecutionState == nil || normalizePlanExecutionStateStatus(state.doc.ExecutionState.Status) != PlanExecutionStateInProgress {
		return PlanLifecycleResult{}, fmt.Errorf("%s requires an in-progress plan run", action)
	}
	state.doc.ExecutionState.Status = status
	state.doc.ExecutionState.UpdatedAt = time.Now().UnixMilli()
	if err := ValidatePlanDocument(state.doc); err != nil {
		return PlanLifecycleResult{}, err
	}
	return s.saveLifecyclePlan(state, state.doc.ActiveCheckpointID, action, summary)
}

func (s *PlanLifecycleService) resolveFollowupCheckpointPolicy(state planLifecycleState, explicitDefault string) (string, error) {
	globalDefault := strings.TrimSpace(explicitDefault)
	if globalDefault == "" && s != nil && s.globalFollowupPolicy != nil {
		policy, err := s.globalFollowupPolicy(state.session.AccountScopeID)
		if err != nil {
			return "", err
		}
		globalDefault = strings.TrimSpace(policy)
	}
	return ResolvePlanFollowupCheckpointPolicy(state.doc, globalDefault), nil
}

func (s *PlanLifecycleService) loadApprovedPlan(sessionID, planID, action string) (planLifecycleState, error) {
	state, err := s.loadPlanForLifecycle(sessionID, planID, action)
	if err != nil {
		return planLifecycleState{}, err
	}
	if !IsValidMode(NormalizeMode(state.session.Mode)) {
		return planLifecycleState{}, fmt.Errorf("%s requires a valid session mode, got %q", action, state.session.Mode)
	}
	if !strings.EqualFold(strings.TrimSpace(state.plan.ApprovalState), "approved") {
		return planLifecycleState{}, fmt.Errorf("%s requires an approved plan", action)
	}
	if state.doc.ExecutionPolicy.Mode == "" || state.doc.ExecutionPolicy.Shape == "" {
		return planLifecycleState{}, fmt.Errorf("%s requires an accepted plan execution policy", action)
	}
	return state, nil
}

func (s *PlanLifecycleService) loadPlanForLifecycle(sessionID, planID, action string) (planLifecycleState, error) {
	if err := s.requireConfigured(); err != nil {
		return planLifecycleState{}, err
	}
	session, err := s.requireSession(sessionID)
	if err != nil {
		return planLifecycleState{}, err
	}
	planID = strings.TrimSpace(planID)
	var plan pebblestore.SessionPlanSnapshot
	var ok bool
	if planID == "" || strings.EqualFold(planID, "active") {
		plan, ok, err = s.sessions.GetActivePlan(session.ID)
	} else {
		plan, ok, err = s.sessions.GetPlan(session.ID, planID)
	}
	if err != nil {
		return planLifecycleState{}, err
	}
	if !ok || plan.Document == nil {
		return planLifecycleState{}, fmt.Errorf("%s requires an active structured plan", action)
	}
	doc := clonePlanLifecycleDocument(plan.Document)
	if strings.TrimSpace(doc.ID) == "" {
		doc.ID = strings.TrimSpace(plan.ID)
	}
	if strings.TrimSpace(doc.Title) == "" {
		doc.Title = strings.TrimSpace(plan.Title)
	}
	if err := ValidatePlanDocument(doc); err != nil {
		return planLifecycleState{}, err
	}
	return planLifecycleState{session: session, plan: plan, doc: doc}, nil
}

func (s *PlanLifecycleService) saveLifecyclePlan(state planLifecycleState, checkpointID, updateKind, updateSummary string) (PlanLifecycleResult, error) {
	return s.saveLifecyclePlanWithStatus(state, checkpointID, updateKind, updateSummary, state.plan.Status, state.plan.ApprovalState)
}

func (s *PlanLifecycleService) saveLifecyclePlanWithStatus(state planLifecycleState, checkpointID, updateKind, updateSummary, status, approvalState string) (PlanLifecycleResult, error) {
	if err := ValidatePlanDocument(state.doc); err != nil {
		return PlanLifecycleResult{}, err
	}
	saved, event, err := s.sessions.SavePlanWithMetadata(state.session.ID, state.plan.ID, state.plan.Title, state.plan.Plan, status, approvalState, true, PlanSaveMetadata{UpdateSummary: updateSummary, UpdateScope: checkpointID, UpdateKind: updateKind, Checkpoint: true, Document: state.doc})
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	return PlanLifecycleResult{Session: state.session, Plan: saved, PlanEvent: event, Summary: SummarizePlanExecution(saved.Document), CheckpointID: checkpointID, Action: updateKind, Message: updateSummary}, nil
}

func (s *PlanLifecycleService) requireConfigured() error {
	if s == nil || s.sessions == nil {
		return errors.New("plan lifecycle service is not configured")
	}
	return nil
}

func (s *PlanLifecycleService) requireSession(sessionID string) (pebblestore.SessionSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionSnapshot{}, errors.New("session id is required")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	if !ok {
		return pebblestore.SessionSnapshot{}, fmt.Errorf("session %q not found", sessionID)
	}
	return session, nil
}

func requireSessionMode(session pebblestore.SessionSnapshot, want, action string) error {
	mode := NormalizeMode(session.Mode)
	if mode != want {
		return fmt.Errorf("%s requires session mode %q, got %q", action, want, mode)
	}
	return nil
}

func requireCheckpointRunnable(doc *pebblestore.SessionPlanDocument, checkpointID string) error {
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return errors.New("checkpoint_id is required")
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return fmt.Errorf("plan document checkpoint %q was not found", checkpointID)
	}
	checkpoint := doc.Checkpoints[idx]
	status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
	if status != PlanCheckpointStatusPending && status != PlanCheckpointStatusInProgress {
		return fmt.Errorf("checkpoint %q status %q is not runnable", checkpointID, status)
	}
	if planCheckpointReviewPending(doc.ExecutionPolicy, checkpoint, idx < len(doc.Checkpoints)-1) {
		return fmt.Errorf("checkpoint %q is waiting for review", checkpointID)
	}
	if doc.ExecutionState != nil && normalizePlanExecutionStateStatus(doc.ExecutionState.Status) == PlanExecutionStateInProgress {
		active := strings.TrimSpace(doc.ActiveCheckpointID)
		if active != "" && !strings.EqualFold(active, checkpointID) {
			return fmt.Errorf("plan run is already in progress for checkpoint %q", active)
		}
		if strings.TrimSpace(doc.ExecutionState.ActiveAttemptID) != "" && status == PlanCheckpointStatusInProgress {
			return fmt.Errorf("checkpoint %q already has an in-progress attempt", checkpointID)
		}
	}
	return nil
}

func requireCheckpointReviewable(doc *pebblestore.SessionPlanDocument, checkpointID string) error {
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return fmt.Errorf("plan document checkpoint %q was not found", checkpointID)
	}
	status := normalizePlanCheckpointStatusForSave(doc.Checkpoints[idx].Status)
	if status != PlanCheckpointStatusCompleted && status != PlanCheckpointStatusNeedsReview {
		return fmt.Errorf("checkpoint %q status %q is not waiting for review", checkpointID, status)
	}
	return nil
}

func requireCheckpointResettable(doc *pebblestore.SessionPlanDocument, checkpointID string) error {
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return errors.New("checkpoint_id is required")
	}
	if findPlanCheckpointIndex(doc.Checkpoints, checkpointID) < 0 {
		return fmt.Errorf("plan document checkpoint %q was not found", checkpointID)
	}
	return nil
}

func nextFollowupCheckpointID(doc *pebblestore.SessionPlanDocument) string {
	base := "followup"
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if doc == nil || findPlanCheckpointIndex(doc.Checkpoints, candidate) < 0 {
			return candidate
		}
	}
}

func truncatePlanLifecycleTitle(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

func planLifecycleDocumentTitle(doc *pebblestore.SessionPlanDocument) string {
	if doc == nil {
		return ""
	}
	return strings.TrimSpace(doc.Title)
}

func clonePlanLifecycleDocument(doc *pebblestore.SessionPlanDocument) *pebblestore.SessionPlanDocument {
	if doc == nil {
		return nil
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		clone := *doc
		return &clone
	}
	var clone pebblestore.SessionPlanDocument
	if err := json.Unmarshal(raw, &clone); err != nil {
		clone := *doc
		return &clone
	}
	return &clone
}
