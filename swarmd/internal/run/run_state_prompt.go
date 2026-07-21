package run

import (
	"encoding/json"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	runKindPlanParent       = "plan_parent"
	runKindAutoParent       = "auto_parent"
	runKindInlineCheckpoint = "inline_checkpoint"
	runKindFreshCheckpoint  = "fresh_checkpoint"

	contextPolicySessionHistory = "session_history"
	contextPolicyFresh          = "fresh_checkpoint_context"
)

// compactRunState is the bounded, durable state projection supplied to every
// provider run. It intentionally excludes rendered plans and transcript data.
type compactRunState struct {
	SessionMode         string                  `json:"session_mode"`
	ActivePlanPresent   bool                    `json:"active_plan_present"`
	RunKind             string                  `json:"run_kind"`
	ContextPolicy       string                  `json:"context_policy"`
	CurrentRunID        string                  `json:"current_run_id,omitempty"`
	CurrentSessionID    string                  `json:"current_session_id,omitempty"`
	ExecutionOrigin     string                  `json:"execution_origin,omitempty"`
	PlanID              string                  `json:"plan_id,omitempty"`
	PlanStatus          string                  `json:"plan_status,omitempty"`
	ExecutionStatus     string                  `json:"execution_status,omitempty"`
	ActiveCheckpoint    *compactCheckpointState `json:"active_checkpoint,omitempty"`
	RunOwnership        *compactRunOwnership    `json:"run_ownership,omitempty"`
	ReviewRequired      bool                    `json:"review_required"`
	Blocked             bool                    `json:"blocked"`
	Failed              bool                    `json:"failed"`
	NextLifecycleAction string                  `json:"next_lifecycle_action"`
}

type compactCheckpointState struct {
	ID              string                `json:"id"`
	Title           string                `json:"title,omitempty"`
	Status          string                `json:"status,omitempty"`
	Attempt         string                `json:"attempt_id,omitempty"`
	RunID           string                `json:"run_id,omitempty"`
	Session         string                `json:"session_id,omitempty"`
	Tasks           []string              `json:"tasks,omitempty"`
	Subtasks        []compactSubtaskState `json:"subtasks,omitempty"`
	ActiveSubtaskID string                `json:"active_subtask_id,omitempty"`
}

type compactSubtaskState struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type compactRunOwnership struct {
	ActiveAttemptID  string `json:"active_attempt_id,omitempty"`
	ParentSessionID  string `json:"parent_session_id,omitempty"`
	CurrentSessionID string `json:"current_session_id,omitempty"`
	CurrentRunID     string `json:"current_run_id,omitempty"`
}

func (s *Service) durableRunStateInstructions(sessionID, mode, runID string, options RunOptions) (string, error) {
	state := compactRunState{
		SessionMode:         sessionruntime.NormalizeMode(mode),
		RunKind:             runKindForOptions(mode, options, ""),
		ContextPolicy:       contextPolicyForOptions(options),
		CurrentRunID:        strings.TrimSpace(runID),
		CurrentSessionID:    strings.TrimSpace(sessionID),
		NextLifecycleAction: "continue_current_turn",
	}
	if s == nil || s.sessions == nil {
		return renderCompactRunState(state)
	}
	plan, ok, err := s.sessions.GetActivePlan(strings.TrimSpace(sessionID))
	if err != nil {
		return "", fmt.Errorf("load durable active plan for run state: %w", err)
	}
	if !ok || plan.Document == nil {
		return renderCompactRunState(state)
	}
	return renderDurablePlanRunState(state, plan, options)
}

func renderDurablePlanRunState(state compactRunState, plan pebblestore.SessionPlanSnapshot, options RunOptions) (string, error) {
	doc := plan.Document
	origin := sessionruntime.NormalizePlanExecutionOrigin(doc.ExecutionOrigin)
	state.ActivePlanPresent = true
	state.ExecutionOrigin = origin
	state.RunKind = runKindForOptions(state.SessionMode, options, origin)
	state.PlanID = strings.TrimSpace(plan.ID)
	state.PlanStatus = strings.TrimSpace(plan.Status)
	if doc.ExecutionState != nil {
		state.ExecutionStatus = strings.TrimSpace(doc.ExecutionState.Status)
		state.RunOwnership = &compactRunOwnership{
			ActiveAttemptID:  strings.TrimSpace(doc.ExecutionState.ActiveAttemptID),
			ParentSessionID:  strings.TrimSpace(doc.ExecutionState.ParentSessionID),
			CurrentSessionID: strings.TrimSpace(doc.ExecutionState.CurrentSessionID),
			CurrentRunID:     strings.TrimSpace(doc.ExecutionState.CurrentRunID),
		}
	}
	summary := sessionruntime.SummarizePlanExecution(doc)
	state.ReviewRequired = summary.ReviewRequired
	state.Blocked = summary.Blocked
	state.Failed = summary.Failed
	state.NextLifecycleAction = nextPlanLifecycleAction(summary)
	checkpointID := strings.TrimSpace(doc.ActiveCheckpointID)
	if checkpointID == "" {
		checkpointID = strings.TrimSpace(summary.NextCheckpointID)
	}
	if idx := findPlanRunCheckpointIndex(doc.Checkpoints, checkpointID); idx >= 0 {
		checkpoint := doc.Checkpoints[idx]
		state.ActiveCheckpoint = &compactCheckpointState{
			ID:              strings.TrimSpace(checkpoint.ID),
			Title:           truncateRunes(strings.TrimSpace(checkpoint.Title), 120),
			Status:          strings.TrimSpace(checkpoint.Status),
			Attempt:         strings.TrimSpace(checkpoint.AttemptID),
			RunID:           strings.TrimSpace(checkpoint.RunID),
			Session:         strings.TrimSpace(checkpoint.SessionID),
			Tasks:           compactCheckpointTasks(checkpoint.Tasks),
			Subtasks:        compactCheckpointSubtasks(checkpoint.Subtasks),
			ActiveSubtaskID: strings.TrimSpace(checkpoint.ActiveSubtaskID),
		}
	}
	return renderCompactRunState(state)
}

