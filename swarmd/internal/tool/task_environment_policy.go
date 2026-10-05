package tool

import (
	"encoding/json"

	agentruntime "swarm/packages/swarmd/internal/agent"
)

// TaskEnvironmentConsumer fails closed for incomplete project-task metadata.
// Orchestrator project conversations retain account-authorized administration.
func TaskEnvironmentConsumer(metadata map[string]any) bool {
	if metadata["task_id"] != nil || metadata["task_attempt_id"] != nil {
		return true
	}
	if metadata["project_id"] == nil { return false }
	var profile struct { Name string `json:"name"` }
	raw, _ := json.Marshal(metadata["agent_profile"])
	_ = json.Unmarshal(raw, &profile)
	return !agentruntime.IsOrchestratorAgentName(profile.Name)
}

func taskEnvironmentRoutedAction(action string) bool {
	switch action {
	case "list_attachments", "attach_task", "detach_task", "acquire_attachment", "exec", "release", "get_operation", "cancel_operation", "get_deployment", "build", "ensure", "deploy":
		return true
	}
	return false
}

func cloneTaskEnvironmentArgs(args map[string]any, action string) map[string]any {
	out := make(map[string]any, len(args))
	for key, value := range args { out[key] = value }
	out["action"] = action
	return out
}
