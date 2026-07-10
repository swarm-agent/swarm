package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// NormalizePlanDocumentForSave returns the structured one-plan document that
// should be stored with the next plan revision. A nil incoming document means
// the caller did not edit the structured model, so the existing document is
// preserved. A non-nil incoming document is normalized from the enclosing plan
// identity/title where needed and validated before it is saved.
func NormalizePlanDocumentForSave(planID, title string, incoming, existing *pebblestore.SessionPlanDocument) (*pebblestore.SessionPlanDocument, error) {
	planID = strings.TrimSpace(planID)
	title = strings.TrimSpace(title)
	if incoming == nil {
		doc := clonePlanDocument(existing)
		if doc != nil {
			doc.ExecutionOrigin = NormalizePlanExecutionOrigin(doc.ExecutionOrigin)
			restoreOriginalCheckpointsForCheckpointedExecution(doc)
			normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
		}
		return doc, nil
	}
	doc := clonePlanDocument(incoming)
	if doc == nil {
		return nil, nil
	}
	doc.ID = strings.TrimSpace(doc.ID)
	if doc.ID == "" {
		doc.ID = planID
	}
	doc.Title = strings.TrimSpace(doc.Title)
	if doc.Title == "" {
		doc.Title = title
	}
	doc.Status = strings.TrimSpace(doc.Status)
	doc.SchemaVersion = strings.TrimSpace(doc.SchemaVersion)
	doc.RevisionID = strings.TrimSpace(doc.RevisionID)
	doc.ActiveCheckpointID = strings.TrimSpace(doc.ActiveCheckpointID)
	doc.ExecutionOrigin = NormalizePlanExecutionOrigin(doc.ExecutionOrigin)
	doc.RenderedText = strings.TrimSpace(doc.RenderedText)
	doc.DisplayText = strings.TrimSpace(doc.DisplayText)
	trimPlanInfo(&doc.Info)
	for i := range doc.Checkpoints {
		trimPlanCheckpoint(&doc.Checkpoints[i])
		if doc.Checkpoints[i].Order == 0 {
			doc.Checkpoints[i].Order = i + 1
		}
	}
	for i := range doc.OriginalCheckpoints {
		trimPlanCheckpoint(&doc.OriginalCheckpoints[i])
		if doc.OriginalCheckpoints[i].Order == 0 {
			doc.OriginalCheckpoints[i].Order = i + 1
		}
	}
	restoreOriginalCheckpointsForCheckpointedExecution(doc)
	normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
	normalizePlanExecutionState(doc.ExecutionState)
	if doc.ActiveCheckpointID == "" && doc.ExecutionPolicy.Shape == PlanExecutionShapeCheckpointed {
		doc.ActiveCheckpointID = defaultActiveCheckpointID(doc.Checkpoints)
	}
	if err := ValidatePlanDocument(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func ValidatePlanDocument(doc *pebblestore.SessionPlanDocument) error {
	if doc == nil {
		return nil
	}
	if strings.TrimSpace(doc.ID) == "" {
		return errors.New("plan document id is required")
	}
	if strings.TrimSpace(doc.Title) == "" {
		return errors.New("plan document title is required")
	}
	if err := validatePlanExecutionPolicy(doc.ExecutionPolicy); err != nil {
		return err
	}
	if err := validatePlanExecutionState(doc.ExecutionState); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(doc.Checkpoints))
	for i, checkpoint := range doc.Checkpoints {
		id := strings.TrimSpace(checkpoint.ID)
		if id == "" {
			return fmt.Errorf("plan document checkpoint at index %d requires id", i)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("plan document checkpoint id %q is duplicated", id)
		}
		seen[id] = struct{}{}
		if err := validatePlanCheckpointRuntime(checkpoint); err != nil {
			return err
		}
	}
	activeID := strings.TrimSpace(doc.ActiveCheckpointID)
	if activeID != "" {
		if _, ok := seen[activeID]; !ok {
			return fmt.Errorf("plan document active_checkpoint_id %q does not match a checkpoint", activeID)
		}
	}
	if err := validatePlanCheckpointContinuity(doc); err != nil {
		return err
	}
	return nil
}

// PlanDocumentPatch is the structured, modular edit format for one-plan
// documents. The service applies the whole patch atomically and stores exactly
// one normal plan revision for the accepted update.
type PlanDocumentPatch struct {
	Operation          string                                           `json:"operation,omitempty"`
	Info               *pebblestore.SessionPlanInfo                     `json:"info,omitempty"`
	InfoFields         map[string]json.RawMessage                       `json:"-"`
	ExecutionPolicy    *pebblestore.SessionPlanExecutionPolicy          `json:"execution_policy,omitempty"`
	ExecutionState     *pebblestore.SessionPlanExecutionState           `json:"execution_state,omitempty"`
	Checkpoint         *pebblestore.SessionPlanCheckpoint               `json:"checkpoint,omitempty"`
	CheckpointFields   map[string]json.RawMessage                       `json:"-"`
	CheckpointID       string                                           `json:"checkpoint_id,omitempty"`
	CheckpointOrder    []string                                         `json:"checkpoint_order,omitempty"`
	Subtask            *pebblestore.SessionPlanSubtask                  `json:"subtask,omitempty"`
	SubtaskID          string                                           `json:"subtask_id,omitempty"`
	SubtaskOrder       []string                                         `json:"subtask_order,omitempty"`
	ActiveCheckpointID string                                           `json:"active_checkpoint_id,omitempty"`
	Status             string                                           `json:"status,omitempty"`
	AttemptID          string                                           `json:"attempt_id,omitempty"`
	RunID              string                                           `json:"run_id,omitempty"`
	RunSessionID       string                                           `json:"run_session_id,omitempty"`
	ParentSessionID    string                                           `json:"parent_session_id,omitempty"`
	StartedAt          int64                                            `json:"started_at,omitempty"`
	CompletedAt        int64                                            `json:"completed_at,omitempty"`
	Notes              string                                           `json:"notes,omitempty"`
	Report             string                                           `json:"report,omitempty"`
	Result             string                                           `json:"result,omitempty"`
	ChangedFiles       []string                                         `json:"changed_files,omitempty"`
	Validation         []string                                         `json:"validation,omitempty"`
	Recommendation     *pebblestore.SessionPlanCheckpointRecommendation `json:"recommendation,omitempty"`
	Operations         []PlanDocumentPatchOperation                     `json:"operations,omitempty"`
}

type PlanDocumentPatchOperation = PlanDocumentPatch

func (p *PlanDocumentPatch) UnmarshalJSON(raw []byte) error {
	type alias PlanDocumentPatch
	var base alias
	if err := json.Unmarshal(raw, &base); err != nil {
		return err
	}
	*p = PlanDocumentPatch(base)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if infoRaw, ok := payload["info"]; ok && len(infoRaw) > 0 && string(infoRaw) != "null" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(infoRaw, &fields); err != nil {
			return err
		}
		var info pebblestore.SessionPlanInfo
		if err := json.Unmarshal(infoRaw, &info); err != nil {
			return err
		}
		if rawScope, ok := fields["scope"]; ok && len(rawScope) > 0 {
			var scope string
			if json.Unmarshal(rawScope, &scope) == nil {
				info.Scope = scope
			}
		}
		p.Info = &info
		p.InfoFields = fields
	}
	if checkpointRaw, ok := payload["checkpoint"]; ok && len(checkpointRaw) > 0 && string(checkpointRaw) != "null" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(checkpointRaw, &fields); err != nil {
			return err
		}
		var checkpoint pebblestore.SessionPlanCheckpoint
		if err := json.Unmarshal(checkpointRaw, &checkpoint); err != nil {
			return err
		}
		p.Checkpoint = &checkpoint
		p.CheckpointFields = fields
	}
	return nil
}

