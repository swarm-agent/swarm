package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// Purpose: the system prompt the Claude Code CLI runs with names every
// Swarm tool as the CLI knows it (mcp__swarm__<name>) and says once that
// the prefix is required, so the model never calls a catalog tool by its
// bare name. Identifier tool names in the prose (manage_projects,
// manage-sessions) are prefixed exactly once; plain-word names (bash, read)
// stay ordinary English in the prose and are listed prefixed; longer
// identifiers that merely contain a tool name and names already prefixed
// are left alone; a turn without tools keeps its prompt as given.
// Threat (GPUI-vs-MCP parity rerun): the Orchestrator on claudecode called
// "manage_projects", the CLI answered "No such tool available" (it knows
// mcp__swarm__manage_projects), and each mistake cost a retry. The CLI
// rejects an unknown name itself, so the endpoint cannot alias it.
// Boundary: Runner.planTurn writing --system-prompt-file
// (mcpToolInstructions).
// Narrowest layer: planTurn is where the static instructions become the
// CLI's prompt file; no CLI process runs.
func TestClaudeCodeSystemPromptNamesToolsWithTheirPrefix(t *testing.T) {
	const prompt = "You are the Orchestrator.\n" +
		"- Send work with manage_projects action=propose_task; read a session with manage-sessions.\n" +
		"- Track it with task_progress. Read each outcome before you answer; use bash only when asked.\n" +
		"- swarm_manage_projects_extra is not a tool; mcp__swarm__manage_projects is already prefixed.\n" +
		"- The envelope says {\"tool\":\"manage_projects\"}.\n"
	tools := []provideriface.ToolDefinition{{Name: "manage_projects"}, {Name: "manage-sessions"}, {Name: "task_progress"}, {Name: "bash"}, {Name: "read"}}
	runner := NewRunner(nil, NewCLI())

	plan := func(t *testing.T, choice string) string {
		t.Helper()
		dir := t.TempDir()
		_, err := runner.planTurn(context.Background(), provideriface.Request{Model: "fixture", Instructions: prompt, Tools: tools, ToolChoice: choice, ToolInvoker: fixedToolInvoker{output: "ok"}, Input: []map[string]any{{"role": "user", "content": "hi"}}}, dir)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "system-prompt.txt"))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	got := plan(t, "")
	for _, want := range []string{
		"mcp__swarm__manage_projects action=propose_task",
		"read a session with mcp__swarm__manage-sessions.",
		"Track it with mcp__swarm__task_progress.",
		"Read each outcome before you answer; use bash only when asked.",
		"swarm_manage_projects_extra is not a tool",
		"a call without the prefix fails",
		"Call them as: mcp__swarm__bash, mcp__swarm__manage-sessions, mcp__swarm__manage_projects, mcp__swarm__read, mcp__swarm__task_progress.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("system prompt lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "mcp__swarm__mcp__swarm__") {
		t.Fatalf("a prefixed name was prefixed again:\n%s", got)
	}
	// No catalog tool name stands bare anywhere in the prompt.
	for _, name := range []string{"manage_projects", "manage-sessions", "task_progress"} {
		bare := regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_-])`)
		if loc := bare.FindStringIndex(got); loc != nil {
			t.Fatalf("bare tool name %q is presented as callable at %q:\n%s", name, got[max(loc[0]-20, 0):min(loc[1]+20, len(got))], got)
		}
	}

	// A turn that cannot call tools keeps its prompt as given.
	if none := plan(t, "none"); strings.TrimSpace(none) != strings.TrimSpace(prompt) {
		t.Fatalf("a tool-less turn's prompt changed:\n%s", none)
	}
}
