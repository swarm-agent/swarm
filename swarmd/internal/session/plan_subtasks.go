package session

import (
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func planSubtaskCheckpoint(doc *pebblestore.SessionPlanDocument, op PlanDocumentPatchOperation) (*pebblestore.SessionPlanCheckpoint, error) {
	id := strings.TrimSpace(op.CheckpointID)
	if id == "" && doc != nil {
		id = strings.TrimSpace(doc.ActiveCheckpointID)
	}
	if id == "" {
		return nil, errors.New("subtask operation requires checkpoint_id or active_checkpoint_id")
	}
	idx := findPlanCheckpointIndex(doc.Checkpoints, id)
	if idx < 0 {
		return nil, fmt.Errorf("plan document checkpoint %q was not found", id)
	}
	checkpoint := &doc.Checkpoints[idx]
	normalizePlanCheckpointSubtasks(checkpoint)
	return checkpoint, nil
}

func findPlanSubtaskIndex(checkpoint *pebblestore.SessionPlanCheckpoint, id string) int {
	id = strings.TrimSpace(id)
	for i := range checkpoint.Subtasks {
		if checkpoint.Subtasks[i].ID == id {
			return i
		}
	}
	return -1
}

func nextPlanSubtaskID(checkpoint *pebblestore.SessionPlanCheckpoint) string {
	for n := len(checkpoint.Subtasks) + 1; ; n++ {
		id := fmt.Sprintf("task-%d", n)
		if findPlanSubtaskIndex(checkpoint, id) < 0 {
			return id
		}
	}
}

func addPlanCheckpointSubtask(doc *pebblestore.SessionPlanDocument, op PlanDocumentPatchOperation) error {
	checkpoint, err := planSubtaskCheckpoint(doc, op)
	if err != nil {
		return err
	}
	if op.Subtask == nil {
		return errors.New("add_subtask requires subtask")
	}
	subtask := *op.Subtask
	subtask.ID = strings.TrimSpace(firstNonBlank(subtask.ID, op.SubtaskID))
	if subtask.ID == "" {
		subtask.ID = nextPlanSubtaskID(checkpoint)
	}
	subtask.Title = strings.TrimSpace(subtask.Title)
	if subtask.Title == "" {
		return errors.New("add_subtask requires subtask.title")
	}
	if findPlanSubtaskIndex(checkpoint, subtask.ID) >= 0 {
		return fmt.Errorf("checkpoint %q subtask %q already exists", checkpoint.ID, subtask.ID)
	}
	subtask.Status = normalizePlanSubtaskStatus(subtask.Status)
	if subtask.Order == 0 {
		subtask.Order = len(checkpoint.Subtasks) + 1
	}
	checkpoint.Subtasks = append(checkpoint.Subtasks, subtask)
	resumeCheckpointForSubtask(doc, checkpoint, op)
	if checkpoint.ActiveSubtaskID == "" {
		return focusPlanCheckpointSubtask(doc, PlanDocumentPatchOperation{CheckpointID: checkpoint.ID, SubtaskID: subtask.ID, RunID: op.RunID, RunSessionID: op.RunSessionID, ParentSessionID: op.ParentSessionID, StartedAt: op.StartedAt})
	}
	return nil
}

func updatePlanCheckpointSubtask(doc *pebblestore.SessionPlanDocument, op PlanDocumentPatchOperation) error {
	checkpoint, err := planSubtaskCheckpoint(doc, op)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(firstNonBlank(op.SubtaskID, subtaskID(op.Subtask)))
	idx := findPlanSubtaskIndex(checkpoint, id)
	if idx < 0 {
		return fmt.Errorf("checkpoint %q subtask %q was not found", checkpoint.ID, id)
	}
	if op.Subtask != nil {
		if title := strings.TrimSpace(op.Subtask.Title); title != "" {
			checkpoint.Subtasks[idx].Title = title
		}
		checkpoint.Subtasks[idx].Notes = strings.TrimSpace(op.Subtask.Notes)
		checkpoint.Subtasks[idx].Result = strings.TrimSpace(op.Subtask.Result)
	}
	resumeCheckpointForSubtask(doc, checkpoint, op)
	return nil
}

func focusPlanCheckpointSubtask(doc *pebblestore.SessionPlanDocument, op PlanDocumentPatchOperation) error {
	checkpoint, err := planSubtaskCheckpoint(doc, op)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(firstNonBlank(op.SubtaskID, subtaskID(op.Subtask)))
	idx := findPlanSubtaskIndex(checkpoint, id)
	if idx < 0 {
		return fmt.Errorf("checkpoint %q subtask %q was not found", checkpoint.ID, id)
	}
	for i := range checkpoint.Subtasks {
		if checkpoint.Subtasks[i].Status == PlanSubtaskStatusInProgress {
			checkpoint.Subtasks[i].Status = PlanSubtaskStatusPending
		}
	}
	checkpoint.Subtasks[idx].Status = PlanSubtaskStatusInProgress
	if checkpoint.Subtasks[idx].StartedAt == 0 {
		checkpoint.Subtasks[idx].StartedAt = op.StartedAt
	}
	checkpoint.ActiveSubtaskID = id
	resumeCheckpointForSubtask(doc, checkpoint, op)
	return nil
}

func completeUnresolvedPlanCheckpointSubtasks(checkpoint *pebblestore.SessionPlanCheckpoint, completedAt int64) {
	if checkpoint == nil {
		return
	}
	for i := range checkpoint.Subtasks {
		if checkpoint.Subtasks[i].Status == PlanSubtaskStatusCompleted {
			continue
		}
		checkpoint.Subtasks[i].Status = PlanSubtaskStatusCompleted
		if completedAt > 0 {
			checkpoint.Subtasks[i].CompletedAt = completedAt
		}
	}
	checkpoint.ActiveSubtaskID = ""
}

func completePlanCheckpointSubtask(doc *pebblestore.SessionPlanDocument, op PlanDocumentPatchOperation) error {
	checkpoint, err := planSubtaskCheckpoint(doc, op)
	if err != nil {
		return err
	}
	ids := append([]string(nil), op.SubtaskIDs...)
	if len(ids) == 0 {
		ids = []string{firstNonBlank(op.SubtaskID, subtaskID(op.Subtask), checkpoint.ActiveSubtaskID)}
	}
	indexes := make([]int, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return errors.New("complete_subtask requires subtask_id, subtask_ids, or active_subtask_id")
		}
		if seen[id] {
			return fmt.Errorf("complete_subtask subtask_ids contains duplicate %q", id)
		}
		seen[id] = true
		idx := findPlanSubtaskIndex(checkpoint, id)
		if idx < 0 {
			return fmt.Errorf("checkpoint %q subtask %q was not found", checkpoint.ID, id)
		}
		indexes = append(indexes, idx)
	}
	if op.CompleteCheckpoint {
		for _, subtask := range checkpoint.Subtasks {
			if subtask.Status == PlanSubtaskStatusCompleted || seen[subtask.ID] {
				continue
			}
			return fmt.Errorf("cannot complete checkpoint %q while subtask %q is %q; include every finished subtask in subtask_ids or keep checkpoint progress open", checkpoint.ID, subtask.ID, subtask.Status)
		}
	}
	for _, idx := range indexes {
		checkpoint.Subtasks[idx].Status = PlanSubtaskStatusCompleted
		checkpoint.Subtasks[idx].CompletedAt = op.CompletedAt
		if strings.TrimSpace(op.Result) != "" {
			checkpoint.Subtasks[idx].Result = strings.TrimSpace(op.Result)
		}
	}
	checkpoint.ActiveSubtaskID = ""
	if op.CompleteCheckpoint {
		_, err := ApplyPlanCheckpointOutcome(doc, PlanCheckpointOutcomeOptions{
			CheckpointID: checkpoint.ID, Outcome: PlanCheckpointStatusCompleted,
			AttemptID: op.AttemptID, RunID: op.RunID, SessionID: op.RunSessionID, ParentSessionID: op.ParentSessionID,
			Report: op.Report, Result: op.Result, ChangedFiles: op.ChangedFiles, Validation: op.Validation,
			Recommendation: op.Recommendation, StartedAt: op.StartedAt, CompletedAt: op.CompletedAt,
		})
		return err
	}
	for i := range checkpoint.Subtasks {
		if checkpoint.Subtasks[i].Status == PlanSubtaskStatusPending {
			checkpoint.Subtasks[i].Status = PlanSubtaskStatusInProgress
			checkpoint.Subtasks[i].StartedAt = op.CompletedAt
			checkpoint.ActiveSubtaskID = checkpoint.Subtasks[i].ID
			break
		}
	}
	return nil
}

