package session

import (
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	PlanExecutionPolicyModeAutomatic            = "automatic"
	PlanExecutionPolicyModeReviewEachCheckpoint = "review_each_checkpoint"

	PlanExecutionShapeSingleRun    = "single_run"
	PlanExecutionShapeCheckpointed = "checkpointed"

	PlanExecutionStateIdle          = "idle"
	PlanExecutionStateInProgress    = "in_progress"
	PlanExecutionStateWaitingReview = "waiting_review"
	PlanExecutionStateBlocked       = "blocked"
	PlanExecutionStateFailed        = "failed"
	PlanExecutionStateCompleted     = "completed"

	PlanCheckpointStatusPending     = "pending"
	PlanCheckpointStatusInProgress  = "in_progress"
	PlanCheckpointStatusNeedsReview = "needs_review"
	PlanCheckpointStatusCompleted   = "completed"
	PlanCheckpointStatusBlocked     = "blocked"
	PlanCheckpointStatusFailed      = "failed"

	PlanCheckpointReviewStatusPending  = "pending"
	PlanCheckpointReviewStatusApproved = "approved"
	PlanCheckpointReviewStatusRejected = "rejected"
)

type PlanExecutionSummary struct {
	PolicyMode           string `json:"policy_mode"`
	ExecutionShape       string `json:"execution_shape"`
	ActiveCheckpointID   string `json:"active_checkpoint_id,omitempty"`
	NextCheckpointID     string `json:"next_checkpoint_id,omitempty"`
	NextCheckpointStatus string `json:"next_checkpoint_status,omitempty"`
	ReviewRequired       bool   `json:"review_required"`
	Blocked              bool   `json:"blocked"`
	Failed               bool   `json:"failed"`
	PlanComplete         bool   `json:"plan_complete"`
	AutoAdvanceAllowed   bool   `json:"auto_advance_allowed"`
	StopReason           string `json:"stop_reason,omitempty"`
}

type PlanCheckpointOutcomeOptions struct {
	PlanID          string
	CheckpointID    string
	Outcome         string
	AttemptID       string
	RunID           string
	SessionID       string
	ParentSessionID string
	Report          string
	Result          string
	ChangedFiles    []string
	Validation      []string
	StartedAt       int64
	CompletedAt     int64
}

type PlanCheckpointOutcomeDecision struct {
	CheckpointID       string `json:"checkpoint_id"`
	Outcome            string `json:"outcome"`
	Status             string `json:"status"`
	NextCheckpointID   string `json:"next_checkpoint_id,omitempty"`
	ReviewRequired     bool   `json:"review_required"`
	AutoAdvanceAllowed bool   `json:"auto_advance_allowed"`
	PlanComplete       bool   `json:"plan_complete"`
	StopReason         string `json:"stop_reason,omitempty"`
}

func normalizePlanExecutionPolicy(policy *pebblestore.SessionPlanExecutionPolicy, checkpointCount int) {
	if policy == nil {
		return
	}
	policy.Mode = normalizePlanExecutionPolicyMode(policy.Mode)
	if policy.Mode == "" {
		policy.Mode = PlanExecutionPolicyModeReviewEachCheckpoint
	}
	policy.Shape = normalizePlanExecutionShape(policy.Shape)
	if policy.Shape == "" {
		if checkpointCount > 0 {
			policy.Shape = PlanExecutionShapeCheckpointed
		} else {
			policy.Shape = PlanExecutionShapeSingleRun
		}
	}
}

func normalizePlanExecutionState(state *pebblestore.SessionPlanExecutionState) {
	if state == nil {
		return
	}
	state.Status = normalizePlanExecutionStateStatus(state.Status)
	state.ActiveAttemptID = strings.TrimSpace(state.ActiveAttemptID)
	state.ParentSessionID = strings.TrimSpace(state.ParentSessionID)
	state.CurrentSessionID = strings.TrimSpace(state.CurrentSessionID)
	state.CurrentRunID = strings.TrimSpace(state.CurrentRunID)
	state.LastCheckpointID = strings.TrimSpace(state.LastCheckpointID)
	state.LastAttemptID = strings.TrimSpace(state.LastAttemptID)
	state.LastOutcome = normalizePlanCheckpointOutcome(state.LastOutcome)
}