func (p PlanDocumentPatch) IsZero() bool {
	return strings.TrimSpace(p.Operation) == "" && p.Info == nil && len(p.InfoFields) == 0 && p.ExecutionPolicy == nil && p.ExecutionState == nil && p.Checkpoint == nil && len(p.CheckpointFields) == 0 && strings.TrimSpace(p.CheckpointID) == "" && len(p.CheckpointOrder) == 0 && p.Subtask == nil && strings.TrimSpace(p.SubtaskID) == "" && len(p.SubtaskOrder) == 0 && strings.TrimSpace(p.ActiveCheckpointID) == "" && strings.TrimSpace(p.Status) == "" && strings.TrimSpace(p.AttemptID) == "" && strings.TrimSpace(p.RunID) == "" && strings.TrimSpace(p.RunSessionID) == "" && strings.TrimSpace(p.ParentSessionID) == "" && p.StartedAt == 0 && p.CompletedAt == 0 && strings.TrimSpace(p.Notes) == "" && strings.TrimSpace(p.Report) == "" && strings.TrimSpace(p.Result) == "" && len(p.ChangedFiles) == 0 && len(p.Validation) == 0 && len(p.Operations) == 0
}

func ApplyPlanDocumentPatch(planID, title string, existing *pebblestore.SessionPlanDocument, patch PlanDocumentPatch) (*pebblestore.SessionPlanDocument, error) {
	if patch.IsZero() {
		return nil, errors.New("plan document patch requires at least one structured operation")
	}
	doc := clonePlanDocument(existing)
	if doc == nil {
		doc = &pebblestore.SessionPlanDocument{ID: strings.TrimSpace(planID), Title: strings.TrimSpace(title)}
	}
	ops := patch.Operations
	if len(ops) == 0 {
		ops = []PlanDocumentPatchOperation{patch}
	}
	for _, op := range ops {
		if err := applyPlanDocumentPatchOperation(doc, op); err != nil {
			return nil, err
		}
	}
	return NormalizePlanDocumentForSave(planID, title, doc, nil)
}

