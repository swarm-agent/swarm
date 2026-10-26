package run

import (
	agentruntime "swarm/packages/swarmd/internal/agent"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Purpose: workflow execution roles must retain structured blocker reporting even
// with stale or custom narrowed contracts. CompileStoredV3AgentToolContract is
// the execution authority; this unit boundary proves tool admission without
// granting work/control tools or changing unrelated custom/helper sessions.
func TestWorkflowExecutionProgressContract(t *testing.T) {
	s := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	for _, name := range []string{agentruntime.SwarmAgentID, agentruntime.SwarmOrchestratorAgentID, agentruntime.CoderAgentID, agentruntime.WorkerCoderAgentID, agentruntime.FinderAgentID, agentruntime.DesignerAgentID, agentruntime.ReviewerAgentID, "system-reviewer-fixer", "custom-reader", agentruntime.HelperAgentID} {
		t.Run(name, func(t *testing.T) {
			p := store.AgentProfile{Name: name, Mode: "subagent", ToolContract: &store.AgentToolContract{Preset: "custom", Tools: map[string]store.AgentToolConfig{"task_progress": {Enabled: store.BoolPtr(false)}}}, ToolScope: &store.AgentToolScope{DenyTools: []string{"task_progress", "bash", "write", "task"}}}
			resolved, _, err := s.CompileStoredV3AgentToolContract("account", p)
			if err != nil {
				t.Fatal(err)
			}
			want := name != "custom-reader" && name != agentruntime.HelperAgentID
			if resolved.Tools["task_progress"].Enabled != want {
				t.Fatalf("progress enabled=%v, want %v", resolved.Tools["task_progress"].Enabled, want)
			}
			for _, denied := range []string{"bash", "write", "task"} {
				if resolved.Tools[denied].Enabled {
					t.Fatalf("unexpected authority: %s", denied)
				}
			}
		})
	}
}