func normalizePlanCheckpointRuntime(checkpoint *pebblestore.SessionPlanCheckpoint) {
	if checkpoint == nil {
		return
	}
	checkpoint.Status = normalizePlanCheckpointStatusForSave(checkpoint.Status)
	checkpoint.AttemptID = strings.TrimSpace(checkpoint.AttemptID)
	checkpoint.RunID = strings.TrimSpace(checkpoint.RunID)
	checkpoint.SessionID = strings.TrimSpace(checkpoint.SessionID)
	if checkpoint.Review != nil {
		normalizePlanCheckpointReview(checkpoint.Review)
		if isZeroPlanCheckpointReview(*checkpoint.Review) {
			checkpoint.Review = nil
		}
	}
	for i := range checkpoint.Attempts {
		normalizePlanCheckpointAttempt(&checkpoint.Attempts[i], checkpoint.ID)
	}
}

func normalizePlanCheckpointAttempt(attempt *pebblestore.SessionPlanCheckpointAttempt, checkpointID string) {
	if attempt == nil {
		return
	}
	attempt.ID = strings.TrimSpace(attempt.ID)
	attempt.CheckpointID = strings.TrimSpace(attempt.CheckpointID)
	if attempt.CheckpointID == "" {
		attempt.CheckpointID = strings.TrimSpace(checkpointID)
	}
	attempt.Status = normalizePlanCheckpointStatus(attempt.Status)
	attempt.Outcome = normalizePlanCheckpointOutcome(attempt.Outcome)
	attempt.RunID = strings.TrimSpace(attempt.RunID)
	attempt.SessionID = strings.TrimSpace(attempt.SessionID)
	attempt.ParentSessionID = strings.TrimSpace(attempt.ParentSessionID)
	attempt.Report = strings.TrimSpace(attempt.Report)
	attempt.Result = strings.TrimSpace(attempt.Result)
	attempt.ChangedFiles = trimStringSlice(attempt.ChangedFiles)
	attempt.Validation = trimStringSlice(attempt.Validation)
}

func normalizePlanCheckpointReview(review *pebblestore.SessionPlanCheckpointReview) {
	if review == nil {
		return
	}
	review.Status = normalizePlanCheckpointReviewStatus(review.Status)
	review.ReviewerID = strings.TrimSpace(review.ReviewerID)
	review.ReviewerType = strings.TrimSpace(review.ReviewerType)
	review.Result = strings.TrimSpace(review.Result)
	review.Notes = strings.TrimSpace(review.Notes)
}

func normalizePlanExecutionPolicyMode(mode string) string {
	switch token := normalizePlanToken(mode); token {
	case "", "default":
		return ""
	case "auto", "automatic":
		return PlanExecutionPolicyModeAutomatic
	case "review", "review_each", "review_each_checkpoint", "manual", "step", "stepwise", "pause", "pause_each_checkpoint":
		return PlanExecutionPolicyModeReviewEachCheckpoint
	default:
		return token
	}
}

func normalizePlanExecutionShape(shape string) string {
	switch token := normalizePlanToken(shape); token {
	case "", "default":
		return ""
	case "single", "single_run", "one_run", "whole_plan":
		return PlanExecutionShapeSingleRun
	case "checkpoint", "checkpoints", "checkpointed", "one_checkpoint_per_run":
		return PlanExecutionShapeCheckpointed
	default:
		return token
	}
}

func normalizePlanExecutionStateStatus(status string) string {
	switch token := normalizePlanToken(status); token {
	case "", "idle", "pending":
		return token
	case "active", "running", "started", "in_progress":
		return PlanExecutionStateInProgress
	case "review", "needs_review", "waiting_review", "awaiting_review":
		return PlanExecutionStateWaitingReview
	case "blocked":
		return PlanExecutionStateBlocked
	case "failed", "failure", "error":
		return PlanExecutionStateFailed
	case "done", "complete", "completed", "success":
		return PlanExecutionStateCompleted
	default:
		return token
	}
}

