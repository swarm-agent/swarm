package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// PlanLifecycleService owns user-facing typed plan lifecycle transitions. HTTP
// handlers and tools should call these explicit methods instead of embedding
// lifecycle state changes inline: session checkpoint requests append one ordered
// checkpoint to the active session chain, amendments replace or append the protected
// future definition, and new-plan requests create or replace an approved plan.
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
	ApplySessionMutation  func(SessionMutationInput) (SessionMutationResult, error)
	ModeEventFields       map[string]any
	ModePreference        pebblestore.ModelPreference
	ModeAgentProfile      *pebblestore.AgentProfile
	BuildLifecycleMessage func(pebblestore.SessionPlanSnapshot, PlanExecutionSummary) *pebblestore.MessageSnapshot
}

type PlanLifecycleFollowupCheckpointInput struct {
	SessionID           string
	PlanID              string
	ChangeRequest       string
	Title               string
	Tasks               []string
	AcceptanceCriteria  []string
	Artifacts           []pebblestore.SessionPlanArtifactReference
	Notes               string
	SourceMessageID     string
	GlobalDefaultPolicy string
	ApprovalConfirmed   bool
	RunID               string
	RunSessionID        string
	ParentSessionID     string
	StartedAt           int64
	AttemptID           string
}

type PlanLifecycleSessionCheckpointInput struct {
	SessionID          string
	ChangeRequest      string
	Title              string
	CheckpointID       string
	Tasks              []string
	AcceptanceCriteria []string
	Artifacts          []pebblestore.SessionPlanArtifactReference
	Notes              string
	SourceMessageID    string
	RunID              string
	RunSessionID       string
	ParentSessionID    string
	StartedAt          int64
	AttemptID          string
}

type PlanLifecycleProposalInput struct {
	SessionID             string
	PlanID                string
	Title                 string
	Plan                  string
	Document              *pebblestore.SessionPlanDocument
	Reason                string
	ApprovalConfirmed     bool
	ExecutionGranularity  string
	ContinuationPolicy    string
	ContinueAutomatically *bool
}

type PlanLifecycleAmendmentInput struct {
	SessionID               string
	PlanID                  string
	Title                   string
	Plan                    string
	Document                *pebblestore.SessionPlanDocument
	BaseRevision            int
	UpdateSummary           string
	ReplaceFromCheckpointID string
	AmendFutureCheckpoints  bool
	OverrideStale           bool
}

type PlanLifecycleRevisionRestoreInput struct {
	SessionID             string
	PlanID                string
	Version               int
	CheckpointID          string
	ExecutionGranularity  string
	ContinuationPolicy    string
	ContinueAutomatically *bool
	Restart               bool
	Start                 bool
	SkipPrior             bool
	RunID                 string
	RunSessionID          string
	ParentSessionID       string
	AttemptID             string
	StartedAt             int64
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
	StartNext               bool
	RequestedCheckpointKind string
	ReplacementRequest      string
	ReplacementTitle        string
	ReplacementTasks        []string
	ReplacementCriteria     []string
	ReplacementArtifacts    []pebblestore.SessionPlanArtifactReference
	ReplacementNotes        string
	ReplacementSourceID     string
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
	ModeChanged  bool
	V3Mutation   *SessionMutationResult
}

func (s *PlanLifecycleService) EnterPlanMode(sessionID string) (PlanLifecycleResult, error) {
	pebblestore.ObserveV3PlanLifecycleMutation()
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
	pebblestore.ObserveV3PlanLifecycleMutation()
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
	if document == nil {
		return PlanLifecycleResult{}, errors.New("submit plan for approval requires an explicit structured document; plan text and an existing saved plan are display context only")
	}
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
		document.ExecutionOrigin = PlanExecutionOriginApprovedPlan
	}
	if err := ValidateExecutablePlanDocument(document); err != nil {
		return PlanLifecycleResult{}, err
	}
	if input.ApplySessionMutation != nil {
		committed, err := s.sessions.CommitV3PlanAcceptance(PlanAcceptanceCommitInput{Session: session, PlanID: planID, Title: title, Plan: planText, Document: document, ApplySessionMutation: input.ApplySessionMutation, ModeEventFields: input.ModeEventFields, ModePreference: input.ModePreference, ModeAgentProfile: input.ModeAgentProfile, BuildLifecycleMessage: input.BuildLifecycleMessage})
		if err != nil {
			return PlanLifecycleResult{}, err
		}
		summary := SummarizePlanExecution(committed.Plan.Document)
		checkpointID := ""
		if summary.AutoAdvanceAllowed && !summary.PlanComplete && !summary.ReviewRequired && !summary.Blocked && !summary.Failed {
			checkpointID = strings.TrimSpace(summary.NextCheckpointID)
		}
		return PlanLifecycleResult{Session: committed.Session, Plan: committed.Plan, Summary: summary, CheckpointID: checkpointID, Action: "submit_plan_for_approval", Message: "structured plan saved, approved; mode switched to auto", ModeChanged: true, V3Mutation: &committed.Mutation}, nil
	}
	// Transitional non-V3 adapters retain the legacy persistence path until they
	// can supply the canonical V3 mutation boundary.
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
	return PlanLifecycleResult{Session: updated, Plan: saved, PlanEvent: planEvent, ModeEvent: modeEvent, Summary: summary, CheckpointID: checkpointID, Action: "submit_plan_for_approval", Message: "structured plan saved, approved; mode switched to auto", ModeChanged: modeEvent != nil}, nil
}

func (s *PlanLifecycleService) RequestFollowupCheckpoint(input PlanLifecycleFollowupCheckpointInput) (PlanLifecycleResult, error) {
	if err := s.requireConfigured(); err != nil {
		return PlanLifecycleResult{}, err
	}
	unlock := s.sessions.lockPlanLifecycleSession(input.SessionID)
	defer unlock()
	return s.requestFollowupCheckpoint(input)
}

func (s *PlanLifecycleService) requestFollowupCheckpoint(input PlanLifecycleFollowupCheckpointInput) (PlanLifecycleResult, error) {
	if strings.TrimSpace(input.PlanID) == "" {
		if err := s.requireConfigured(); err != nil {
			return PlanLifecycleResult{}, err
		}
		if _, ok, err := s.sessions.GetActivePlan(strings.TrimSpace(input.SessionID)); err != nil {
			return PlanLifecycleResult{}, err
		} else if !ok {
			// A follow-up requires an existing structured plan. Auto mode with no
			// active plan uses the same payload as the atomic create-and-start path,
			// so normalize stale model routing instead of forcing a failed call and
			// a second manual lifecycle action.
			return s.startSessionCheckpoint(PlanLifecycleSessionCheckpointInput{
				SessionID:          input.SessionID,
				ChangeRequest:      input.ChangeRequest,
				Title:              input.Title,
				Tasks:              input.Tasks,
				AcceptanceCriteria: input.AcceptanceCriteria,
				Notes:              input.Notes,
				SourceMessageID:    input.SourceMessageID,
				RunID:              input.RunID,
				RunSessionID:       input.RunSessionID,
				ParentSessionID:    input.ParentSessionID,
				StartedAt:          input.StartedAt,
				AttemptID:          input.AttemptID,
			})
		}
	}
	pebblestore.ObserveV3PlanLifecycleMutation()
	state, err := s.loadApprovedPlan(input.SessionID, input.PlanID, "request_followup_checkpoint")
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	request := strings.TrimSpace(input.ChangeRequest)
	if request == "" {
		return PlanLifecycleResult{}, errors.New("request_followup_checkpoint requires change_request")
	}
	if sourceMessageID := strings.TrimSpace(input.SourceMessageID); sourceMessageID != "" {
		for _, existing := range state.doc.Checkpoints {
			if strings.TrimSpace(existing.SourceMessageID) != sourceMessageID {
				continue
			}
			summary := SummarizePlanExecution(state.doc)
			return PlanLifecycleResult{Session: state.session, Plan: state.plan, Summary: summary, CheckpointID: strings.TrimSpace(existing.ID), AttemptID: strings.TrimSpace(existing.AttemptID), Action: "request_followup_checkpoint", Message: "Reused existing session checkpoint for duplicate source message"}, nil
		}
	}
	insertionPoint := followupCheckpointInsertionPointForDocument(state.doc)
	if insertionPoint.StopReason == PlanCheckpointStatusFailed {
		return PlanLifecycleResult{}, fmt.Errorf("request_followup_checkpoint cannot continue a failed plan; current stop reason is %q", insertionPoint.StopReason)
	}
	policy, err := s.resolveFollowupCheckpointPolicy(state, input.GlobalDefaultPolicy)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	if policy == PlanFollowupCheckpointPolicyRequireApproval && !input.ApprovalConfirmed {
		return PlanLifecycleResult{}, errors.New("request_followup_checkpoint requires user approval by resolved session checkpoint policy")
	}
	checkpointID, err := nextFollowupCheckpointID(state.doc, insertionPoint.Index)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = fmt.Sprintf("Session checkpoint: %s", truncatePlanLifecycleTitle(request, 72))
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
		Artifacts:          trimPlanArtifacts(input.Artifacts),
		Notes:              buildCheckpointHandoffNotes(request, input.Notes),
		SourceMessageID:    strings.TrimSpace(input.SourceMessageID),
		Order:              insertionPoint.Index + 1,
	}
	resolvedID, err := resolveFollowupInsertionPoint(state.doc, insertionPoint, checkpointID, time.Now().UnixMilli())
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	state.doc.Checkpoints = insertPlanCheckpointAt(state.doc.Checkpoints, insertionPoint.Index, checkpoint)
	normalizeCheckpointOrder(state.doc)
	if resolvedID != "" {
		state.doc.ExecutionState.LastCheckpointID = resolvedID
	}
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
		if strings.TrimSpace(input.RunID) == "" {
			return s.saveLifecyclePlan(state, checkpointID, "request_followup_checkpoint", "Inserted session checkpoint and queued fresh-context checkpoint start")
		}
		return s.applyCheckpointStartAndSave(state, PlanLifecycleExecutionInput{SessionID: input.SessionID, PlanID: state.plan.ID, CheckpointID: checkpointID, RunID: input.RunID, RunSessionID: input.RunSessionID, ParentSessionID: input.ParentSessionID, AttemptID: input.AttemptID, StartedAt: input.StartedAt}, checkpointID, "request_followup_checkpoint", "Inserted session checkpoint and started it with the current run", state.plan.Status, state.plan.ApprovalState)
	}
	return s.saveLifecyclePlan(state, checkpointID, "request_followup_checkpoint", "Inserted session checkpoint")
}

