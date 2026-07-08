package run

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type checkpointRunPromptPayload struct {
	PlanID           string                                 `json:"plan_id"`
	PlanTitle        string                                 `json:"plan_title,omitempty"`
	Goal             string                                 `json:"goal,omitempty"`
	Scope            string                                 `json:"scope,omitempty"`
	Decisions        []string                               `json:"decisions,omitempty"`
	RelevantFiles    []string                               `json:"relevant_files,omitempty"`
	Validation       string                                 `json:"validation_strategy,omitempty"`
	ExecutionPolicy  pebblestore.SessionPlanExecutionPolicy `json:"execution_policy"`
	ExecutionSummary sessionruntime.PlanExecutionSummary    `json:"execution_summary"`
	FinalCheckpoint  bool                                   `json:"final_checkpoint,omitempty"`
	Checkpoint       pebblestore.SessionPlanCheckpoint      `json:"checkpoint"`
	AttemptID        string                                 `json:"attempt_id,omitempty"`
	RunID            string                                 `json:"run_id,omitempty"`
	RunSessionID     string                                 `json:"run_session_id,omitempty"`
	ParentSessionID  string                                 `json:"parent_session_id,omitempty"`
}

func (s *Service) buildPlanCheckpointRunInput(sessionID, runID string, options RunOptions) ([]map[string]any, bool, error) {
	ctx := options.PlanCheckpointContext
	if ctx == nil {
		return nil, false, nil
	}
	if s == nil || s.sessions == nil {
		return nil, true, fmt.Errorf("checkpoint run context requires session service")
	}
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	planID := strings.TrimSpace(ctx.PlanID)
	var plan pebblestore.SessionPlanSnapshot
	var ok bool
	var err error
	if planID == "" || strings.EqualFold(planID, "active") {
		plan, ok, err = s.sessions.GetActivePlan(sessionID)
	} else {
		plan, ok, err = s.sessions.GetPlan(sessionID, planID)
	}
	if err != nil {
		return nil, true, err
	}
	if !ok || plan.Document == nil {
		return nil, true, fmt.Errorf("checkpoint run requires an active structured plan")
	}
	planID = strings.TrimSpace(plan.ID)
	doc := plan.Document
	checkpointID := strings.TrimSpace(ctx.CheckpointID)
	if checkpointID == "" {
		checkpointID = strings.TrimSpace(doc.ActiveCheckpointID)
	}
	if checkpointID == "" {
		selected, _, selectedOK := sessionruntime.SelectNextPlanCheckpoint(doc)
		if !selectedOK {
			return nil, true, fmt.Errorf("checkpoint run requires a runnable checkpoint")
		}
		checkpointID = strings.TrimSpace(selected.ID)
	}
	idx := findPlanRunCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return nil, true, fmt.Errorf("checkpoint %q was not found in active plan", checkpointID)
	}
	checkpoint := doc.Checkpoints[idx]
	attemptID := strings.TrimSpace(ctx.AttemptID)
	if attemptID == "" {
		attemptID = strings.TrimSpace(checkpoint.AttemptID)
	}
	parentSessionID := strings.TrimSpace(ctx.ParentSessionID)
	if parentSessionID == "" && doc.ExecutionState != nil {
		parentSessionID = strings.TrimSpace(doc.ExecutionState.ParentSessionID)
	}
	if parentSessionID == "" {
		parentSessionID = sessionID
	}
	startedAt := time.Now().UnixMilli()
	patched, event, patchErr := s.sessions.PatchPlan(sessionID, sessionruntime.PlanPatchOptions{
		PlanID: planID,
		DocumentPatch: &sessionruntime.PlanDocumentPatch{
			Operation:       "start_checkpoint",
			CheckpointID:    checkpointID,
			AttemptID:       attemptID,
			RunID:           runID,
			RunSessionID:    sessionID,
			ParentSessionID: parentSessionID,
			StartedAt:       startedAt,
		},
		Metadata: sessionruntime.PlanSaveMetadata{
			UpdateSummary: "Started checkpoint run with fresh context",
			UpdateScope:   checkpointID,
			UpdateKind:    "checkpoint_start",
			Checkpoint:    true,
		},
	})
	if patchErr != nil {
		return nil, true, patchErr
	}
	if err := s.persistPlanSavedV3Mutation(patched, event, options.ApplySessionMutation); err != nil {
		return nil, true, fmt.Errorf("publish checkpoint start plan saved: %w", err)
	}
	if patched.Document != nil {
		plan = patched
		doc = patched.Document
		idx = findPlanRunCheckpointIndex(doc.Checkpoints, checkpointID)
		if idx < 0 {
			return nil, true, fmt.Errorf("checkpoint %q was not found in patched plan", checkpointID)
		}
		checkpoint = doc.Checkpoints[idx]
		attemptID = strings.TrimSpace(checkpoint.AttemptID)
	}
	payload := checkpointRunPromptPayload{
		PlanID:           planID,
		PlanTitle:        firstNonEmptyString(strings.TrimSpace(doc.Title), strings.TrimSpace(plan.Title)),
		Goal:             strings.TrimSpace(doc.Info.Goal),
		Scope:            strings.TrimSpace(doc.Info.Scope),
		Decisions:        trimStringSliceForPrompt(doc.Info.Decisions),
		RelevantFiles:    trimStringSliceForPrompt(doc.Info.RelevantFiles),
		Validation:       strings.TrimSpace(doc.Info.ValidationStrategy),
		ExecutionPolicy:  doc.ExecutionPolicy,
		ExecutionSummary: sessionruntime.SummarizePlanExecution(doc),
		FinalCheckpoint:  isFinalPlanCheckpointRun(doc, checkpointID),
		Checkpoint:       checkpoint,
		AttemptID:        attemptID,
		RunID:            runID,
		RunSessionID:     sessionID,
		ParentSessionID:  parentSessionID,
	}
	prompt, err := renderCheckpointRunPrompt(payload)
	if err != nil {
		return nil, true, err
	}
	return []map[string]any{{
		"role": "user",
		"content": []map[string]any{{
			"type": "input_text",
			"text": prompt,
		}},
	}}, true, nil
}

