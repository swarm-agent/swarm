package taskrouter

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: BuildAgentSeedPrompt must teach task-created planners the actual
// publication contract, including late multi-repository discovery. This prompt
// test checks guidance only; provider invocation/API tests prove its behavior.
func TestPlanningSeedExplainsMultiWorkspaceSubmission(t *testing.T) {
	root := t.TempDir()
	prompt := (&Service{}).BuildAgentSeedPrompt(&pebblestore.ProjectTaskRecord{Title: "Plan work", Agent: "plan", Status: "planning"}, &pebblestore.ProjectRecord{Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: root, WorkspaceID: "secondary"}}})
	for _, required := range []string{"plan_manage action=help", "exit_plan_mode", "pending approval", "checkpoint.task_program", "workspace_path", "Publication does not grant filesystem access", root, "secondary"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("planning seed missing %q", required)
		}
	}
}