func (s *PlanLifecycleService) StartSessionCheckpoint(input PlanLifecycleSessionCheckpointInput) (PlanLifecycleResult, error) {
	if err := s.requireConfigured(); err != nil {
		return PlanLifecycleResult{}, err
	}
	unlock := s.sessions.lockPlanLifecycleSession(input.SessionID)
	defer unlock()
	return s.startSessionCheckpoint(input)
}

func (s *PlanLifecycleService) startSessionCheckpoint(input PlanLifecycleSessionCheckpointInput) (PlanLifecycleResult, error) {
	pebblestore.ObserveV3PlanLifecycleMutation()
	if err := s.requireConfigured(); err != nil {
		return PlanLifecycleResult{}, err
	}
	session, err := s.requireSession(input.SessionID)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	if err := requireSessionMode(session, ModeAuto, "start_session_checkpoint"); err != nil {
		return PlanLifecycleResult{}, err
	}
	if active, ok, err := s.sessions.GetActivePlan(session.ID); err != nil {
		return PlanLifecycleResult{}, err
	} else if ok {
		return PlanLifecycleResult{}, fmt.Errorf("start_session_checkpoint requires no active plan; active plan %q already exists, use request_followup_checkpoint for one ordered checkpoint, amend_plan for future changes, or request_new_plan with plan_id for whole-plan replacement", active.ID)
	}
	request := strings.TrimSpace(input.ChangeRequest)
	if request == "" {
		return PlanLifecycleResult{}, errors.New("start_session_checkpoint requires change_request")
	}
	checkpointID := strings.TrimSpace(input.CheckpointID)
	if checkpointID == "" {
		checkpointID = "cp-1"
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = fmt.Sprintf("Session checkpoint: %s", truncatePlanLifecycleTitle(request, 72))
	}
	tasks := trimStringSlice(input.Tasks)
	if len(tasks) == 0 {
		tasks = []string{request}
	}
	doc := &pebblestore.SessionPlanDocument{
		Title:  title,
		Status: "approved",
		Info: pebblestore.SessionPlanInfo{
			Goal:               request,
			Scope:              "Auto-mode single session checkpoint created from a straightforward user request.",
			ValidationStrategy: "Use the narrowest validation that directly covers this checkpoint; report validation actually run in the terminal checkpoint outcome.",
		},
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
		ExecutionOrigin: PlanExecutionOriginAutoSession,
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID:                 checkpointID,
			Title:              title,
			Status:             PlanCheckpointStatusPending,
			Objective:          request,
			Tasks:              tasks,
			AcceptanceCriteria: trimStringSlice(input.AcceptanceCriteria),
			Artifacts:          trimPlanArtifacts(input.Artifacts),
			Notes:              buildCheckpointHandoffNotes(request, input.Notes),
			SourceMessageID:    strings.TrimSpace(input.SourceMessageID),
			Order:              1,
		}},
		ActiveCheckpointID: checkpointID,
	}
	if err := ValidateExecutablePlanDocument(doc); err != nil {
		return PlanLifecycleResult{}, err
	}
	if err := requireCheckpointRunnable(doc, checkpointID); err != nil {
		return PlanLifecycleResult{}, err
	}
	planText := renderSessionCheckpointPlanText(title, request, tasks, doc.Checkpoints[0].AcceptanceCriteria)
	if strings.TrimSpace(input.RunID) == "" {
		saved, event, err := s.sessions.SavePlanWithMetadata(session.ID, "", title, planText, "approved", "approved", true, PlanSaveMetadata{UpdateSummary: "Created auto-mode session checkpoint", UpdateScope: checkpointID, UpdateKind: "start_session_checkpoint", RevisionKind: PlanRevisionKindExecution, Checkpoint: true, Document: doc})
		if err != nil {
			return PlanLifecycleResult{}, err
		}
		return PlanLifecycleResult{Session: session, Plan: saved, PlanEvent: event, Summary: SummarizePlanExecution(saved.Document), CheckpointID: checkpointID, Action: "start_session_checkpoint", Message: "Created session checkpoint and queued fresh-context checkpoint start"}, nil
	}
	startedAt := input.StartedAt
	if startedAt <= 0 {
		startedAt = time.Now().UnixMilli()
	}
	decision, err := ApplyPlanCheckpointStart(doc, PlanCheckpointStartOptions{CheckpointID: checkpointID, AttemptID: input.AttemptID, RunID: input.RunID, SessionID: input.RunSessionID, ParentSessionID: firstNonBlank(input.ParentSessionID, session.ID), StartedAt: startedAt})
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	saved, event, err := s.sessions.SavePlanWithMetadata(session.ID, "", title, planText, "approved", "approved", true, PlanSaveMetadata{UpdateSummary: "Started auto-mode session checkpoint", UpdateScope: checkpointID, UpdateKind: "start_session_checkpoint", RevisionKind: PlanRevisionKindExecution, Checkpoint: true, Document: doc})
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	return PlanLifecycleResult{Session: session, Plan: saved, PlanEvent: event, Summary: SummarizePlanExecution(saved.Document), CheckpointID: decision.CheckpointID, AttemptID: decision.AttemptID, Action: "start_session_checkpoint", Message: "Created session checkpoint and started it with the current run"}, nil
}

func renderSessionCheckpointPlanText(title, request string, tasks, criteria []string) string {
	var b strings.Builder
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Session checkpoint"
	}
	b.WriteString("# ")
	b.WriteString(title)
	request = strings.TrimSpace(request)
	if request != "" {
		b.WriteString("\n\n## Request\n\n")
		b.WriteString(request)
	}
	if len(tasks) > 0 {
		b.WriteString("\n\n## Tasks\n")
		for _, task := range trimStringSlice(tasks) {
			b.WriteString("\n- [ ] ")
			b.WriteString(task)
		}
	}
	if len(criteria) > 0 {
		b.WriteString("\n\n## Acceptance Criteria\n")
		for _, criterion := range trimStringSlice(criteria) {
			b.WriteString("\n- ")
			b.WriteString(criterion)
		}
	}
	return strings.TrimSpace(b.String())
}

func buildCheckpointHandoffNotes(changeRequest, notes string) string {
	changeRequest = strings.TrimSpace(changeRequest)
	notes = strings.TrimSpace(notes)
	sections := make([]string, 0, 2)
	if changeRequest != "" {
		sections = append(sections, "Current user request / change_request:\n"+changeRequest)
	}
	if notes != "" {
		sections = append(sections, "Handoff notes:\n"+notes)
	}
	return strings.Join(sections, "\n\n")
}