func removePlanCheckpointSubtask(doc *pebblestore.SessionPlanDocument, op PlanDocumentPatchOperation) error {
	checkpoint, err := planSubtaskCheckpoint(doc, op)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(op.SubtaskID)
	idx := findPlanSubtaskIndex(checkpoint, id)
	if idx < 0 {
		return fmt.Errorf("checkpoint %q subtask %q was not found", checkpoint.ID, id)
	}
	checkpoint.Subtasks = append(checkpoint.Subtasks[:idx], checkpoint.Subtasks[idx+1:]...)
	if checkpoint.ActiveSubtaskID == id {
		checkpoint.ActiveSubtaskID = ""
	}
	for i := range checkpoint.Subtasks {
		checkpoint.Subtasks[i].Order = i + 1
	}
	return nil
}

func reorderPlanCheckpointSubtasks(doc *pebblestore.SessionPlanDocument, op PlanDocumentPatchOperation) error {
	checkpoint, err := planSubtaskCheckpoint(doc, op)
	if err != nil {
		return err
	}
	if len(op.SubtaskOrder) != len(checkpoint.Subtasks) {
		return fmt.Errorf("reorder_subtasks subtask_order length %d does not match subtask count %d", len(op.SubtaskOrder), len(checkpoint.Subtasks))
	}
	byID := make(map[string]pebblestore.SessionPlanSubtask, len(checkpoint.Subtasks))
	for _, subtask := range checkpoint.Subtasks {
		byID[subtask.ID] = subtask
	}
	reordered := make([]pebblestore.SessionPlanSubtask, 0, len(op.SubtaskOrder))
	for _, rawID := range op.SubtaskOrder {
		id := strings.TrimSpace(rawID)
		subtask, ok := byID[id]
		if !ok {
			return fmt.Errorf("checkpoint %q subtask %q was not found", checkpoint.ID, id)
		}
		delete(byID, id)
		subtask.Order = len(reordered) + 1
		reordered = append(reordered, subtask)
	}
	checkpoint.Subtasks = reordered
	return nil
}

