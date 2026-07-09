package run

import (
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const PlanExecutionLifecycleMessageSource = "plan_execution_lifecycle"
const PlanExecutionFinalHandoffMessageSource = "plan_execution_final_handoff"
const PlanExecutionBlockedHandoffMessageSource = "plan_execution_blocked_handoff"

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
	if action == "resolve_blocked_checkpoint" && (nextAction == "await_review" || nextAction == "plan_complete") {
		freshContext = false
	}

	checkpointLabel := planLifecycleCheckpointLabel(checkpointID, checkpointTitle)
	modeLabel := planLifecycleModeLabel(doc.ExecutionPolicy)
	lines := []string{planLifecycleHeadline(action, doc, summary, nextAction, modeLabel)}
	bodyLines := make([]string, 0, 4)
	if planLabel := planLifecyclePlanLabel(input.Plan, doc); planLabel != "" {
		bodyLines = append(bodyLines, "Plan: "+planLabel)
	}
	if checkpointID != "" {
		checkpointLineLabel := "Checkpoint"
		if planLifecycleActionCompleted(action) {
			checkpointLineLabel = "Completed"
		}
		if action == "resolve_blocked_checkpoint" {
			if resolvedID := stringFromPlanPayload(input.Payload, "resolved_checkpoint_id"); resolvedID != "" && resolvedID != checkpointID {
				bodyLines = append(bodyLines, "Resolved: "+planLifecycleCheckpointLabel(resolvedID, planLifecycleCheckpointTitle(doc, resolvedID)))
			}
		}
		bodyLines = append(bodyLines, checkpointLineLabel+": "+checkpointLabel)
	}
	if nextCheckpointID != "" && nextCheckpointID != checkpointID && nextAction != "await_review" && nextAction != "stopped" && nextAction != "plan_complete" {
		bodyLines = append(bodyLines, "Next: "+planLifecycleCheckpointLabel(nextCheckpointID, nextCheckpointTitle))
	}
	finalReview := nextAction == "await_review" && allPlanLifecycleCheckpointsCompleted(doc)
	if nextAction == "await_review" {
		if finalReview {
			bodyLines = append(bodyLines, "Next: all checkpoints are complete; waiting for user review.")
		} else {
			bodyLines = append(bodyLines, "Next: waiting for checkpoint review.")
		}
	} else if nextAction == "stopped" {
		bodyLines = append(bodyLines, "Next: execution stopped until the blocker or failure is resolved.")
	} else if nextAction == "plan_complete" {
		bodyLines = append(bodyLines, "Next: plan complete.")
	}
	if !finalReview && isPlanExecutionOutcomeMessageAction(action) && action != "mark_blocked" {
		bodyLines = append(bodyLines, planLifecycleOutcomeDetailLines(input.Payload, false)...)
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
	case "approve_and_start", "accept_checkpoint", "start_checkpoint", "continue_checkpoint", "restart_checkpoint", "rewind_to_checkpoint", "resolve_blocked_checkpoint":
		return true
	default:
		return isPlanExecutionOutcomeMessageAction(action)
	}
}

func isPlanExecutionOutcomeMessageAction(action string) bool {
	switch strings.TrimSpace(action) {
	case "complete_checkpoint", "checkpoint_outcome", "mark_needs_review", "mark_blocked", "mark_failed":
		return true
	default:
		return false
	}
}