func (s *PlanLifecycleService) AmendPlan(input PlanLifecycleAmendmentInput) (PlanLifecycleResult, error) {
	state, err := s.loadPlanForLifecycle(input.SessionID, input.PlanID, "amend_plan")
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	currentRevision := state.plan.Version
	if currentRevision <= 0 {
		currentRevision = 1
	}
	if input.BaseRevision <= 0 && !input.OverrideStale {
		return PlanLifecycleResult{}, fmt.Errorf("amend_plan requires base_revision (current revision is %d), or override_stale=true to intentionally amend without a current base revision", currentRevision)
	}
	if input.BaseRevision > 0 && currentRevision > 0 && input.BaseRevision != currentRevision && !input.OverrideStale {
		return PlanLifecycleResult{}, fmt.Errorf("amend_plan base_revision %d is stale; current revision is %d", input.BaseRevision, currentRevision)
	}
	updateSummary := strings.TrimSpace(input.UpdateSummary)
	if updateSummary == "" {
		return PlanLifecycleResult{}, errors.New("amend_plan requires update_summary")
	}
	if !input.AmendFutureCheckpoints && strings.TrimSpace(input.ReplaceFromCheckpointID) == "" {
		return PlanLifecycleResult{}, errors.New("amend_plan requires amend_future_checkpoints=true or replace_from_checkpoint_id")
	}
	proposed := clonePlanLifecycleDocument(input.Document)
	if proposed == nil {
		return PlanLifecycleResult{}, errors.New("amend_plan requires document")
	}
	title := strings.TrimSpace(firstNonBlank(input.Title, proposed.Title, state.plan.Title))
	if title == "" {
		title = "Plan"
	}
	proposed.ID = state.plan.ID
	proposed.Title = title
	proposed.Status = strings.TrimSpace(firstNonBlank(state.doc.Status, state.plan.Status))
	preparePlanAmendmentProposedDocumentForNormalize(proposed)
	proposed, err = NormalizePlanDocumentForSave(state.plan.ID, title, proposed, state.doc)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	doc, updateScope, err := applyPlanFutureAmendment(state.doc, proposed, input)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	doc.ID = state.plan.ID
	doc.Title = title
	doc.Status = strings.TrimSpace(firstNonBlank(state.doc.Status, state.plan.Status))
	if err := ValidateExecutablePlanDocument(doc); err != nil {
		return PlanLifecycleResult{}, err
	}
	planText := strings.TrimSpace(input.Plan)
	if planText == "" {
		planText = strings.TrimSpace(firstNonBlank(proposed.DisplayText, proposed.RenderedText, state.plan.Plan))
	}
	if planText == "" {
		planText = "# " + title
	}
	saved, event, err := s.sessions.SavePlanWithMetadata(state.session.ID, state.plan.ID, title, planText, state.plan.Status, state.plan.ApprovalState, true, PlanSaveMetadata{UpdateSummary: updateSummary, UpdateScope: updateScope, UpdateKind: "plan_amendment", RevisionKind: PlanRevisionKindDefinition, Checkpoint: false, Document: doc})
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	return PlanLifecycleResult{Session: state.session, Plan: saved, PlanEvent: event, Summary: SummarizePlanExecution(saved.Document), Action: "amend_plan", Message: updateSummary}, nil
}

func (s *PlanLifecycleService) RestorePlanRevision(input PlanLifecycleRevisionRestoreInput) (PlanLifecycleResult, error) {
	if err := s.requireConfigured(); err != nil {
		return PlanLifecycleResult{}, err
	}
	session, err := s.requireSession(input.SessionID)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	planID := strings.TrimSpace(input.PlanID)
	var current pebblestore.SessionPlanSnapshot
	var ok bool
	if planID == "" || strings.EqualFold(planID, "active") {
		current, ok, err = s.sessions.GetActivePlan(session.ID)
		if err != nil {
			return PlanLifecycleResult{}, err
		}
		if !ok {
			return PlanLifecycleResult{}, errors.New("restore_revision requires an active plan or plan_id")
		}
		planID = strings.TrimSpace(current.ID)
	} else {
		current, ok, err = s.sessions.GetPlan(session.ID, planID)
		if err != nil {
			return PlanLifecycleResult{}, err
		}
		if !ok {
			return PlanLifecycleResult{}, fmt.Errorf("plan %q not found", planID)
		}
	}
	if err := s.requireRestoreRevisionSafe(session.ID, current); err != nil {
		return PlanLifecycleResult{}, err
	}
	revision, ok, err := s.sessions.GetPlanRevision(session.ID, planID, input.Version)
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	if !ok {
		return PlanLifecycleResult{}, fmt.Errorf("plan %q revision version %d was not found", planID, input.Version)
	}
	doc := clonePlanLifecycleDocument(revision.Document)
	title := strings.TrimSpace(firstNonBlank(revision.Title, current.Title, planLifecycleDocumentTitle(doc), "Plan"))
	planText := strings.TrimSpace(revision.Plan)
	if planText == "" && doc != nil {
		planText = strings.TrimSpace(firstNonBlank(doc.DisplayText, doc.RenderedText))
	}
	if planText == "" {
		planText = "# " + title
	}
	status := strings.ToLower(strings.TrimSpace(firstNonBlank(revision.Status, current.Status, "approved")))
	approvalState := strings.ToLower(strings.TrimSpace(firstNonBlank(revision.ApprovalState, current.ApprovalState, "approved")))
	restart := input.Restart || input.Start || strings.TrimSpace(input.RunID) != ""
	if restart {
		status = "approved"
		approvalState = "approved"
	}
	checkpointID := strings.TrimSpace(input.CheckpointID)
	attemptID := strings.TrimSpace(input.AttemptID)
	if doc != nil {
		doc.ID = planID
		doc.Title = title
		doc.Status = status
		checkpointID, err = preparePlanRevisionRestoreDocument(doc, input)
		if err != nil {
			return PlanLifecycleResult{}, err
		}
		if input.Start {
			if checkpointID == "" {
				return PlanLifecycleResult{}, errors.New("restart_from_revision requires a runnable checkpoint for checkpointed execution")
			}
			if err := requireCheckpointRunnable(doc, checkpointID); err != nil {
				return PlanLifecycleResult{}, err
			}
			startedAt := input.StartedAt
			if startedAt <= 0 {
				startedAt = time.Now().UnixMilli()
			}
			decision, err := ApplyPlanCheckpointStart(doc, PlanCheckpointStartOptions{CheckpointID: checkpointID, PlanID: planID, AttemptID: attemptID, RunID: input.RunID, SessionID: input.RunSessionID, ParentSessionID: firstNonBlank(input.ParentSessionID, session.ID), StartedAt: startedAt})
			if err != nil {
				return PlanLifecycleResult{}, err
			}
			checkpointID = decision.CheckpointID
			attemptID = decision.AttemptID
		}
	} else if input.Start {
		return PlanLifecycleResult{}, errors.New("restart_from_revision requires a structured plan document")
	}
	updateKind := "restore_revision"
	updateSummary := fmt.Sprintf("Restored plan revision v%d", revision.Version)
	if restart {
		updateKind = "restart_from_revision"
		updateSummary = fmt.Sprintf("Restored plan revision v%d and prepared fresh-context checkpoint start", revision.Version)
	}
	if input.SkipPrior {
		updateKind = "jump_to_checkpoint"
		if checkpointID != "" {
			updateSummary = fmt.Sprintf("Restored plan revision v%d and jumped to checkpoint %s; prior incomplete checkpoints were recorded as skipped", revision.Version, checkpointID)
		} else {
			updateSummary = fmt.Sprintf("Restored plan revision v%d with explicit skip-prior jump semantics", revision.Version)
		}
	}
	updateScope := fmt.Sprintf("revision:%d", revision.Version)
	if checkpointID != "" {
		updateScope = checkpointID
	}
	saved, event, err := s.sessions.SavePlanWithMetadata(session.ID, planID, title, planText, status, approvalState, true, PlanSaveMetadata{UpdateSummary: updateSummary, UpdateScope: updateScope, UpdateKind: updateKind, RevisionKind: PlanRevisionKindDefinition, RestoredFromVersion: revision.Version, Checkpoint: false, Document: doc})
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	return PlanLifecycleResult{Session: session, Plan: saved, PlanEvent: event, Summary: SummarizePlanExecution(saved.Document), CheckpointID: checkpointID, AttemptID: attemptID, Action: updateKind, Message: updateSummary}, nil
}

