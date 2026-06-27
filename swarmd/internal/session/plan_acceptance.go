package session

import (
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	PlanAcceptanceGranularityRunThrough   = "run_through"
	PlanAcceptanceGranularityCheckpointed = "checkpointed"

	PlanAcceptanceContinuationAutomatic            = "automatic"
	PlanAcceptanceContinuationReviewEachCheckpoint = "review_each_checkpoint"
)

// PlanAcceptanceExecutionOptions are the only user-facing execution choices on
// the plan approval/start surface. AI-drafted plan documents may propose
// checkpoint content, but the UI/backend must translate these session-scoped
// choices into execution_policy; model-authored runtime policy/state is not
// authority for how an accepted plan runs.
//
// Acceptance/start control contract:
//   - Approve & Start saves/approves the plan, applies these session-scoped
//     choices, clears prior execution runtime, then enters the single
//     Start/Continue path.
//   - Start/Continue always starts plan_checkpoint_context with fresh provider
//     input from the active plan/checkpoint; old chat history is not execution
//     context.
//   - run_through maps to shape=single_run and is implemented by the backend
//     as one execution checkpoint for the whole accepted plan.
//   - checkpointed preserves approved checkpoint boundaries; continuation
//     policy decides whether a completed non-final checkpoint starts the next
//     checkpoint automatically or pauses for review.
//   - complete_checkpoint means checkpoint acceptance criteria are met;
//     mark_needs_review means user/audit input is required; mark_blocked means
//     external dependency/input is required; mark_failed means execution failed.
//   - needs_review, blocked, and failed always stop. Auto-continue only applies
//     to completed non-final checkpoints under the automatic continuation
//     policy. Final completion marks plan execution completed.
//   - Accept & Continue approves a waiting-review checkpoint and re-enters
//     Start/Continue for the next checkpoint with fresh context.
//   - Retry/Restart from zero clears the selected checkpoint attempt/run/review
//     runtime and restarts that checkpoint with fresh context.
//   - Rewind to checkpoint resets the selected checkpoint and all later
//     checkpoints to pending, clears their runtime, sets the selected checkpoint
//     active, and re-enters Start/Continue with fresh context.
type PlanAcceptanceExecutionOptions struct {
	// ExecutionGranularity controls the checkpoint shape used by the backend
	// state machine: run_through becomes a single execution checkpoint, while
	// checkpointed preserves approved checkpoint boundaries.
	ExecutionGranularity string

	// ContinuationPolicy controls what happens after a non-final completed
	// checkpoint in checkpointed execution. It is ignored for run_through because
	// there are no intermediate checkpoint boundaries to pause on.
	ContinuationPolicy string
}

// NormalizePlanAcceptanceExecutionPolicy translates the approval/start choices
// into the durable execution_policy stored on the active plan. This function is
// intentionally independent from AI-authored document normalization: callers
// should use these user/UI-owned options when a plan is accepted or restarted.
func NormalizePlanAcceptanceExecutionPolicy(options PlanAcceptanceExecutionOptions) (pebblestore.SessionPlanExecutionPolicy, error) {
	granularity, err := normalizePlanAcceptanceGranularity(options.ExecutionGranularity)
	if err != nil {
		return pebblestore.SessionPlanExecutionPolicy{}, err
	}
	continuation, err := normalizePlanAcceptanceContinuation(options.ContinuationPolicy)
	if err != nil {
		return pebblestore.SessionPlanExecutionPolicy{}, err
	}

	switch granularity {
	case PlanAcceptanceGranularityRunThrough:
		return pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeSingleRun}, nil
	case PlanAcceptanceGranularityCheckpointed:
		mode := PlanExecutionPolicyModeReviewEachCheckpoint
		if continuation == PlanAcceptanceContinuationAutomatic {
			mode = PlanExecutionPolicyModeAutomatic
		}
		return pebblestore.SessionPlanExecutionPolicy{Mode: mode, Shape: PlanExecutionShapeCheckpointed}, nil
	default:
		return pebblestore.SessionPlanExecutionPolicy{}, fmt.Errorf("plan acceptance execution granularity %q is not supported", options.ExecutionGranularity)
	}
}

