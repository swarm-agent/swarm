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

	// PlanExecutionOriginApprovedPlan preserves the fresh-context runner
	// behavior used by approved plans, including legacy documents.
	PlanExecutionOriginApprovedPlan = "approved_plan"
	// PlanExecutionOriginAutoSession identifies lightweight auto-mode session
	// work created from a direct user request.
	PlanExecutionOriginAutoSession = "auto_session"

	PlanExecutionShapeCheckpointed = "checkpointed"

	PlanFollowupCheckpointPolicyRequireApproval = "require_approval"
	PlanFollowupCheckpointPolicyAutoStart       = "auto_start"

	PlanExecutionStateIdle          = "idle"
	PlanExecutionStateInProgress    = "in_progress"
	PlanExecutionStateWaitingReview = "waiting_review"
	PlanExecutionStatePaused        = "paused"
	PlanExecutionStateBlocked       = "blocked"
	PlanExecutionStateFailed        = "failed"
	PlanExecutionStateCompleted     = "completed"

	PlanCheckpointStatusPending     = "pending"
	PlanCheckpointStatusInProgress  = "in_progress"
	PlanCheckpointStatusNeedsReview = "needs_review"
	PlanCheckpointStatusCompleted   = "completed"
	PlanCheckpointStatusPaused      = "paused"
	PlanCheckpointStatusBlocked     = "blocked"
	PlanCheckpointStatusFailed      = "failed"

	PlanSubtaskStatusPending    = "pending"
	PlanSubtaskStatusInProgress = "in_progress"
	PlanSubtaskStatusCompleted  = "completed"

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
	Paused               bool   `json:"paused"`
	Blocked              bool   `json:"blocked"`
	Failed               bool   `json:"failed"`
	PlanComplete         bool   `json:"plan_complete"`
	AutoAdvanceAllowed   bool   `json:"auto_advance_allowed"`
	StopReason           string `json:"stop_reason,omitempty"`
}

type PlanCheckpointStartOptions struct {
	PlanID          string
	CheckpointID    string
	AttemptID       string
	RunID           string
	SessionID       string
	ParentSessionID string
	StartedAt       int64
}

type PlanCheckpointStartDecision struct {
	CheckpointID       string `json:"checkpoint_id"`
	AttemptID          string `json:"attempt_id,omitempty"`
	Status             string `json:"status"`
	NextCheckpointID   string `json:"next_checkpoint_id,omitempty"`
	ReviewRequired     bool   `json:"review_required"`
	AutoAdvanceAllowed bool   `json:"auto_advance_allowed"`
	PlanComplete       bool   `json:"plan_complete"`
	StopReason         string `json:"stop_reason,omitempty"`
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
	Artifacts       []pebblestore.SessionPlanArtifactReference
	Recommendation  *pebblestore.SessionPlanCheckpointRecommendation
	Handoff         *pebblestore.SessionPlanCheckpointHandoff
	StartedAt       int64
	CompletedAt     int64
}

func mergePlanCheckpointArtifacts(existing, added []pebblestore.SessionPlanArtifactReference) []pebblestore.SessionPlanArtifactReference {
	merged := trimPlanArtifacts(append(append([]pebblestore.SessionPlanArtifactReference(nil), existing...), added...))
	result := make([]pebblestore.SessionPlanArtifactReference, 0, len(merged))
	seen := make(map[string]struct{}, len(merged))
	for _, artifact := range merged {
		key := strings.Join([]string{artifact.Path, artifact.Role, artifact.Description, artifact.MediaType}, "\x00")
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, artifact)
	}
	return result
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

type PlanCheckpointReviewAcceptanceOptions struct {
	CheckpointID string
	ReviewerID   string
	ReviewerType string
	Result       string
	Notes        string
	ReviewedAt   int64
}

type PlanCheckpointResetOptions struct {
	CheckpointID string
	Rewind       bool
}

type PlanCheckpointBlockResolutionOptions struct {
	CheckpointID string
	Result       string
	Notes        string
	ResolvedAt   int64
}

type PlanCheckpointCancellationOptions struct {
	PlanID          string
	CheckpointID    string
	AttemptID       string
	RunID           string
	SessionID       string
	ParentSessionID string
	Reason          string
	CancelledAt     int64
}

type PlanCheckpointCancellationDecision struct {
	CheckpointID string `json:"checkpoint_id,omitempty"`
	AttemptID    string `json:"attempt_id,omitempty"`
	Changed      bool   `json:"changed"`
}

// NormalizePlanExecutionOrigin returns the fail-safe approved-plan behavior
// for missing or unknown persisted values.
func normalizePlanCheckpointRecommendation(value pebblestore.SessionPlanCheckpointRecommendation) pebblestore.SessionPlanCheckpointRecommendation {
	value.Decision = strings.ToLower(strings.TrimSpace(value.Decision))
	value.Action = strings.ToLower(strings.TrimSpace(value.Action))
	value.Reason = strings.TrimSpace(value.Reason)
	value.ActionState = strings.ToLower(strings.TrimSpace(value.ActionState))
	return value
}

func validatePlanCheckpointRecommendation(value pebblestore.SessionPlanCheckpointRecommendation) error {
	if value.Decision == "" && value.Action == "" && value.Reason == "" && value.ActionState == "" {
		return nil
	}
	if value.Decision != "ship" && value.Decision != "change" && value.Decision != "revert" && value.Decision != "defer" {
		return fmt.Errorf("review recommendation decision %q is not supported", value.Decision)
	}
	if value.Action == "" || value.Reason == "" || value.ActionState == "" {
		return errors.New("review recommendation requires action, reason, and action_state")
	}
	if value.ActionState != "taken" && value.ActionState != "ready" && value.ActionState != "needs_approval" {
		return fmt.Errorf("review recommendation action_state %q is not supported", value.ActionState)
	}
	return nil
}

func NormalizePlanExecutionOrigin(origin string) string {
	switch normalizePlanToken(origin) {
	case "auto", "auto_session", "autosession", "session":
		return PlanExecutionOriginAutoSession
	case "", "approved", "approved_plan", "plan":
		return PlanExecutionOriginApprovedPlan
	default:
		return PlanExecutionOriginApprovedPlan
	}
}

func normalizePlanExecutionPolicy(policy *pebblestore.SessionPlanExecutionPolicy, checkpointCount int) {
	if policy == nil {
		return
	}
	policy.Mode = normalizePlanExecutionPolicyMode(policy.Mode)
	if policy.Mode == "" {
		policy.Mode = PlanExecutionPolicyModeAutomatic
	}
	// Checkpointed execution is canonical. Legacy single-run shapes are
	// normalized on load/save rather than retaining a second state-machine path.
	policy.Shape = PlanExecutionShapeCheckpointed
	_ = checkpointCount
	policy.FollowupCheckpointPolicy = normalizePlanFollowupCheckpointPolicy(policy.FollowupCheckpointPolicy)
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
	normalizePlanCheckpointSubtasks(checkpoint)
	if checkpoint.Review != nil {
		normalizePlanCheckpointReview(checkpoint.Review)
		if isZeroPlanCheckpointReview(*checkpoint.Review) {
			checkpoint.Review = nil
		}
	}
	if checkpoint.Recommendation != nil {
		recommendation := normalizePlanCheckpointRecommendation(*checkpoint.Recommendation)
		checkpoint.Recommendation = &recommendation
	}
	if checkpoint.Handoff != nil {
		checkpoint.Handoff.Title = strings.TrimSpace(checkpoint.Handoff.Title)
		checkpoint.Handoff.Overview = strings.TrimSpace(checkpoint.Handoff.Overview)
		checkpoint.Handoff.ImpactBullets = trimStringSlice(checkpoint.Handoff.ImpactBullets)
		for i := range checkpoint.Handoff.SuggestedPrompts {
			checkpoint.Handoff.SuggestedPrompts[i].Label = strings.TrimSpace(checkpoint.Handoff.SuggestedPrompts[i].Label)
			checkpoint.Handoff.SuggestedPrompts[i].Prompt = strings.TrimSpace(checkpoint.Handoff.SuggestedPrompts[i].Prompt)
		}
	}
	for i := range checkpoint.Attempts {
		normalizePlanCheckpointAttempt(&checkpoint.Attempts[i], checkpoint.ID)
	}
}