func normalizePlanCheckpointStatusForSave(status string) string {
	if strings.TrimSpace(status) == "" {
		return PlanCheckpointStatusPending
	}
	return normalizePlanCheckpointStatus(status)
}

func normalizePlanCheckpointStatus(status string) string {
	switch token := normalizePlanToken(status); token {
	case "":
		return ""
	case "todo", "queued", "not_started", "pending":
		return PlanCheckpointStatusPending
	case "active", "running", "started", "in_progress":
		return PlanCheckpointStatusInProgress
	case "review", "needs_review", "waiting_review", "awaiting_review":
		return PlanCheckpointStatusNeedsReview
	case "done", "complete", "completed", "success":
		return PlanCheckpointStatusCompleted
	case "blocked":
		return PlanCheckpointStatusBlocked
	case "failed", "failure", "error":
		return PlanCheckpointStatusFailed
	default:
		return token
	}
}

func normalizePlanCheckpointOutcome(outcome string) string {
	switch token := normalizePlanToken(outcome); token {
	case "":
		return ""
	case "review", "needs_review", "waiting_review", "awaiting_review":
		return PlanCheckpointStatusNeedsReview
	case "done", "complete", "completed", "success":
		return PlanCheckpointStatusCompleted
	case "blocked":
		return PlanCheckpointStatusBlocked
	case "failed", "failure", "error":
		return PlanCheckpointStatusFailed
	default:
		return token
	}
}

func normalizePlanCheckpointReviewStatus(status string) string {
	switch token := normalizePlanToken(status); token {
	case "":
		return ""
	case "review", "needs_review", "waiting_review", "awaiting_review", "pending":
		return PlanCheckpointReviewStatusPending
	case "approve", "approved", "accepted":
		return PlanCheckpointReviewStatusApproved
	case "reject", "rejected", "failed":
		return PlanCheckpointReviewStatusRejected
	default:
		return token
	}
}

func normalizePlanToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	for strings.Contains(value, "__") {
		value = strings.ReplaceAll(value, "__", "_")
	}
	return value
}

func validatePlanExecutionPolicy(policy pebblestore.SessionPlanExecutionPolicy) error {
	switch policy.Mode {
	case "", PlanExecutionPolicyModeAutomatic, PlanExecutionPolicyModeReviewEachCheckpoint:
	default:
		return fmt.Errorf("plan document execution_policy.mode %q is not supported", policy.Mode)
	}
	switch policy.Shape {
	case "", PlanExecutionShapeSingleRun, PlanExecutionShapeCheckpointed:
	default:
		return fmt.Errorf("plan document execution_policy.shape %q is not supported", policy.Shape)
	}
	return nil
}

func validatePlanExecutionState(state *pebblestore.SessionPlanExecutionState) error {
	if state == nil {
		return nil
	}
	switch state.Status {
	case "", PlanExecutionStateIdle, PlanExecutionStateInProgress, PlanExecutionStateWaitingReview, PlanExecutionStateBlocked, PlanExecutionStateFailed, PlanExecutionStateCompleted:
	default:
		return fmt.Errorf("plan document execution_state.status %q is not supported", state.Status)
	}
	if state.LastOutcome != "" && !isValidPlanCheckpointOutcome(state.LastOutcome) {
		return fmt.Errorf("plan document execution_state.last_outcome %q is not supported", state.LastOutcome)
	}
	return nil
}

