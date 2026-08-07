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

const CheckpointBoundaryTransitionAction = "transition_checkpoint_boundary"

// CheckpointBoundaryTransitionInput is the complete durable contract for moving
// a parent provider turn across a checkpoint boundary. SourceMessageID is the
// idempotent user-message authority; SourceRunID relinquishes ownership and the
// Next* fields identify the one fresh checkpoint run committed in its place.
type CheckpointBoundaryTransitionInput struct {
	SessionID          string
	PlanID             string
	ChangeRequest      string
	Title              string
	Tasks              []string
	AcceptanceCriteria []string
	Artifacts          []pebblestore.SessionPlanArtifactReference
	Notes              string
	SourceMessageID    string
	SourceRunID        string
	NextRunID          string
	NextRunSessionID   string
	ParentSessionID    string
	NextAttemptID      string
	StartedAt          int64
}

// CheckpointBoundaryTransitionResult carries only identities committed by the
// canonical V3 mutation. Executors can replay this result without selecting or
// starting the checkpoint a second time.
type CheckpointBoundaryTransitionResult struct {
	Session      pebblestore.SessionSnapshot
	Plan         pebblestore.SessionPlanSnapshot
	Summary      PlanExecutionSummary
	CheckpointID string
	AttemptID    string
	RunIntent    pebblestore.V3SessionRunIntent
	Mutation     SessionMutationResult
	Replayed     bool
}

// CheckpointBoundaryService is the sole authority for appending, selecting, and
// assigning a fresh checkpoint run from a parent provider turn. It deliberately
// does not call PlanLifecycleService.RequestFollowupCheckpoint.
type CheckpointBoundaryService struct {
	sessions             *Service
	applySessionMutation func(SessionMutationInput) (SessionMutationResult, error)
}

func NewCheckpointBoundaryService(sessions *Service) *CheckpointBoundaryService {
	service := &CheckpointBoundaryService{sessions: sessions}
	if sessions != nil {
		service.applySessionMutation = sessions.ApplySessionMutation
	}
	return service
}

func (s *CheckpointBoundaryService) SetApplySessionMutation(apply func(SessionMutationInput) (SessionMutationResult, error)) {
	if s != nil {
		s.applySessionMutation = apply
	}
}

