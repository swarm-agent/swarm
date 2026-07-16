package ui

import (
	"encoding/json"
	"strings"
)

type chatPermissionDestination uint8

const (
	chatPermissionDestinationOrdinaryInline chatPermissionDestination = iota
	chatPermissionDestinationPlanModal
	chatPermissionDestinationManageSessionsModal
	chatPermissionDestinationSpecialized
)

// classifyChatPermission selects exactly one TUI approval surface. Plan and
// manage-sessions approvals require both their canonical tool identity and a
// matching lifecycle requirement so an ordinary tool cannot be diverted by a
// coincidental requirement string.
func classifyChatPermission(record ChatPermissionRecord) chatPermissionDestination {
	toolName := normalizePermissionToolName(record.ToolName)
	requirement := strings.ToLower(strings.TrimSpace(record.Requirement))

	if toolName == "exit_plan_mode" || (toolName == "plan_manage" && isPlanApprovalRequirement(requirement)) {
		return chatPermissionDestinationPlanModal
	}
	if toolName == "manage_sessions" && isManageSessionsApprovalRequirement(requirement) {
		return chatPermissionDestinationManageSessionsModal
	}
	if isManageTodosPermission(record) || isAskUserPermission(record) || isWorkspaceScopePermission(record) || isTaskLaunchPermission(record) || isThemeChangePermission(record) || isAgentChangePermission(record) || isSkillChangePermission(record) {
		return chatPermissionDestinationSpecialized
	}
	return chatPermissionDestinationOrdinaryInline
}

func isPlanApprovalRequirement(requirement string) bool {
	switch strings.ToLower(strings.TrimSpace(requirement)) {
	case "plan_followup_request", "plan_revision_request", "plan_amendment_request", "plan_new_request":
		return true
	default:
		return false
	}
}

func canonicalPermissionApprovedArguments(record ChatPermissionRecord) string {
	approved := strings.TrimSpace(record.ApprovedArguments)
	if approved != "" {
		var object map[string]any
		if json.Unmarshal([]byte(approved), &object) == nil && object != nil {
			return approved
		}
	}
	payload := decodePermissionArguments(record.ToolArguments)
	if payload == nil {
		return ""
	}
	value, ok := payload["approved_arguments"]
	if !ok {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return ""
	}
	return string(raw)
}

func isManageSessionsApprovalRequirement(requirement string) bool {
	switch strings.ToLower(strings.TrimSpace(requirement)) {
	case "session_deploy", "session_commit", "session_archive", "session_unarchive":
		return true
	default:
		return false
	}
}
