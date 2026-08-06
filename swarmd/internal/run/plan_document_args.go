package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func planPatchArgsPresent(args map[string]any, action string) bool {
	keys := []string{"old_text", "new_text", "text", "checklist_item", "item", "replace_all", "checked", "patch"}
	if action == "update_section" {
		keys = append(keys, "section", "update_scope", "scope")
	} else {
		keys = append(keys, "section")
	}
	for _, key := range keys {
		if _, ok := args[key]; ok {
			return true
		}
	}
	return false
}

func planDocumentFromArgs(args map[string]any) (*pebblestore.SessionPlanDocument, error) {
	return planDocumentFromArgsForTool(args, "plan_manage")
}

func planDocumentFromArgsForTool(args map[string]any, toolName string) (*pebblestore.SessionPlanDocument, error) {
	value, ok := args["document"]
	if !ok || value == nil {
		return nil, nil
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		toolName = "plan_manage"
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var document pebblestore.SessionPlanDocument
	if err := unmarshalPlanToolArg(value, &document, fmt.Sprintf("%s document", toolName)); err != nil {
		return nil, err
	}
	return &document, nil
}

func planArtifactsFromArgs(args map[string]any) ([]pebblestore.SessionPlanArtifactReference, error) {
	value, ok := args["artifacts"]
	if !ok || value == nil {
		return nil, nil
	}
	var artifacts []pebblestore.SessionPlanArtifactReference
	if err := unmarshalPlanToolArg(value, &artifacts, "plan_manage artifacts"); err != nil {
		return nil, err
	}
	return artifacts, nil
}

func unmarshalPlanToolArg(value any, target any, label string) error {
	raw, err := planToolArgJSON(value)
	if err != nil {
		return fmt.Errorf("%s invalid: %w", label, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%s invalid: %w", label, err)
	}
	return nil
}

func planToolArgJSON(value any) ([]byte, error) {
	if text, ok := value.(string); ok {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil, errors.New("empty JSON string")
		}
		return []byte(trimmed), nil
	}
	return json.Marshal(value)
}

func planManageDocumentPatchActions() map[string]bool {
	return map[string]bool{
		"update_info":             true,
		"update_execution_policy": true,
		"update_execution_state":  true,
		"upsert_checkpoint":       true,
		"update_checkpoint":       true,
		"start_checkpoint":        true,
		"continue_checkpoint":     true,
		"complete_checkpoint":     true,
		"checkpoint_outcome":      true,
		"mark_needs_review":       true,
		"mark_blocked":            true,
		"mark_failed":             true,
		"remove_checkpoint":       true,
		"reorder_checkpoints":     true,
		"set_active_checkpoint":   true,
		"add_subtask":             true,
		"replace_subtasks":        true,
		"update_subtask":          true,
		"remove_subtask":          true,
		"reorder_subtasks":        true,
		"focus_subtask":           true,
		"complete_subtask":        true,
	}
}

func planManageActionUsesDocumentPatch(action string) bool {
	return planManageDocumentPatchActions()[strings.ReplaceAll(strings.ToLower(strings.TrimSpace(action)), "-", "_")]
}

func planManageApprovedArgumentKeys(action string) map[string]bool {
	keys := map[string]bool{}
	add := func(names ...string) {
		for _, name := range names {
			keys[name] = true
		}
	}
	if planManageActionUsesDocumentPatch(action) {
		add("plan", "document", "document_patch", "document_operation", "operations", "info", "execution_policy", "execution_state", "checkpoint_id", "checkpoint_order", "subtask", "subtasks", "subtask_id", "subtask_ids", "subtask_order", "complete_checkpoint", "active_checkpoint_id", "active_checkpoint", "status", "outcome", "attempt_id", "run_id", "run_session_id", "session_id", "parent_session_id", "started_at", "completed_at", "reviewed_at", "notes", "report", "result", "changed_files", "validation", "recommendation", "handoff_title", "handoff_overview", "impact_bullets", "suggested_prompts", "pull_request_url")
		return keys
	}
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(action)), "-", "_") {
	case "patch", "update_section":
		add("plan_id", "id", "patch", "operation", "patch_operation", "patch_action", "section", "update_scope", "scope", "old_text", "new_text", "text", "checklist_item", "item", "checked", "replace_all")
	case "approve_and_start":
		add("plan_id", "id", "checkpoint_id", "active_checkpoint_id", "active_checkpoint", "execution_granularity", "granularity", "execution_shape", "shape", "continuation_policy", "continuation", "mode", "continue_automatically")
	case "restart_checkpoint":
		add("plan_id", "id", "checkpoint_id", "active_checkpoint_id", "active_checkpoint", "change_request", "user_request", "request", "prompt", "text", "checkpoint_title", "title", "tasks", "acceptance_criteria", "artifacts", "notes", "handoff_notes", "context", "source_message_id", "source_message")
	case "rewind_to_checkpoint":
		add("plan_id", "id", "checkpoint_id", "active_checkpoint_id", "active_checkpoint")
	case "resolve_blocked_checkpoint", "resolve_block", "clear_block", "unblock_checkpoint":
		add("plan_id", "id", "checkpoint_id", "active_checkpoint_id", "active_checkpoint", "result", "resolution_result", "notes", "resolution_notes", "report", "reviewed_at", "start_next", "continue_next", "attempt_id", "run_id", "run_session_id", "session_id", "parent_session_id", "started_at")
	case "start_session_checkpoint":
		add("checkpoint_id", "id", "change_request", "user_request", "request", "prompt", "text", "checkpoint_title", "title", "tasks", "acceptance_criteria", "artifacts", "notes", "handoff_notes", "context", "source_message_id", "source_message", "attempt_id", "run_id", "run_session_id", "session_id", "parent_session_id", "started_at", "fresh_context", "execution_context")
	case "request_followup_checkpoint":
		add("plan_id", "id", "change_request", "user_request", "request", "prompt", "text", "checkpoint_title", "title", "tasks", "acceptance_criteria", "artifacts", "notes", "handoff_notes", "context", "source_message_id", "source_message", "approval_confirmed", "attempt_id", "run_id", "run_session_id", "session_id", "parent_session_id", "started_at")
	case "amend_plan":
		add("plan_id", "id", "title", "plan", "document", "base_revision", "update_summary", "summary", "reason", "replace_from_checkpoint_id", "checkpoint_id", "amend_future_checkpoints", "override_stale")
	case "request_new_plan":
		add("plan_id", "id", "title", "plan", "document", "reason", "update_summary", "summary", "approval_confirmed", "execution_granularity", "granularity", "execution_shape", "shape", "continuation_policy", "continuation", "mode", "continue_automatically")
	}
	return keys
}