func validatePlanCheckpointRuntime(checkpoint pebblestore.SessionPlanCheckpoint) error {
	if checkpoint.Status != "" && !isValidPlanCheckpointStatus(checkpoint.Status) {
		return fmt.Errorf("plan document checkpoint %q status %q is not supported", checkpoint.ID, checkpoint.Status)
	}
	if checkpoint.Review != nil {
		if err := validatePlanCheckpointReview(*checkpoint.Review, checkpoint.ID); err != nil {
			return err
		}
	}
	seenAttempts := make(map[string]struct{}, len(checkpoint.Attempts))
	for i, attempt := range checkpoint.Attempts {
		if strings.TrimSpace(attempt.ID) == "" {
			return fmt.Errorf("plan document checkpoint %q attempt at index %d requires id", checkpoint.ID, i)
		}
		if _, ok := seenAttempts[attempt.ID]; ok {
			return fmt.Errorf("plan document checkpoint %q attempt id %q is duplicated", checkpoint.ID, attempt.ID)
		}
		seenAttempts[attempt.ID] = struct{}{}
		if attempt.CheckpointID != "" && attempt.CheckpointID != strings.TrimSpace(checkpoint.ID) {
			return fmt.Errorf("plan document checkpoint %q attempt %q references checkpoint %q", checkpoint.ID, attempt.ID, attempt.CheckpointID)
		}
		if attempt.Status != "" && !isValidPlanCheckpointStatus(attempt.Status) {
			return fmt.Errorf("plan document checkpoint %q attempt %q status %q is not supported", checkpoint.ID, attempt.ID, attempt.Status)
		}
		if attempt.Outcome != "" && !isValidPlanCheckpointOutcome(attempt.Outcome) {
			return fmt.Errorf("plan document checkpoint %q attempt %q outcome %q is not supported", checkpoint.ID, attempt.ID, attempt.Outcome)
		}
	}
	return nil
}

func validatePlanCheckpointReview(review pebblestore.SessionPlanCheckpointReview, checkpointID string) error {
	switch review.Status {
	case "", PlanCheckpointReviewStatusPending, PlanCheckpointReviewStatusApproved, PlanCheckpointReviewStatusRejected:
		return nil
	default:
		return fmt.Errorf("plan document checkpoint %q review.status %q is not supported", checkpointID, review.Status)
	}
}

func isValidPlanCheckpointStatus(status string) bool {
	switch status {
	case PlanCheckpointStatusPending, PlanCheckpointStatusInProgress, PlanCheckpointStatusNeedsReview, PlanCheckpointStatusCompleted, PlanCheckpointStatusBlocked, PlanCheckpointStatusFailed:
		return true
	default:
		return false
	}
}

func isValidPlanCheckpointOutcome(outcome string) bool {
	switch outcome {
	case PlanCheckpointStatusNeedsReview, PlanCheckpointStatusCompleted, PlanCheckpointStatusBlocked, PlanCheckpointStatusFailed:
		return true
	default:
		return false
	}
}

func isZeroPlanCheckpointReview(review pebblestore.SessionPlanCheckpointReview) bool {
	return review.Status == "" && review.ReviewerID == "" && review.ReviewerType == "" && review.Result == "" && review.Notes == "" && review.ReviewedAt == 0
}

func defaultActiveCheckpointID(checkpoints []pebblestore.SessionPlanCheckpoint) string {
	for _, checkpoint := range checkpoints {
		status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
		switch status {
		case PlanCheckpointStatusPending, PlanCheckpointStatusInProgress:
			return strings.TrimSpace(checkpoint.ID)
		}
	}
	return ""
}