// ApplyPlanAcceptanceExecutionPolicy applies the backend-owned acceptance
// contract to a plan document without starting a run. It overwrites any
// AI-supplied execution_policy/execution_state with the user-selected policy and
// clears checkpoint runtime metadata so the next Start/Continue begins from a
// deterministic fresh context.
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
	if policy.Shape == PlanExecutionShapeSingleRun {
		normalizePlanDocumentSingleRunCheckpointForAcceptance(doc)
	}
	normalizePlanExecutionPolicy(&doc.ExecutionPolicy, len(doc.Checkpoints))
	for i := range doc.Checkpoints {
		trimPlanCheckpoint(&doc.Checkpoints[i])
	}
	doc.ActiveCheckpointID = defaultActiveCheckpointID(doc.Checkpoints)
	return doc.ExecutionPolicy, nil
}

func normalizePlanAcceptanceGranularity(value string) (string, error) {
	switch token := normalizePlanToken(value); token {
	case "", "default", "checkpoint", "checkpoints", "checkpointed", "checkpoint_by_checkpoint", "one_checkpoint_per_run", "step", "stepwise":
		return PlanAcceptanceGranularityCheckpointed, nil
	case "run", "run_through", "run_straight_through", "straight_through", "continuous", "continuous_run", "single", "single_run", "one_run", "whole_plan":
		return PlanAcceptanceGranularityRunThrough, nil
	default:
		return "", fmt.Errorf("plan acceptance execution granularity %q is not supported", value)
	}
}

func normalizePlanAcceptanceContinuation(value string) (string, error) {
	switch token := normalizePlanToken(value); token {
	case "", "default", "review", "review_each", "review_each_checkpoint", "manual", "pause", "pause_for_review", "pause_each_checkpoint":
		return PlanAcceptanceContinuationReviewEachCheckpoint, nil
	case "auto", "automatic", "auto_continue", "continue_automatically", "continue_automatic":
		return PlanAcceptanceContinuationAutomatic, nil
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

func normalizePlanDocumentSingleRunCheckpointForAcceptance(doc *pebblestore.SessionPlanDocument) {
	if doc == nil {
		return
	}
	if len(doc.Checkpoints) == 0 {
		doc.Checkpoints = []pebblestore.SessionPlanCheckpoint{{
			ID:        "plan-run",
			Title:     "Run approved plan",
			Status:    PlanCheckpointStatusPending,
			Objective: "Complete the approved plan end to end.",
			Order:     1,
		}}
		return
	}
	if len(doc.Checkpoints) == 1 {
		checkpoint := doc.Checkpoints[0]
		if strings.TrimSpace(checkpoint.ID) == "" {
			checkpoint.ID = "plan-run"
		}
		if strings.TrimSpace(checkpoint.Title) == "" {
			checkpoint.Title = "Run approved plan"
		}
		checkpoint.Status = PlanCheckpointStatusPending
		checkpoint.Order = 1
		doc.Checkpoints = []pebblestore.SessionPlanCheckpoint{checkpoint}
		return
	}

	merged := pebblestore.SessionPlanCheckpoint{
		ID:        "plan-run",
		Title:     "Run approved plan",
		Status:    PlanCheckpointStatusPending,
		Objective: "Complete the approved plan end to end. Original checkpoint boundaries are reference material for this single execution run.",
		Order:     1,
	}
	for _, checkpoint := range doc.Checkpoints {
		label := strings.TrimSpace(checkpoint.Title)
		if label == "" {
			label = strings.TrimSpace(checkpoint.ID)
		}
		if label != "" {
			merged.Tasks = append(merged.Tasks, fmt.Sprintf("Complete original checkpoint %s.", label))
		}
		if objective := strings.TrimSpace(checkpoint.Objective); objective != "" {
			merged.Tasks = append(merged.Tasks, objective)
		}
		for _, task := range checkpoint.Tasks {
			if task = strings.TrimSpace(task); task != "" {
				merged.Tasks = append(merged.Tasks, task)
			}
		}
		for _, criterion := range checkpoint.AcceptanceCriteria {
			if criterion = strings.TrimSpace(criterion); criterion != "" {
				merged.AcceptanceCriteria = append(merged.AcceptanceCriteria, criterion)
			}
		}
	}
	merged.Tasks = trimStringSlice(merged.Tasks)
	merged.AcceptanceCriteria = trimStringSlice(merged.AcceptanceCriteria)
	doc.Checkpoints = []pebblestore.SessionPlanCheckpoint{merged}
}
