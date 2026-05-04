package run

import (
	"encoding/json"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/tool"
)

func shouldApproveManageImage(arguments string) bool {
	return permission.ShouldApproveManageImage(arguments)
}

func (s *Service) buildManageImagePermissionPayload(sessionID string, call tool.Call) (map[string]any, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, fmt.Errorf("manage-image arguments invalid: %w", err)
	}
	payload := cloneGenericMap(args)
	if payload == nil {
		payload = map[string]any{}
	}
	payload["tool"] = "manage-image"
	payload["approval_summary"] = manageImageApprovalSummary(payload)
	payload["host_execution"] = "workspace-owning daemon executes provider calls and writes durable host image session storage"
	payload["transcript_policy"] = "final tool output returns compact thread/session IDs, URLs, and asset refs only; no raw image bytes/base64"
	payload["approved_arguments"] = manageImageApprovedArguments(payload)
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		payload["session_id"] = sessionID
	}
	return payload, nil
}

func manageImageApprovedArguments(payload map[string]any) map[string]any {
	approved := cloneGenericMap(payload)
	if approved == nil {
		approved = map[string]any{}
	}
	for _, key := range []string{"tool", "approval_summary", "host_execution", "transcript_policy", "approved_arguments", "session_id"} {
		delete(approved, key)
	}
	return approved
}

func manageImageApprovalSummary(payload map[string]any) string {
	action := strings.ToLower(strings.TrimSpace(mapString(payload, "action")))
	if action == "" {
		action = "generate"
	}
	if action == "inspect" {
		return "Inspect available image providers and models."
	}
	prompt := strings.TrimSpace(mapString(payload, "prompt"))
	count := mapInt(payload, "count")
	if count <= 0 {
		count = 1
	}
	provider := firstNonEmptyManageImageValue(mapString(payload, "provider"), "default provider")
	model := firstNonEmptyManageImageValue(mapString(payload, "model"), "default model")
	threadID := strings.TrimSpace(mapString(payload, "thread_id"))
	threadText := "create a new image session"
	if threadID != "" {
		threadText = "append to image session " + threadID
	}
	if prompt == "" {
		prompt = "(prompt missing)"
	}
	return fmt.Sprintf("Generate %d image(s) with %s/%s and %s.", count, provider, model, threadText)
}

func firstNonEmptyManageImageValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallback
}
