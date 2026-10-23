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
	// WorkspaceToolSetup asks the person's app to ready a project folder for
	// agents (a Git repository with a first commit). It is a client tool like
	// the machine tools, delivered the same way, but no machine access gates
	// it: the folder is on this machine, and the app readies it through
	// Swarm's own repository routes.
	WorkspaceToolSetup = "request_workspace_setup"
)

// IsMachineToolName reports whether name is one of the machine tools.
func IsMachineToolName(name string) bool {
	switch name {
	case MachineToolRequestAccess, MachineToolRun, MachineToolRead, MachineToolWrite:
		return true
	}
	return false
}

// MachineClientTools are the machine tools' definitions, and the workspace
// setup request, which travels the same way. Each waits up to the
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
		{
			// Not a machine tool: it concerns a project folder on this machine,
			// which the person readies through their app, so no machine access
			// gates it (IsMachineToolName leaves it out).
			Name: WorkspaceToolSetup, Kind: AgentCustomToolKindClient, Effect: AgentCustomToolEffectWrite, Confirm: never, TimeoutMS: AgentClientToolMaxTimeoutMS,
			Description: "Ask the person, through their app, to ready one of the project's folders for agents when inspect_source reports it is not ready (not_repository, needs_assisted_setup or needs_initial_commit: no Git repository with a first commit yet). Their app has Swarm make an empty first commit in an empty folder, or shows the folder's files so the person picks the first commit. Never hand the person Git commands instead. The result says the folder's state afterwards: ready with head_commit, or declined. Then inspect the source again and go on.",
			InputSchema: object([]string{"workspace_path", "reason"}, map[string]any{
				"workspace_path": text("The folder's absolute path, as list_sources or inspect_source gives it.", 1000),
				"reason":         text("One sentence: what you want to do in the folder once it is ready.", 500),
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
