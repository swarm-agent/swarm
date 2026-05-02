package run

import "testing"

func TestBuildBackgroundRunMetadataPreservesFlowTargetIdentity(t *testing.T) {
	existing := map[string]any{
		"source":            "flow",
		"lineage_kind":      "flow",
		"owner_transport":   "flow_scheduler",
		"flow_id":           "flow-child-4",
		"target_kind":       "remote",
		"target_name":       "swarm child 4",
		"target_swarm_id":   "child-4-swarm",
		"swarm_target_name": "swarm child 4",
		"flow_agent_kind":   "background",
		"flow_agent_name":   "memory",
	}

	metadata := buildBackgroundRunMetadata(existing, "background", "memory", resolvedRunExecutionContext{WorkspacePath: "/workspaces/swarm-go"})

	if metadata["target_kind"] != "remote" || metadata["target_name"] != "swarm child 4" {
		t.Fatalf("flow target identity was overwritten: %+v", metadata)
	}
	if metadata["flow_agent_kind"] != "background" || metadata["flow_agent_name"] != "memory" {
		t.Fatalf("flow agent identity missing: %+v", metadata)
	}
	if metadata["launch_mode"] != "background" || metadata["background"] != true {
		t.Fatalf("background metadata missing: %+v", metadata)
	}
}

func TestBuildBackgroundRunMetadataSetsTargetForOrdinaryBackgroundRuns(t *testing.T) {
	metadata := buildBackgroundRunMetadata(map[string]any{"source": "chat"}, "background", "memory", resolvedRunExecutionContext{WorkspacePath: "/workspace"})

	if metadata["target_kind"] != "background" || metadata["target_name"] != "memory" {
		t.Fatalf("ordinary background target metadata = %+v", metadata)
	}
}