func SummarizePlanExecution(doc *pebblestore.SessionPlanDocument) PlanExecutionSummary {
	if doc == nil {
		return PlanExecutionSummary{PolicyMode: PlanExecutionPolicyModeReviewEachCheckpoint, ExecutionShape: PlanExecutionShapeSingleRun, PlanComplete: true}
	}
	policy := doc.ExecutionPolicy
	normalizePlanExecutionPolicy(&policy, len(doc.Checkpoints))
	summary := PlanExecutionSummary{
		PolicyMode:         policy.Mode,
		ExecutionShape:     policy.Shape,
		ActiveCheckpointID: strings.TrimSpace(doc.ActiveCheckpointID),
		AutoAdvanceAllowed: policy.Mode == PlanExecutionPolicyModeAutomatic,
	}
	if policy.Shape == PlanExecutionShapeSingleRun || len(doc.Checkpoints) == 0 {
		summary.PlanComplete = len(doc.Checkpoints) == 0
		return summary
	}

	startIndex := 0
	if summary.ActiveCheckpointID != "" {
		idx := findPlanCheckpointIndex(doc.Checkpoints, summary.ActiveCheckpointID)
		if idx >= 0 {
			checkpoint := doc.Checkpoints[idx]
			status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
			if planCheckpointReviewPending(policy, checkpoint) {
				summary.NextCheckpointID = strings.TrimSpace(checkpoint.ID)
				summary.NextCheckpointStatus = status
				summary.ReviewRequired = true
				summary.AutoAdvanceAllowed = false
				summary.StopReason = PlanCheckpointStatusNeedsReview
				return summary
			}
			switch status {
			case PlanCheckpointStatusPending, PlanCheckpointStatusInProgress:
				summary.NextCheckpointID = strings.TrimSpace(checkpoint.ID)
				summary.NextCheckpointStatus = status
				return summary
			case PlanCheckpointStatusNeedsReview:
				summary.NextCheckpointID = strings.TrimSpace(checkpoint.ID)
				summary.NextCheckpointStatus = status
				summary.ReviewRequired = true
				summary.StopReason = PlanCheckpointStatusNeedsReview
				return summary
			case PlanCheckpointStatusBlocked:
				summary.NextCheckpointID = strings.TrimSpace(checkpoint.ID)
				summary.NextCheckpointStatus = status
				summary.Blocked = true
				summary.StopReason = PlanCheckpointStatusBlocked
				return summary
			case PlanCheckpointStatusFailed:
				summary.NextCheckpointID = strings.TrimSpace(checkpoint.ID)
				summary.NextCheckpointStatus = status
				summary.Failed = true
				summary.StopReason = PlanCheckpointStatusFailed
				return summary
			case PlanCheckpointStatusCompleted:
				startIndex = idx + 1
			}
		}
	}

	for i := startIndex; i < len(doc.Checkpoints); i++ {
		checkpoint := doc.Checkpoints[i]
		status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
		if planCheckpointReviewPending(policy, checkpoint) {
			summary.NextCheckpointID = strings.TrimSpace(checkpoint.ID)
			summary.NextCheckpointStatus = status
			summary.ReviewRequired = true
			summary.AutoAdvanceAllowed = false
			summary.StopReason = PlanCheckpointStatusNeedsReview
			return summary
		}
		switch status {
		case PlanCheckpointStatusPending, PlanCheckpointStatusInProgress:
			summary.NextCheckpointID = strings.TrimSpace(checkpoint.ID)
			summary.NextCheckpointStatus = status
			return summary
		case PlanCheckpointStatusNeedsReview:
			summary.NextCheckpointID = strings.TrimSpace(checkpoint.ID)
			summary.NextCheckpointStatus = status
			summary.ReviewRequired = true
			summary.StopReason = PlanCheckpointStatusNeedsReview
			return summary
		case PlanCheckpointStatusBlocked:
			summary.NextCheckpointID = strings.TrimSpace(checkpoint.ID)
			summary.NextCheckpointStatus = status
			summary.Blocked = true
			summary.StopReason = PlanCheckpointStatusBlocked
			return summary
		case PlanCheckpointStatusFailed:
			summary.NextCheckpointID = strings.TrimSpace(checkpoint.ID)
			summary.NextCheckpointStatus = status
			summary.Failed = true
			summary.StopReason = PlanCheckpointStatusFailed
			return summary
		}
	}
	summary.PlanComplete = true
	summary.AutoAdvanceAllowed = false
	return summary
}

func planCheckpointReviewPending(policy pebblestore.SessionPlanExecutionPolicy, checkpoint pebblestore.SessionPlanCheckpoint) bool {
	if normalizePlanCheckpointStatusForSave(checkpoint.Status) != PlanCheckpointStatusCompleted {
		return false
	}
	if checkpoint.Review != nil {
		switch normalizePlanCheckpointReviewStatus(checkpoint.Review.Status) {
		case PlanCheckpointReviewStatusPending, PlanCheckpointReviewStatusRejected:
			return true
		case PlanCheckpointReviewStatusApproved:
			return false
		}
	}
	return policy.Mode == PlanExecutionPolicyModeReviewEachCheckpoint
}

func SelectNextPlanCheckpoint(doc *pebblestore.SessionPlanDocument) (pebblestore.SessionPlanCheckpoint, PlanExecutionSummary, bool) {
	summary := SummarizePlanExecution(doc)
	if doc == nil || summary.NextCheckpointID == "" || summary.ReviewRequired || summary.Blocked || summary.Failed {
		return pebblestore.SessionPlanCheckpoint{}, summary, false
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, summary.NextCheckpointID)
	if idx < 0 {
		return pebblestore.SessionPlanCheckpoint{}, summary, false
	}
	return doc.Checkpoints[idx], summary, true
}

