package permission

import (
	"encoding/json"
	"strings"
)

// ShouldApproveManageSessionsArchive keeps discovery/read actions prompt-free while
// requiring an explicit permission decision for durable archive mutations.
func ManageSessionsAction(arguments string) string {
	var payload struct {
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(arguments), &payload) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(payload.Action))
}

func ShouldApproveManageSessionsDeploy(arguments string) bool {
	return ManageSessionsAction(arguments) == "deploy"
}

func ShouldApproveManageSessionsArchive(arguments string) bool {
	var payload struct {
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(arguments), &payload) != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(payload.Action), "archive")
}