func resumeCheckpointForSubtask(doc *pebblestore.SessionPlanDocument, checkpoint *pebblestore.SessionPlanCheckpoint, op PlanDocumentPatchOperation) {
	checkpoint.Status = PlanCheckpointStatusInProgress
	checkpoint.Review = nil
	checkpoint.CompletedAt = 0
	if strings.TrimSpace(op.RunID) != "" {
		checkpoint.RunID = strings.TrimSpace(op.RunID)
	}
	if strings.TrimSpace(op.RunSessionID) != "" {
		checkpoint.SessionID = strings.TrimSpace(op.RunSessionID)
	}
	doc.ActiveCheckpointID = checkpoint.ID
	if doc.ExecutionState == nil {
		doc.ExecutionState = &pebblestore.SessionPlanExecutionState{}
	}
	doc.ExecutionState.Status = PlanExecutionStateInProgress
	doc.ExecutionState.CompletedAt = 0
	if strings.TrimSpace(op.RunID) != "" {
		doc.ExecutionState.CurrentRunID = strings.TrimSpace(op.RunID)
	}
	if strings.TrimSpace(op.RunSessionID) != "" {
		doc.ExecutionState.CurrentSessionID = strings.TrimSpace(op.RunSessionID)
	}
	if strings.TrimSpace(op.ParentSessionID) != "" {
		doc.ExecutionState.ParentSessionID = strings.TrimSpace(op.ParentSessionID)
	}
}

func subtaskID(subtask *pebblestore.SessionPlanSubtask) string {
	if subtask == nil {
		return ""
	}
	return subtask.ID
}
