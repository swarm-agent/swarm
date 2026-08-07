package run

import (
	"encoding/json"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type checkpointRunPromptPayload struct {
	PlanID           string                                     `json:"plan_id"`
	PlanTitle        string                                     `json:"plan_title,omitempty"`
	Scope            string                                     `json:"scope,omitempty"`
	Decisions        []string                                   `json:"decisions,omitempty"`
	RelevantFiles    []string                                   `json:"relevant_files,omitempty"`
	Artifacts        []pebblestore.SessionPlanArtifactReference `json:"artifacts,omitempty"`
	Validation       string                                     `json:"validation_strategy,omitempty"`
	ExecutionPolicy  pebblestore.SessionPlanExecutionPolicy     `json:"execution_policy"`
	ExecutionOrigin  string                                     `json:"execution_origin"`
	RunKind          string                                     `json:"run_kind"`
	ContextPolicy    string                                     `json:"context_policy"`
	ExecutionSummary sessionruntime.PlanExecutionSummary        `json:"execution_summary"`
	CheckpointIndex  []checkpointRunOrientation                 `json:"checkpoint_index"`
	FinalCheckpoint  bool                                       `json:"final_checkpoint,omitempty"`
	Checkpoint       pebblestore.SessionPlanCheckpoint          `json:"checkpoint"`
	AttemptID        string                                     `json:"attempt_id,omitempty"`
	RunID            string                                     `json:"run_id,omitempty"`
	RunSessionID     string                                     `json:"run_session_id,omitempty"`
	ParentSessionID  string                                     `json:"parent_session_id,omitempty"`
	SourceMessageID  string                                     `json:"source_message_id,omitempty"`
}

type checkpointRunOrientation struct {
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	Status     string `json:"status,omitempty"`
	Order      int    `json:"order,omitempty"`
	HasHandoff bool   `json:"has_handoff,omitempty"`
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
	checkpoint.Objective = checkpointRunObjective(checkpoint)
	if checkpoint.Objective == "" {
		return nil, true, fmt.Errorf("checkpoint %q requires a current objective, task, or title", checkpointID)
	}
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
	payload := checkpointRunPromptPayload{
		PlanID:           planID,
		PlanTitle:        firstNonEmptyString(strings.TrimSpace(doc.Title), strings.TrimSpace(plan.Title)),
		Scope:            strings.TrimSpace(doc.Info.Scope),
		Decisions:        trimStringSliceForPrompt(doc.Info.Decisions),
		RelevantFiles:    trimStringSliceForPrompt(doc.Info.RelevantFiles),
		Artifacts:        combinedCheckpointArtifacts(doc.Artifacts, checkpoint.Artifacts),
		Validation:       strings.TrimSpace(doc.Info.ValidationStrategy),
		ExecutionPolicy:  doc.ExecutionPolicy,
		ExecutionOrigin:  sessionruntime.NormalizePlanExecutionOrigin(doc.ExecutionOrigin),
		RunKind:          runKindFreshCheckpoint,
		ContextPolicy:    contextPolicyFresh,
		ExecutionSummary: sessionruntime.SummarizePlanExecution(doc),
		CheckpointIndex:  checkpointRunOrientationIndex(doc.Checkpoints),
		FinalCheckpoint:  isFinalPlanCheckpointRun(doc, checkpointID),
		Checkpoint:       checkpoint,
		AttemptID:        attemptID,
		RunID:            runID,
		RunSessionID:     sessionID,
		ParentSessionID:  parentSessionID,
		SourceMessageID:  strings.TrimSpace(ctx.SourceMessageID),
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
		"Execute exactly one checkpoint: " + checkpointID + ". Its checkpoint.objective is the sole current objective for this run; plan metadata and completed checkpoints are context only. Do not revive an earlier plan goal or checkpoint objective, and do not begin later checkpoints in this run.",
		"The checkpoint_index is only a lightweight orientation list of checkpoint IDs, titles, statuses, order, and whether a handoff exists. It is not the full plan, scope, context, checkpoint details, or handoff content, and you must not assume omitted context was supplied. Treat compact checkpoint context as orientation, not proof of plan or checkpoint facts. Before answering any user question whose answer depends on plan or checkpoint state or content—including status, blocked/failed/review state, details, prior results, or handoffs—retrieve the canonical active plan with plan_manage get-active and base the answer on that tool result, even when the compact payload appears sufficient. Never guess or present an inference from compact context as verified fact. If canonical retrieval is unavailable or fails, state that the answer could not be verified and report the specific limitation or error instead of assuming. Use the same canonical retrieval when another checkpoint's details or handoff are materially needed to execute the current objective; do not introduce or use a duplicate plan or handoff retrieval path.",
		"Use plan_manage as the only checkpoint lifecycle surface for this run. Do not use manage_todos for agent self-tracking, checkpoint progress, or terminal outcomes; manage_todos is reserved for user-owned workspace todos.",
		"Do not call plan_manage update_checkpoint or structured document patches merely to record routine progress or summarize completed work. Keep typed subtask state durable while a multi-task checkpoint is underway: at a genuine boundary call complete_subtask for one task, or pass subtask_ids to atomically record every task completed since the last progress call. If work continues, that transition advances the next task and makes live client state visible. Do not call complete_subtask for discovery-only activity or for a single-step checkpoint. If new user feedback reaches this current run, route it by contract impact: inquiry/guidance only requires no plan mutation; a bounded same-deliverable refinement whose existing checklist remains valid uses add_subtask and continues here without changing checkpoint identity or attempt history; same-contract feedback that supersedes the checklist uses replace_subtasks with the complete authoritative list; feedback that invalidates the objective or acceptance criteria requires parent-owned restart_checkpoint; independently shippable work or a separate review/failure boundary requires a later parent-owned transition_checkpoint_boundary. Prefer the least disruptive valid route and do not classify by imperative wording alone. Never use add_subtask to clear blocked or failed state.",
	}
	if len(payload.Artifacts) > 0 {
		parts = append(parts, "Artifact references are workspace-relative metadata, not embedded file contents. Read only artifacts with role=input that are needed for this checkpoint, using the workspace file tools; do not bulk-read them. Create every role=deliverable artifact in the workspace before terminal completion and reference its path from the terminal structured handoff. Do not emit a separate assistant completion report merely to deliver or link an artifact.")
	}
	if payload.FinalCheckpoint {
		parts = append(parts, "Final checkpoint handoff required: this is the last remaining checkpoint. Completing it will put the plan into final waiting_review/final-review state, not start another checkpoint. The terminal plan_manage outcome is the single canonical user-visible completion. Do not emit an assistant text completion report before or after it. Before the terminal call, create every requested durable deliverable artifact in the workspace; reference deliverable paths from the structured handoff instead of sending a separate assistant message. In the terminal plan_manage call, keep report substantive and lossless, and author the compact structured handoff: handoff_overview is required and concise; handoff_title is optional; impact_bullets contains at most three short behavioral-impact items; suggested_prompts contains at most three inert label/prompt objects; and pull_request_url may carry an optional public https://github.com/<owner>/<repository>/pull/<number> link when a real PR exists. Suggested prompts are ordinary future user chat messages only and must never be tool calls, shell commands, Git operations, or lifecycle mutations. Supply the single canonical recommendation separately with recommendation. Do not put handoff content inside XML-like tags or emit a swarm-handoff-summary marker. The backend persists the concise source fields on the checkpoint, derives schema-versioned lifecycle metadata, and joins report, result, changed_files, and validation as lossless details without duplicating that evidence in the handoff source fields.")
	}
	parts = append(parts, "Blocked checkpoint handoff required when using mark_blocked: make the blocker immediately understandable without forcing the user to read the full report. Include a short handoff_title that names the blocked outcome, a concise handoff_overview that identifies the external dependency/input/permission and why it prevents progress, and up to three impact_bullets led by the exact resolution required and any important safety/state note. When useful, include up to three suggested_prompts as ordinary user messages for likely next steps, such as confirming that the dependency is resolved and asking Swarm to resume. Keep report, result, changed_files, and validation substantive and lossless; the client presents that evidence collapsed beneath the compact blocked handoff. Do not repeat the full report in the concise handoff fields.")
	parts = append(parts,
		"Complete this checkpoint with exactly one terminal plan_manage outcome: complete_checkpoint, a final complete_subtask call with complete_checkpoint=true, mark_needs_review, mark_blocked, or mark_failed. Always include the current checkpoint_id from the payload in that terminal call.",
		"The terminal outcome tool call must include checkpoint_id, attempt_id when present, run_id, run_session_id, parent_session_id, report, changed_files, validation, and result/next-action evidence; report/result/validation text may use markdown where helpful, and final notes and evidence belong in that terminal call instead of a separate update_checkpoint call.",
		"Do not call start_session_checkpoint or transition_checkpoint_boundary from this checkpoint run; request_followup_checkpoint and its aliases are retired and rejected. The backend rejects session-checkpoint creation owned by an active provider-managed checkpoint run; do not call it speculatively and do not retry it after an error. The checkpoint payload is already the selected unit of work for this fresh-context run. If more work is discovered and remains part of this objective, incorporate it and keep working here. If a genuinely independent later checkpoint is required, record the proposed title, tasks, acceptance criteria, notes, and artifact inputs in the terminal result/next-action evidence and tell the user plainly that it was not created; a later parent-conversation turn must append it with transition_checkpoint_boundary, whose successful result is terminal for that parent turn and commits one fresh checkpoint run. Never use update_checkpoint, an artifact attachment, or assistant prose as a substitute for successful creation, and never claim a checkpoint was added unless the plan_manage result succeeded and returned the new checkpoint. Avoid this boundary when the original request already requires multiple AIs, fresh-context stages, or ordered checkpoints: the parent should create a multi-checkpoint request_new_plan before the first checkpoint starts.",
		"Keep implementing until every acceptance criterion is met whenever the remaining gap is resolvable with the available tools. Discovering more work, a missing interface or API, scope growth, uncertainty, or an incomplete/failed first approach is implementation work to address, not a reason to pause. Safely adapt the implementation within this checkpoint when needed; do not call mark_needs_review or mark_blocked merely because the original approach was insufficient.",
		"After all acceptance criteria are met, finish successfully either with complete_checkpoint or by setting complete_checkpoint=true on the final complete_subtask call. The combined complete_subtask call may batch subtask_ids and must include the same terminal report, changed_files, validation, result, and run ownership evidence; use it to avoid a redundant second tool call. If the checkpoint is not done, record only the completed subset and keep working. The backend alone decides whether completion continues to a next checkpoint; the model must not start it manually.",
		"Use mark_needs_review only when user or audit judgment is inherently required and cannot be replaced by further implementation or verification. It always pauses for review and never advances until review is accepted; uncertainty, scope growth, missing implementation, or a failed first approach are not review reasons by themselves.",
		"Use mark_blocked only for a named external dependency, required input, or unavailable permission that cannot be obtained or worked around with the available tools. State the dependency and exact resolution required, and include the compact blocked handoff fields described above. Use mark_failed only for a nonrecoverable execution error after reasonable recovery attempts. Neither action is appropriate merely because more resolvable work was discovered; both stop execution instead of advancing.",
		"After any terminal plan_manage call, do not emit a text-only completion or begin another checkpoint; backend durable plan state decides continuation.",
		"Checkpoint payload:",
		string(raw),
	)
	return strings.TrimSpace(strings.Join(parts, "\n\n")), nil
}

func checkpointRunOrientationIndex(checkpoints []pebblestore.SessionPlanCheckpoint) []checkpointRunOrientation {
	out := make([]checkpointRunOrientation, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		id := strings.TrimSpace(checkpoint.ID)
		if id == "" {
			continue
		}
		out = append(out, checkpointRunOrientation{
			ID:         id,
			Title:      truncateRunes(strings.TrimSpace(checkpoint.Title), 120),
			Status:     strings.TrimSpace(checkpoint.Status),
			Order:      checkpoint.Order,
			HasHandoff: checkpoint.Handoff != nil,
		})
	}
	return out
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

func checkpointRunObjective(checkpoint pebblestore.SessionPlanCheckpoint) string {
	if objective := strings.TrimSpace(checkpoint.Objective); objective != "" {
		return objective
	}
	if tasks := trimStringSliceForPrompt(checkpoint.Tasks); len(tasks) != 0 {
		return strings.Join(tasks, "\n")
	}
	return strings.TrimSpace(checkpoint.Title)
}

func combinedCheckpointArtifacts(planArtifacts, checkpointArtifacts []pebblestore.SessionPlanArtifactReference) []pebblestore.SessionPlanArtifactReference {
	if len(planArtifacts) == 0 && len(checkpointArtifacts) == 0 {
		return nil
	}
	out := make([]pebblestore.SessionPlanArtifactReference, 0, len(planArtifacts)+len(checkpointArtifacts))
	out = append(out, planArtifacts...)
	out = append(out, checkpointArtifacts...)
	return out
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