func applyPlanDocumentPatchOperation(doc *pebblestore.SessionPlanDocument, op PlanDocumentPatchOperation) error {
	operation := strings.ToLower(strings.TrimSpace(op.Operation))
	operation = strings.ReplaceAll(operation, "-", "_")
	if operation == "" {
		switch {
		case op.Info != nil || len(op.InfoFields) > 0:
			operation = "update_info"
		case op.ExecutionPolicy != nil:
			operation = "update_execution_policy"
		case op.ExecutionState != nil:
			operation = "update_execution_state"
		case op.Checkpoint != nil:
			operation = "upsert_checkpoint"
		case len(op.CheckpointOrder) > 0:
			operation = "reorder_checkpoints"
		case op.Subtask != nil:
			operation = "add_subtask"
		case len(op.SubtaskOrder) > 0:
			operation = "reorder_subtasks"
		case strings.TrimSpace(op.ActiveCheckpointID) != "":
			operation = "set_active_checkpoint"
		default:
			return errors.New("plan document patch operation is required")
		}
	}
	switch operation {
	case "update_info", "patch_info":
		if op.Info == nil && len(op.InfoFields) == 0 {
			return errors.New("update_info plan document patch requires info")
		}
		if err := mergePlanInfoPatch(&doc.Info, op.Info, op.InfoFields); err != nil {
			return err
		}
		trimPlanInfo(&doc.Info)
		return nil
	case "replace_info", "set_info":
		if op.Info == nil {
			return errors.New("replace_info plan document patch requires info")
		}
		doc.Info = *op.Info
		trimPlanInfo(&doc.Info)
		return nil
	case "update_execution_policy", "set_execution_policy", "execution_policy":
		if op.ExecutionPolicy == nil {
			return errors.New("update_execution_policy plan document patch requires execution_policy")
		}
		doc.ExecutionPolicy = *op.ExecutionPolicy
		normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
		return nil
	case "update_execution_state", "set_execution_state", "execution_state":
		if op.ExecutionState == nil {
			return errors.New("update_execution_state plan document patch requires execution_state")
		}
		state := *op.ExecutionState
		doc.ExecutionState = &state
		normalizePlanExecutionState(doc.ExecutionState)
		return nil
	case "upsert_checkpoint", "replace_checkpoint", "set_checkpoint":
		if op.Checkpoint == nil {
			return errors.New("upsert_checkpoint plan document patch requires checkpoint")
		}
		checkpoint := *op.Checkpoint
		trimPlanCheckpoint(&checkpoint)
		if checkpoint.ID == "" {
			return errors.New("upsert_checkpoint plan document patch requires checkpoint.id")
		}
		if checkpoint.Status == PlanCheckpointStatusInProgress {
			if inProgressID, _, ok := findInProgressPlanCheckpoint(doc.Checkpoints, checkpoint.ID); ok {
				return fmt.Errorf("cannot upsert checkpoint %q as in_progress while checkpoint %q is in_progress; resolve it first", checkpoint.ID, inProgressID)
			}
		}
		idx := findPlanCheckpointIndex(doc.Checkpoints, checkpoint.ID)
		if idx >= 0 {
			if checkpoint.Order == 0 {
				checkpoint.Order = doc.Checkpoints[idx].Order
			}
			doc.Checkpoints[idx] = checkpoint
		} else {
			if checkpoint.Order == 0 {
				checkpoint.Order = len(doc.Checkpoints) + 1
			}
			doc.Checkpoints = append(doc.Checkpoints, checkpoint)
		}
		normalizeCheckpointOrder(doc)
		return nil
	case "update_checkpoint", "patch_checkpoint":
		id := strings.TrimSpace(firstNonBlank(op.CheckpointID, checkpointIDFromPatch(op.Checkpoint)))
		if id == "" {
			return errors.New("update_checkpoint plan document patch requires checkpoint_id")
		}
		idx := findPlanCheckpointIndex(doc.Checkpoints, id)
		if idx < 0 {
			return fmt.Errorf("plan document checkpoint %q was not found", id)
		}
		if op.Checkpoint != nil {
			if err := mergePlanCheckpointPatch(&doc.Checkpoints[idx], op.Checkpoint, op.CheckpointFields, id); err != nil {
				return err
			}
		}
		applyCheckpointCompletionFields(&doc.Checkpoints[idx], op, false)
		return nil
	case "start_checkpoint", "continue_checkpoint", "advance_checkpoint", "next_checkpoint":
		id := strings.TrimSpace(firstNonBlank(op.CheckpointID, checkpointIDFromPatch(op.Checkpoint)))
		_, err := ApplyPlanCheckpointStart(doc, PlanCheckpointStartOptions{
			CheckpointID:    id,
			AttemptID:       op.AttemptID,
			RunID:           op.RunID,
			SessionID:       op.RunSessionID,
			ParentSessionID: op.ParentSessionID,
			StartedAt:       op.StartedAt,
		})
		return err
	case "complete_checkpoint", "finish_checkpoint":
		id := strings.TrimSpace(firstNonBlank(op.CheckpointID, checkpointIDFromPatch(op.Checkpoint)))
		if id == "" {
			id = strings.TrimSpace(doc.ActiveCheckpointID)
		}
		if id == "" {
			return errors.New("complete_checkpoint plan document patch requires checkpoint_id or active_checkpoint_id")
		}
		_, err := ApplyPlanCheckpointOutcome(doc, PlanCheckpointOutcomeOptions{
			CheckpointID:    id,
			Outcome:         firstNonBlank(op.Status, PlanCheckpointStatusCompleted),
			AttemptID:       op.AttemptID,
			RunID:           op.RunID,
			SessionID:       op.RunSessionID,
			ParentSessionID: op.ParentSessionID,
			Report:          op.Report,
			Result:          op.Result,
			ChangedFiles:    op.ChangedFiles,
			Validation:      op.Validation,
			Recommendation:  op.Recommendation,
			StartedAt:       op.StartedAt,
			CompletedAt:     op.CompletedAt,
		})
		return err
	case "checkpoint_outcome", "mark_checkpoint_outcome", "mark_checkpoint", "finish_checkpoint_with_outcome", "mark_needs_review", "mark_completed", "mark_blocked", "mark_failed":
		id := strings.TrimSpace(firstNonBlank(op.CheckpointID, checkpointIDFromPatch(op.Checkpoint)))
		if id == "" {
			id = strings.TrimSpace(doc.ActiveCheckpointID)
		}
		if id == "" {
			return errors.New("checkpoint_outcome plan document patch requires checkpoint_id or active_checkpoint_id")
		}
		outcome := op.Status
		switch operation {
		case "mark_needs_review":
			outcome = PlanCheckpointStatusNeedsReview
		case "mark_completed":
			outcome = PlanCheckpointStatusCompleted
		case "mark_blocked":
			outcome = PlanCheckpointStatusBlocked
		case "mark_failed":
			outcome = PlanCheckpointStatusFailed
		}
		_, err := ApplyPlanCheckpointOutcome(doc, PlanCheckpointOutcomeOptions{
			CheckpointID:    id,
			Outcome:         outcome,
			AttemptID:       op.AttemptID,
			RunID:           op.RunID,
			SessionID:       op.RunSessionID,
			ParentSessionID: op.ParentSessionID,
			Report:          op.Report,
			Result:          op.Result,
			ChangedFiles:    op.ChangedFiles,
			Validation:      op.Validation,
			Recommendation:  op.Recommendation,
			StartedAt:       op.StartedAt,
			CompletedAt:     op.CompletedAt,
		})
		return err
	case "accept_checkpoint_review", "approve_checkpoint":
		id := strings.TrimSpace(firstNonBlank(op.CheckpointID, checkpointIDFromPatch(op.Checkpoint)))
		if id == "" {
			id = strings.TrimSpace(doc.ActiveCheckpointID)
		}
		_, err := ApplyPlanCheckpointReviewAcceptance(doc, PlanCheckpointReviewAcceptanceOptions{
			CheckpointID: id,
			Result:       op.Result,
			Notes:        firstNonBlank(op.Notes, op.Report),
			ReviewedAt:   op.CompletedAt,
		})
		return err
	case "restart_checkpoint", "retry_checkpoint", "restart_checkpoint_from_zero", "reset_checkpoint":
		id := strings.TrimSpace(firstNonBlank(op.CheckpointID, checkpointIDFromPatch(op.Checkpoint)))
		_, err := ApplyPlanCheckpointReset(doc, PlanCheckpointResetOptions{CheckpointID: id})
		return err
	case "rewind_to_checkpoint", "rewind_checkpoint":
		id := strings.TrimSpace(firstNonBlank(op.CheckpointID, checkpointIDFromPatch(op.Checkpoint)))
		_, err := ApplyPlanCheckpointReset(doc, PlanCheckpointResetOptions{CheckpointID: id, Rewind: true})
		return err
	case "add_subtask", "create_subtask", "upsert_subtask":
		return addPlanCheckpointSubtask(doc, op)
	case "update_subtask", "patch_subtask":
		return updatePlanCheckpointSubtask(doc, op)
	case "focus_subtask", "set_active_subtask", "start_subtask":
		return focusPlanCheckpointSubtask(doc, op)
	case "complete_subtask", "finish_subtask":
		return completePlanCheckpointSubtask(doc, op)
	case "remove_subtask", "delete_subtask":
		return removePlanCheckpointSubtask(doc, op)
	case "reorder_subtasks":
		return reorderPlanCheckpointSubtasks(doc, op)
	case "remove_checkpoint", "delete_checkpoint":
		id := strings.TrimSpace(firstNonBlank(op.CheckpointID, checkpointIDFromPatch(op.Checkpoint)))
		if id == "" {
			return errors.New("remove_checkpoint plan document patch requires checkpoint_id")
		}
		idx := findPlanCheckpointIndex(doc.Checkpoints, id)
		if idx < 0 {
			return fmt.Errorf("plan document checkpoint %q was not found", id)
		}
		if strings.TrimSpace(doc.ActiveCheckpointID) == id {
			return fmt.Errorf("cannot remove active checkpoint %q", id)
		}
		doc.Checkpoints = append(doc.Checkpoints[:idx], doc.Checkpoints[idx+1:]...)
		normalizeCheckpointOrder(doc)
		return nil
	case "reorder_checkpoints", "reorder_checkpoint":
		return reorderPlanDocumentCheckpoints(doc, op.CheckpointOrder)
	case "set_active_checkpoint", "activate_checkpoint":
		id := strings.TrimSpace(firstNonBlank(op.ActiveCheckpointID, op.CheckpointID, checkpointIDFromPatch(op.Checkpoint)))
		if id == "" {
			return errors.New("set_active_checkpoint plan document patch requires active_checkpoint_id or checkpoint_id")
		}
		idx := findPlanCheckpointIndex(doc.Checkpoints, id)
		if idx < 0 {
			return fmt.Errorf("plan document checkpoint %q was not found", id)
		}
		if inProgressID, _, ok := findInProgressPlanCheckpoint(doc.Checkpoints, id); ok {
			return fmt.Errorf("cannot set active_checkpoint_id to %q while checkpoint %q is in_progress; resolve it first", id, inProgressID)
		}
		for i := 0; i < idx; i++ {
			checkpoint := doc.Checkpoints[i]
			status := normalizePlanCheckpointStatusForSave(checkpoint.Status)
			checkpointID := strings.TrimSpace(checkpoint.ID)
			if status != PlanCheckpointStatusCompleted {
				return fmt.Errorf("cannot set active_checkpoint_id to %q while earlier checkpoint %q status is %q; resolve it first", id, checkpointID, status)
			}
			if planCheckpointReviewPending(doc.ExecutionPolicy, checkpoint, i < len(doc.Checkpoints)-1) {
				return fmt.Errorf("cannot set active_checkpoint_id to %q while earlier checkpoint %q is waiting for review; resolve it first", id, checkpointID)
			}
		}
		doc.ActiveCheckpointID = id
		return nil
	default:
		return fmt.Errorf("unsupported plan document patch operation %q", op.Operation)
	}
}