func renderCompactRunState(state compactRunState) (string, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("marshal durable run state: %w", err)
	}
	return "Durable run state (authoritative; do not infer or override it from transcript or UI):\n" + string(raw) + "\n" + compactRunStateLifecycleInstructions(state), nil
}

func compactRunStateLifecycleInstructions(state compactRunState) string {
	const planPresenceInstruction = "The active_plan_present field is authoritative. Do not call plan_manage get-active merely to determine whether a plan exists; use get-active only when full plan details are materially needed beyond this injected durable state."
	if !state.ActivePlanPresent {
		switch sessionruntime.NormalizeMode(state.SessionMode) {
		case sessionruntime.ModePlan:
			return planPresenceInstruction + " No active plan exists. In plan mode, run only the targeted discovery needed to make the plan actionable, then call exit_plan_mode exactly once with the complete structured plan document (info and checkpoints). Do not call start_session_checkpoint, and do not save a draft merely to submit the same plan afterward."
		default:
			return planPresenceInstruction + " No active plan exists. In auto mode, for a clear bounded task call plan_manage start_session_checkpoint directly with a self-contained checkpoint definition. For broad, uncertain, high-risk, or multi-phase work, make exactly one approval-gated plan_manage request_new_plan call with a complete multi-checkpoint structured document. Do not create a draft with new/save, do not propose a plan and then manually start it, and do not call exit_plan_mode from auto."
		}
	}
	return planPresenceInstruction + " An active plan exists; the injected plan and checkpoint fields are authoritative. Continue the current work when it is runnable without injecting or following any no-plan bootstrap path. Maintain the active checkpoint's typed subtasks inline: small fixes, validation, documentation, review, and commit preparation for the same deliverable are subtasks; an independent deliverable or separate failure/review boundary is a new checkpoint. If new user feedback changes requirements for a stopped or paused checkpoint, do not blindly restart the stale definition: classify whether it is the same deliverable or an independent task. For the same deliverable, use restart_checkpoint with change_request and complete replacement title/tasks/acceptance_criteria/notes so replacement and restart are atomic; for an independent redirected deliverable, create a new ordered checkpoint with request_followup_checkpoint. Routine subtask mutations do not require plan approval. Ask the user only for a real product decision or a risky operation."
}

func runKindForOptions(mode string, options RunOptions, origin string) string {
	if options.PlanCheckpointContext != nil {
		return runKindFreshCheckpoint
	}
	if sessionruntime.NormalizeMode(mode) == sessionruntime.ModePlan {
		return runKindPlanParent
	}
	if sessionruntime.NormalizePlanExecutionOrigin(origin) == sessionruntime.PlanExecutionOriginAutoSession {
		return runKindInlineCheckpoint
	}
	return runKindAutoParent
}

func contextPolicyForOptions(options RunOptions) string {
	if options.PlanCheckpointContext != nil {
		return contextPolicyFresh
	}
	return contextPolicySessionHistory
}

func nextPlanLifecycleAction(summary sessionruntime.PlanExecutionSummary) string {
	switch {
	case summary.Blocked:
		return "remain_blocked"
	case summary.Failed:
		return "remain_failed"
	case summary.ReviewRequired:
		return "await_review"
	case summary.PlanComplete:
		return "await_final_review"
	case summary.NextCheckpointID != "" && summary.AutoAdvanceAllowed:
		return "continue_or_start_next_checkpoint"
	case summary.NextCheckpointID != "":
		return "await_checkpoint_review"
	default:
		return "continue_current_turn"
	}
}

func compactCheckpointSubtasks(subtasks []pebblestore.SessionPlanSubtask) []compactSubtaskState {
	const maxSubtasks = 8
	out := make([]compactSubtaskState, 0, maxSubtasks)
	for _, subtask := range subtasks {
		if strings.TrimSpace(subtask.ID) == "" || strings.TrimSpace(subtask.Title) == "" {
			continue
		}
		out = append(out, compactSubtaskState{ID: strings.TrimSpace(subtask.ID), Title: truncateRunes(strings.TrimSpace(subtask.Title), 240), Status: strings.TrimSpace(subtask.Status)})
		if len(out) == maxSubtasks {
			break
		}
	}
	return out
}

func compactCheckpointTasks(tasks []string) []string {
	const maxTasks = 4
	out := make([]string, 0, maxTasks)
	for _, task := range tasks {
		task = truncateRunes(strings.TrimSpace(task), 240)
		if task == "" {
			continue
		}
		out = append(out, task)
		if len(out) == maxTasks {
			break
		}
	}
	return out
}