func normalizePlanCheckpointSubtasks(checkpoint *pebblestore.SessionPlanCheckpoint) {
	if checkpoint == nil {
		return
	}
	checkpoint.ActiveSubtaskID = strings.TrimSpace(checkpoint.ActiveSubtaskID)
	if len(checkpoint.Subtasks) == 0 && len(checkpoint.Tasks) > 0 {
		checkpoint.Subtasks = make([]pebblestore.SessionPlanSubtask, 0, len(checkpoint.Tasks))
		for _, task := range checkpoint.Tasks {
			title := strings.TrimSpace(task)
			if title == "" {
				continue
			}
			status := PlanSubtaskStatusPending
			if strings.HasPrefix(strings.ToLower(title), "[x]") {
				status = PlanSubtaskStatusCompleted
				title = strings.TrimSpace(title[3:])
			} else if strings.HasPrefix(strings.ToLower(title), "[ ]") {
				title = strings.TrimSpace(title[3:])
			}
			checkpoint.Subtasks = append(checkpoint.Subtasks, pebblestore.SessionPlanSubtask{ID: fmt.Sprintf("task-%d", len(checkpoint.Subtasks)+1), Title: title, Status: status, Order: len(checkpoint.Subtasks) + 1})
		}
	}
	for i := range checkpoint.Subtasks {
		subtask := &checkpoint.Subtasks[i]
		subtask.ID = strings.TrimSpace(subtask.ID)
		subtask.Title = strings.TrimSpace(subtask.Title)
		subtask.Notes = strings.TrimSpace(subtask.Notes)
		subtask.Result = strings.TrimSpace(subtask.Result)
		subtask.Status = normalizePlanSubtaskStatus(subtask.Status)
		if subtask.Status == "" {
			subtask.Status = PlanSubtaskStatusPending
		}
		if subtask.Order == 0 {
			subtask.Order = i + 1
		}
	}
}

func normalizePlanSubtaskStatus(status string) string {
	switch normalizePlanToken(status) {
	case "", "todo", "queued", "pending":
		return PlanSubtaskStatusPending
	case "active", "running", "started", "in_progress":
		return PlanSubtaskStatusInProgress
	case "done", "complete", "completed", "success":
		return PlanSubtaskStatusCompleted
	default:
		return normalizePlanToken(status)
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
	case "single", "single_run", "one_run", "whole_plan", "checkpoint", "checkpoints", "checkpointed", "one_checkpoint_per_run":
		return PlanExecutionShapeCheckpointed
	default:
		return token
	}
}

func normalizePlanFollowupCheckpointPolicy(policy string) string {
	switch token := normalizePlanToken(policy); token {
	case "", "default", "inherit", "global", "global_default":
		return ""
	case "approval", "approve", "require_approval", "manual", "ask":
		return PlanFollowupCheckpointPolicyRequireApproval
	case "auto", "automatic", "auto_start", "append_and_start", "start":
		return PlanFollowupCheckpointPolicyAutoStart
	case "auto_append", "append", "append_only":
		return ""
	default:
		return token
	}
}

func ResolvePlanFollowupCheckpointPolicy(doc *pebblestore.SessionPlanDocument, globalDefault string) string {
	policy := ""
	if doc != nil {
		policy = normalizePlanFollowupCheckpointPolicy(doc.ExecutionPolicy.FollowupCheckpointPolicy)
	}
	if policy == "" {
		policy = normalizePlanFollowupCheckpointPolicy(globalDefault)
	}
	if policy == "" {
		return PlanFollowupCheckpointPolicyRequireApproval
	}
	return policy
}

func normalizePlanExecutionStateStatus(status string) string {
	switch token := normalizePlanToken(status); token {
	case "", "idle", "pending":
		return token
	case "active", "running", "started", "in_progress":
		return PlanExecutionStateInProgress
	case "review", "needs_review", "waiting_review", "awaiting_review":
		return PlanExecutionStateWaitingReview
	case "pause", "paused", "stopped", "cancelled", "canceled":
		return PlanExecutionStatePaused
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
	case "pause", "paused", "stopped", "cancelled", "canceled":
		return PlanCheckpointStatusPaused
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
	case "pause", "paused", "stopped", "cancelled", "canceled":
		return PlanCheckpointStatusPaused
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
	case "", PlanExecutionShapeCheckpointed:
	default:
		return fmt.Errorf("plan document execution_policy.shape %q is not supported", policy.Shape)
	}
	switch policy.FollowupCheckpointPolicy {
	case "", PlanFollowupCheckpointPolicyRequireApproval, PlanFollowupCheckpointPolicyAutoStart:
	default:
		return fmt.Errorf("plan document execution_policy.followup_checkpoint_policy %q is not supported", policy.FollowupCheckpointPolicy)
	}
	return nil
}