func (s *PlanLifecycleService) RequestNewPlan(input PlanLifecycleProposalInput) (PlanLifecycleResult, error) {
	if err := s.requireConfigured(); err != nil {
		return PlanLifecycleResult{}, err
	}
	session, err := s.requireSession(input.SessionID)
	if err != nil {
		return PlanLifecycleResult{}, err
	}

	planID := strings.TrimSpace(input.PlanID)
	var target pebblestore.SessionPlanSnapshot
	replacing := false
	if planID != "" {
		var ok bool
		if strings.EqualFold(planID, "active") {
			target, ok, err = s.sessions.GetActivePlan(session.ID)
			if err != nil {
				return PlanLifecycleResult{}, err
			}
			if !ok {
				return PlanLifecycleResult{}, errors.New("request_new_plan replacement requires an active plan")
			}
			planID = strings.TrimSpace(target.ID)
		} else {
			target, ok, err = s.sessions.GetPlan(session.ID, planID)
			if err != nil {
				return PlanLifecycleResult{}, err
			}
			if !ok {
				return PlanLifecycleResult{}, fmt.Errorf("request_new_plan replacement target plan %q was not found", planID)
			}
		}
		replacing = true
	}
	if !input.ApprovalConfirmed && !replacing && NormalizeMode(session.Mode) == ModeAuto {
		if active, ok, err := s.sessions.GetActivePlan(session.ID); err != nil {
			return PlanLifecycleResult{}, err
		} else if !ok || strings.TrimSpace(active.ID) == "" {
			return PlanLifecycleResult{}, errors.New("request_new_plan in auto mode with no active plan requires user approval; invoke plan_manage request_new_plan with a structured document through the approval flow, or use start_session_checkpoint for a single bounded checkpoint")
		}
	}

	doc := clonePlanLifecycleDocument(input.Document)
	title := strings.TrimSpace(firstNonBlank(input.Title, planLifecycleDocumentTitle(doc), target.Title, "New plan proposal"))
	planText := strings.TrimSpace(input.Plan)
	if planText == "" && doc != nil {
		planText = strings.TrimSpace(firstNonBlank(doc.DisplayText, doc.RenderedText))
	}
	if planText == "" {
		planText = "# " + title
	}
	if input.ApprovalConfirmed && doc == nil {
		return PlanLifecycleResult{}, errors.New("request_new_plan approval requires a structured document")
	}
	if doc != nil {
		doc.Title = title
		if input.ApprovalConfirmed {
			if replacing {
				doc.ID = planID
			} else {
				doc.ID = ""
			}
			doc.Status = "approved"
			granularity := strings.TrimSpace(input.ExecutionGranularity)
			if granularity == "" {
				granularity = PlanAcceptanceGranularityCheckpointed
			}
			continuation := strings.TrimSpace(input.ContinuationPolicy)
			if input.ContinueAutomatically != nil {
				if *input.ContinueAutomatically {
					continuation = PlanAcceptanceContinuationAutomatic
				} else {
					continuation = PlanAcceptanceContinuationReviewEachCheckpoint
				}
			} else if continuation == "" {
				continuation = PlanAcceptanceContinuationAutomatic
			}
			if _, err := ApplyPlanAcceptanceExecutionPolicy(doc, PlanAcceptanceExecutionOptions{ExecutionGranularity: granularity, ContinuationPolicy: continuation}); err != nil {
				return PlanLifecycleResult{}, err
			}
		} else {
			doc.ID = ""
			doc.Status = "pending_approval"
		}
	}

	if err := ValidateExecutablePlanDocument(doc); err != nil {
		return PlanLifecycleResult{}, err
	}

	if input.ApprovalConfirmed {
		approvedPlanID := ""
		message := "new plan approved and activated"
		updateSummary := "New plan approved and activated"
		if replacing {
			approvedPlanID = planID
			message = "replacement plan approved and activated"
			updateSummary = "Replacement plan approved and activated"
		}
		saved, event, err := s.sessions.SavePlanWithMetadata(session.ID, approvedPlanID, title, planText, "approved", "approved", true, PlanSaveMetadata{UpdateSummary: firstNonBlank(strings.TrimSpace(input.Reason), updateSummary), UpdateScope: "plan", UpdateKind: "request_new_plan", RevisionKind: PlanRevisionKindDefinition, Document: doc})
		if err != nil {
			return PlanLifecycleResult{}, err
		}
		return PlanLifecycleResult{Session: session, Plan: saved, PlanEvent: event, Summary: SummarizePlanExecution(saved.Document), Action: "request_new_plan", Message: message}, nil
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
	return s.saveLifecyclePlan(state, "", "set_followup_checkpoint_policy", firstNonBlank(strings.TrimSpace(input.Reason), "Updated session checkpoint policy"))
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

func (s *PlanLifecycleService) ResumeCheckpointRun(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	return s.resetAndStartCheckpoint(input, false, "resume_checkpoint")
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

// ReconcileCancelledRun durably terminalizes the active checkpoint attempt only
// when every persisted ownership field matches the cancelled run. Non-plan runs
// and stale cancellation requests are safe no-ops.
func (s *PlanLifecycleService) ReconcileCancelledRun(input PlanLifecycleExecutionInput) (PlanLifecycleResult, bool, error) {
	if err := s.requireConfigured(); err != nil {
		return PlanLifecycleResult{}, false, err
	}
	session, err := s.requireSession(input.SessionID)
	if err != nil {
		return PlanLifecycleResult{}, false, err
	}
	plan, ok, err := s.sessions.GetActivePlan(session.ID)
	if err != nil {
		return PlanLifecycleResult{}, false, err
	}
	if !ok || plan.Document == nil {
		return PlanLifecycleResult{}, false, nil
	}
	if planID := strings.TrimSpace(input.PlanID); planID != "" && strings.TrimSpace(plan.ID) != planID {
		return PlanLifecycleResult{}, false, nil
	}
	doc := clonePlanLifecycleDocument(plan.Document)
	if doc == nil {
		return PlanLifecycleResult{}, false, nil
	}
	cancelledAt := input.ReviewedAt
	if cancelledAt <= 0 {
		cancelledAt = time.Now().UnixMilli()
	}
	decision, err := ApplyPlanCheckpointCancellation(doc, PlanCheckpointCancellationOptions{
		PlanID:          plan.ID,
		CheckpointID:    input.CheckpointID,
		AttemptID:       input.AttemptID,
		RunID:           input.RunID,
		SessionID:       input.RunSessionID,
		ParentSessionID: input.ParentSessionID,
		Reason:          input.Notes,
		CancelledAt:     cancelledAt,
	})
	if err != nil {
		return PlanLifecycleResult{}, false, err
	}
	if !decision.Changed {
		return PlanLifecycleResult{}, false, nil
	}
	state := planLifecycleState{session: session, plan: plan, doc: doc}
	result, err := s.saveLifecyclePlan(state, decision.CheckpointID, "run_paused", "Reconciled user-cancelled run as a paused checkpoint attempt")
	if err != nil {
		return PlanLifecycleResult{}, false, err
	}
	result.AttemptID = decision.AttemptID
	return result, true, nil
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
	message := "Restarted checkpoint from zero and prepared fresh-context checkpoint start"
	if strings.TrimSpace(input.ReplacementRequest) != "" {
		if err := replaceRestartedCheckpointRequirements(state.doc, checkpointID, input); err != nil {
			return PlanLifecycleResult{}, err
		}
		message = "Replaced checkpoint requirements and prepared fresh-context checkpoint restart"
	}
	return s.saveLifecyclePlan(state, checkpointID, "restart_checkpoint", message)
}

func replaceRestartedCheckpointRequirements(doc *pebblestore.SessionPlanDocument, checkpointID string, input PlanLifecycleExecutionInput) error {
	request := strings.TrimSpace(input.ReplacementRequest)
	if request == "" {
		return nil
	}
	criteria := trimStringSlice(input.ReplacementCriteria)
	if len(criteria) == 0 {
		return errors.New("restart_checkpoint replacement requires acceptance_criteria")
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return fmt.Errorf("plan document checkpoint %q was not found", checkpointID)
	}
	checkpoint := &doc.Checkpoints[idx]
	title := strings.TrimSpace(input.ReplacementTitle)
	if title == "" {
		return errors.New("restart_checkpoint replacement requires checkpoint_title")
	}
	tasks := trimStringSlice(input.ReplacementTasks)
	if len(tasks) == 0 {
		return errors.New("restart_checkpoint replacement requires tasks")
	}
	checkpoint.Title = title
	checkpoint.Objective = request
	checkpoint.Tasks = tasks
	checkpoint.Subtasks = nil
	checkpoint.ActiveSubtaskID = ""
	normalizePlanCheckpointSubtasks(checkpoint)
	checkpoint.AcceptanceCriteria = criteria
	checkpoint.Artifacts = trimPlanArtifacts(input.ReplacementArtifacts)
	checkpoint.Notes = buildCheckpointHandoffNotes(request, input.ReplacementNotes)
	checkpoint.SourceMessageID = strings.TrimSpace(input.ReplacementSourceID)
	return ValidatePlanDocument(doc)
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

func (s *PlanLifecycleService) ResolveBlockedCheckpoint(input PlanLifecycleExecutionInput) (PlanLifecycleResult, error) {
	state, err := s.loadApprovedPlan(input.SessionID, input.PlanID, "resolve blocked checkpoint")
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	checkpointID := strings.TrimSpace(firstNonBlank(input.CheckpointID, state.doc.ActiveCheckpointID))
	resolvedAt := input.ReviewedAt
	if resolvedAt <= 0 {
		resolvedAt = time.Now().UnixMilli()
	}
	summary, err := ApplyPlanCheckpointBlockResolution(state.doc, PlanCheckpointBlockResolutionOptions{CheckpointID: checkpointID, Result: input.Result, Notes: input.Notes, ResolvedAt: resolvedAt})
	if err != nil {
		return PlanLifecycleResult{}, err
	}
	if input.StartNext && summary.NextCheckpointID != "" && !summary.ReviewRequired && !summary.Blocked && !summary.Failed && !summary.PlanComplete {
		nextCheckpointID := summary.NextCheckpointID
		if strings.TrimSpace(input.RunID) == "" {
			input.RunID = fmt.Sprintf("plan-resolve-run:%s:%d", nextCheckpointID, resolvedAt)
		}
		if strings.TrimSpace(input.RunSessionID) == "" {
			input.RunSessionID = state.session.ID
		}
		if strings.TrimSpace(input.ParentSessionID) == "" {
			input.ParentSessionID = state.session.ID
		}
		input.StartedAt = resolvedAt
		decision, err := ApplyPlanCheckpointStart(state.doc, PlanCheckpointStartOptions{CheckpointID: nextCheckpointID, PlanID: state.plan.ID, AttemptID: input.AttemptID, RunID: input.RunID, SessionID: input.RunSessionID, ParentSessionID: input.ParentSessionID, StartedAt: input.StartedAt})
		if err != nil {
			return PlanLifecycleResult{}, err
		}
		result, err := s.saveLifecyclePlan(state, nextCheckpointID, "resolve_blocked_checkpoint", "Resolved blocked checkpoint and prepared fresh-context checkpoint start")
		if err != nil {
			return PlanLifecycleResult{}, err
		}
		result.CheckpointID = decision.CheckpointID
		result.AttemptID = decision.AttemptID
		return result, nil
	}
	return s.saveLifecyclePlan(state, checkpointID, "resolve_blocked_checkpoint", "Resolved blocked checkpoint without restart")
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
	if err := ValidateExecutablePlanDocument(state.doc); err != nil {
		return PlanLifecycleResult{}, err
	}
	if _, err := ApplyPlanAcceptanceExecutionPolicy(state.doc, options); err != nil {
		return PlanLifecycleResult{}, err
	}
	if err := ValidateExecutablePlanDocument(state.doc); err != nil {
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
	if err := ValidateExecutablePlanDocument(state.doc); err != nil {
		return PlanLifecycleResult{}, err
	}
	if _, err := ApplyPlanAcceptanceExecutionPolicy(state.doc, options); err != nil {
		return PlanLifecycleResult{}, err
	}
	if err := ValidateExecutablePlanDocument(state.doc); err != nil {
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
	summary := "Prepared fresh-context checkpoint start"
	if action == "restart_checkpoint" && strings.TrimSpace(input.ReplacementRequest) != "" {
		if err := replaceRestartedCheckpointRequirements(state.doc, checkpointID, input); err != nil {
			return PlanLifecycleResult{}, err
		}
		summary = "Replaced checkpoint requirements and prepared fresh-context checkpoint restart"
	}
	return s.applyCheckpointStartAndSave(state, input, checkpointID, action, summary, state.plan.Status, state.plan.ApprovalState)
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
	if err := ValidateExecutablePlanDocument(state.doc); err != nil {
		return PlanLifecycleResult{}, err
	}
	if err := requireCheckpointRunnable(state.doc, checkpointID); err != nil {
		return PlanLifecycleResult{}, err
	}
	return s.applyCheckpointStartAndSave(state, input, checkpointID, action, "Prepared fresh-context checkpoint start", state.plan.Status, state.plan.ApprovalState)
}

func (s *PlanLifecycleService) applyCheckpointStartAndSave(state planLifecycleState, input PlanLifecycleExecutionInput, checkpointID, action, summary, status, approvalState string) (PlanLifecycleResult, error) {
	if err := ValidateExecutablePlanDocument(state.doc); err != nil {
		return PlanLifecycleResult{}, err
	}
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

type followupCheckpointInsertionPoint struct {
	Index             int
	CheckpointID      string
	StopReason        string
	ResolveCheckpoint bool
}

func followupCheckpointInsertionPointForDocument(doc *pebblestore.SessionPlanDocument) followupCheckpointInsertionPoint {
	if doc == nil {
		return followupCheckpointInsertionPoint{}
	}
	for i := range doc.Checkpoints {
		checkpoint := doc.Checkpoints[i]
		id := strings.TrimSpace(checkpoint.ID)
		status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
		if planCheckpointReviewPending(doc.ExecutionPolicy, checkpoint, i < len(doc.Checkpoints)-1) || status == PlanCheckpointStatusNeedsReview {
			return followupCheckpointInsertionPoint{Index: i + 1, CheckpointID: id, StopReason: PlanCheckpointStatusNeedsReview, ResolveCheckpoint: true}
		}
		switch status {
		case PlanCheckpointStatusPending:
			return followupCheckpointInsertionPoint{Index: i, CheckpointID: id}
		case PlanCheckpointStatusInProgress:
			return followupCheckpointInsertionPoint{Index: i, CheckpointID: id, ResolveCheckpoint: true}
		case PlanCheckpointStatusPaused, PlanCheckpointStatusBlocked:
			return followupCheckpointInsertionPoint{Index: i + 1, CheckpointID: id, StopReason: status, ResolveCheckpoint: true}
		case PlanCheckpointStatusFailed:
			return followupCheckpointInsertionPoint{Index: i, CheckpointID: id, StopReason: status, ResolveCheckpoint: false}
		}
	}
	if planFinalReviewPending(doc) {
		checkpointID := finalPlanReviewCheckpointID(doc)
		idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
		if idx < 0 {
			idx = len(doc.Checkpoints) - 1
		}
		return followupCheckpointInsertionPoint{Index: idx + 1, CheckpointID: strings.TrimSpace(checkpointID), StopReason: PlanCheckpointStatusNeedsReview, ResolveCheckpoint: true}
	}
	return followupCheckpointInsertionPoint{Index: len(doc.Checkpoints)}
}

func resolveFollowupInsertionPoint(doc *pebblestore.SessionPlanDocument, point followupCheckpointInsertionPoint, followupID string, resolvedAt int64) (string, error) {
	if doc == nil || !point.ResolveCheckpoint {
		return "", nil
	}
	checkpointID := strings.TrimSpace(point.CheckpointID)
	if checkpointID == "" || checkpointID == strings.TrimSpace(followupID) {
		return "", nil
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return "", nil
	}
	checkpoint := &doc.Checkpoints[idx]
	status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
	if status == PlanCheckpointStatusInProgress {
		return resolveCurrentInProgressCheckpointForFollowup(doc, followupID, resolvedAt), nil
	}
	if status == PlanCheckpointStatusPaused {
		checkpoint.Status = PlanCheckpointStatusCompleted
		checkpoint.Result = "superseded_by_followup"
		checkpoint.Report = firstNonBlank(strings.TrimSpace(checkpoint.Report), fmt.Sprintf("Paused checkpoint superseded by session checkpoint %q.", followupID))
		if resolvedAt > 0 && checkpoint.CompletedAt == 0 {
			checkpoint.CompletedAt = resolvedAt
		}
		if checkpoint.Review == nil {
			checkpoint.Review = &pebblestore.SessionPlanCheckpointReview{}
		}
		checkpoint.Review.Status = PlanCheckpointReviewStatusApproved
		checkpoint.Review.Result = "superseded_by_followup"
		checkpoint.Review.Notes = firstNonBlank(strings.TrimSpace(checkpoint.Review.Notes), fmt.Sprintf("Paused checkpoint was closed because session checkpoint %q superseded it.", followupID))
		if resolvedAt > 0 && checkpoint.Review.ReviewedAt == 0 {
			checkpoint.Review.ReviewedAt = resolvedAt
		}
		if doc.ExecutionState == nil {
			doc.ExecutionState = &pebblestore.SessionPlanExecutionState{}
		}
		doc.ExecutionState.LastCheckpointID = checkpointID
		doc.ExecutionState.LastAttemptID = strings.TrimSpace(checkpoint.AttemptID)
		doc.ExecutionState.LastOutcome = PlanCheckpointStatusCompleted
		doc.ExecutionState.UpdatedAt = resolvedAt
		return checkpointID, nil
	}
	if status == PlanCheckpointStatusBlocked {
		if _, err := ApplyPlanCheckpointBlockResolution(doc, PlanCheckpointBlockResolutionOptions{
			CheckpointID: checkpointID,
			Result:       "superseded_by_followup",
			Notes:        fmt.Sprintf("Blocked checkpoint superseded by session checkpoint %q.", followupID),
			ResolvedAt:   resolvedAt,
		}); err != nil {
			return "", fmt.Errorf("resolve blocked checkpoint for follow-up: %w", err)
		}
		return checkpointID, nil
	}
	if status != PlanCheckpointStatusCompleted && status != PlanCheckpointStatusNeedsReview {
		return "", nil
	}
	checkpoint.Status = PlanCheckpointStatusCompleted
	checkpoint.Result = "superseded_by_followup"
	checkpoint.Report = firstNonBlank(strings.TrimSpace(checkpoint.Report), fmt.Sprintf("Superseded by session checkpoint %q before plan execution continued.", followupID))
	if resolvedAt > 0 && checkpoint.CompletedAt == 0 {
		checkpoint.CompletedAt = resolvedAt
	}
	if checkpoint.Review == nil {
		checkpoint.Review = &pebblestore.SessionPlanCheckpointReview{}
	}
	checkpoint.Review.Status = PlanCheckpointReviewStatusApproved
	checkpoint.Review.Result = "superseded_by_followup"
	checkpoint.Review.Notes = firstNonBlank(strings.TrimSpace(checkpoint.Review.Notes), fmt.Sprintf("Review was closed because session checkpoint %q was inserted before execution continued.", followupID))
	if resolvedAt > 0 && checkpoint.Review.ReviewedAt == 0 {
		checkpoint.Review.ReviewedAt = resolvedAt
	}
	if doc.ExecutionState == nil {
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{}
	}
	doc.ExecutionState.LastCheckpointID = checkpointID
	doc.ExecutionState.LastOutcome = PlanCheckpointStatusCompleted
	doc.ExecutionState.UpdatedAt = resolvedAt
	return checkpointID, nil
}

func insertPlanCheckpointAt(checkpoints []pebblestore.SessionPlanCheckpoint, idx int, checkpoint pebblestore.SessionPlanCheckpoint) []pebblestore.SessionPlanCheckpoint {
	if idx < 0 || idx >= len(checkpoints) {
		return append(checkpoints, checkpoint)
	}
	checkpoints = append(checkpoints, pebblestore.SessionPlanCheckpoint{})
	copy(checkpoints[idx+1:], checkpoints[idx:])
	checkpoints[idx] = checkpoint
	return checkpoints
}

func resolveCurrentInProgressCheckpointForFollowup(doc *pebblestore.SessionPlanDocument, followupID string, resolvedAt int64) string {
	if doc == nil {
		return ""
	}
	followupID = strings.TrimSpace(followupID)
	activeID := strings.TrimSpace(doc.ActiveCheckpointID)
	if activeID == "" || activeID == followupID {
		return ""
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, activeID)
	if idx < 0 {
		return ""
	}
	checkpoint := &doc.Checkpoints[idx]
	if normalizePlanCheckpointStatusForSave(checkpoint.Status) != PlanCheckpointStatusInProgress {
		return ""
	}
	checkpoint.Status = PlanCheckpointStatusNeedsReview
	checkpoint.Result = firstNonBlank(strings.TrimSpace(checkpoint.Result), "superseded_by_followup")
	checkpoint.Report = firstNonBlank(strings.TrimSpace(checkpoint.Report), fmt.Sprintf("Superseded by session checkpoint %q before active checkpoint changed.", followupID))
	if resolvedAt > 0 {
		checkpoint.CompletedAt = resolvedAt
	}
	if checkpoint.Review == nil {
		checkpoint.Review = &pebblestore.SessionPlanCheckpointReview{}
	}
	checkpoint.Review.Status = PlanCheckpointReviewStatusPending
	checkpoint.Review.Notes = firstNonBlank(strings.TrimSpace(checkpoint.Review.Notes), fmt.Sprintf("Checkpoint was superseded by session checkpoint %q.", followupID))
	attemptID := strings.TrimSpace(checkpoint.AttemptID)
	if attemptID == "" && doc.ExecutionState != nil {
		attemptID = strings.TrimSpace(doc.ExecutionState.ActiveAttemptID)
	}
	if attemptID != "" {
		runID := strings.TrimSpace(checkpoint.RunID)
		runSessionID := strings.TrimSpace(checkpoint.SessionID)
		parentSessionID := ""
		if doc.ExecutionState != nil {
			parentSessionID = strings.TrimSpace(doc.ExecutionState.ParentSessionID)
		}
		upsertPlanCheckpointAttempt(checkpoint, pebblestore.SessionPlanCheckpointAttempt{
			ID:              attemptID,
			CheckpointID:    activeID,
			Status:          PlanCheckpointStatusNeedsReview,
			Outcome:         PlanCheckpointStatusNeedsReview,
			RunID:           runID,
			SessionID:       runSessionID,
			ParentSessionID: parentSessionID,
			StartedAt:       checkpoint.StartedAt,
			CompletedAt:     resolvedAt,
			Report:          checkpoint.Report,
			Result:          checkpoint.Result,
			ChangedFiles:    cloneStringSlice(checkpoint.ChangedFiles),
			Validation:      cloneStringSlice(checkpoint.Validation),
		})
		checkpoint.AttemptID = attemptID
	}
	if doc.ExecutionState == nil {
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{}
	}
	doc.ExecutionState.LastCheckpointID = activeID
	doc.ExecutionState.LastAttemptID = attemptID
	doc.ExecutionState.LastOutcome = PlanCheckpointStatusNeedsReview
	if resolvedAt > 0 {
		doc.ExecutionState.UpdatedAt = resolvedAt
	}
	return activeID
}

func (s *PlanLifecycleService) requireRestoreRevisionSafe(sessionID string, current pebblestore.SessionPlanSnapshot) error {
	if current.Document == nil || current.Document.ExecutionState == nil {
		return nil
	}
	if normalizePlanExecutionStateStatus(current.Document.ExecutionState.Status) != PlanExecutionStateInProgress {
		return nil
	}
	runID := strings.TrimSpace(current.Document.ExecutionState.CurrentRunID)
	if runID == "" {
		return errors.New("restore_revision requires the in-progress plan run to be paused or stopped before restoring a revision")
	}
	if runIntent, ok, err := s.sessions.GetSessionActiveRunIntent(sessionID); err != nil {
		return err
	} else if ok && strings.EqualFold(strings.TrimSpace(runIntent.RunID), runID) {
		switch runIntent.Status {
		case RunIntentPendingExecutor, RunIntentRunning:
			return fmt.Errorf("restore_revision requires active run %q to be paused or stopped before restoring a revision", runID)
		}
	}
	return errors.New("restore_revision requires the in-progress plan run to be paused or stopped before restoring a revision")
}

func preparePlanRevisionRestoreDocument(doc *pebblestore.SessionPlanDocument, input PlanLifecycleRevisionRestoreInput) (string, error) {
	if doc == nil {
		return "", nil
	}
	restart := input.Restart || input.Start || strings.TrimSpace(input.RunID) != "" || strings.TrimSpace(input.CheckpointID) != ""
	if input.ContinueAutomatically != nil {
		if *input.ContinueAutomatically {
			input.ContinuationPolicy = PlanAcceptanceContinuationAutomatic
		} else {
			input.ContinuationPolicy = PlanAcceptanceContinuationReviewEachCheckpoint
		}
	}
	if restart || strings.TrimSpace(input.ExecutionGranularity) != "" || strings.TrimSpace(input.ContinuationPolicy) != "" {
		if _, err := ApplyPlanAcceptanceExecutionPolicy(doc, PlanAcceptanceExecutionOptions{ExecutionGranularity: input.ExecutionGranularity, ContinuationPolicy: input.ContinuationPolicy}); err != nil {
			return "", err
		}
	} else {
		resetPlanExecutionRuntimeForAcceptance(doc)
		normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
		doc.ActiveCheckpointID = defaultActiveCheckpointID(doc.Checkpoints)
	}
	checkpointID := strings.TrimSpace(input.CheckpointID)
	if doc.ExecutionPolicy.Shape == PlanExecutionShapeCheckpointed {
		if checkpointID != "" {
			if err := resetPlanRevisionRestoreFromCheckpoint(doc, checkpointID, input.SkipPrior); err != nil {
				return "", err
			}
		} else {
			checkpointID = strings.TrimSpace(doc.ActiveCheckpointID)
			if checkpointID == "" {
				checkpointID = defaultActiveCheckpointID(doc.Checkpoints)
				doc.ActiveCheckpointID = checkpointID
			}
		}
	} else {
		checkpointID = strings.TrimSpace(doc.ActiveCheckpointID)
		if checkpointID == "" {
			checkpointID = defaultActiveCheckpointID(doc.Checkpoints)
			doc.ActiveCheckpointID = checkpointID
		}
	}
	normalizeCheckpointOrder(doc)
	if err := ValidatePlanDocument(doc); err != nil {
		return "", err
	}
	return checkpointID, nil
}

func resetPlanRevisionRestoreFromCheckpoint(doc *pebblestore.SessionPlanDocument, checkpointID string, skipPrior bool) error {
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return fmt.Errorf("plan document checkpoint %q was not found", checkpointID)
	}
	for i := idx; i < len(doc.Checkpoints); i++ {
		resetPlanCheckpointRuntimeForFreshStart(&doc.Checkpoints[i])
	}
	for i := 0; i < idx; i++ {
		status := normalizePlanCheckpointStatusForSave(doc.Checkpoints[i].Status)
		checkpointIDBeforeTarget := strings.TrimSpace(doc.Checkpoints[i].ID)
		needsSkip := status != PlanCheckpointStatusCompleted || planCheckpointReviewPending(doc.ExecutionPolicy, doc.Checkpoints[i], i < len(doc.Checkpoints)-1)
		if needsSkip && !skipPrior {
			return fmt.Errorf("restart_from_revision checkpoint %q requires skip_prior=true to jump over earlier checkpoint %q status %q", checkpointID, checkpointIDBeforeTarget, status)
		}
		if !needsSkip {
			continue
		}
		resetPlanCheckpointRuntimeForFreshStart(&doc.Checkpoints[i])
		doc.Checkpoints[i].Status = PlanCheckpointStatusCompleted
		if doc.Checkpoints[i].Review == nil {
			doc.Checkpoints[i].Review = &pebblestore.SessionPlanCheckpointReview{}
		}
		doc.Checkpoints[i].Review.Status = PlanCheckpointReviewStatusApproved
		doc.Checkpoints[i].Review.Result = "user_skipped_to_checkpoint"
		doc.Checkpoints[i].Review.Notes = strings.TrimSpace(firstNonBlank(doc.Checkpoints[i].Review.Notes, fmt.Sprintf("User explicitly skipped this checkpoint while jumping to %s.", checkpointID)))
	}
	doc.ActiveCheckpointID = checkpointID
	doc.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateIdle}
	return nil
}

func applyPlanFutureAmendment(current, proposed *pebblestore.SessionPlanDocument, input PlanLifecycleAmendmentInput) (*pebblestore.SessionPlanDocument, string, error) {
	if current == nil || proposed == nil {
		return nil, "", errors.New("amend_plan requires current and proposed structured plan documents")
	}
	doc := clonePlanLifecycleDocument(current)
	if doc == nil {
		return nil, "", errors.New("amend_plan requires current structured plan document")
	}
	replaceID := strings.TrimSpace(input.ReplaceFromCheckpointID)
	replaceIndex := -1
	if replaceID != "" {
		replaceIndex = findPlanCheckpointIndex(doc.Checkpoints, replaceID)
		if replaceIndex < 0 {
			return nil, "", fmt.Errorf("amend_plan replace_from_checkpoint_id %q was not found", replaceID)
		}
	} else if input.AmendFutureCheckpoints {
		replaceIndex = firstFutureAmendmentCheckpointIndex(doc)
		if replaceIndex < 0 {
			return applyPlanFutureAppend(doc, proposed)
		}
		replaceID = strings.TrimSpace(doc.Checkpoints[replaceIndex].ID)
	}
	if err := requirePlanAmendmentCanReplaceFrom(doc, replaceIndex, replaceID); err != nil {
		return nil, "", err
	}
	proposedIndex := findPlanCheckpointIndex(proposed.Checkpoints, replaceID)
	if proposedIndex < 0 {
		return nil, "", fmt.Errorf("amend_plan proposed document must include replace_from_checkpoint_id %q", replaceID)
	}
	future := clonePlanCheckpointSlice(proposed.Checkpoints[proposedIndex:])
	if len(future) == 0 {
		return nil, "", errors.New("amend_plan proposed document must include at least one replacement checkpoint")
	}
	for i := range future {
		resetPlanCheckpointRuntimeForFreshStart(&future[i])
		if strings.TrimSpace(future[i].Status) == "" {
			future[i].Status = PlanCheckpointStatusPending
		}
	}
	doc.Info = proposed.Info
	preserveCurrentRuntime := planAmendmentPreservesCurrentRuntime(doc, replaceIndex)
	doc.Checkpoints = append(doc.Checkpoints[:replaceIndex], future...)
	normalizeCheckpointOrder(doc)
	if !preserveCurrentRuntime {
		doc.ActiveCheckpointID = defaultActiveCheckpointID(doc.Checkpoints)
		if doc.ActiveCheckpointID == "" {
			doc.ActiveCheckpointID = replaceID
		}
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateIdle}
	}
	doc.ExecutionPolicy = proposed.ExecutionPolicy
	normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
	doc.RenderedText = strings.TrimSpace(proposed.RenderedText)
	doc.DisplayText = strings.TrimSpace(proposed.DisplayText)
	if err := ValidatePlanDocument(doc); err != nil {
		return nil, "", err
	}
	return doc, replaceID, nil
}

func applyPlanFutureAppend(current, proposed *pebblestore.SessionPlanDocument) (*pebblestore.SessionPlanDocument, string, error) {
	if current == nil || proposed == nil {
		return nil, "", errors.New("amend_plan append requires current and proposed structured plan documents")
	}
	if len(proposed.Checkpoints) == 0 {
		return nil, "", errors.New("amend_plan append requires at least one proposed checkpoint")
	}
	for _, checkpoint := range current.Checkpoints {
		status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
		switch status {
		case PlanCheckpointStatusCompleted:
			continue
		case PlanCheckpointStatusInProgress:
			if strings.TrimSpace(checkpoint.ID) == strings.TrimSpace(current.ActiveCheckpointID) {
				continue
			}
		}
		return nil, "", fmt.Errorf("amend_plan cannot append after checkpoint %q status %q", strings.TrimSpace(checkpoint.ID), status)
	}

	existingIDs := make(map[string]int, len(current.Checkpoints))
	for i, checkpoint := range current.Checkpoints {
		existingIDs[strings.TrimSpace(checkpoint.ID)] = i
	}
	future := make([]pebblestore.SessionPlanCheckpoint, 0, len(proposed.Checkpoints))
	lastExistingIndex := -1
	seenNew := false
	for _, checkpoint := range proposed.Checkpoints {
		checkpointID := strings.TrimSpace(checkpoint.ID)
		if checkpointID == "" {
			return nil, "", errors.New("amend_plan append checkpoint id is required")
		}
		if existingIndex, exists := existingIDs[checkpointID]; exists {
			if seenNew || existingIndex <= lastExistingIndex {
				return nil, "", fmt.Errorf("amend_plan append existing checkpoint %q must remain in its original prefix order", checkpointID)
			}
			lastExistingIndex = existingIndex
			continue
		}
		seenNew = true
		existingIDs[checkpointID] = len(current.Checkpoints) + len(future)
		future = append(future, checkpoint)
	}
	if len(future) == 0 {
		return nil, "", errors.New("amend_plan append requires at least one new checkpoint id")
	}
	future = clonePlanCheckpointSlice(future)
	for i := range future {
		resetPlanCheckpointRuntimeForFreshStart(&future[i])
		future[i].Status = PlanCheckpointStatusPending
	}

	doc := clonePlanLifecycleDocument(current)
	doc.Info = proposed.Info
	doc.Checkpoints = append(doc.Checkpoints, future...)
	normalizeCheckpointOrder(doc)
	doc.ExecutionPolicy = proposed.ExecutionPolicy
	normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
	doc.RenderedText = strings.TrimSpace(proposed.RenderedText)
	doc.DisplayText = strings.TrimSpace(proposed.DisplayText)
	if !planAmendmentCurrentRunActive(doc) {
		doc.ActiveCheckpointID = strings.TrimSpace(future[0].ID)
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateIdle}
	}
	if err := ValidatePlanDocument(doc); err != nil {
		return nil, "", err
	}
	return doc, strings.TrimSpace(future[0].ID), nil
}

func planAmendmentCurrentRunActive(doc *pebblestore.SessionPlanDocument) bool {
	if doc == nil || doc.ExecutionState == nil {
		return false
	}
	if normalizePlanExecutionStateStatus(doc.ExecutionState.Status) != PlanExecutionStateInProgress {
		return false
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, strings.TrimSpace(doc.ActiveCheckpointID))
	return idx >= 0 && normalizePlanCheckpointStatusForSave(doc.Checkpoints[idx].Status) == PlanCheckpointStatusInProgress
}

func preparePlanAmendmentProposedDocumentForNormalize(doc *pebblestore.SessionPlanDocument) {
	if doc == nil {
		return
	}
	// Amendment proposals replace only the future suffix. Preserve runtime state from
	// the current document instead of requiring callers to restate an earlier
	// active/review checkpoint in the proposed checkpoint slice.
	doc.ExecutionState = nil
	activeID := strings.TrimSpace(doc.ActiveCheckpointID)
	if activeID != "" && findPlanCheckpointIndex(doc.Checkpoints, activeID) < 0 {
		doc.ActiveCheckpointID = ""
	}
}

func firstFutureAmendmentCheckpointIndex(doc *pebblestore.SessionPlanDocument) int {
	if doc == nil {
		return -1
	}
	for i := range doc.Checkpoints {
		status := normalizePlanCheckpointStatusForSave(doc.Checkpoints[i].Status)
		switch status {
		case PlanCheckpointStatusPending:
			return i
		case PlanCheckpointStatusInProgress:
			if strings.TrimSpace(doc.Checkpoints[i].ID) != strings.TrimSpace(doc.ActiveCheckpointID) {
				return i
			}
		}
	}
	return -1
}

func requirePlanAmendmentCanReplaceFrom(doc *pebblestore.SessionPlanDocument, replaceIndex int, replaceID string) error {
	if doc == nil || replaceIndex < 0 || replaceIndex >= len(doc.Checkpoints) {
		return errors.New("amend_plan replacement checkpoint is required")
	}
	activeID := strings.TrimSpace(doc.ActiveCheckpointID)
	activeIndex := -1
	if activeID != "" {
		activeIndex = findPlanCheckpointIndex(doc.Checkpoints, activeID)
	}
	for i := 0; i < replaceIndex; i++ {
		checkpoint := doc.Checkpoints[i]
		status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
		checkpointID := strings.TrimSpace(checkpoint.ID)
		if i == activeIndex && status == PlanCheckpointStatusInProgress {
			continue
		}
		if status != PlanCheckpointStatusCompleted {
			return fmt.Errorf("amend_plan cannot replace from %q while earlier checkpoint %q status is %q", replaceID, checkpointID, status)
		}
		if planCheckpointReviewPending(doc.ExecutionPolicy, checkpoint, i < len(doc.Checkpoints)-1) && !planAmendmentPreservesWaitingReviewCheckpoint(doc, i, replaceIndex) {
			return fmt.Errorf("amend_plan cannot replace from %q while earlier checkpoint %q is waiting for review", replaceID, checkpointID)
		}
	}
	checkpoint := doc.Checkpoints[replaceIndex]
	status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
	if status == PlanCheckpointStatusCompleted || status == PlanCheckpointStatusNeedsReview || status == PlanCheckpointStatusBlocked || status == PlanCheckpointStatusFailed {
		return fmt.Errorf("amend_plan cannot replace checkpoint %q with protected status %q", replaceID, status)
	}
	if activeIndex >= replaceIndex && normalizePlanCheckpointStatusForSave(doc.Checkpoints[activeIndex].Status) == PlanCheckpointStatusInProgress {
		return fmt.Errorf("amend_plan cannot replace current in-progress checkpoint %q; use explicit reset/restart controls first", activeID)
	}
	return nil
}

func planAmendmentPreservesCurrentRuntime(doc *pebblestore.SessionPlanDocument, replaceIndex int) bool {
	if doc == nil || doc.ExecutionState == nil {
		return false
	}
	activeID := strings.TrimSpace(doc.ActiveCheckpointID)
	if activeID == "" {
		return false
	}
	activeIndex := findPlanCheckpointIndex(doc.Checkpoints, activeID)
	if activeIndex < 0 || activeIndex >= replaceIndex {
		return false
	}
	switch normalizePlanExecutionStateStatus(doc.ExecutionState.Status) {
	case PlanExecutionStateInProgress:
		return normalizePlanCheckpointStatusForSave(doc.Checkpoints[activeIndex].Status) == PlanCheckpointStatusInProgress
	case PlanExecutionStateWaitingReview:
		return planAmendmentPreservesWaitingReviewCheckpoint(doc, activeIndex, replaceIndex)
	default:
		return false
	}
}

func planAmendmentPreservesWaitingReviewCheckpoint(doc *pebblestore.SessionPlanDocument, checkpointIndex, replaceIndex int) bool {
	if doc == nil || doc.ExecutionState == nil || checkpointIndex < 0 || checkpointIndex >= len(doc.Checkpoints) || checkpointIndex >= replaceIndex {
		return false
	}
	if normalizePlanExecutionStateStatus(doc.ExecutionState.Status) != PlanExecutionStateWaitingReview {
		return false
	}
	checkpointID := strings.TrimSpace(doc.Checkpoints[checkpointIndex].ID)
	if checkpointID == "" {
		return false
	}
	activeID := strings.TrimSpace(doc.ActiveCheckpointID)
	lastID := strings.TrimSpace(doc.ExecutionState.LastCheckpointID)
	if checkpointID != activeID && checkpointID != lastID {
		return false
	}
	return planCheckpointReviewPending(doc.ExecutionPolicy, doc.Checkpoints[checkpointIndex], checkpointIndex < len(doc.Checkpoints)-1)
}

func clonePlanCheckpointSlice(checkpoints []pebblestore.SessionPlanCheckpoint) []pebblestore.SessionPlanCheckpoint {
	if len(checkpoints) == 0 {
		return nil
	}
	raw, err := json.Marshal(checkpoints)
	if err != nil {
		return append([]pebblestore.SessionPlanCheckpoint(nil), checkpoints...)
	}
	var clone []pebblestore.SessionPlanCheckpoint
	if err := json.Unmarshal(raw, &clone); err != nil {
		return append([]pebblestore.SessionPlanCheckpoint(nil), checkpoints...)
	}
	return clone
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
	if err := validatePlanDocumentForLifecycleAction(doc, action); err != nil {
		return planLifecycleState{}, err
	}
	return planLifecycleState{session: session, plan: plan, doc: doc}, nil
}

func validatePlanDocumentForLifecycleAction(doc *pebblestore.SessionPlanDocument, action string) error {
	err := ValidatePlanDocument(doc)
	if err == nil || action != "request_followup_checkpoint" || doc == nil {
		return err
	}

	// A cancelled run can be reconciled after a later checkpoint was already
	// selected, leaving the paused checkpoint before active_checkpoint_id. A
	// follow-up request is the repair boundary for that state: it closes the
	// paused checkpoint before inserting and selecting the new checkpoint.
	activeIndex := findPlanCheckpointIndex(doc.Checkpoints, strings.TrimSpace(doc.ActiveCheckpointID))
	if activeIndex <= 0 {
		return err
	}
	repairCandidate := clonePlanLifecycleDocument(doc)
	repaired := false
	for i := 0; i < activeIndex; i++ {
		checkpoint := &repairCandidate.Checkpoints[i]
		if normalizePlanCheckpointStatusForSave(checkpoint.Status) != PlanCheckpointStatusPaused {
			continue
		}
		checkpoint.Status = PlanCheckpointStatusCompleted
		if checkpoint.Review != nil {
			checkpoint.Review.Status = PlanCheckpointReviewStatusApproved
		}
		repaired = true
	}
	if !repaired {
		return err
	}
	if repairErr := ValidatePlanDocument(repairCandidate); repairErr != nil {
		return err
	}
	return nil
}

func (s *PlanLifecycleService) saveLifecyclePlan(state planLifecycleState, checkpointID, updateKind, updateSummary string) (PlanLifecycleResult, error) {
	return s.saveLifecyclePlanWithStatus(state, checkpointID, updateKind, updateSummary, state.plan.Status, state.plan.ApprovalState)
}

func (s *PlanLifecycleService) saveLifecyclePlanWithStatus(state planLifecycleState, checkpointID, updateKind, updateSummary, status, approvalState string) (PlanLifecycleResult, error) {
	if err := ValidatePlanDocument(state.doc); err != nil {
		return PlanLifecycleResult{}, err
	}
	saved, event, err := s.sessions.SavePlanWithMetadata(state.session.ID, state.plan.ID, state.plan.Title, state.plan.Plan, status, approvalState, true, PlanSaveMetadata{UpdateSummary: updateSummary, UpdateScope: checkpointID, UpdateKind: updateKind, RevisionKind: PlanRevisionKindExecution, Checkpoint: true, Document: state.doc})
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

func nextFollowupCheckpointID(doc *pebblestore.SessionPlanDocument, insertionIndex int) (string, error) {
	const base = "followup"
	if doc == nil || len(doc.Checkpoints) == 0 || insertionIndex >= len(doc.Checkpoints) {
		for i := 1; ; i++ {
			candidate := fmt.Sprintf("%s-%d", base, i)
			if doc == nil || findPlanCheckpointIndex(doc.Checkpoints, candidate) < 0 {
				return candidate, nil
			}
		}
	}
	if insertionIndex < 0 {
		insertionIndex = 0
	}
	if insertionIndex > len(doc.Checkpoints) {
		insertionIndex = len(doc.Checkpoints)
	}
	previous, hasPrevious := adjacentFollowupIDNumber(doc.Checkpoints, insertionIndex-1)
	next, hasNext := adjacentFollowupIDNumber(doc.Checkpoints, insertionIndex)
	var candidateNumber *big.Rat
	switch {
	case hasPrevious && hasNext:
		candidateNumber = new(big.Rat).Add(previous, next)
		candidateNumber.Quo(candidateNumber, big.NewRat(2, 1))
	case hasPrevious:
		candidateNumber = new(big.Rat).Add(previous, big.NewRat(1, 1))
	case hasNext:
		candidateNumber = new(big.Rat).Sub(next, big.NewRat(1, 2))
		if candidateNumber.Sign() <= 0 {
			candidateNumber = new(big.Rat).Quo(next, big.NewRat(2, 1))
		}
	default:
		return nextFollowupCheckpointID(doc, len(doc.Checkpoints))
	}
	if candidateNumber == nil || candidateNumber.Sign() <= 0 {
		return "", fmt.Errorf("request_followup_checkpoint could not resolve a positive follow-up id before checkpoint %q", strings.TrimSpace(doc.Checkpoints[insertionIndex].ID))
	}
	candidate := formatFollowupCheckpointID(candidateNumber)
	if candidate == "" || findPlanCheckpointIndex(doc.Checkpoints, candidate) >= 0 {
		return "", fmt.Errorf("request_followup_checkpoint could not resolve a unique follow-up id %q at insertion index %d", candidate, insertionIndex)
	}
	return candidate, nil
}

func adjacentFollowupIDNumber(checkpoints []pebblestore.SessionPlanCheckpoint, index int) (*big.Rat, bool) {
	if index < 0 || index >= len(checkpoints) {
		return nil, false
	}
	id := strings.TrimSpace(checkpoints[index].ID)
	if !strings.HasPrefix(id, "followup-") {
		return nil, false
	}
	value := strings.TrimSpace(strings.TrimPrefix(id, "followup-"))
	if value == "" {
		return nil, false
	}
	r := new(big.Rat)
	if _, ok := r.SetString(value); !ok || r.Sign() <= 0 {
		return nil, false
	}
	return r, true
}

func formatFollowupCheckpointID(value *big.Rat) string {
	if value == nil || value.Sign() <= 0 {
		return ""
	}
	if value.IsInt() {
		return "followup-" + value.Num().String()
	}
	for _, precision := range []int{1, 2, 3, 6, 9, 12} {
		text := strings.TrimRight(strings.TrimRight(value.FloatString(precision), "0"), ".")
		if parsed, ok := new(big.Rat).SetString(text); ok && parsed.Cmp(value) == 0 {
			return "followup-" + text
		}
	}
	return ""
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
