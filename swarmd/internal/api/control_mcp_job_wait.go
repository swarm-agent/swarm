package api

import (
	"encoding/json"
	"fmt"
)

// Only path fields are exposed here, never commands, questions or credentials.
func controlMCPJobPermissionWait(record map[string]any) string {
	name, _ := record["tool_name"].(string)
	requirement, _ := record["requirement"].(string)
	var args map[string]any
	raw, _ := record["tool_arguments"].(string)
	_ = json.Unmarshal([]byte(raw), &args)
	request, _ := args["request"].(map[string]any)
	path, _ := request["requested_path"].(string)
	if path == "" {
		path, _ = args["path"].(string)
	}
	fix := "deny this pending approval and retry within the authorized workspace/write lane; report missing network or other authority"
	if requirement == "workspace_scope" {
		fix = "deny this pending approval; host-side tools cannot see sandbox temp paths: use bash inside the sandbox, or use an authorized workspace path"
	}
	if name == "ask_user" || requirement == "ask_user" {
		fix = "answer the question with swarm_answer_job"
	}
	return fmt.Sprintf("tool %s, path %q, requirement %s: %s", name, path, requirement, fix)
}