func planDocumentPatchFromArgs(args map[string]any) (*sessionruntime.PlanDocumentPatch, error) {
	if value, ok := args["document_patch"]; ok && value != nil {
		var patch sessionruntime.PlanDocumentPatch
		if err := unmarshalPlanToolArg(value, &patch, "plan_manage document_patch"); err != nil {
			return nil, err
		}
		return &patch, nil
	}
	if !planDocumentPatchArgsPresent(args) {
		return nil, nil
	}
	patch := sessionruntime.PlanDocumentPatch{
		Operation:          strings.TrimSpace(firstNonEmptyString(mapString(args, "document_operation"), mapString(args, "operation"), mapString(args, "op"))),
		CheckpointID:       strings.TrimSpace(firstNonEmptyString(mapString(args, "checkpoint_id"), mapString(args, "id"))),
		CheckpointOrder:    mapStringSlice(args, "checkpoint_order"),
		SubtaskID:          strings.TrimSpace(mapString(args, "subtask_id")),
		SubtaskIDs:         mapStringSlice(args, "subtask_ids"),
		SubtaskOrder:       mapStringSlice(args, "subtask_order"),
		CompleteCheckpoint: mapBool(args, "complete_checkpoint"),
		ActiveCheckpointID: strings.TrimSpace(firstNonEmptyString(mapString(args, "active_checkpoint_id"), mapString(args, "active_checkpoint"))),
		Status:             strings.TrimSpace(firstNonEmptyString(mapString(args, "status"), mapString(args, "outcome"))),
		AttemptID:          strings.TrimSpace(mapString(args, "attempt_id")),
		RunID:              strings.TrimSpace(mapString(args, "run_id")),
		RunSessionID:       strings.TrimSpace(firstNonEmptyString(mapString(args, "run_session_id"), mapString(args, "session_id"))),
		ParentSessionID:    strings.TrimSpace(mapString(args, "parent_session_id")),
		StartedAt:          int64(mapInt(args, "started_at")),
		CompletedAt:        int64(mapInt(args, "completed_at")),
		Notes:              rawStringArg(args, "notes"),
		Report:             rawStringArg(args, "report"),
		Result:             rawStringArg(args, "result"),
		ChangedFiles:       mapStringSlice(args, "changed_files"),
		Validation:         mapStringSlice(args, "validation"),
	}
	if value, ok := args["recommendation"]; ok && value != nil {
		var recommendation pebblestore.SessionPlanCheckpointRecommendation
		if err := unmarshalPlanToolArg(value, &recommendation, "plan_manage recommendation"); err != nil {
			return nil, err
		}
		patch.Recommendation = &recommendation
	}
	if planFinalHandoffArgsPresent(args) {
		handoff := pebblestore.SessionPlanCheckpointHandoff{
			Title:          rawStringArg(args, "handoff_title"),
			Overview:       rawStringArg(args, "handoff_overview"),
			ImpactBullets:  mapStringSlice(args, "impact_bullets"),
			PullRequestURL: rawStringArg(args, "pull_request_url"),
		}
		if value, ok := args["suggested_prompts"]; ok && value != nil {
			if err := unmarshalPlanToolArg(value, &handoff.SuggestedPrompts, "plan_manage suggested_prompts"); err != nil {
				return nil, err
			}
		}
		normalized, err := sessionruntime.NormalizePlanCheckpointHandoff(handoff)
		if err != nil {
			return nil, err
		}
		patch.Handoff = &normalized
	}
	if value, ok := args["subtasks"]; ok && value != nil {
		if err := unmarshalPlanToolArg(value, &patch.Subtasks, "plan_manage subtasks"); err != nil {
			return nil, err
		}
	}
	if value, ok := args["subtask"]; ok && value != nil {
		var subtask pebblestore.SessionPlanSubtask
		if err := unmarshalPlanToolArg(value, &subtask, "plan_manage subtask"); err != nil {
			return nil, err
		}
		patch.Subtask = &subtask
	}
	if value, ok := args["info"]; ok && value != nil {
		var info pebblestore.SessionPlanInfo
		raw, err := planToolArgJSON(value)
		if err != nil {
			return nil, fmt.Errorf("plan_manage document info invalid: %w", err)
		}
		if err := json.Unmarshal(raw, &info); err != nil {
			return nil, fmt.Errorf("plan_manage document info invalid: %w", err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("plan_manage document info invalid: %w", err)
		}
		if rawScope, ok := fields["scope"]; ok && len(rawScope) > 0 {
			var scope string
			if json.Unmarshal(rawScope, &scope) == nil {
				info.Scope = scope
			}
		}
		patch.Info = &info
		patch.InfoFields = fields
	}
	if value, ok := args["execution_policy"]; ok && value != nil {
		var policy pebblestore.SessionPlanExecutionPolicy
		if err := unmarshalPlanToolArg(value, &policy, "plan_manage execution_policy"); err != nil {
			return nil, err
		}
		patch.ExecutionPolicy = &policy
	}
	if value, ok := args["execution_state"]; ok && value != nil {
		var state pebblestore.SessionPlanExecutionState
		if err := unmarshalPlanToolArg(value, &state, "plan_manage execution_state"); err != nil {
			return nil, err
		}
		patch.ExecutionState = &state
	}
	if value, ok := args["checkpoint"]; ok && value != nil {
		if _, isBool := value.(bool); isBool {
			// checkpoint=true is revision metadata, not a structured checkpoint object.
		} else {
			raw, err := planToolArgJSON(value)
			if err != nil {
				return nil, fmt.Errorf("plan_manage checkpoint invalid: %w", err)
			}
			var checkpoint pebblestore.SessionPlanCheckpoint
			if err := json.Unmarshal(raw, &checkpoint); err != nil {
				return nil, fmt.Errorf("plan_manage checkpoint invalid: %w", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				return nil, fmt.Errorf("plan_manage checkpoint invalid: %w", err)
			}
			patch.Checkpoint = &checkpoint
			patch.CheckpointFields = fields
		}
	}
	if value, ok := args["operations"]; ok && value != nil {
		if err := unmarshalPlanToolArg(value, &patch.Operations, "plan_manage document operations"); err != nil {
			return nil, err
		}
	}
	return &patch, nil
}

func planDocumentActionUsesStatusForDocument(action string) bool {
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(action)), "-", "_") {
	case "upsert_checkpoint", "replace_checkpoint", "set_checkpoint", "update_checkpoint", "patch_checkpoint", "start_checkpoint", "continue_checkpoint", "advance_checkpoint", "next_checkpoint", "complete_checkpoint", "finish_checkpoint", "checkpoint_outcome", "mark_checkpoint_outcome", "mark_checkpoint", "finish_checkpoint_with_outcome", "mark_needs_review", "mark_completed", "mark_blocked", "mark_failed", "accept_checkpoint_review", "approve_checkpoint", "restart_checkpoint", "retry_checkpoint", "restart_checkpoint_from_zero", "reset_checkpoint", "rewind_to_checkpoint", "rewind_checkpoint":
		return true
	default:
		return false
	}
}

