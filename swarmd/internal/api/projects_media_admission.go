package api

import (
	"errors"
	"fmt"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const mediaBatchConfirmationThreshold = 25

// Only provider-backed media is exempt from routine task approval. Designer,
// plans, and programs keep their own approval contracts.
func isOrdinaryMediaAgent(agent string) bool {
	return agent == "image" || agent == "video" || agent == "sound" || agent == "audio"
}

func projectMediaBatchCount(variants, deliverables, slots int) (int, error) {
	if variants < 0 || deliverables < 0 {
		return 0, errors.New("media counts cannot be negative")
	}
	if variants > 0 && deliverables > 0 && variants != deliverables {
		return 0, errors.New("media variant and deliverable counts must match")
	}
	count := max(1, variants, deliverables, slots)
	if slots > 0 && max(variants, deliverables) > 0 && slots != max(variants, deliverables) {
		return 0, errors.New("media deliverable slots must match the requested count")
	}
	return count, nil
}

// Evaluate the complete request, never a provider chunk. Generic auto_approve
// is deliberately not confirmation of a large media batch: its persisted task
// must be explicitly approved before dispatch.
func admitProjectMediaTask(task *pebblestore.ProjectTaskRecord) error {
	if !isOrdinaryMediaAgent(task.Agent) || task.TaskProgram != nil || task.TaskProgramID != "" || task.PlanDocument != nil || task.PlanBinding != nil {
		return nil
	}
	count, err := projectMediaBatchCount(task.VariantCount, 0, len(task.Deliverables))
	if err != nil {
		return err
	}
	task.VariantCount = count
	task.AutoApprove = count < mediaBatchConfirmationThreshold
	if task.AutoApprove {
		task.Status, task.ActionNeeded = "in_progress", ""
	} else {
		task.Status = "pending_approval"
		task.ActionNeeded = fmt.Sprintf("Confirm all %d media iterations before generation; reject to cancel without generating", count)
	}
	return nil
}