func mergePlanInfoPatch(target *pebblestore.SessionPlanInfo, info *pebblestore.SessionPlanInfo, fields map[string]json.RawMessage) error {
	if target == nil {
		return errors.New("plan document info target is required")
	}
	if len(fields) == 0 {
		if info == nil {
			return nil
		}
		fields = infoFieldPresence(info)
	}
	if info == nil {
		info = &pebblestore.SessionPlanInfo{}
		if len(fields) > 0 {
			raw, err := json.Marshal(fields)
			if err != nil {
				return fmt.Errorf("update_info plan document patch info invalid: %w", err)
			}
			if err := json.Unmarshal(raw, info); err != nil {
				return fmt.Errorf("update_info plan document patch info invalid: %w", err)
			}
		}
	}
	for field, raw := range fields {
		switch normalizePlanInfoFieldName(field) {
		case "goal":
			target.Goal = stringFromPlanInfoRaw(raw, info.Goal)
		case "scope":
			target.Scope = stringFromPlanInfoRaw(raw, info.Scope)
		case "context":
			target.Context = stringFromPlanInfoRaw(raw, info.Context)
			if target.Scope == "" {
				target.Scope = target.Context
			}
		case "decisions":
			target.Decisions = stringSliceFromPlanInfoRaw(raw, info.Decisions)
		case "constraints":
			target.Constraints = stringSliceFromPlanInfoRaw(raw, info.Constraints)
		case "assumptions":
			target.Assumptions = stringSliceFromPlanInfoRaw(raw, info.Assumptions)
		case "open_questions":
			target.OpenQuestions = stringSliceFromPlanInfoRaw(raw, info.OpenQuestions)
		case "relevant_files":
			target.RelevantFiles = stringSliceFromPlanInfoRaw(raw, info.RelevantFiles)
		case "files":
			if values := stringSliceFromPlanInfoRaw(raw, nil); len(values) > 0 || strings.TrimSpace(string(raw)) == "[]" {
				target.RelevantFiles = values
			} else if value := stringFromPlanInfoRaw(raw, ""); value != "" {
				target.RelevantFiles = []string{value}
			}
		case "success_criteria":
			target.SuccessCriteria = stringSliceFromPlanInfoRaw(raw, info.SuccessCriteria)
		case "validation_strategy":
			target.ValidationStrategy = stringFromPlanInfoRaw(raw, info.ValidationStrategy)
		case "validation":
			if value := stringFromPlanInfoRaw(raw, ""); value != "" || strings.TrimSpace(string(raw)) == `""` {
				target.ValidationStrategy = value
			} else if values := stringSliceFromPlanInfoRaw(raw, nil); len(values) > 0 {
				target.ValidationStrategy = strings.Join(values, "; ")
			}
		}
	}
	return nil
}

