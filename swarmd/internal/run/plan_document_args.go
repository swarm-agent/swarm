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
	case "upsert_checkpoint", "replace_checkpoint", "set_checkpoint", "update_checkpoint", "patch_checkpoint", "complete_checkpoint", "finish_checkpoint", "checkpoint_outcome", "mark_checkpoint_outcome", "mark_checkpoint", "finish_checkpoint_with_outcome":
		return true
	default:
		return false
	}
}

func planDocumentPatchArgsPresent(args map[string]any) bool {
	keys := []string{"document_patch", "document_operation", "info", "execution_policy", "execution_state", "checkpoint", "checkpoint_id", "checkpoint_order", "active_checkpoint_id", "active_checkpoint", "notes", "report", "result", "changed_files", "validation", "operations"}
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
	case "update_info", "patch_info", "replace_info", "set_info", "update_execution_policy", "set_execution_policy", "execution_policy", "update_execution_state", "set_execution_state", "execution_state", "upsert_checkpoint", "replace_checkpoint", "set_checkpoint", "update_checkpoint", "patch_checkpoint", "complete_checkpoint", "finish_checkpoint", "checkpoint_outcome", "mark_checkpoint_outcome", "mark_checkpoint", "finish_checkpoint_with_outcome", "remove_checkpoint", "delete_checkpoint", "reorder_checkpoints", "reorder_checkpoint", "set_active_checkpoint", "activate_checkpoint":
		return true
	}
	if planDocumentActionUsesStatusForDocument(mapString(args, "action")) {
		return true
	}
	return false
}
