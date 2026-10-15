package run

import (
	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Purpose: ResolveAgentToolContract must reject forged helper tool grants,
// including recursive delegation and delivery, at the runtime authority boundary.
// This layer proves the executable contract, not provider-backed fan-out.
func TestHelperToolBoundary(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	p := agentruntime.HelperAgentProfileForParent(pebblestore.AgentProfile{})
	for _, name := range []string{"write", "edit", "git_commit", "deliver", "manage_sessions", "manage_artifact", "task"} {
		p.ToolContract.Tools[name] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(true)}
	}
	resolved, _, _, err := svc.ResolveAgentToolContract(p)
	if err != nil {
		t.Fatal(err)
	}
	for name, state := range resolved.Tools {
		allowed := name == "read" || name == "search" || name == "find" || name == "list" || name == "bash"
		if state.Enabled != allowed {
			t.Fatalf("%s enabled=%v", name, state.Enabled)
		}
	}
	coder, _, _, err := svc.ResolveAgentToolContract(agentruntime.WorkerCoderAgentProfileForParent(pebblestore.AgentProfile{}))
	if err != nil || !coder.Tools["task"].Enabled {
		t.Fatalf("coder task: %+v %v", coder.Tools["task"], err)
	}
}

// Purpose: preparation must retain the exact job checkout and attenuate a
// trusted Finder profile. Real durable child creation proves the session
// boundary, without pretending to run a provider-backed conversation.
func TestHelperChildWorktree(t *testing.T) {
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, ok, err := svc.sessions.GetSession(id)
	if err != nil || !ok {
		t.Fatal(err)
	}
	parent.Metadata = map[string]any{}
	parent.Metadata["agent_profile"] = agentruntime.WorkerCoderAgentProfileForParent(pebblestore.AgentProfile{})
	parent.WorktreeEnabled = true
	parent.WorktreeRootPath = parent.WorkspacePath
	profile := agentruntime.FinderAgentProfileForParent(pebblestore.AgentProfile{})
	child, err := svc.prepareDelegatedSubagentLaunchWithProfile(parent, "auto", taskLaunchPrepared{LogicalTaskID: "helper-test", RequestedSubagent: "finder", MetaPrompt: "inspect", LaunchIndex: 1}, "inspect", "", &profile, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if child.SubagentProfile.Name != agentruntime.HelperAgentID || child.ChildSession.WorkspacePath != parent.WorktreeRootPath {
		t.Fatalf("wrong child: %+v", child.ChildSession)
	}
	helper := child.ChildSession
	helper.Metadata["agent_profile"] = child.SubagentProfile
	if err := validateHelperTask(helper, taskCallArguments{}); err == nil {
		t.Fatal("recursive helper accepted")
	}
}