func stringFromPlanInfoRaw(raw json.RawMessage, fallback string) string {
	var value string
	if len(raw) > 0 && json.Unmarshal(raw, &value) == nil {
		return value
	}
	return fallback
}

func stringSliceFromPlanInfoRaw(raw json.RawMessage, fallback []string) []string {
	if len(raw) > 0 {
		var values []string
		if json.Unmarshal(raw, &values) == nil {
			return values
		}
	}
	return cloneStringSlice(fallback)
}

func mergePlanCheckpointPatch(target *pebblestore.SessionPlanCheckpoint, checkpoint *pebblestore.SessionPlanCheckpoint, fields map[string]json.RawMessage, targetID string) error {
	if target == nil {
		return errors.New("plan document checkpoint target is required")
	}
	if checkpoint == nil {
		return nil
	}
	if len(fields) == 0 {
		fields = checkpointFieldPresence(checkpoint)
	}
	trimmed := *checkpoint
	trimPlanCheckpoint(&trimmed)
	if trimmed.ID == "" {
		trimmed.ID = strings.TrimSpace(targetID)
	}
	if trimmed.ID != strings.TrimSpace(targetID) {
		return fmt.Errorf("update_checkpoint checkpoint id %q does not match target %q", trimmed.ID, strings.TrimSpace(targetID))
	}
	for field, raw := range fields {
		switch normalizePlanInfoFieldName(field) {
		case "id":
			id := stringFromPlanInfoRaw(raw, trimmed.ID)
			id = strings.TrimSpace(id)
			if id == "" {
				id = strings.TrimSpace(targetID)
			}
			if id != strings.TrimSpace(targetID) {
				return fmt.Errorf("update_checkpoint checkpoint id %q does not match target %q", id, strings.TrimSpace(targetID))
			}
			target.ID = id
		case "title":
			target.Title = strings.TrimSpace(stringFromPlanInfoRaw(raw, trimmed.Title))
		case "status":
			target.Status = normalizePlanCheckpointStatus(stringFromPlanInfoRaw(raw, trimmed.Status))
		case "objective":
			target.Objective = strings.TrimSpace(stringFromPlanInfoRaw(raw, trimmed.Objective))
		case "tasks":
			target.Tasks = trimStringSlice(stringSliceFromPlanInfoRaw(raw, trimmed.Tasks))
			if len(target.Subtasks) > 0 {
				target.Subtasks = nil
				target.ActiveSubtaskID = ""
				normalizePlanCheckpointSubtasks(target)
			}
		case "subtasks":
			var subtasks []pebblestore.SessionPlanSubtask
			if len(raw) > 0 && json.Unmarshal(raw, &subtasks) == nil {
				target.Subtasks = subtasks
				normalizePlanCheckpointSubtasks(target)
			}
		case "active_subtask_id":
			target.ActiveSubtaskID = strings.TrimSpace(stringFromPlanInfoRaw(raw, trimmed.ActiveSubtaskID))
		case "acceptance_criteria":
			target.AcceptanceCriteria = trimStringSlice(stringSliceFromPlanInfoRaw(raw, trimmed.AcceptanceCriteria))
		case "notes":
			target.Notes = strings.TrimSpace(stringFromPlanInfoRaw(raw, trimmed.Notes))
		case "report":
			target.Report = strings.TrimSpace(stringFromPlanInfoRaw(raw, trimmed.Report))
		case "result":
			target.Result = strings.TrimSpace(stringFromPlanInfoRaw(raw, trimmed.Result))
		case "changed_files":
			target.ChangedFiles = trimStringSlice(stringSliceFromPlanInfoRaw(raw, trimmed.ChangedFiles))
		case "validation":
			target.Validation = trimStringSlice(stringSliceFromPlanInfoRaw(raw, trimmed.Validation))
		case "attempt_id":
			target.AttemptID = strings.TrimSpace(stringFromPlanInfoRaw(raw, trimmed.AttemptID))
		case "run_id":
			target.RunID = strings.TrimSpace(stringFromPlanInfoRaw(raw, trimmed.RunID))
		case "session_id", "run_session_id":
			target.SessionID = strings.TrimSpace(stringFromPlanInfoRaw(raw, trimmed.SessionID))
		case "started_at":
			var startedAt int64
			if len(raw) > 0 && json.Unmarshal(raw, &startedAt) == nil {
				target.StartedAt = startedAt
			} else if trimmed.StartedAt != 0 {
				target.StartedAt = trimmed.StartedAt
			}
		case "completed_at":
			var completedAt int64
			if len(raw) > 0 && json.Unmarshal(raw, &completedAt) == nil {
				target.CompletedAt = completedAt
			} else if trimmed.CompletedAt != 0 {
				target.CompletedAt = trimmed.CompletedAt
			}
		case "review":
			var review pebblestore.SessionPlanCheckpointReview
			if len(raw) > 0 && json.Unmarshal(raw, &review) == nil {
				normalizePlanCheckpointReview(&review)
				target.Review = &review
			}
		case "attempts":
			var attempts []pebblestore.SessionPlanCheckpointAttempt
			if len(raw) > 0 && json.Unmarshal(raw, &attempts) == nil {
				target.Attempts = attempts
				for i := range target.Attempts {
					normalizePlanCheckpointAttempt(&target.Attempts[i], target.ID)
				}
			}
		case "order":
			var order int
			if len(raw) > 0 && json.Unmarshal(raw, &order) == nil {
				target.Order = order
			} else if trimmed.Order != 0 {
				target.Order = trimmed.Order
			}
		}
	}
	if target.ID == "" {
		target.ID = strings.TrimSpace(targetID)
	}
	return nil
}