func (s *CheckpointBoundaryService) Transition(input CheckpointBoundaryTransitionInput) (CheckpointBoundaryTransitionResult, error) {
	if s == nil || s.sessions == nil || s.applySessionMutation == nil {
		return CheckpointBoundaryTransitionResult{}, errors.New("checkpoint boundary service requires the canonical V3 mutation authority")
	}
	input = normalizeCheckpointBoundaryTransitionInput(input)
	if err := validateCheckpointBoundaryTransitionInput(input); err != nil {
		return CheckpointBoundaryTransitionResult{}, err
	}
	unlock := s.sessions.lockPlanLifecycleSession(input.SessionID)
	defer unlock()

	lifecycle := NewPlanLifecycleService(s.sessions)
	state, err := lifecycle.loadApprovedPlan(input.SessionID, input.PlanID, CheckpointBoundaryTransitionAction)
	if err != nil {
		return CheckpointBoundaryTransitionResult{}, err
	}
	if err := requireSessionMode(state.session, ModeAuto, CheckpointBoundaryTransitionAction); err != nil {
		return CheckpointBoundaryTransitionResult{}, err
	}
	if duplicate, ok, err := s.replayForSourceMessage(state, input.SourceMessageID); err != nil || ok {
		return duplicate, err
	}
	active, activeOK, err := s.sessions.GetSessionActiveRunIntent(input.SessionID)
	if err != nil {
		return CheckpointBoundaryTransitionResult{}, err
	}
	if !activeOK {
		return CheckpointBoundaryTransitionResult{}, errors.New("checkpoint boundary transition requires an active source run")
	}
	if strings.TrimSpace(active.RunID) != input.SourceRunID {
		return CheckpointBoundaryTransitionResult{}, fmt.Errorf("checkpoint boundary source run %q conflicts with active run %q", input.SourceRunID, active.RunID)
	}

	point := followupCheckpointInsertionPointForDocument(state.doc)
	if point.StopReason == PlanCheckpointStatusFailed {
		return CheckpointBoundaryTransitionResult{}, fmt.Errorf("checkpoint boundary transition cannot continue failed plan at checkpoint %q", point.CheckpointID)
	}
	checkpointID, err := nextFollowupCheckpointID(state.doc, point.Index)
	if err != nil {
		return CheckpointBoundaryTransitionResult{}, err
	}
	title := input.Title
	if title == "" {
		title = fmt.Sprintf("Session checkpoint: %s", truncatePlanLifecycleTitle(input.ChangeRequest, 72))
	}
	tasks := trimStringSlice(input.Tasks)
	if len(tasks) == 0 {
		tasks = []string{input.ChangeRequest}
	}
	checkpoint := pebblestore.SessionPlanCheckpoint{
		ID:                 checkpointID,
		Title:              title,
		Status:             PlanCheckpointStatusPending,
		Objective:          input.ChangeRequest,
		Tasks:              tasks,
		AcceptanceCriteria: trimStringSlice(input.AcceptanceCriteria),
		Artifacts:          trimPlanArtifacts(input.Artifacts),
		Notes:              buildCheckpointHandoffNotes(input.ChangeRequest, input.Notes),
		SourceMessageID:    input.SourceMessageID,
		Order:              point.Index + 1,
	}
	now := input.StartedAt
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	resolvedID, err := resolveFollowupInsertionPoint(state.doc, point, checkpointID, now)
	if err != nil {
		return CheckpointBoundaryTransitionResult{}, err
	}
	state.doc.Checkpoints = insertPlanCheckpointAt(state.doc.Checkpoints, point.Index, checkpoint)
	normalizeCheckpointOrder(state.doc)
	if state.doc.ExecutionState == nil {
		state.doc.ExecutionState = &pebblestore.SessionPlanExecutionState{}
	}
	if resolvedID != "" {
		state.doc.ExecutionState.LastCheckpointID = resolvedID
	}
	state.doc.ActiveCheckpointID = checkpointID
	state.doc.ExecutionState.Status = PlanExecutionStateIdle
	state.doc.ExecutionState.ActiveAttemptID = ""
	state.doc.ExecutionState.CurrentRunID = ""
	state.doc.ExecutionState.CurrentSessionID = ""
	state.doc.ExecutionPolicy.Shape = PlanExecutionShapeCheckpointed
	normalizePlanExecutionPolicy(&state.doc.ExecutionPolicy, len(state.doc.Checkpoints))

	decision, err := ApplyPlanCheckpointStart(state.doc, PlanCheckpointStartOptions{
		CheckpointID:    checkpointID,
		PlanID:          state.plan.ID,
		AttemptID:       input.NextAttemptID,
		RunID:           input.NextRunID,
		SessionID:       input.NextRunSessionID,
		ParentSessionID: input.ParentSessionID,
		StartedAt:       now,
	})
	if err != nil {
		return CheckpointBoundaryTransitionResult{}, err
	}
	if err := ValidatePlanDocument(state.doc); err != nil {
		return CheckpointBoundaryTransitionResult{}, err
	}
	prepared, err := s.sessions.PreparePlanSaveWithMetadata(state.session.ID, state.plan.ID, state.plan.Title, state.plan.Plan, state.plan.Status, state.plan.ApprovalState, true, PlanSaveMetadata{
		UpdateSummary: "Committed checkpoint boundary and fresh execution ownership",
		UpdateScope:   checkpointID,
		UpdateKind:    CheckpointBoundaryTransitionAction,
		RevisionKind:  PlanRevisionKindExecution,
		Checkpoint:    true,
		Document:      state.doc,
	})
	if err != nil {
		return CheckpointBoundaryTransitionResult{}, err
	}
	hash, err := checkpointBoundaryPayloadHash(input, prepared.Plan, decision.AttemptID)
	if err != nil {
		return CheckpointBoundaryTransitionResult{}, err
	}
	clientRequestID := "checkpoint-boundary:" + input.SourceMessageID
	mutation, err := s.applySessionMutation(SessionMutationInput{
		SessionID:       state.session.ID,
		UserID:          state.session.UserID,
		AccountScopeID:  state.session.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     hash,
		RequestHash:     hash,
		Kind:            SessionMutationCommitCheckpointBoundary,
		EventType:       "session.checkpoint_boundary.committed",
		RunIntent: &pebblestore.V3SessionRunIntent{
			RunID:           input.NextRunID,
			Status:          RunIntentPendingExecutor,
			PlanID:          state.plan.ID,
			CheckpointID:    checkpointID,
			AttemptID:       decision.AttemptID,
			RunSessionID:    input.NextRunSessionID,
			ParentSessionID: input.ParentSessionID,
			ResumeContext:   false,
		},
		PlanSave: &pebblestore.V3PlanSaveMutation{
			Plan:                  prepared.Plan,
			ArchivedRevision:      prepared.ArchivedRevision,
			Activate:              prepared.Activate,
			ExpectedParentVersion: prepared.Plan.ParentRevision,
		},
		CheckpointBoundary: &pebblestore.V3CheckpointBoundaryMutation{SourceRunID: input.SourceRunID, SourceMessageID: input.SourceMessageID},
		NowUnixMs:          now,
	})
	if err != nil {
		return CheckpointBoundaryTransitionResult{}, err
	}
	if mutation.Plan == nil || mutation.RunIntent == nil {
		return CheckpointBoundaryTransitionResult{}, errors.New("checkpoint boundary mutation did not return committed plan and run intent")
	}
	return CheckpointBoundaryTransitionResult{
		Session:      state.session,
		Plan:         *mutation.Plan,
		Summary:      SummarizePlanExecution(mutation.Plan.Document),
		CheckpointID: checkpointID,
		AttemptID:    decision.AttemptID,
		RunIntent:    *mutation.RunIntent,
		Mutation:     mutation,
		Replayed:     mutation.Replayed,
	}, nil
}