func validatePlanExecutionState(state *pebblestore.SessionPlanExecutionState) error {
	if state == nil {
		return nil
	}
	switch state.Status {
	case "", PlanExecutionStateIdle, PlanExecutionStateInProgress, PlanExecutionStateWaitingReview, PlanExecutionStatePaused, PlanExecutionStateBlocked, PlanExecutionStateFailed, PlanExecutionStateCompleted:
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
	seenSubtasks := make(map[string]struct{}, len(checkpoint.Subtasks))
	inProgressSubtask := ""
	for i, subtask := range checkpoint.Subtasks {
		if subtask.ID == "" || subtask.Title == "" {
			return fmt.Errorf("plan document checkpoint %q subtask at index %d requires id and title", checkpoint.ID, i)
		}
		if _, ok := seenSubtasks[subtask.ID]; ok {
			return fmt.Errorf("plan document checkpoint %q subtask id %q is duplicated", checkpoint.ID, subtask.ID)
		}
		seenSubtasks[subtask.ID] = struct{}{}
		if subtask.Status != PlanSubtaskStatusPending && subtask.Status != PlanSubtaskStatusInProgress && subtask.Status != PlanSubtaskStatusCompleted {
			return fmt.Errorf("plan document checkpoint %q subtask %q status %q is not supported", checkpoint.ID, subtask.ID, subtask.Status)
		}
		if subtask.Status == PlanSubtaskStatusInProgress {
			if inProgressSubtask != "" {
				return fmt.Errorf("plan document checkpoint %q has multiple in_progress subtasks", checkpoint.ID)
			}
			inProgressSubtask = subtask.ID
		}
	}
	if checkpoint.ActiveSubtaskID != "" {
		if _, ok := seenSubtasks[checkpoint.ActiveSubtaskID]; !ok {
			return fmt.Errorf("plan document checkpoint %q active_subtask_id %q was not found", checkpoint.ID, checkpoint.ActiveSubtaskID)
		}
		if checkpoint.ActiveSubtaskID != inProgressSubtask {
			return fmt.Errorf("plan document checkpoint %q active_subtask_id must identify its in_progress subtask", checkpoint.ID)
		}
	} else if inProgressSubtask != "" {
		return fmt.Errorf("plan document checkpoint %q in_progress subtask requires active_subtask_id", checkpoint.ID)
	}
	if checkpoint.Review != nil {
		if err := validatePlanCheckpointReview(*checkpoint.Review, checkpoint.ID); err != nil {
			return err
		}
	}
	if checkpoint.Recommendation != nil {
		if err := validatePlanCheckpointRecommendation(*checkpoint.Recommendation); err != nil {
			return fmt.Errorf("plan document checkpoint %q: %w", checkpoint.ID, err)
		}
	}
	if checkpoint.Handoff != nil {
		if _, err := NormalizePlanCheckpointHandoff(*checkpoint.Handoff); err != nil {
			return fmt.Errorf("plan document checkpoint %q: %w", checkpoint.ID, err)
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
	case PlanCheckpointStatusPending, PlanCheckpointStatusInProgress, PlanCheckpointStatusNeedsReview, PlanCheckpointStatusCompleted, PlanCheckpointStatusPaused, PlanCheckpointStatusBlocked, PlanCheckpointStatusFailed:
		return true
	default:
		return false
	}
}

func isValidPlanCheckpointOutcome(outcome string) bool {
	switch outcome {
	case PlanCheckpointStatusNeedsReview, PlanCheckpointStatusCompleted, PlanCheckpointStatusPaused, PlanCheckpointStatusBlocked, PlanCheckpointStatusFailed:
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

func findInProgressPlanCheckpoint(checkpoints []pebblestore.SessionPlanCheckpoint, excludeID string) (string, int, bool) {
	excludeID = strings.TrimSpace(excludeID)
	for i := range checkpoints {
		if normalizePlanCheckpointStatusForSave(checkpoints[i].Status) != PlanCheckpointStatusInProgress {
			continue
		}
		id := strings.TrimSpace(checkpoints[i].ID)
		if excludeID != "" && id == excludeID {
			continue
		}
		return id, i, true
	}
	return "", -1, false
}

func validatePlanCheckpointContinuity(doc *pebblestore.SessionPlanDocument) error {
	if doc == nil {
		return nil
	}
	activeID := strings.TrimSpace(doc.ActiveCheckpointID)
	activeIdx := -1
	if activeID != "" {
		activeIdx = findPlanCheckpointIndex(doc.Checkpoints, activeID)
	}
	if activeIdx > 0 {
		for i := 0; i < activeIdx; i++ {
			checkpoint := doc.Checkpoints[i]
			id := strings.TrimSpace(checkpoint.ID)
			status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
			if status != PlanCheckpointStatusCompleted {
				return fmt.Errorf("plan document checkpoint %q status %q is before active_checkpoint_id %q; resolve it before advancing active checkpoint", id, status, activeID)
			}
			if planCheckpointReviewPending(doc.ExecutionPolicy, checkpoint, i < len(doc.Checkpoints)-1) {
				return fmt.Errorf("plan document checkpoint %q is waiting for review before active_checkpoint_id %q; accept or resolve it before advancing active checkpoint", id, activeID)
			}
		}
	}
	for i := range doc.Checkpoints {
		if normalizePlanCheckpointStatusForSave(doc.Checkpoints[i].Status) != PlanCheckpointStatusInProgress {
			continue
		}
		id := strings.TrimSpace(doc.Checkpoints[i].ID)
		if activeID == "" {
			return fmt.Errorf("plan document checkpoint %q is in_progress but active_checkpoint_id is empty", id)
		}
		if id != activeID {
			if activeIdx >= 0 && i < activeIdx {
				return fmt.Errorf("plan document checkpoint %q is in_progress before active_checkpoint_id %q; resolve it before advancing active checkpoint", id, activeID)
			}
			return fmt.Errorf("plan document checkpoint %q is in_progress but active_checkpoint_id is %q", id, activeID)
		}
	}
	if doc.ExecutionState != nil && normalizePlanExecutionStateStatus(doc.ExecutionState.Status) == PlanExecutionStateInProgress {
		if activeID == "" {
			return errors.New("plan document execution_state is in_progress but active_checkpoint_id is empty")
		}
		if activeIdx < 0 {
			return fmt.Errorf("plan document execution_state is in_progress but active_checkpoint_id %q does not match a checkpoint", activeID)
		}
		if normalizePlanCheckpointStatusForSave(doc.Checkpoints[activeIdx].Status) != PlanCheckpointStatusInProgress {
			return fmt.Errorf("plan document execution_state is in_progress but active checkpoint %q status is %q", activeID, normalizePlanCheckpointStatusForSave(doc.Checkpoints[activeIdx].Status))
		}
	}
	return nil
}

func SummarizePlanExecution(doc *pebblestore.SessionPlanDocument) PlanExecutionSummary {
	if doc == nil {
		return PlanExecutionSummary{PolicyMode: PlanExecutionPolicyModeReviewEachCheckpoint, ExecutionShape: PlanExecutionShapeCheckpointed, PlanComplete: true}
	}
	policy := doc.ExecutionPolicy
	normalizePlanExecutionPolicy(&policy, len(doc.Checkpoints))
	summary := PlanExecutionSummary{
		PolicyMode:         policy.Mode,
		ExecutionShape:     policy.Shape,
		ActiveCheckpointID: strings.TrimSpace(doc.ActiveCheckpointID),
		AutoAdvanceAllowed: policy.Mode == PlanExecutionPolicyModeAutomatic,
	}
	if len(doc.Checkpoints) == 0 {
		summary.PlanComplete = true
		summary.AutoAdvanceAllowed = false
		return summary
	}

	startIndex := 0
	if summary.ActiveCheckpointID != "" {
		idx := findPlanCheckpointIndex(doc.Checkpoints, summary.ActiveCheckpointID)
		if idx >= 0 {
			checkpoint := doc.Checkpoints[idx]
			status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
			if planCheckpointReviewPending(policy, checkpoint, idx < len(doc.Checkpoints)-1) {
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
			case PlanCheckpointStatusPaused:
				summary.NextCheckpointID = strings.TrimSpace(checkpoint.ID)
				summary.NextCheckpointStatus = status
				summary.Paused = true
				summary.AutoAdvanceAllowed = false
				summary.StopReason = PlanCheckpointStatusPaused
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
		if planCheckpointReviewPending(policy, checkpoint, i < len(doc.Checkpoints)-1) {
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
		case PlanCheckpointStatusPaused:
			summary.NextCheckpointID = strings.TrimSpace(checkpoint.ID)
			summary.NextCheckpointStatus = status
			summary.Paused = true
			summary.AutoAdvanceAllowed = false
			summary.StopReason = PlanCheckpointStatusPaused
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
	if planFinalReviewPending(doc) {
		checkpointID := finalPlanReviewCheckpointID(doc)
		if checkpointID != "" {
			summary.NextCheckpointID = checkpointID
			if idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID); idx >= 0 {
				summary.NextCheckpointStatus = normalizePlanCheckpointStatusForSave(doc.Checkpoints[idx].Status)
			}
		}
		summary.ReviewRequired = true
		summary.AutoAdvanceAllowed = false
		summary.StopReason = PlanCheckpointStatusNeedsReview
		return summary
	}
	summary.PlanComplete = true
	summary.AutoAdvanceAllowed = false
	return summary
}

func planFinalReviewPending(doc *pebblestore.SessionPlanDocument) bool {
	if doc == nil || doc.ExecutionState == nil {
		return false
	}
	if normalizePlanExecutionStateStatus(doc.ExecutionState.Status) != PlanExecutionStateWaitingReview {
		return false
	}
	return allPlanCheckpointsCompleted(doc.Checkpoints)
}

func allPlanCheckpointsCompleted(checkpoints []pebblestore.SessionPlanCheckpoint) bool {
	if len(checkpoints) == 0 {
		return false
	}
	for _, checkpoint := range checkpoints {
		if normalizePlanCheckpointStatusForSave(checkpoint.Status) != PlanCheckpointStatusCompleted {
			return false
		}
	}
	return true
}

func finalPlanReviewCheckpointID(doc *pebblestore.SessionPlanDocument) string {
	if doc == nil {
		return ""
	}
	if active := strings.TrimSpace(doc.ActiveCheckpointID); active != "" {
		return active
	}
	if doc.ExecutionState != nil {
		if last := strings.TrimSpace(doc.ExecutionState.LastCheckpointID); last != "" {
			return last
		}
	}
	if len(doc.Checkpoints) == 0 {
		return ""
	}
	return strings.TrimSpace(doc.Checkpoints[len(doc.Checkpoints)-1].ID)
}

func planCheckpointReviewPending(policy pebblestore.SessionPlanExecutionPolicy, checkpoint pebblestore.SessionPlanCheckpoint, hasLaterCheckpoint bool) bool {
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
	return hasLaterCheckpoint && policy.Mode == PlanExecutionPolicyModeReviewEachCheckpoint
}

func SelectNextPlanCheckpoint(doc *pebblestore.SessionPlanDocument) (pebblestore.SessionPlanCheckpoint, PlanExecutionSummary, bool) {
	summary := SummarizePlanExecution(doc)
	if doc == nil || summary.NextCheckpointID == "" || summary.ReviewRequired || summary.Paused || summary.Blocked || summary.Failed {
		return pebblestore.SessionPlanCheckpoint{}, summary, false
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, summary.NextCheckpointID)
	if idx < 0 {
		return pebblestore.SessionPlanCheckpoint{}, summary, false
	}
	return doc.Checkpoints[idx], summary, true
}

func ApplyPlanCheckpointStart(doc *pebblestore.SessionPlanDocument, options PlanCheckpointStartOptions) (PlanCheckpointStartDecision, error) {
	if doc == nil {
		return PlanCheckpointStartDecision{}, errors.New("plan document is required")
	}
	normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
	normalizePlanExecutionState(doc.ExecutionState)
	checkpointID := strings.TrimSpace(options.CheckpointID)
	if checkpointID == "" {
		checkpointID = strings.TrimSpace(doc.ActiveCheckpointID)
	}
	if checkpointID == "" {
		_, summary, ok := SelectNextPlanCheckpoint(doc)
		if !ok {
			return PlanCheckpointStartDecision{NextCheckpointID: summary.NextCheckpointID, ReviewRequired: summary.ReviewRequired, AutoAdvanceAllowed: summary.AutoAdvanceAllowed, PlanComplete: summary.PlanComplete, StopReason: summary.StopReason}, errors.New("no runnable checkpoint is available")
		}
		checkpointID = summary.NextCheckpointID
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return PlanCheckpointStartDecision{}, fmt.Errorf("plan document checkpoint %q was not found", checkpointID)
	}
	checkpoint := &doc.Checkpoints[idx]
	status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
	if planID := strings.TrimSpace(options.PlanID); planID != "" && strings.TrimSpace(doc.ID) != planID {
		return PlanCheckpointStartDecision{}, fmt.Errorf("checkpoint start plan_id %q does not match plan %q", planID, strings.TrimSpace(doc.ID))
	}
	if status == PlanCheckpointStatusInProgress {
		if planCheckpointOwnershipMatches(doc, checkpoint, options.AttemptID, options.RunID, options.SessionID, options.ParentSessionID) {
			return PlanCheckpointStartDecision{CheckpointID: checkpointID, AttemptID: strings.TrimSpace(checkpoint.AttemptID), Status: status, NextCheckpointID: checkpointID, AutoAdvanceAllowed: doc.ExecutionPolicy.Mode == PlanExecutionPolicyModeAutomatic}, nil
		}
		return PlanCheckpointStartDecision{CheckpointID: checkpointID, Status: status, NextCheckpointID: checkpointID, StopReason: PlanCheckpointStatusInProgress}, fmt.Errorf("checkpoint %q is already in_progress under different run ownership", checkpointID)
	}
	if inProgressID, _, ok := findInProgressPlanCheckpoint(doc.Checkpoints, checkpointID); ok {
		return PlanCheckpointStartDecision{CheckpointID: checkpointID, Status: status, NextCheckpointID: inProgressID, StopReason: PlanCheckpointStatusInProgress}, fmt.Errorf("cannot start checkpoint %q while checkpoint %q is in_progress; resolve it first", checkpointID, inProgressID)
	}
	summary := SummarizePlanExecution(doc)
	if planCheckpointReviewPending(doc.ExecutionPolicy, *checkpoint, idx < len(doc.Checkpoints)-1) || status == PlanCheckpointStatusNeedsReview {
		return PlanCheckpointStartDecision{CheckpointID: checkpointID, Status: status, NextCheckpointID: checkpointID, ReviewRequired: true, StopReason: PlanCheckpointStatusNeedsReview}, fmt.Errorf("checkpoint %q is waiting for review", checkpointID)
	}
	switch status {
	case PlanCheckpointStatusPaused, PlanCheckpointStatusBlocked, PlanCheckpointStatusFailed:
		return PlanCheckpointStartDecision{CheckpointID: checkpointID, Status: status, NextCheckpointID: checkpointID, StopReason: status}, fmt.Errorf("checkpoint %q is %s", checkpointID, status)
	case PlanCheckpointStatusCompleted:
		if summary.NextCheckpointID != checkpointID {
			return PlanCheckpointStartDecision{CheckpointID: checkpointID, Status: status, NextCheckpointID: summary.NextCheckpointID, AutoAdvanceAllowed: summary.AutoAdvanceAllowed, PlanComplete: summary.PlanComplete, StopReason: summary.StopReason}, fmt.Errorf("checkpoint %q is already completed", checkpointID)
		}
	case PlanCheckpointStatusPending, PlanCheckpointStatusInProgress:
		// Runnable.
	default:
		return PlanCheckpointStartDecision{}, fmt.Errorf("checkpoint %q status %q is not runnable", checkpointID, status)
	}

	attemptID := strings.TrimSpace(options.AttemptID)
	if attemptID == "" {
		attemptID = fmt.Sprintf("%s:attempt-%d", checkpointID, len(checkpoint.Attempts)+1)
	}
	startedAt := options.StartedAt
	if startedAt > 0 && checkpoint.StartedAt == 0 {
		checkpoint.StartedAt = startedAt
	}
	checkpoint.Status = PlanCheckpointStatusInProgress
	normalizePlanCheckpointSubtasks(checkpoint)
	if checkpoint.ActiveSubtaskID == "" {
		for i := range checkpoint.Subtasks {
			if checkpoint.Subtasks[i].Status == PlanSubtaskStatusPending {
				checkpoint.Subtasks[i].Status = PlanSubtaskStatusInProgress
				checkpoint.Subtasks[i].StartedAt = firstPositiveInt64(checkpoint.Subtasks[i].StartedAt, startedAt)
				checkpoint.ActiveSubtaskID = checkpoint.Subtasks[i].ID
				break
			}
		}
	}
	checkpoint.AttemptID = attemptID
	checkpoint.RunID = strings.TrimSpace(options.RunID)
	checkpoint.SessionID = strings.TrimSpace(options.SessionID)
	upsertPlanCheckpointAttempt(checkpoint, pebblestore.SessionPlanCheckpointAttempt{
		ID:              attemptID,
		CheckpointID:    checkpointID,
		Status:          PlanCheckpointStatusInProgress,
		RunID:           strings.TrimSpace(options.RunID),
		SessionID:       strings.TrimSpace(options.SessionID),
		ParentSessionID: strings.TrimSpace(options.ParentSessionID),
		StartedAt:       firstPositiveInt64(startedAt, checkpoint.StartedAt),
	})
	doc.ActiveCheckpointID = checkpointID
	if doc.ExecutionState == nil {
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{}
	}
	doc.ExecutionState.Status = PlanExecutionStateInProgress
	doc.ExecutionState.ActiveAttemptID = attemptID
	if strings.TrimSpace(options.ParentSessionID) != "" {
		doc.ExecutionState.ParentSessionID = strings.TrimSpace(options.ParentSessionID)
	}
	if strings.TrimSpace(options.SessionID) != "" {
		doc.ExecutionState.CurrentSessionID = strings.TrimSpace(options.SessionID)
	}
	if strings.TrimSpace(options.RunID) != "" {
		doc.ExecutionState.CurrentRunID = strings.TrimSpace(options.RunID)
	}
	if startedAt > 0 {
		doc.ExecutionState.StartedAt = firstPositiveInt64(doc.ExecutionState.StartedAt, startedAt)
		doc.ExecutionState.UpdatedAt = startedAt
	}
	return PlanCheckpointStartDecision{CheckpointID: checkpointID, AttemptID: attemptID, Status: PlanCheckpointStatusInProgress, NextCheckpointID: checkpointID, AutoAdvanceAllowed: doc.ExecutionPolicy.Mode == PlanExecutionPolicyModeAutomatic}, nil
}

func planCheckpointOwnershipMatches(doc *pebblestore.SessionPlanDocument, checkpoint *pebblestore.SessionPlanCheckpoint, attemptID, runID, sessionID, parentSessionID string) bool {
	if doc == nil || checkpoint == nil || doc.ExecutionState == nil {
		return false
	}
	state := doc.ExecutionState
	attemptID = strings.TrimSpace(attemptID)
	runID = strings.TrimSpace(runID)
	sessionID = strings.TrimSpace(sessionID)
	parentSessionID = strings.TrimSpace(parentSessionID)
	if attemptID == "" || runID == "" || sessionID == "" || parentSessionID == "" {
		return false
	}
	return strings.TrimSpace(doc.ActiveCheckpointID) == strings.TrimSpace(checkpoint.ID) &&
		normalizePlanExecutionStateStatus(state.Status) == PlanExecutionStateInProgress &&
		strings.TrimSpace(state.ActiveAttemptID) == attemptID &&
		strings.TrimSpace(state.CurrentRunID) == runID &&
		strings.TrimSpace(state.CurrentSessionID) == sessionID &&
		strings.TrimSpace(state.ParentSessionID) == parentSessionID &&
		strings.TrimSpace(checkpoint.AttemptID) == attemptID &&
		strings.TrimSpace(checkpoint.RunID) == runID &&
		strings.TrimSpace(checkpoint.SessionID) == sessionID
}

func planCheckpointOutcomeOwnershipMatches(doc *pebblestore.SessionPlanDocument, checkpoint *pebblestore.SessionPlanCheckpoint, options PlanCheckpointOutcomeOptions) bool {
	attemptID := strings.TrimSpace(options.AttemptID)
	runID := strings.TrimSpace(options.RunID)
	sessionID := strings.TrimSpace(options.SessionID)
	parentSessionID := strings.TrimSpace(options.ParentSessionID)
	if attemptID == "" && runID == "" && sessionID == "" && parentSessionID == "" {
		// User-driven lifecycle actions may not be provider-run-owned. They are
		// valid only when the persisted checkpoint has no run ownership either.
		return strings.TrimSpace(checkpoint.AttemptID) == "" && strings.TrimSpace(checkpoint.RunID) == "" && strings.TrimSpace(checkpoint.SessionID) == ""
	}
	return planCheckpointOwnershipMatches(doc, checkpoint, attemptID, runID, sessionID, parentSessionID)
}

func planCheckpointTerminalOutcomeRetryMatches(checkpoint *pebblestore.SessionPlanCheckpoint, outcome string, options PlanCheckpointOutcomeOptions) bool {
	if checkpoint == nil || normalizePlanCheckpointStatusForSave(checkpoint.Status) != outcome {
		return false
	}
	attemptID := strings.TrimSpace(options.AttemptID)
	runID := strings.TrimSpace(options.RunID)
	sessionID := strings.TrimSpace(options.SessionID)
	parentSessionID := strings.TrimSpace(options.ParentSessionID)
	if attemptID == "" || runID == "" || sessionID == "" || parentSessionID == "" || strings.TrimSpace(checkpoint.AttemptID) != attemptID || strings.TrimSpace(checkpoint.RunID) != runID || strings.TrimSpace(checkpoint.SessionID) != sessionID {
		return false
	}
	for i := range checkpoint.Attempts {
		attempt := checkpoint.Attempts[i]
		if strings.TrimSpace(attempt.ID) == attemptID && normalizePlanCheckpointStatus(attempt.Status) == outcome && strings.TrimSpace(attempt.RunID) == runID && strings.TrimSpace(attempt.SessionID) == sessionID && strings.TrimSpace(attempt.ParentSessionID) == parentSessionID {
			return true
		}
	}
	return false
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
	if planID := strings.TrimSpace(options.PlanID); planID != "" && strings.TrimSpace(doc.ID) != planID {
		return PlanCheckpointOutcomeDecision{}, fmt.Errorf("checkpoint outcome plan_id %q does not match plan %q", planID, strings.TrimSpace(doc.ID))
	}
	currentStatus := normalizePlanCheckpointStatusForSave(checkpoint.Status)
	runOwned := strings.TrimSpace(checkpoint.AttemptID) != "" || strings.TrimSpace(checkpoint.RunID) != "" || strings.TrimSpace(checkpoint.SessionID) != ""
	exactTerminalRetry := planCheckpointTerminalOutcomeRetryMatches(checkpoint, outcome, options)
	if (currentStatus == PlanCheckpointStatusInProgress || runOwned) && !planCheckpointOutcomeOwnershipMatches(doc, checkpoint, options) && !exactTerminalRetry {
		return PlanCheckpointOutcomeDecision{}, fmt.Errorf("checkpoint %q outcome does not match active run ownership", checkpointID)
	}
	if exactTerminalRetry {
		summary := SummarizePlanExecution(doc)
		return PlanCheckpointOutcomeDecision{CheckpointID: checkpointID, Outcome: outcome, Status: currentStatus, NextCheckpointID: summary.NextCheckpointID, ReviewRequired: summary.ReviewRequired, AutoAdvanceAllowed: summary.AutoAdvanceAllowed, PlanComplete: summary.PlanComplete, StopReason: summary.StopReason}, nil
	}
	normalizePlanCheckpointSubtasks(checkpoint)
	if outcome == PlanCheckpointStatusCompleted {
		completeUnresolvedPlanCheckpointSubtasks(checkpoint, options.CompletedAt)
	}
	currentSummary := SummarizePlanExecution(doc)
	if currentStatus == PlanCheckpointStatusCompleted && outcome == PlanCheckpointStatusCompleted && currentSummary.ReviewRequired && currentSummary.StopReason == PlanCheckpointStatusNeedsReview && allPlanCheckpointsCompleted(doc.Checkpoints) {
		return PlanCheckpointOutcomeDecision{}, errors.New("plan is already waiting for final review; accept review or request_followup_checkpoint instead of completing the checkpoint again")
	}
	if currentStatus == PlanCheckpointStatusCompleted || currentStatus == PlanCheckpointStatusPaused || currentStatus == PlanCheckpointStatusBlocked || currentStatus == PlanCheckpointStatusFailed {
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
	if len(options.Artifacts) > 0 {
		checkpoint.Artifacts = mergePlanCheckpointArtifacts(checkpoint.Artifacts, options.Artifacts)
	}
	if options.Recommendation != nil {
		recommendation := normalizePlanCheckpointRecommendation(*options.Recommendation)
		if err := validatePlanCheckpointRecommendation(recommendation); err != nil {
			return PlanCheckpointOutcomeDecision{}, err
		}
		checkpoint.Recommendation = &recommendation
	}
	if options.Handoff != nil {
		handoff, err := NormalizePlanCheckpointHandoff(*options.Handoff)
		if err != nil {
			return PlanCheckpointOutcomeDecision{}, err
		}
		checkpoint.Handoff = &handoff
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
	if outcome == PlanCheckpointStatusCompleted && doc.ExecutionPolicy.Mode == PlanExecutionPolicyModeReviewEachCheckpoint && idx < len(doc.Checkpoints)-1 {
		if checkpoint.Review == nil {
			checkpoint.Review = &pebblestore.SessionPlanCheckpointReview{}
		}
		if normalizePlanCheckpointReviewStatus(checkpoint.Review.Status) == "" {
			checkpoint.Review.Status = PlanCheckpointReviewStatusPending
		}
	}
	attemptID := strings.TrimSpace(options.AttemptID)
	if attemptID == "" {
		attemptID = strings.TrimSpace(checkpoint.AttemptID)
	}
	if attemptID == "" && doc.ExecutionState != nil {
		attemptID = strings.TrimSpace(doc.ExecutionState.ActiveAttemptID)
	}
	runID := strings.TrimSpace(firstNonBlank(options.RunID, checkpoint.RunID))
	runSessionID := strings.TrimSpace(firstNonBlank(options.SessionID, checkpoint.SessionID))
	parentSessionID := strings.TrimSpace(options.ParentSessionID)
	if parentSessionID == "" && doc.ExecutionState != nil {
		parentSessionID = strings.TrimSpace(doc.ExecutionState.ParentSessionID)
	}
	if attemptID == "" && (runID != "" || runSessionID != "" || parentSessionID != "") {
		attemptID = fmt.Sprintf("%s:attempt-%d", checkpointID, len(checkpoint.Attempts)+1)
	}
	if attemptID != "" {
		upsertPlanCheckpointAttempt(checkpoint, pebblestore.SessionPlanCheckpointAttempt{
			ID:              attemptID,
			CheckpointID:    checkpointID,
			Status:          outcome,
			Outcome:         outcome,
			RunID:           runID,
			SessionID:       runSessionID,
			ParentSessionID: parentSessionID,
			StartedAt:       firstPositiveInt64(options.StartedAt, checkpoint.StartedAt),
			CompletedAt:     options.CompletedAt,
			Report:          checkpoint.Report,
			Result:          checkpoint.Result,
			ChangedFiles:    cloneStringSlice(checkpoint.ChangedFiles),
			Validation:      cloneStringSlice(checkpoint.Validation),
		})
		checkpoint.AttemptID = attemptID
		checkpoint.RunID = runID
		checkpoint.SessionID = runSessionID
	}

	if doc.ExecutionState == nil {
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{}
	}
	doc.ExecutionState.LastCheckpointID = checkpointID
	doc.ExecutionState.LastAttemptID = attemptID
	doc.ExecutionState.LastOutcome = outcome
	doc.ExecutionState.UpdatedAt = options.CompletedAt
	if parentSessionID != "" {
		doc.ExecutionState.ParentSessionID = parentSessionID
	}
	if runSessionID != "" {
		doc.ExecutionState.CurrentSessionID = runSessionID
	}
	if runID != "" {
		doc.ExecutionState.CurrentRunID = runID
	}

	decision := PlanCheckpointOutcomeDecision{CheckpointID: checkpointID, Outcome: outcome, Status: outcome}
	summary := SummarizePlanExecution(doc)
	switch outcome {
	case PlanCheckpointStatusCompleted:
		if policy := doc.ExecutionPolicy; policy.Mode == PlanExecutionPolicyModeReviewEachCheckpoint && idx < len(doc.Checkpoints)-1 {
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
			doc.ExecutionState.Status = PlanExecutionStateIdle
			doc.ExecutionState.ActiveAttemptID = ""
			doc.ExecutionState.CurrentRunID = ""
			doc.ExecutionState.CurrentSessionID = ""
		} else if summary.PlanComplete {
			doc.ActiveCheckpointID = checkpointID
			decision.ReviewRequired = true
			decision.StopReason = PlanCheckpointStatusNeedsReview
			doc.ExecutionState.Status = PlanExecutionStateWaitingReview
			if checkpoint.Review == nil {
				checkpoint.Review = &pebblestore.SessionPlanCheckpointReview{}
			}
			if normalizePlanCheckpointReviewStatus(checkpoint.Review.Status) != PlanCheckpointReviewStatusApproved {
				checkpoint.Review.Status = PlanCheckpointReviewStatusPending
			}
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

func ApplyPlanCheckpointReviewAcceptance(doc *pebblestore.SessionPlanDocument, options PlanCheckpointReviewAcceptanceOptions) (PlanExecutionSummary, error) {
	if doc == nil {
		return PlanExecutionSummary{}, errors.New("plan document is required")
	}
	normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
	normalizePlanExecutionState(doc.ExecutionState)
	checkpointID := strings.TrimSpace(options.CheckpointID)
	if checkpointID == "" {
		checkpointID = strings.TrimSpace(doc.ActiveCheckpointID)
	}
	if checkpointID == "" {
		return PlanExecutionSummary{}, errors.New("checkpoint_id is required")
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return PlanExecutionSummary{}, fmt.Errorf("plan document checkpoint %q was not found", checkpointID)
	}
	checkpoint := &doc.Checkpoints[idx]
	status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
	if status != PlanCheckpointStatusCompleted && status != PlanCheckpointStatusNeedsReview {
		return PlanExecutionSummary{}, fmt.Errorf("checkpoint %q status %q is not waiting for review", checkpointID, status)
	}
	if checkpoint.Review == nil {
		checkpoint.Review = &pebblestore.SessionPlanCheckpointReview{}
	}
	checkpoint.Review.Status = PlanCheckpointReviewStatusApproved
	checkpoint.Review.ReviewerID = strings.TrimSpace(options.ReviewerID)
	checkpoint.Review.ReviewerType = strings.TrimSpace(options.ReviewerType)
	checkpoint.Review.Result = strings.TrimSpace(options.Result)
	checkpoint.Review.Notes = strings.TrimSpace(options.Notes)
	if options.ReviewedAt > 0 {
		checkpoint.Review.ReviewedAt = options.ReviewedAt
	}
	if status == PlanCheckpointStatusNeedsReview {
		checkpoint.Status = PlanCheckpointStatusCompleted
	}
	if doc.ExecutionState == nil {
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{}
	}
	doc.ExecutionState.LastCheckpointID = checkpointID
	doc.ExecutionState.LastOutcome = PlanCheckpointStatusCompleted
	if options.ReviewedAt > 0 {
		doc.ExecutionState.UpdatedAt = options.ReviewedAt
	}
	if allPlanCheckpointsCompleted(doc.Checkpoints) {
		doc.ActiveCheckpointID = ""
		doc.ExecutionState.Status = PlanExecutionStateCompleted
		doc.ExecutionState.ActiveAttemptID = ""
		doc.ExecutionState.CurrentRunID = ""
		doc.ExecutionState.CurrentSessionID = ""
		if options.ReviewedAt > 0 {
			doc.ExecutionState.CompletedAt = options.ReviewedAt
		}
		return SummarizePlanExecution(doc), nil
	}
	summary := SummarizePlanExecution(doc)
	if summary.PlanComplete {
		doc.ActiveCheckpointID = ""
		doc.ExecutionState.Status = PlanExecutionStateCompleted
		if options.ReviewedAt > 0 {
			doc.ExecutionState.CompletedAt = options.ReviewedAt
		}
		return SummarizePlanExecution(doc), nil
	}
	if summary.NextCheckpointID != "" {
		doc.ActiveCheckpointID = summary.NextCheckpointID
		doc.ExecutionState.Status = PlanExecutionStateIdle
		doc.ExecutionState.ActiveAttemptID = ""
		doc.ExecutionState.CurrentRunID = ""
		doc.ExecutionState.CurrentSessionID = ""
	}
	return SummarizePlanExecution(doc), nil
}

// RebindInProgressPlanForUserMessage transfers an already-resumed checkpoint
// to a later parent turn after its prior owning run has ended. Same-checkpoint
// refinements preserve checkpoint and attempt identity, so a plain subsequent
// user continuation must refresh only the active run ownership.
func RebindInProgressPlanForUserMessage(doc *pebblestore.SessionPlanDocument, runID, runSessionID, parentSessionID string, updatedAt int64) (PlanExecutionSummary, bool, error) {
	if doc == nil || doc.ExecutionState == nil || normalizePlanExecutionStateStatus(doc.ExecutionState.Status) != PlanExecutionStateInProgress {
		return SummarizePlanExecution(doc), false, nil
	}
	checkpointID := strings.TrimSpace(doc.ActiveCheckpointID)
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if checkpointID == "" || idx < 0 {
		return PlanExecutionSummary{}, false, errors.New("in-progress plan has no active checkpoint to rebind")
	}
	checkpoint := &doc.Checkpoints[idx]
	if normalizePlanCheckpointStatusForSave(checkpoint.Status) != PlanCheckpointStatusInProgress {
		return PlanExecutionSummary{}, false, fmt.Errorf("in-progress plan active checkpoint %q is %q", checkpointID, checkpoint.Status)
	}
	runID = strings.TrimSpace(runID)
	runSessionID = strings.TrimSpace(runSessionID)
	parentSessionID = strings.TrimSpace(parentSessionID)
	if runID == "" || runSessionID == "" || parentSessionID == "" || strings.TrimSpace(checkpoint.AttemptID) == "" {
		return PlanExecutionSummary{}, false, errors.New("in-progress plan rebind requires complete run and attempt ownership")
	}
	if strings.TrimSpace(checkpoint.RunID) == runID && strings.TrimSpace(checkpoint.SessionID) == runSessionID && strings.TrimSpace(doc.ExecutionState.CurrentRunID) == runID && strings.TrimSpace(doc.ExecutionState.CurrentSessionID) == runSessionID && strings.TrimSpace(doc.ExecutionState.ParentSessionID) == parentSessionID {
		return SummarizePlanExecution(doc), false, nil
	}
	checkpoint.RunID = runID
	checkpoint.SessionID = runSessionID
	doc.ExecutionState.ActiveAttemptID = strings.TrimSpace(checkpoint.AttemptID)
	doc.ExecutionState.CurrentRunID = runID
	doc.ExecutionState.CurrentSessionID = runSessionID
	doc.ExecutionState.ParentSessionID = parentSessionID
	if updatedAt > 0 {
		doc.ExecutionState.UpdatedAt = updatedAt
	}
	if err := ValidatePlanDocument(doc); err != nil {
		return PlanExecutionSummary{}, false, err
	}
	return SummarizePlanExecution(doc), true, nil
}

// ReactivatePausedPlanForUserMessage makes a paused checkpoint processable by
// the new parent turn and assigns that turn a new attempt while preserving the
// paused attempt in history. The provider that receives the message owns the
// decision to continue, refine, or redirect the durable plan.
func ReactivatePausedPlanForUserMessage(doc *pebblestore.SessionPlanDocument, runID, runSessionID, parentSessionID string, startedAt int64) (PlanExecutionSummary, bool, error) {
	if doc == nil || doc.ExecutionState == nil || normalizePlanExecutionStateStatus(doc.ExecutionState.Status) != PlanExecutionStatePaused {
		return SummarizePlanExecution(doc), false, nil
	}
	checkpointID := strings.TrimSpace(doc.ActiveCheckpointID)
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if checkpointID == "" || idx < 0 {
		return PlanExecutionSummary{}, false, errors.New("paused plan has no active checkpoint to reactivate")
	}
	checkpoint := &doc.Checkpoints[idx]
	if normalizePlanCheckpointStatusForSave(checkpoint.Status) != PlanCheckpointStatusPaused {
		return PlanExecutionSummary{}, false, fmt.Errorf("paused plan active checkpoint %q is %q", checkpointID, checkpoint.Status)
	}
	runID = strings.TrimSpace(runID)
	runSessionID = strings.TrimSpace(runSessionID)
	parentSessionID = strings.TrimSpace(parentSessionID)
	if runID == "" || runSessionID == "" || parentSessionID == "" {
		return PlanExecutionSummary{}, false, errors.New("paused plan reactivation requires complete run ownership")
	}
	attemptID := fmt.Sprintf("%s:attempt-%d", checkpointID, len(checkpoint.Attempts)+1)
	checkpoint.Status = PlanCheckpointStatusInProgress
	checkpoint.CompletedAt = 0
	checkpoint.AttemptID = attemptID
	checkpoint.RunID = runID
	checkpoint.SessionID = runSessionID
	for i := range checkpoint.Subtasks {
		if checkpoint.Subtasks[i].Status == PlanSubtaskStatusInProgress {
			checkpoint.Subtasks[i].Status = PlanSubtaskStatusPending
			checkpoint.Subtasks[i].CompletedAt = 0
		}
	}
	checkpoint.ActiveSubtaskID = ""
	upsertPlanCheckpointAttempt(checkpoint, pebblestore.SessionPlanCheckpointAttempt{ID: attemptID, CheckpointID: checkpointID, Status: PlanCheckpointStatusInProgress, RunID: runID, SessionID: runSessionID, ParentSessionID: parentSessionID, StartedAt: startedAt})
	doc.ExecutionState.Status = PlanExecutionStateInProgress
	doc.ExecutionState.ActiveAttemptID = attemptID
	doc.ExecutionState.CurrentRunID = runID
	doc.ExecutionState.CurrentSessionID = runSessionID
	doc.ExecutionState.ParentSessionID = parentSessionID
	doc.ExecutionState.CompletedAt = 0
	if startedAt > 0 {
		doc.ExecutionState.UpdatedAt = startedAt
	}
	if err := ValidatePlanDocument(doc); err != nil {
		return PlanExecutionSummary{}, false, err
	}
	return SummarizePlanExecution(doc), true, nil
}

func ApplyPlanCheckpointReset(doc *pebblestore.SessionPlanDocument, options PlanCheckpointResetOptions) (PlanExecutionSummary, error) {
	if doc == nil {
		return PlanExecutionSummary{}, errors.New("plan document is required")
	}
	normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
	checkpointID := strings.TrimSpace(options.CheckpointID)
	if checkpointID == "" {
		checkpointID = strings.TrimSpace(doc.ActiveCheckpointID)
	}
	if checkpointID == "" {
		_, summary, ok := SelectNextPlanCheckpoint(doc)
		if !ok {
			return summary, errors.New("no checkpoint is available to reset")
		}
		checkpointID = summary.NextCheckpointID
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return PlanExecutionSummary{}, fmt.Errorf("plan document checkpoint %q was not found", checkpointID)
	}
	end := idx + 1
	if options.Rewind {
		end = len(doc.Checkpoints)
	}
	for i := idx; i < end; i++ {
		resetPlanCheckpointRuntimeForFreshStart(&doc.Checkpoints[i])
	}
	doc.ActiveCheckpointID = checkpointID
	doc.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateIdle}
	return SummarizePlanExecution(doc), nil
}

// ApplyPlanCheckpointCancellation pauses only the active checkpoint attempt
// owned by the user-cancelled run. It never invents successful work, and the
// existing checkpoint restart transition remains the explicit retry path.
func ApplyPlanCheckpointCancellation(doc *pebblestore.SessionPlanDocument, options PlanCheckpointCancellationOptions) (PlanCheckpointCancellationDecision, error) {
	if doc == nil {
		return PlanCheckpointCancellationDecision{}, errors.New("plan document is required")
	}
	planID := strings.TrimSpace(options.PlanID)
	if planID != "" && strings.TrimSpace(doc.ID) != planID {
		return PlanCheckpointCancellationDecision{}, nil
	}
	checkpointID := strings.TrimSpace(options.CheckpointID)
	attemptID := strings.TrimSpace(options.AttemptID)
	runID := strings.TrimSpace(options.RunID)
	runSessionID := strings.TrimSpace(options.SessionID)
	parentSessionID := strings.TrimSpace(options.ParentSessionID)
	if checkpointID == "" || attemptID == "" || runID == "" || runSessionID == "" {
		return PlanCheckpointCancellationDecision{}, nil
	}
	if strings.TrimSpace(doc.ActiveCheckpointID) != checkpointID || doc.ExecutionState == nil {
		return PlanCheckpointCancellationDecision{}, nil
	}
	state := doc.ExecutionState
	if normalizePlanExecutionStateStatus(state.Status) != PlanExecutionStateInProgress ||
		strings.TrimSpace(state.ActiveAttemptID) != attemptID ||
		strings.TrimSpace(state.CurrentRunID) != runID ||
		strings.TrimSpace(state.CurrentSessionID) != runSessionID ||
		(parentSessionID != "" && strings.TrimSpace(state.ParentSessionID) != parentSessionID) {
		return PlanCheckpointCancellationDecision{}, nil
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return PlanCheckpointCancellationDecision{}, nil
	}
	checkpoint := &doc.Checkpoints[idx]
	if normalizePlanCheckpointStatusForSave(checkpoint.Status) != PlanCheckpointStatusInProgress ||
		strings.TrimSpace(checkpoint.AttemptID) != attemptID ||
		strings.TrimSpace(checkpoint.RunID) != runID ||
		strings.TrimSpace(checkpoint.SessionID) != runSessionID {
		return PlanCheckpointCancellationDecision{}, nil
	}
	attemptIdx := -1
	for i := range checkpoint.Attempts {
		attempt := &checkpoint.Attempts[i]
		if strings.TrimSpace(attempt.ID) != attemptID {
			continue
		}
		if normalizePlanCheckpointStatus(attempt.Status) != PlanCheckpointStatusInProgress ||
			strings.TrimSpace(attempt.RunID) != runID ||
			strings.TrimSpace(attempt.SessionID) != runSessionID ||
			(parentSessionID != "" && strings.TrimSpace(attempt.ParentSessionID) != parentSessionID) {
			return PlanCheckpointCancellationDecision{}, nil
		}
		attemptIdx = i
		break
	}
	if attemptIdx < 0 {
		return PlanCheckpointCancellationDecision{}, nil
	}

	cancelledAt := options.CancelledAt
	reason := strings.TrimSpace(options.Reason)
	if reason == "" {
		reason = "Run cancelled. Restart the checkpoint to retry."
	}
	checkpoint.Status = PlanCheckpointStatusPaused
	checkpoint.Report = reason
	checkpoint.Result = "run_paused"
	for i := range checkpoint.Subtasks {
		if checkpoint.Subtasks[i].Status == PlanSubtaskStatusInProgress {
			checkpoint.Subtasks[i].Status = PlanSubtaskStatusPending
			checkpoint.Subtasks[i].CompletedAt = 0
		}
	}
	checkpoint.ActiveSubtaskID = ""
	if cancelledAt > 0 {
		checkpoint.CompletedAt = cancelledAt
	}
	attempt := &checkpoint.Attempts[attemptIdx]
	attempt.Status = PlanCheckpointStatusPaused
	attempt.Outcome = PlanCheckpointStatusPaused
	attempt.Report = reason
	attempt.Result = "run_paused"
	if cancelledAt > 0 {
		attempt.CompletedAt = cancelledAt
	}
	state.Status = PlanExecutionStatePaused
	state.LastCheckpointID = checkpointID
	state.LastAttemptID = attemptID
	state.LastOutcome = PlanCheckpointStatusPaused
	state.ActiveAttemptID = ""
	state.CurrentRunID = ""
	state.CurrentSessionID = ""
	if cancelledAt > 0 {
		state.UpdatedAt = cancelledAt
	}
	if err := ValidatePlanDocument(doc); err != nil {
		return PlanCheckpointCancellationDecision{}, err
	}
	return PlanCheckpointCancellationDecision{CheckpointID: checkpointID, AttemptID: attemptID, Changed: true}, nil
}

func ApplyPlanCheckpointBlockResolution(doc *pebblestore.SessionPlanDocument, options PlanCheckpointBlockResolutionOptions) (PlanExecutionSummary, error) {
	if doc == nil {
		return PlanExecutionSummary{}, errors.New("plan document is required")
	}
	normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
	normalizePlanExecutionState(doc.ExecutionState)
	checkpointID := strings.TrimSpace(options.CheckpointID)
	if checkpointID == "" {
		checkpointID = strings.TrimSpace(doc.ActiveCheckpointID)
	}
	if checkpointID == "" {
		return PlanExecutionSummary{}, errors.New("checkpoint_id is required")
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return PlanExecutionSummary{}, fmt.Errorf("plan document checkpoint %q was not found", checkpointID)
	}
	checkpoint := &doc.Checkpoints[idx]
	status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
	if status != PlanCheckpointStatusBlocked {
		return PlanExecutionSummary{}, fmt.Errorf("checkpoint %q status %q is not blocked", checkpointID, status)
	}
	if doc.ExecutionState != nil && normalizePlanExecutionStateStatus(doc.ExecutionState.Status) == PlanExecutionStateInProgress {
		return PlanExecutionSummary{}, errors.New("resolve blocked checkpoint requires no active in-progress run")
	}
	resolvedAt := options.ResolvedAt
	resolutionResult := strings.TrimSpace(options.Result)
	resolutionNotes := strings.TrimSpace(options.Notes)
	resolutionContext := "Blocker confirmed resolved. Resume this checkpoint and decide its normal outcome after finishing any remaining work."
	if resolutionResult != "" {
		resolutionContext += "\nResolution: " + resolutionResult
	}
	if resolutionNotes != "" {
		resolutionContext += "\nContext: " + resolutionNotes
	}
	if existingReport := strings.TrimSpace(checkpoint.Report); existingReport != "" {
		checkpoint.Report = existingReport + "\n\n" + resolutionContext
	} else {
		checkpoint.Report = resolutionContext
	}
	checkpoint.Result = "blocker_resolved_resume_required"
	checkpoint.Status = PlanCheckpointStatusPending
	checkpoint.CompletedAt = 0
	checkpoint.Review = nil
	checkpoint.AttemptID = ""
	checkpoint.RunID = ""
	checkpoint.SessionID = ""
	for i := range checkpoint.Subtasks {
		if checkpoint.Subtasks[i].Status == PlanSubtaskStatusInProgress {
			checkpoint.Subtasks[i].Status = PlanSubtaskStatusPending
			checkpoint.Subtasks[i].CompletedAt = 0
		}
	}
	checkpoint.ActiveSubtaskID = ""
	if doc.ExecutionState == nil {
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{}
	}
	doc.ActiveCheckpointID = checkpointID
	doc.ExecutionState.Status = PlanExecutionStateIdle
	doc.ExecutionState.ActiveAttemptID = ""
	doc.ExecutionState.CurrentRunID = ""
	doc.ExecutionState.CurrentSessionID = ""
	if resolvedAt > 0 {
		doc.ExecutionState.UpdatedAt = resolvedAt
	}
	return SummarizePlanExecution(doc), nil
}

func resetPlanCheckpointRuntimeForFreshStart(checkpoint *pebblestore.SessionPlanCheckpoint) {
	if checkpoint == nil {
		return
	}
	checkpoint.Status = PlanCheckpointStatusPending
	checkpoint.Report = ""
	checkpoint.Result = ""
	checkpoint.ChangedFiles = nil
	checkpoint.Validation = nil
	checkpoint.Recommendation = nil
	checkpoint.Handoff = nil
	checkpoint.AttemptID = ""
	checkpoint.RunID = ""
	checkpoint.SessionID = ""
	checkpoint.StartedAt = 0
	checkpoint.CompletedAt = 0
	checkpoint.Review = nil
	checkpoint.Attempts = nil
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
