package pebblestore

// Machine tools let a session's agent work on the person's own machine. They
// are client tools Swarm defines itself: the person's app answers each call,
// running it as the person, and Swarm runs nothing. A session's access
// (SessionAccess.Machine) decides whether a call may reach the app at all:
// with none, every machine tool but the request is refused, and the request
// is refused once access exists, so the agent never asks twice.
const (
	MachineToolRequestAccess = "request_machine_access"
	MachineToolRun           = "machine_run"
	MachineToolRead          = "machine_read"
	MachineToolWrite         = "machine_write"
)

// IsMachineToolName reports whether name is one of the machine tools.
func IsMachineToolName(name string) bool {
	switch name {
	case MachineToolRequestAccess, MachineToolRun, MachineToolRead, MachineToolWrite:
		return true
	}
	return false
}

// MachineClientTools are the machine tools' definitions. Each waits up to the
// client tool maximum for the person's app: with access "ask", the person
// decides each call in that time.
func MachineClientTools() []AgentCustomToolDefinition {
	object := func(required []string, properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "required": required, "properties": properties, "additionalProperties": false}
	}
	text := func(description string, max int) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": max, "description": description}
	}
	never := BoolPtr(false)
	return []AgentCustomToolDefinition{
		{
			Name: MachineToolRequestAccess, Kind: AgentCustomToolKindClient, Effect: AgentCustomToolEffectRead, Confirm: never, TimeoutMS: AgentClientToolMaxTimeoutMS,
			Description: "Ask the person for access to their own machine (outside Swarm's project folders), only when they asked for something that needs it and the session has no machine access. Their app shows your reason; they answer full (commands run as them), ask (they approve each command) or deny. The result says which.",
			InputSchema: object([]string{"reason"}, map[string]any{"reason": text("One or two sentences: what you need to do on their machine and why.", 500)}),
		},
		{
			Name: MachineToolRun, Kind: AgentCustomToolKindClient, Effect: AgentCustomToolEffectWrite, Confirm: never, TimeoutMS: AgentClientToolMaxTimeoutMS,
			Description: "Run a shell command on the person's own machine, as the person, through their app. Needs machine access. The result has exit_code, stdout and stderr (long output is cut).",
			InputSchema: object([]string{"command"}, map[string]any{
				"command": text("The command, run by the person's shell.", 4000),
				"cwd":     text("Working directory (default: their home folder).", 1000),
			}),
		},
		{
			Name: MachineToolRead, Kind: AgentCustomToolKindClient, Effect: AgentCustomToolEffectRead, Confirm: never, TimeoutMS: AgentClientToolMaxTimeoutMS,
			Description: "Read a text file on the person's own machine through their app. Needs machine access. Long files are cut.",
			InputSchema: object([]string{"path"}, map[string]any{"path": text("Absolute path, or ~/ for their home folder.", 1000)}),
		},
		{
			Name: MachineToolWrite, Kind: AgentCustomToolKindClient, Effect: AgentCustomToolEffectWrite, Confirm: never, TimeoutMS: AgentClientToolMaxTimeoutMS,
			Description: "Write a text file on the person's own machine through their app, replacing it. Needs machine access.",
			InputSchema: object([]string{"path", "content"}, map[string]any{
				"path":    text("Absolute path, or ~/ for their home folder.", 1000),
				"content": map[string]any{"type": "string", "maxLength": 30000, "description": "The whole new content (at most 30000 characters)."},
			}),
		},
	}
}

// MachineClientTool returns the named machine tool's definition.
func MachineClientTool(name string) (AgentCustomToolDefinition, bool) {
	for _, definition := range MachineClientTools() {
		if definition.Name == name {
			return definition, true
		}
	}
	return AgentCustomToolDefinition{}, false
}