func (s *CheckpointBoundaryService) replayForSourceMessage(state planLifecycleState, sourceMessageID string) (CheckpointBoundaryTransitionResult, bool, error) {
	for _, checkpoint := range state.doc.Checkpoints {
		if strings.TrimSpace(checkpoint.SourceMessageID) != sourceMessageID {
			continue
		}
		runID := strings.TrimSpace(checkpoint.RunID)
		if runID == "" {
			return CheckpointBoundaryTransitionResult{}, true, fmt.Errorf("checkpoint boundary source message %q already belongs to checkpoint %q without durable run ownership", sourceMessageID, checkpoint.ID)
		}
		intent, ok, err := s.sessions.GetV3SessionRunIntent(state.session.ID, runID)
		if err != nil {
			return CheckpointBoundaryTransitionResult{}, true, err
		}
		if !ok {
			return CheckpointBoundaryTransitionResult{}, true, fmt.Errorf("checkpoint boundary replay is missing run intent %q", runID)
		}
		return CheckpointBoundaryTransitionResult{Session: state.session, Plan: state.plan, Summary: SummarizePlanExecution(state.doc), CheckpointID: checkpoint.ID, AttemptID: checkpoint.AttemptID, RunIntent: intent, Replayed: true}, true, nil
	}
	return CheckpointBoundaryTransitionResult{}, false, nil
}

func normalizeCheckpointBoundaryTransitionInput(input CheckpointBoundaryTransitionInput) CheckpointBoundaryTransitionInput {
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.PlanID = strings.TrimSpace(input.PlanID)
	input.ChangeRequest = strings.TrimSpace(input.ChangeRequest)
	input.Title = strings.TrimSpace(input.Title)
	input.Notes = strings.TrimSpace(input.Notes)
	input.SourceMessageID = strings.TrimSpace(input.SourceMessageID)
	input.SourceRunID = strings.TrimSpace(input.SourceRunID)
	input.NextRunID = strings.TrimSpace(input.NextRunID)
	if input.NextRunID == "" && input.SessionID != "" && input.SourceMessageID != "" {
		sum := sha256.Sum256([]byte(input.SessionID + "\x00" + input.SourceMessageID + "\x00checkpoint-boundary"))
		input.NextRunID = "desktop-v3-run:" + hex.EncodeToString(sum[:16])
	}
	input.NextRunSessionID = strings.TrimSpace(input.NextRunSessionID)
	if input.NextRunSessionID == "" {
		input.NextRunSessionID = input.SessionID
	}
	input.ParentSessionID = strings.TrimSpace(input.ParentSessionID)
	if input.ParentSessionID == "" {
		input.ParentSessionID = input.SessionID
	}
	input.NextAttemptID = strings.TrimSpace(input.NextAttemptID)
	return input
}

func validateCheckpointBoundaryTransitionInput(input CheckpointBoundaryTransitionInput) error {
	if input.SessionID == "" || input.ChangeRequest == "" || input.SourceMessageID == "" {
		return errors.New("checkpoint boundary transition requires session_id, change_request, and source_message_id")
	}
	if input.SourceRunID == "" || input.NextRunID == "" || input.NextRunSessionID == "" || input.ParentSessionID == "" {
		return errors.New("checkpoint boundary transition requires complete source and next run ownership")
	}
	if input.SourceRunID == input.NextRunID {
		return errors.New("checkpoint boundary transition requires a distinct next run")
	}
	if len(trimStringSlice(input.AcceptanceCriteria)) == 0 {
		return errors.New("checkpoint boundary transition requires acceptance_criteria")
	}
	return nil
}

func checkpointBoundaryPayloadHash(input CheckpointBoundaryTransitionInput, plan pebblestore.SessionPlanSnapshot, attemptID string) (string, error) {
	raw, err := json.Marshal(struct {
		Input     CheckpointBoundaryTransitionInput `json:"input"`
		Plan      pebblestore.SessionPlanSnapshot   `json:"plan"`
		AttemptID string                            `json:"attempt_id"`
	}{Input: input, Plan: plan, AttemptID: attemptID})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
