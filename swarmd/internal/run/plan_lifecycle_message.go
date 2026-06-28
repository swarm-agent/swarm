package run

import (
	"fmt"
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

	lines := []string{planLifecycleHeadline(action, summary, nextAction)}
	if planLabel := planLifecyclePlanLabel(input.Plan, doc); planLabel != "" {
		lines = append(lines, "Plan: "+planLabel)
	}
	if checkpointID != "" {
		lines = append(lines, "Checkpoint: "+planLifecycleCheckpointLabel(checkpointID, checkpointTitle))
	}
	policy := planLifecyclePolicyLabel(doc.ExecutionPolicy)
	if policy != "" {
		lines = append(lines, "Policy: "+policy)
	}
	if freshContext {
		lines = append(lines, "Fresh context: previous checkpoint context cleared for this run.")
	}
	if nextCheckpointID != "" && nextCheckpointID != checkpointID && nextAction != "await_review" && nextAction != "stopped" && nextAction != "plan_complete" {
		lines = append(lines, "Next: "+planLifecycleCheckpointLabel(nextCheckpointID, nextCheckpointTitle))
	}
	if nextAction == "await_review" {
		lines = append(lines, "Next: waiting for checkpoint review.")
	} else if nextAction == "stopped" {
		lines = append(lines, "Next: execution stopped until the blocker or failure is resolved.")
	} else if nextAction == "plan_complete" {
		lines = append(lines, "Next: plan complete.")
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
	case "approve_and_start", "start_checkpoint", "continue_checkpoint", "complete_checkpoint", "checkpoint_outcome", "mark_needs_review", "mark_blocked", "mark_failed", "accept_checkpoint", "accept_and_continue", "restart_checkpoint", "rewind_to_checkpoint":
		return true
	default:
		return false
	}
}

func planLifecycleHeadline(action string, summary sessionruntime.PlanExecutionSummary, nextAction string) string {
	switch action {
	case "approve_and_start":
		return "Plan accepted"
	case "start_checkpoint", "continue_checkpoint":
		return "Checkpoint started"
	case "restart_checkpoint":
		return "Checkpoint restarted"
	case "rewind_to_checkpoint":
		return "Plan rewound to checkpoint"
	case "accept_checkpoint", "accept_and_continue":
		return "Checkpoint review accepted"
	case "mark_needs_review":
		return "Checkpoint paused for review"
	case "mark_blocked":
		return "Checkpoint blocked"
	case "mark_failed":
		return "Checkpoint failed"
	case "complete_checkpoint", "checkpoint_outcome":
		if summary.PlanComplete || nextAction == "plan_complete" {
			return "Plan complete"
		}
		if nextAction == "run_checkpoint_with_fresh_context" {
			return "Checkpoint complete · continuing automatically"
		}
		return "Checkpoint complete"
	default:
		return "Plan execution updated"
	}
}

func planLifecycleCheckpointID(action string, doc *pebblestore.SessionPlanDocument, summary sessionruntime.PlanExecutionSummary, payload map[string]any) string {
	if doc == nil {
		return ""
	}
	if action == "start_checkpoint" || action == "continue_checkpoint" || action == "approve_and_start" || action == "restart_checkpoint" || action == "rewind_to_checkpoint" || action == "accept_and_continue" {
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
	title := strings.TrimSpace(firstNonEmptyString(plan.Title, doc.Title))
	id := strings.TrimSpace(firstNonEmptyString(plan.ID, doc.ID))
	if title == "" {
		return id
	}
	if id == "" {
		return title
	}
	return fmt.Sprintf("%s (%s)", title, id)
}

func planLifecycleCheckpointLabel(id, title string) string {
	id = strings.TrimSpace(id)
	title = strings.TrimSpace(title)
	if id == "" {
		return title
	}
	if title == "" {
		return id
	}
	return fmt.Sprintf("%s %s", id, title)
}

func planLifecyclePolicyLabel(policy pebblestore.SessionPlanExecutionPolicy) string {
	mode := strings.TrimSpace(policy.Mode)
	shape := strings.TrimSpace(policy.Shape)
	if mode == "" {
		return shape
	}
	if shape == "" {
		return mode
	}
	return mode + " / " + shape
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
