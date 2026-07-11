package permission

import (
	"encoding/json"
	"strings"
)

// ShouldApproveManageSessionsArchive keeps discovery/read actions prompt-free while
// requiring an explicit permission decision for durable archive mutations.
func ShouldApproveManageSessionsArchive(arguments string) bool {
	var payload struct {
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(arguments), &payload) != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(payload.Action), "archive")
}
