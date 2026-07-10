package session

import (
	"errors"
	"fmt"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	PlanAcceptanceGranularityCheckpointed = "checkpointed"

	PlanAcceptanceContinuationAutomatic            = "automatic"
	PlanAcceptanceContinuationReviewEachCheckpoint = "review_each_checkpoint"
)

// PlanAcceptanceExecutionOptions are the user-facing execution choices on the
// plan approval/start surface. Checkpoint boundaries are always preserved;
// continuation controls whether successful non-final checkpoints advance
// automatically or pause for review.
type PlanAcceptanceExecutionOptions struct {
	// ExecutionGranularity is ignored. It remains as a source-compatibility field
	// while callers migrate to the checkpointed-only contract.
	ExecutionGranularity string
	ContinuationPolicy   string
}

// NormalizePlanAcceptanceExecutionPolicy translates approval/start choices into
// the durable execution policy. Automatic continuation is the default.
func NormalizePlanAcceptanceExecutionPolicy(options PlanAcceptanceExecutionOptions) (pebblestore.SessionPlanExecutionPolicy, error) {
	continuation, err := normalizePlanAcceptanceContinuation(options.ContinuationPolicy)
	if err != nil {
		return pebblestore.SessionPlanExecutionPolicy{}, err
	}
	mode := PlanExecutionPolicyModeAutomatic
	if continuation == PlanAcceptanceContinuationReviewEachCheckpoint {
		mode = PlanExecutionPolicyModeReviewEachCheckpoint
	}
	return pebblestore.SessionPlanExecutionPolicy{Mode: mode, Shape: PlanExecutionShapeCheckpointed}, nil
}

// ApplyPlanAcceptanceExecutionPolicy applies the backend-owned acceptance
// contract and clears runtime metadata for a deterministic fresh start. Legacy
// single-run documents recover their original checkpoint boundaries.
func ApplyPlanAcceptanceExecutionPolicy(doc *pebblestore.SessionPlanDocument, options PlanAcceptanceExecutionOptions) (pebblestore.SessionPlanExecutionPolicy, error) {
	if doc == nil {
		return pebblestore.SessionPlanExecutionPolicy{}, errors.New("plan document is required")
	}
	policy, err := NormalizePlanAcceptanceExecutionPolicy(options)
	if err != nil {
		return pebblestore.SessionPlanExecutionPolicy{}, err
	}
	doc.ExecutionPolicy = policy
	resetPlanExecutionRuntimeForAcceptance(doc)
	restoreOriginalCheckpointsForCheckpointedExecution(doc)
	normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
	for i := range doc.Checkpoints {
		trimPlanCheckpoint(&doc.Checkpoints[i])
	}
	doc.ActiveCheckpointID = defaultActiveCheckpointID(doc.Checkpoints)
	return doc.ExecutionPolicy, nil
}

func restoreOriginalCheckpointsForCheckpointedExecution(doc *pebblestore.SessionPlanDocument) {
	if doc == nil || len(doc.OriginalCheckpoints) == 0 {
		return
	}
	doc.Checkpoints = clonePlanDocumentCheckpointSlice(doc.OriginalCheckpoints)
	doc.OriginalCheckpoints = nil
}

func normalizePlanAcceptanceContinuation(value string) (string, error) {
	switch token := normalizePlanToken(value); token {
	case "", "default", "auto", "automatic", "auto_continue", "continue_automatically", "continue_automatic":
		return PlanAcceptanceContinuationAutomatic, nil
	case "review", "review_each", "review_each_checkpoint", "manual", "pause", "pause_for_review", "pause_each_checkpoint":
		return PlanAcceptanceContinuationReviewEachCheckpoint, nil
	default:
		return "", fmt.Errorf("plan acceptance continuation policy %q is not supported", value)
	}
}

func resetPlanExecutionRuntimeForAcceptance(doc *pebblestore.SessionPlanDocument) {
	if doc == nil {
		return
	}
	doc.ExecutionState = nil
	doc.ActiveCheckpointID = ""
	for i := range doc.Checkpoints {
		resetPlanCheckpointRuntimeForFreshStart(&doc.Checkpoints[i])
	}
}
