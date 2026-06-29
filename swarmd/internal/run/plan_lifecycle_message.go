package run

import (
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const PlanExecutionLifecycleMessageSource = "plan_execution_lifecycle"

type PlanExecutionLifecycleMessageInput struct {
	Action  string
	Plan    pebblestore.SessionPlanSnapshot
	Payload map[string]any
}

type PlanExecutionLifecycleMessage struct {
	Content  string
	Metadata map[string]any
}

func BuildPlanExecutionLifecycleSystemMessage(input PlanExecutionLifecycleMessageInput) (PlanExecutionLifecycleMessage, bool) {
	action := strings.TrimSpace(input.Action)
	if !isPlanExecutionLifecycleMessageAction(action) || input.Plan.Document == nil {
		return PlanExecutionLifecycleMessage{}, false
	}
	doc := input.Plan.Document
	summary := sessionruntime.SummarizePlanExecution(doc)
	nextAction := stringFromPlanPayload(input.Payload, "next_action")
	if nextAction == "" {
		nextAction = inferPlanExecutionNextAction(summary)
	}
	checkpointID := planLifecycleCheckpointID(action, doc, summary, input.Payload)
	checkpointTitle := planLifecycleCheckpointTitle(doc, checkpointID)
	nextCheckpointID := strings.TrimSpace(summary.NextCheckpointID)
	nextCheckpointTitle := planLifecycleCheckpointTitle(doc, nextCheckpointID)
	freshContext := nextAction == "run_checkpoint_with_fresh_context" || action == "start_checkpoint" || action == "continue_checkpoint" || action == "restart_checkpoint" || action == "rewind_to_checkpoint" || action == "approve_and_start"

	checkpointLabel := planLifecycleCheckpointLabel(checkpointID, checkpointTitle)
	modeLabel := planLifecycleModeLabel(doc.ExecutionPolicy)
	lines := []string{planLifecycleHeadline(action, summary, nextAction, modeLabel)}
	bodyLines := make([]string, 0, 4)
	if planLabel := planLifecyclePlanLabel(input.Plan, doc); planLabel != "" {
		bodyLines = append(bodyLines, "Plan: "+planLabel)
	}
	if checkpointID != "" {
		checkpointLineLabel := "Checkpoint"
		if planLifecycleActionCompleted(action) {
			checkpointLineLabel = "Completed"
		}
		bodyLines = append(bodyLines, checkpointLineLabel+": "+checkpointLabel)
	}
	if nextCheckpointID != "" && nextCheckpointID != checkpointID && nextAction != "await_review" && nextAction != "stopped" && nextAction != "plan_complete" {
		bodyLines = append(bodyLines, "Next: "+planLifecycleCheckpointLabel(nextCheckpointID, nextCheckpointTitle))
	}
	if nextAction == "await_review" {
		if allPlanLifecycleCheckpointsCompleted(doc) {
			bodyLines = append(bodyLines, "Next: all checkpoints are complete; waiting for user review.")
		} else {
			bodyLines = append(bodyLines, "Next: waiting for checkpoint review.")
		}
	} else if nextAction == "stopped" {
		bodyLines = append(bodyLines, "Next: execution stopped until the blocker or failure is resolved.")
	} else if nextAction == "plan_complete" {
		bodyLines = append(bodyLines, "Next: plan complete.")
	}
	if len(bodyLines) > 0 {
		lines = append(lines, "")
		lines = append(lines, bodyLines...)
	}
	if freshContext {
		lines = append(lines, "", "Context: Starting the next checkpoint with fresh context.")
	}

	metadata := map[string]any{
		"source":           PlanExecutionLifecycleMessageSource,
		"kind":             "plan_execution_break",
		"action":           action,
		"plan_id":          strings.TrimSpace(input.Plan.ID),
		"plan_title":       strings.TrimSpace(input.Plan.Title),
		"checkpoint_id":    checkpointID,
		"checkpoint_title": checkpointTitle,
		"policy_mode":      strings.TrimSpace(doc.ExecutionPolicy.Mode),
		"policy_shape":     strings.TrimSpace(doc.ExecutionPolicy.Shape),
		"next_action":      nextAction,
		"fresh_context":    freshContext,
	}
	if doc.ExecutionState != nil {
		metadata["execution_status"] = strings.TrimSpace(doc.ExecutionState.Status)
		metadata["attempt_id"] = strings.TrimSpace(firstNonEmptyString(doc.ExecutionState.ActiveAttemptID, doc.ExecutionState.LastAttemptID))
		metadata["run_id"] = strings.TrimSpace(doc.ExecutionState.CurrentRunID)
		metadata["run_session_id"] = strings.TrimSpace(doc.ExecutionState.CurrentSessionID)
	}
	return PlanExecutionLifecycleMessage{Content: strings.Join(lines, "\n"), Metadata: metadata}, true
}

func isPlanExecutionLifecycleMessageAction(action string) bool {
	switch strings.TrimSpace(action) {
	case "complete_checkpoint", "checkpoint_outcome", "mark_needs_review", "mark_blocked", "mark_failed":
		return true
	default:
		return false
	}
}

func planLifecycleHeadline(action string, summary sessionruntime.PlanExecutionSummary, nextAction, modeLabel string) string {
	base := "Plan execution updated"
	switch action {
	case "mark_needs_review":
		base = "Checkpoint paused for review"
	case "mark_blocked":
		base = "Checkpoint blocked"
	case "mark_failed":
		base = "Checkpoint failed"
	case "complete_checkpoint", "checkpoint_outcome":
		if nextAction == "await_review" && allPlanLifecycleCheckpointsCompletedFromSummary(summary) {
			base = "All checkpoints complete; review required"
		} else if summary.PlanComplete || nextAction == "plan_complete" {
			base = "Plan complete"
		} else {
			base = "Checkpoint complete"
		}
	}
	if modeLabel == "" {
		return base
	}
	return base + " — " + modeLabel
}

func planLifecycleCheckpointID(action string, doc *pebblestore.SessionPlanDocument, summary sessionruntime.PlanExecutionSummary, payload map[string]any) string {
	if doc == nil {
		return ""
	}
	if action == "start_checkpoint" || action == "continue_checkpoint" || action == "approve_and_start" || action == "restart_checkpoint" || action == "rewind_to_checkpoint" {
		if checkpointID := stringFromPlanPayload(payload, "checkpoint_id"); checkpointID != "" {
			return checkpointID
		}
		if summary.NextCheckpointID != "" {
			return summary.NextCheckpointID
		}
	}
	if doc.ExecutionState != nil {
		if doc.ExecutionState.LastCheckpointID != "" {
			return strings.TrimSpace(doc.ExecutionState.LastCheckpointID)
		}
	}
	if doc.ActiveCheckpointID != "" {
		return strings.TrimSpace(doc.ActiveCheckpointID)
	}
	if summary.NextCheckpointID != "" {
		return strings.TrimSpace(summary.NextCheckpointID)
	}
	return ""
}

func planLifecycleCheckpointTitle(doc *pebblestore.SessionPlanDocument, checkpointID string) string {
	if doc == nil || strings.TrimSpace(checkpointID) == "" {
		return ""
	}
	for _, checkpoint := range doc.Checkpoints {
		if strings.EqualFold(strings.TrimSpace(checkpoint.ID), strings.TrimSpace(checkpointID)) {
			return strings.TrimSpace(checkpoint.Title)
		}
	}
	return ""
}

func planLifecyclePlanLabel(plan pebblestore.SessionPlanSnapshot, doc *pebblestore.SessionPlanDocument) string {
	return planLifecycleTrimPlanPrefix(firstNonEmptyString(plan.Title, doc.Title))
}

func planLifecycleTrimPlanPrefix(title string) string {
	title = strings.TrimSpace(title)
	for strings.HasPrefix(strings.ToLower(title), "plan:") {
		title = strings.TrimSpace(title[len("plan:"):])
	}
	return title
}

func planLifecycleActionCompleted(action string) bool {
	switch strings.TrimSpace(action) {
	case "complete_checkpoint", "checkpoint_outcome":
		return true
	default:
		return false
	}
}

func allPlanLifecycleCheckpointsCompletedFromSummary(summary sessionruntime.PlanExecutionSummary) bool {
	return summary.ReviewRequired && summary.NextCheckpointID != "" && !summary.PlanComplete && summary.NextCheckpointStatus == sessionruntime.PlanCheckpointStatusCompleted
}

func allPlanLifecycleCheckpointsCompleted(doc *pebblestore.SessionPlanDocument) bool {
	if doc == nil || len(doc.Checkpoints) == 0 {
		return false
	}
	for _, checkpoint := range doc.Checkpoints {
		if strings.TrimSpace(checkpoint.Status) != sessionruntime.PlanCheckpointStatusCompleted {
			return false
		}
	}
	return true
}

func planLifecycleCheckpointLabel(id, title string) string {
	checkpoint := planLifecycleCheckpointDisplayID(id)
	title = strings.TrimSpace(title)
	if checkpoint == "" {
		return title
	}
	if title == "" {
		return checkpoint
	}
	return checkpoint + " — " + title
}

func planLifecycleCheckpointDisplayID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	lower := strings.ToLower(id)
	if strings.HasPrefix(lower, "checkpoint ") {
		return id
	}
	for _, prefix := range []string{"cp-", "checkpoint-"} {
		if strings.HasPrefix(lower, prefix) {
			suffix := strings.TrimSpace(id[len(prefix):])
			if suffix != "" {
				return "Checkpoint " + suffix
			}
		}
	}
	return "Checkpoint " + id
}

func planLifecycleModeLabel(policy pebblestore.SessionPlanExecutionPolicy) string {
	switch strings.TrimSpace(policy.Mode) {
	case sessionruntime.PlanExecutionPolicyModeAutomatic:
		return "Automatic mode"
	case sessionruntime.PlanExecutionPolicyModeReviewEachCheckpoint:
		return "Manual review mode"
	default:
		return ""
	}
}

func inferPlanExecutionNextAction(summary sessionruntime.PlanExecutionSummary) string {
	if summary.PlanComplete {
		return "plan_complete"
	}
	if summary.ReviewRequired {
		return "await_review"
	}
	if summary.Blocked || summary.Failed {
		return "stopped"
	}
	if summary.AutoAdvanceAllowed && summary.NextCheckpointID != "" {
		return "run_checkpoint_with_fresh_context"
	}
	if summary.NextCheckpointID != "" {
		return "continue_checkpoint"
	}
	return ""
}

func stringFromPlanPayload(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}