func checkpointFieldPresence(checkpoint *pebblestore.SessionPlanCheckpoint) map[string]json.RawMessage {
	if checkpoint == nil {
		return nil
	}
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return nil
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	return fields
}

func infoFieldPresence(info *pebblestore.SessionPlanInfo) map[string]json.RawMessage {
	if info == nil {
		return nil
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return nil
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	return fields
}

func normalizePlanInfoFieldName(field string) string {
	field = strings.TrimSpace(field)
	field = strings.ReplaceAll(field, "-", "_")
	var out strings.Builder
	for i, r := range field {
		if i > 0 && r >= 'A' && r <= 'Z' {
			out.WriteByte('_')
		}
		out.WriteRune(r)
	}
	return strings.ToLower(out.String())
}

func checkpointIDFromPatch(checkpoint *pebblestore.SessionPlanCheckpoint) string {
	if checkpoint == nil {
		return ""
	}
	return checkpoint.ID
}

func applyCheckpointCompletionFields(checkpoint *pebblestore.SessionPlanCheckpoint, op PlanDocumentPatchOperation, complete bool) {
	if complete {
		checkpoint.Status = PlanCheckpointStatusCompleted
	}
	if status := strings.TrimSpace(op.Status); status != "" {
		checkpoint.Status = normalizePlanCheckpointStatus(status)
	}
	if notes := strings.TrimSpace(op.Notes); notes != "" {
		checkpoint.Notes = notes
	}
	if report := strings.TrimSpace(op.Report); report != "" {
		checkpoint.Report = report
	}
	if result := strings.TrimSpace(op.Result); result != "" {
		checkpoint.Result = result
	}
	if len(op.ChangedFiles) > 0 {
		checkpoint.ChangedFiles = trimStringSlice(op.ChangedFiles)
	}
	if len(op.Validation) > 0 {
		checkpoint.Validation = trimStringSlice(op.Validation)
	}
	if op.Recommendation != nil {
		recommendation := normalizePlanCheckpointRecommendation(*op.Recommendation)
		if validatePlanCheckpointRecommendation(recommendation) == nil {
			checkpoint.Recommendation = &recommendation
		}
	}
}

func findPlanCheckpointIndex(checkpoints []pebblestore.SessionPlanCheckpoint, id string) int {
	id = strings.TrimSpace(id)
	for i := range checkpoints {
		if strings.TrimSpace(checkpoints[i].ID) == id {
			return i
		}
	}
	return -1
}

func normalizeCheckpointOrder(doc *pebblestore.SessionPlanDocument) {
	for i := range doc.Checkpoints {
		doc.Checkpoints[i].Order = i + 1
	}
}

func reorderPlanDocumentCheckpoints(doc *pebblestore.SessionPlanDocument, order []string) error {
	if len(order) == 0 {
		return errors.New("reorder_checkpoints plan document patch requires checkpoint_order")
	}
	if len(order) != len(doc.Checkpoints) {
		return fmt.Errorf("reorder_checkpoints checkpoint_order length %d does not match checkpoint count %d", len(order), len(doc.Checkpoints))
	}
	byID := make(map[string]pebblestore.SessionPlanCheckpoint, len(doc.Checkpoints))
	for _, checkpoint := range doc.Checkpoints {
		byID[strings.TrimSpace(checkpoint.ID)] = checkpoint
	}
	reordered := make([]pebblestore.SessionPlanCheckpoint, 0, len(order))
	seen := make(map[string]struct{}, len(order))
	for _, rawID := range order {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return errors.New("reorder_checkpoints checkpoint_order cannot contain empty ids")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("reorder_checkpoints checkpoint id %q is duplicated", id)
		}
		checkpoint, ok := byID[id]
		if !ok {
			return fmt.Errorf("reorder_checkpoints checkpoint id %q was not found", id)
		}
		seen[id] = struct{}{}
		reordered = append(reordered, checkpoint)
	}
	doc.Checkpoints = reordered
	normalizeCheckpointOrder(doc)
	return nil
}