func renderCheckpointRunPrompt(payload checkpointRunPromptPayload) (string, error) {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	checkpointID := strings.TrimSpace(payload.Checkpoint.ID)
	if checkpointID == "" {
		checkpointID = "current checkpoint"
	}
	parts := []string{
		"[checkpoint-run] Deterministic checkpoint execution context.",
		"Conversation history has been intentionally cleared for this run. Use only this payload plus the system/developer instructions and tool results from this run.",
		"Execute exactly one checkpoint: " + checkpointID + ". Do not begin later checkpoints in this run.",
		"Use plan_manage as the only checkpoint lifecycle surface for this run. Do not use manage_todos for agent self-tracking, checkpoint progress, or terminal outcomes; manage_todos is reserved for user-owned workspace todos.",
		"Do not call plan_manage update_checkpoint or structured document patches merely to record checkpoint progress or summarize completed work. The checkpoint payload already contains the task context.",
	}
	if payload.FinalCheckpoint {
		parts = append(parts, "Final checkpoint handoff required: this is the last remaining checkpoint. Completing it will put the plan into final waiting_review/final-review state, not start another checkpoint. Put the full durable report, changed files, validation, and result/next-action evidence in the terminal plan_manage call as usual. Do not save the compact user-facing handoff in the plan document; the backend records the user-facing handoff as a lifecycle system message from the terminal tool payload.")
	}
	parts = append(parts,
		"Complete this checkpoint with exactly one terminal plan_manage outcome action: complete_checkpoint, mark_needs_review, mark_blocked, or mark_failed. Always include the current checkpoint_id from the payload in that terminal call.",
		"The terminal outcome tool call must include checkpoint_id, attempt_id when present, run_id, run_session_id, parent_session_id, report, changed_files, validation, and result/next-action evidence; put final notes and evidence in that terminal call instead of a separate update_checkpoint call.",
		"Do not call start_session_checkpoint or request_followup_checkpoint from this checkpoint run. The checkpoint payload is already the selected unit of work for this fresh-context run; satisfy it and finish with one terminal outcome action. Ordered session checkpoint creation is only valid from the parent conversation before a checkpoint run starts.",
		"If all acceptance criteria are met, use complete_checkpoint. complete_checkpoint may continue to the next checkpoint only if backend execution policy allows it; the model must not start the next checkpoint manually.",
		"If user/audit review is needed, use mark_needs_review. mark_needs_review always pauses for review and never advances to the next checkpoint until review is accepted by the backend.",
		"If external input is required, use mark_blocked. If the checkpoint cannot be completed because of an error, use mark_failed. Both stop execution instead of advancing.",
		"After any terminal plan_manage call, do not emit a text-only completion or begin another checkpoint; backend durable plan state decides continuation.",
		"Checkpoint payload:",
		string(raw),
	)
	return strings.TrimSpace(strings.Join(parts, "\n\n")), nil
}

func isFinalPlanCheckpointRun(doc *pebblestore.SessionPlanDocument, checkpointID string) bool {
	if doc == nil {
		return false
	}
	idx := findPlanRunCheckpointIndex(doc.Checkpoints, checkpointID)
	if idx < 0 {
		return false
	}
	for i, checkpoint := range doc.Checkpoints {
		if i == idx {
			continue
		}
		status := strings.TrimSpace(checkpoint.Status)
		switch status {
		case "", sessionruntime.PlanCheckpointStatusPending, sessionruntime.PlanCheckpointStatusInProgress, sessionruntime.PlanCheckpointStatusNeedsReview, sessionruntime.PlanCheckpointStatusBlocked, sessionruntime.PlanCheckpointStatusFailed:
			return false
		}
	}
	return true
}

func findPlanRunCheckpointIndex(checkpoints []pebblestore.SessionPlanCheckpoint, id string) int {
	id = strings.TrimSpace(id)
	if id == "" {
		return -1
	}
	for i := range checkpoints {
		if strings.TrimSpace(checkpoints[i].ID) == id {
			return i
		}
	}
	return -1
}

func trimStringSliceForPrompt(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
