package permission

import "encoding/json"

// Distinct identities prevent a read rule from granting execution or changes.
func automationPolicyIdentity(arguments string) string {
	var args struct {
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return "automation_change"
	}
	switch args.Action {
	case "list", "get", "search", "history", "context", "inspect", "review", "progress", "help":
		return "automation_read"
	case "run":
		return "automation_run"
	case "cancel":
		return "automation_cancel"
	default:
		return "automation_change"
	}
}

func workflowPolicyIdentity(arguments string) string {
	var args struct {
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(arguments), &args) == nil {
		switch args.Action {
		case "list", "get", "deliveries", "history", "help":
			return "automation_read"
		}
	}
	return "automation_change"
}
