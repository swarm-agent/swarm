package tool

import "strings"

// ManageProjectsDelegation reports whether a manage_projects
// propose_task/create_task call delegates code or audit work: agent coder,
// swarm or finder (or omitted with intent code/audit), intent absent, code or
// audit, and no media operation. Media, planning and other tasks, and
// malformed or non-string fields, are not delegation.
func ManageProjectsDelegation(args map[string]any) bool {
	if args == nil {
		return false
	}
	field := func(name string) (string, bool) {
		raw, present := args[name]
		if !present || raw == nil {
			return "", true
		}
		value, ok := raw.(string)
		return strings.TrimSpace(value), ok
	}
	action, ok := field("action")
	if !ok || (strings.ToLower(action) != "propose_task" && strings.ToLower(action) != "create_task") {
		return false
	}
	agent, okAgent := field("agent")
	intent, okIntent := field("intent")
	operation, okOperation := field("operation")
	if !okAgent || !okIntent || !okOperation {
		return false
	}
	switch agent {
	case "coder", "swarm", "finder":
	case "":
		if intent == "" {
			return false
		}
	default:
		return false
	}
	switch intent {
	case "", "code", "audit":
	default:
		return false
	}
	return operation == ""
}

// ManageProjectsMediaTask reports whether a manage_projects
// propose_task/create_task call asks a media task (agent image, video or
// sound) to make something.
func ManageProjectsMediaTask(args map[string]any) bool {
	action, _ := args["action"].(string)
	agent, _ := args["agent"].(string)
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "propose_task", "create_task":
	default:
		return false
	}
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "image", "video", "sound":
		return true
	}
	return false
}