func clonePlanDocument(doc *pebblestore.SessionPlanDocument) *pebblestore.SessionPlanDocument {
	if doc == nil {
		return nil
	}
	clone := *doc
	if clone.Info.Scope == "" {
		clone.Info.Scope = clone.Info.Context
	}
	clone.Info.Decisions = cloneStringSlice(doc.Info.Decisions)
	clone.Info.Constraints = cloneStringSlice(doc.Info.Constraints)
	clone.Info.Assumptions = cloneStringSlice(doc.Info.Assumptions)
	clone.Info.OpenQuestions = cloneStringSlice(doc.Info.OpenQuestions)
	clone.Info.RelevantFiles = cloneStringSlice(doc.Info.RelevantFiles)
	clone.Info.SuccessCriteria = cloneStringSlice(doc.Info.SuccessCriteria)
	if doc.ExecutionState != nil {
		state := *doc.ExecutionState
		clone.ExecutionState = &state
	}
	clone.Checkpoints = clonePlanDocumentCheckpointSlice(doc.Checkpoints)
	clone.OriginalCheckpoints = clonePlanDocumentCheckpointSlice(doc.OriginalCheckpoints)
	return &clone
}

func clonePlanDocumentCheckpointSlice(checkpoints []pebblestore.SessionPlanCheckpoint) []pebblestore.SessionPlanCheckpoint {
	if checkpoints == nil {
		return nil
	}
	clone := make([]pebblestore.SessionPlanCheckpoint, len(checkpoints))
	for i := range checkpoints {
		clone[i] = checkpoints[i]
		clone[i].Tasks = cloneStringSlice(checkpoints[i].Tasks)
		clone[i].Subtasks = append([]pebblestore.SessionPlanSubtask(nil), checkpoints[i].Subtasks...)
		clone[i].AcceptanceCriteria = cloneStringSlice(checkpoints[i].AcceptanceCriteria)
		clone[i].SourceMessageID = strings.TrimSpace(checkpoints[i].SourceMessageID)
		clone[i].ChangedFiles = cloneStringSlice(checkpoints[i].ChangedFiles)
		clone[i].Validation = cloneStringSlice(checkpoints[i].Validation)
		if checkpoints[i].Review != nil {
			review := *checkpoints[i].Review
			clone[i].Review = &review
		}
		clone[i].Attempts = make([]pebblestore.SessionPlanCheckpointAttempt, len(checkpoints[i].Attempts))
		for j := range checkpoints[i].Attempts {
			clone[i].Attempts[j] = checkpoints[i].Attempts[j]
			clone[i].Attempts[j].ChangedFiles = cloneStringSlice(checkpoints[i].Attempts[j].ChangedFiles)
			clone[i].Attempts[j].Validation = cloneStringSlice(checkpoints[i].Attempts[j].Validation)
		}
	}
	return clone
}

func cloneStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func trimPlanInfo(info *pebblestore.SessionPlanInfo) {
	if info == nil {
		return
	}
	info.Goal = strings.TrimSpace(info.Goal)
	info.Scope = strings.TrimSpace(info.Scope)
	info.Context = strings.TrimSpace(info.Context)
	if info.Scope == "" {
		info.Scope = info.Context
	}
	info.ValidationStrategy = strings.TrimSpace(info.ValidationStrategy)
	info.Decisions = trimStringSlice(info.Decisions)
	info.Constraints = trimStringSlice(info.Constraints)
	info.Assumptions = trimStringSlice(info.Assumptions)
	info.OpenQuestions = trimStringSlice(info.OpenQuestions)
	info.RelevantFiles = trimStringSlice(info.RelevantFiles)
	info.SuccessCriteria = trimStringSlice(info.SuccessCriteria)
}

func trimPlanCheckpoint(checkpoint *pebblestore.SessionPlanCheckpoint) {
	if checkpoint == nil {
		return
	}
	checkpoint.ID = strings.TrimSpace(checkpoint.ID)
	checkpoint.Title = strings.TrimSpace(checkpoint.Title)
	checkpoint.Status = normalizePlanCheckpointStatusForSave(checkpoint.Status)
	checkpoint.Objective = strings.TrimSpace(checkpoint.Objective)
	checkpoint.Tasks = trimStringSlice(checkpoint.Tasks)
	checkpoint.ActiveSubtaskID = strings.TrimSpace(checkpoint.ActiveSubtaskID)
	checkpoint.AcceptanceCriteria = trimStringSlice(checkpoint.AcceptanceCriteria)
	checkpoint.SourceMessageID = strings.TrimSpace(checkpoint.SourceMessageID)
	checkpoint.Notes = strings.TrimSpace(checkpoint.Notes)
	checkpoint.Report = strings.TrimSpace(checkpoint.Report)
	checkpoint.Result = strings.TrimSpace(checkpoint.Result)
	checkpoint.ChangedFiles = trimStringSlice(checkpoint.ChangedFiles)
	checkpoint.Validation = trimStringSlice(checkpoint.Validation)
	normalizePlanCheckpointRuntime(checkpoint)
}

func trimStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