func planFinalHandoffArgsPresent(args map[string]any) bool {
	for _, key := range []string{"handoff_title", "handoff_overview", "impact_bullets", "suggested_prompts", "pull_request_url"} {
		if _, ok := args[key]; ok {
			return true
		}
	}
	return false
}

func planDocumentPatchArgsPresent(args map[string]any) bool {
	keys := []string{"document_patch", "document_operation", "info", "execution_policy", "execution_state", "checkpoint", "checkpoint_id", "checkpoint_order", "subtask", "subtasks", "subtask_id", "subtask_ids", "subtask_order", "complete_checkpoint", "active_checkpoint_id", "active_checkpoint", "attempt_id", "run_id", "run_session_id", "session_id", "parent_session_id", "started_at", "completed_at", "notes", "report", "result", "changed_files", "validation", "recommendation", "handoff_title", "handoff_overview", "impact_bullets", "suggested_prompts", "pull_request_url", "operations"}
	for _, key := range keys {
		value, ok := args[key]
		if !ok {
			continue
		}
		if key == "checkpoint" {
			if _, isBool := value.(bool); isBool {
				continue
			}
		}
		return true
	}
	operation := strings.ToLower(strings.TrimSpace(firstNonEmptyString(mapString(args, "document_operation"), mapString(args, "operation"), mapString(args, "op"))))
	switch strings.ReplaceAll(operation, "-", "_") {
	case "update_info", "patch_info", "replace_info", "set_info", "update_execution_policy", "set_execution_policy", "execution_policy", "update_execution_state", "set_execution_state", "execution_state", "upsert_checkpoint", "replace_checkpoint", "set_checkpoint", "update_checkpoint", "patch_checkpoint", "start_checkpoint", "continue_checkpoint", "advance_checkpoint", "next_checkpoint", "complete_checkpoint", "finish_checkpoint", "checkpoint_outcome", "mark_checkpoint_outcome", "mark_checkpoint", "finish_checkpoint_with_outcome", "mark_needs_review", "mark_completed", "mark_blocked", "mark_failed", "accept_checkpoint_review", "approve_checkpoint", "restart_checkpoint", "retry_checkpoint", "restart_checkpoint_from_zero", "reset_checkpoint", "rewind_to_checkpoint", "rewind_checkpoint", "remove_checkpoint", "delete_checkpoint", "reorder_checkpoints", "reorder_checkpoint", "set_active_checkpoint", "activate_checkpoint", "add_subtask", "create_subtask", "upsert_subtask", "replace_subtasks", "set_subtasks", "update_subtask", "patch_subtask", "remove_subtask", "delete_subtask", "reorder_subtasks", "focus_subtask", "set_active_subtask", "start_subtask", "complete_subtask", "finish_subtask":
		return true
	}
	if planDocumentActionUsesStatusForDocument(mapString(args, "action")) {
		return true
	}
	return false
}
