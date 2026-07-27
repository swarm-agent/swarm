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

func ShouldApproveManageSessionsCommit(arguments string) bool {
	return ManageSessionsAction(arguments) == "commit"
}

func ShouldApproveManageSessionsArchive(arguments string) bool {
	return ManageSessionsAction(arguments) == "archive"
}

func ShouldApproveManageSessionsUnarchive(arguments string) bool {
	return ManageSessionsAction(arguments) == "unarchive"
}
