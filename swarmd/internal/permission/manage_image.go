package permission

import (
	"encoding/json"
	"strings"
)

// ShouldApproveManageImage returns true for image generation calls that can spend
// provider quota and persist generated assets. Discovery/inspection remains a
// normal non-mutating tool call so agents can learn available providers/models
// before requesting a generation approval.
func ShouldApproveManageImage(arguments string) bool {
	action := manageImageAction(arguments)
	if action == "" {
		return true
	}
	return action != "inspect"
}

func manageImageAction(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return ""
	}
	var payload struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(payload.Action))
}