func ApplyPlanCheckpointOutcome(doc *pebblestore.SessionPlanDocument, options PlanCheckpointOutcomeOptions) (PlanCheckpointOutcomeDecision, error) {
	if doc == nil {
		return PlanCheckpointOutcomeDecision{}, errors.New("plan document is required")
	}
	normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
	normalizePlanExecutionState(doc.ExecutionState)
	checkpointID := strings.TrimSpace(options.CheckpointID)
	if checkpointID == "" {
		checkpointID = strings.TrimSpace(doc.ActiveCheckpointID)
	}
	if checkpointID == "" {
		return PlanCheckpointOutcomeDecision{}, errors.New("checkpoint_id is required")
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return PlanCheckpointOutcomeDecision{}, fmt.Errorf("plan document checkpoint %q was not found", checkpointID)
	}
	outcome := normalizePlanCheckpointOutcome(options.Outcome)
	if !isValidPlanCheckpointOutcome(outcome) {
		return PlanCheckpointOutcomeDecision{}, fmt.Errorf("checkpoint outcome %q is not supported", options.Outcome)
	}
	checkpoint := &doc.Checkpoints[idx]
	currentStatus := normalizePlanCheckpointStatusForSave(checkpoint.Status)
	if currentStatus == PlanCheckpointStatusCompleted || currentStatus == PlanCheckpointStatusBlocked || currentStatus == PlanCheckpointStatusFailed {
		if currentStatus != outcome {
			return PlanCheckpointOutcomeDecision{}, fmt.Errorf("checkpoint %q is already terminal with status %q", checkpointID, currentStatus)
		}
	}

	checkpoint.Status = outcome
	if report := strings.TrimSpace(options.Report); report != "" {
		checkpoint.Report = report
	}
	if result := strings.TrimSpace(options.Result); result != "" {
		checkpoint.Result = result
	}
	if len(options.ChangedFiles) > 0 {
		checkpoint.ChangedFiles = trimStringSlice(options.ChangedFiles)
	}
	if len(options.Validation) > 0 {
		checkpoint.Validation = trimStringSlice(options.Validation)
	}
	if options.StartedAt > 0 && checkpoint.StartedAt == 0 {
		checkpoint.StartedAt = options.StartedAt
	}
	if options.CompletedAt > 0 {
		checkpoint.CompletedAt = options.CompletedAt
	}
	if outcome == PlanCheckpointStatusNeedsReview {
		if checkpoint.Review == nil {
			checkpoint.Review = &pebblestore.SessionPlanCheckpointReview{}
		}
		checkpoint.Review.Status = PlanCheckpointReviewStatusPending
	}
	if outcome == PlanCheckpointStatusCompleted && doc.ExecutionPolicy.Mode == PlanExecutionPolicyModeReviewEachCheckpoint {
		if checkpoint.Review == nil {
			checkpoint.Review = &pebblestore.SessionPlanCheckpointReview{}
		}
		if normalizePlanCheckpointReviewStatus(checkpoint.Review.Status) == "" {
			checkpoint.Review.Status = PlanCheckpointReviewStatusPending
		}
	}
	attemptID := strings.TrimSpace(options.AttemptID)
	if attemptID == "" && (strings.TrimSpace(options.RunID) != "" || strings.TrimSpace(options.SessionID) != "" || strings.TrimSpace(options.ParentSessionID) != "") {
		attemptID = fmt.Sprintf("%s:attempt-%d", checkpointID, len(checkpoint.Attempts)+1)
	}
	if attemptID != "" {
		upsertPlanCheckpointAttempt(checkpoint, pebblestore.SessionPlanCheckpointAttempt{
			ID:              attemptID,
			CheckpointID:    checkpointID,
			Status:          outcome,
			Outcome:         outcome,
			RunID:           strings.TrimSpace(options.RunID),
			SessionID:       strings.TrimSpace(options.SessionID),
			ParentSessionID: strings.TrimSpace(options.ParentSessionID),
			StartedAt:       firstPositiveInt64(options.StartedAt, checkpoint.StartedAt),
			CompletedAt:     options.CompletedAt,
			Report:          checkpoint.Report,
			Result:          checkpoint.Result,
			ChangedFiles:    cloneStringSlice(checkpoint.ChangedFiles),
			Validation:      cloneStringSlice(checkpoint.Validation),
		})
		checkpoint.AttemptID = attemptID
		checkpoint.RunID = strings.TrimSpace(options.RunID)
		checkpoint.SessionID = strings.TrimSpace(options.SessionID)
	}

	if doc.ExecutionState == nil {
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{}
	}
	doc.ExecutionState.LastCheckpointID = checkpointID
	doc.ExecutionState.LastAttemptID = attemptID
	doc.ExecutionState.LastOutcome = outcome
	doc.ExecutionState.UpdatedAt = options.CompletedAt
	if strings.TrimSpace(options.ParentSessionID) != "" {
		doc.ExecutionState.ParentSessionID = strings.TrimSpace(options.ParentSessionID)
	}
	if strings.TrimSpace(options.SessionID) != "" {
		doc.ExecutionState.CurrentSessionID = strings.TrimSpace(options.SessionID)
	}
	if strings.TrimSpace(options.RunID) != "" {
		doc.ExecutionState.CurrentRunID = strings.TrimSpace(options.RunID)
	}

	decision := PlanCheckpointOutcomeDecision{CheckpointID: checkpointID, Outcome: outcome, Status: outcome}
	summary := SummarizePlanExecution(doc)
	switch outcome {
	case PlanCheckpointStatusCompleted:
		if policy := doc.ExecutionPolicy; policy.Mode == PlanExecutionPolicyModeReviewEachCheckpoint {
			doc.ActiveCheckpointID = checkpointID
			decision.ReviewRequired = true
			decision.StopReason = PlanCheckpointStatusNeedsReview
			doc.ExecutionState.Status = PlanExecutionStateWaitingReview
			if checkpoint.Review == nil {
				checkpoint.Review = &pebblestore.SessionPlanCheckpointReview{}
			}
			checkpoint.Review.Status = PlanCheckpointReviewStatusPending
			return decision, nil
		}
		if summary.NextCheckpointID != "" {
			doc.ActiveCheckpointID = summary.NextCheckpointID
			decision.NextCheckpointID = summary.NextCheckpointID
			decision.AutoAdvanceAllowed = summary.AutoAdvanceAllowed
		} else if summary.PlanComplete {
			doc.ActiveCheckpointID = ""
			decision.PlanComplete = true
			doc.ExecutionState.Status = PlanExecutionStateCompleted
			doc.ExecutionState.CompletedAt = options.CompletedAt
		}
	case PlanCheckpointStatusNeedsReview:
		doc.ActiveCheckpointID = checkpointID
		decision.ReviewRequired = true
		decision.StopReason = PlanCheckpointStatusNeedsReview
		doc.ExecutionState.Status = PlanExecutionStateWaitingReview
	case PlanCheckpointStatusBlocked:
		doc.ActiveCheckpointID = checkpointID
		decision.StopReason = PlanCheckpointStatusBlocked
		doc.ExecutionState.Status = PlanExecutionStateBlocked
	case PlanCheckpointStatusFailed:
		doc.ActiveCheckpointID = checkpointID
		decision.StopReason = PlanCheckpointStatusFailed
		doc.ExecutionState.Status = PlanExecutionStateFailed
	}
	return decision, nil
}

func upsertPlanCheckpointAttempt(checkpoint *pebblestore.SessionPlanCheckpoint, attempt pebblestore.SessionPlanCheckpointAttempt) {
	normalizePlanCheckpointAttempt(&attempt, checkpoint.ID)
	for i := range checkpoint.Attempts {
		if strings.TrimSpace(checkpoint.Attempts[i].ID) == attempt.ID {
			checkpoint.Attempts[i] = attempt
			return
		}
	}
	checkpoint.Attempts = append(checkpoint.Attempts, attempt)
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
