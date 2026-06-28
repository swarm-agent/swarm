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
	patched, _, patchErr := s.sessions.PatchPlan(sessionID, sessionruntime.PlanPatchOptions{
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
	if patched.Document != nil {
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
	return strings.TrimSpace(strings.Join([]string{
		"[checkpoint-run] Deterministic checkpoint execution context.",
		"Conversation history has been intentionally cleared for this run. Use only this payload plus the system/developer instructions and tool results from this run.",
		"Execute exactly one checkpoint: " + checkpointID + ". Do not begin later checkpoints in this run.",
		"Use plan_manage as the only agent progress and checkpoint lifecycle surface for this run. Do not use manage_todos for agent self-tracking, checkpoint progress, or terminal outcomes; manage_todos is reserved for user-owned workspace todos.",
		"During the checkpoint, use plan_manage update_checkpoint or structured document patches for meaningful progress updates, checklist/task state, notes, report drafts, changed files, or validation evidence.",
		"At the end of the checkpoint, call plan_manage exactly once with one terminal outcome action: complete_checkpoint, mark_needs_review, mark_blocked, or mark_failed.",
		"The terminal outcome tool call must include checkpoint_id, attempt_id when present, run_id, run_session_id, parent_session_id, report, changed_files, validation, and result/next-action evidence.",
		"If all acceptance criteria are met, use complete_checkpoint. complete_checkpoint may continue to the next checkpoint only if backend execution policy allows it; the model must not start the next checkpoint manually.",
		"If user/audit review is needed, use mark_needs_review. mark_needs_review always pauses for review and never advances to the next checkpoint until review is accepted by the backend.",
		"If external input is required, use mark_blocked. If the checkpoint cannot be completed because of an error, use mark_failed. Both stop execution instead of advancing.",
		"After any terminal plan_manage call, do not emit a text-only completion or begin another checkpoint; backend durable plan state decides continuation.",
		"Checkpoint payload:",
		string(raw),
	}, "\n\n")), nil
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