func planLifecycleHeadline(action string, doc *pebblestore.SessionPlanDocument, summary sessionruntime.PlanExecutionSummary, nextAction, modeLabel string) string {
	base := "Plan execution updated"
	if action == "approve_and_start" && nextAction == "run_checkpoint_with_fresh_context" && strings.TrimSpace(summary.NextCheckpointID) != "" {
		return "Plan accepted, starting " + planLifecycleCheckpointLabel(summary.NextCheckpointID, "")
	}
	switch action {
	case "mark_needs_review":
		base = "Checkpoint paused for review"
	case "mark_blocked":
		base = "Checkpoint blocked"
	case "mark_failed":
		base = "Checkpoint failed"
	case "accept_checkpoint":
		if summary.PlanComplete || nextAction == "plan_complete" {
			base = "Plan complete"
		} else {
			base = "Checkpoint review accepted"
		}
	case "resolve_blocked_checkpoint":
		if summary.PlanComplete || nextAction == "plan_complete" {
			base = "Blocked checkpoint resolved; plan complete"
		} else if nextAction == "await_review" {
			base = "Blocked checkpoint resolved; review required"
		} else if nextAction == "run_checkpoint_with_fresh_context" {
			base = "Blocked checkpoint resolved; starting next checkpoint"
		} else {
			base = "Blocked checkpoint resolved"
		}
	case "start_checkpoint", "continue_checkpoint":
		base = "Checkpoint started"
	case "restart_checkpoint":
		base = "Checkpoint restarted"
	case "rewind_to_checkpoint":
		base = "Checkpoint rewound"
	case "complete_checkpoint", "checkpoint_outcome":
		if nextAction == "await_review" && allPlanLifecycleCheckpointsCompleted(doc) {
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
	if action == "start_checkpoint" || action == "continue_checkpoint" || action == "approve_and_start" || action == "restart_checkpoint" || action == "rewind_to_checkpoint" || action == "resolve_blocked_checkpoint" {
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

func BuildFinalPlanExecutionHandoffSystemMessage(input PlanExecutionLifecycleMessageInput) (PlanExecutionLifecycleMessage, bool) {
	action := strings.TrimSpace(input.Action)
	if !planLifecycleActionCompleted(action) || input.Plan.Document == nil {
		return PlanExecutionLifecycleMessage{}, false
	}
	doc := input.Plan.Document
	summary := sessionruntime.SummarizePlanExecution(doc)
	nextAction := stringFromPlanPayload(input.Payload, "next_action")
	if nextAction == "" {
		nextAction = inferPlanExecutionNextAction(summary)
	}
	if nextAction != "await_review" || !allPlanLifecycleCheckpointsCompleted(doc) {
		return PlanExecutionLifecycleMessage{}, false
	}
	checkpointID := planLifecycleCheckpointID(action, doc, summary, input.Payload)
	checkpointTitle := planLifecycleCheckpointTitle(doc, checkpointID)
	lines := []string{
		"Final checkpoint handoff",
		"",
		"The last checkpoint is complete. No additional checkpoint will start unless the user explicitly requests it.",
	}
	if detailLines := planLifecycleOutcomeDetailLines(input.Payload, true); len(detailLines) > 0 {
		lines = append(lines, "")
		lines = append(lines, detailLines...)
	}
	metadata := planExecutionHandoffMetadata(input, action, doc, checkpointID, checkpointTitle, nextAction, PlanExecutionFinalHandoffMessageSource, "plan_final_checkpoint_handoff")
	return PlanExecutionLifecycleMessage{Content: strings.Join(lines, "\n"), Metadata: metadata}, true
}

func BuildBlockedPlanExecutionHandoffSystemMessage(input PlanExecutionLifecycleMessageInput) (PlanExecutionLifecycleMessage, bool) {
	action := strings.TrimSpace(input.Action)
	if action != "mark_blocked" || input.Plan.Document == nil {
		return PlanExecutionLifecycleMessage{}, false
	}
	doc := input.Plan.Document
	summary := sessionruntime.SummarizePlanExecution(doc)
	nextAction := stringFromPlanPayload(input.Payload, "next_action")
	if nextAction == "" {
		nextAction = inferPlanExecutionNextAction(summary)
	}
	if nextAction != "stopped" {
		return PlanExecutionLifecycleMessage{}, false
	}
	checkpointID := planLifecycleCheckpointID(action, doc, summary, input.Payload)
	checkpointTitle := planLifecycleCheckpointTitle(doc, checkpointID)
	lines := []string{
		"Blocked checkpoint handoff",
		"",
		"Checkpoint execution is blocked. Resolve the blocker before continuing.",
	}
	if detailLines := planLifecycleOutcomeDetailLines(input.Payload, true); len(detailLines) > 0 {
		lines = append(lines, "")
		lines = append(lines, detailLines...)
	}
	metadata := planExecutionHandoffMetadata(input, action, doc, checkpointID, checkpointTitle, nextAction, PlanExecutionBlockedHandoffMessageSource, "plan_blocked_checkpoint_handoff")
	return PlanExecutionLifecycleMessage{Content: strings.Join(lines, "\n"), Metadata: metadata}, true
}

func planExecutionHandoffMetadata(input PlanExecutionLifecycleMessageInput, action string, doc *pebblestore.SessionPlanDocument, checkpointID, checkpointTitle, nextAction, source, kind string) map[string]any {
	metadata := map[string]any{
		"source":           source,
		"kind":             kind,
		"action":           action,
		"plan_id":          strings.TrimSpace(input.Plan.ID),
		"plan_title":       strings.TrimSpace(input.Plan.Title),
		"checkpoint_id":    checkpointID,
		"checkpoint_title": checkpointTitle,
		"next_action":      nextAction,
	}
	if doc.ExecutionState != nil {
		metadata["execution_status"] = strings.TrimSpace(doc.ExecutionState.Status)
		metadata["attempt_id"] = strings.TrimSpace(firstNonEmptyString(doc.ExecutionState.ActiveAttemptID, doc.ExecutionState.LastAttemptID))
		metadata["run_id"] = strings.TrimSpace(doc.ExecutionState.CurrentRunID)
		metadata["run_session_id"] = strings.TrimSpace(doc.ExecutionState.CurrentSessionID)
	}
	return metadata
}

func planLifecycleOutcomeDetailLines(payload map[string]any, markdown bool) []string {
	if payload == nil {
		return nil
	}
	var lines []string
	appendSection := func(label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if markdown && len(lines) > 0 {
			lines = append(lines, "")
		}
		if markdown && hasMarkdownBlockStructure(value) {
			lines = append(lines, label+":", value)
			return
		}
		lines = append(lines, label+": "+value)
	}
	appendSection("Report", stringFromPlanPayload(payload, "report"))
	appendSection("Result", stringFromPlanPayload(payload, "result"))
	if validation := stringsFromPlanPayload(payload, "validation"); len(validation) > 0 {
		validationText := strings.Join(validation, "; ")
		if markdown {
			if anyPlanLifecycleMarkdownBlockStructure(validation) {
				validationText = strings.Join(validation, "\n")
			} else {
				validationText = planLifecycleMarkdownValidationList(validation)
			}
		}
		appendSection("Validation", validationText)
	}
	return lines
}

func planLifecycleMarkdownValidationList(values []string) string {
	var items []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		lines := strings.Split(value, "\n")
		for i, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if i == 0 {
				items = append(items, "- "+line)
				continue
			}
			items = append(items, "  "+line)
		}
	}
	return strings.Join(items, "\n")
}

func anyPlanLifecycleMarkdownBlockStructure(values []string) bool {
	for _, value := range values {
		if hasMarkdownBlockStructure(value) {
			return true
		}
	}
	return false
}

func hasMarkdownBlockStructure(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, line := range strings.Split(value, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			return true
		}
	}
	return false
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

func stringsFromPlanPayload(payload map[string]any, key string) []string {
	if payload == nil {
		return nil
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
		return out
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return []string{trimmed}
		}
	}
	return nil
}
