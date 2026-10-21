package run

import (
	"fmt"
	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func workerHelperParent(parent pebblestore.SessionSnapshot) bool {
	profile, err := sessionV3AgentProfileFromMetadataMap(parent.Metadata)
	return err == nil && profile.Name == agentruntime.WorkerCoderAgentID
}

func validateHelperTask(parent pebblestore.SessionSnapshot, parsed taskCallArguments) error {
	profile, _ := sessionV3AgentProfileFromMetadataMap(parent.Metadata)
	if profile.Name == agentruntime.HelperAgentID {
		return fmt.Errorf("read-only helper cannot invoke task: helper depth limit is 1")
	}
	if !workerHelperParent(parent) {
		return nil
	}
	if parsed.Program != nil || parsed.Swarm != nil {
		return fmt.Errorf("coder task permits read-only helpers only, not programs or swarms")
	}
	for _, launch := range parsed.Launches {
		if !agentruntime.IsFinderAgentName(launch.RequestedSubagentType) {
			return fmt.Errorf("coder task permits read-only Finder helpers only")
		}
		if launch.TargetWorkspacePath != "" {
			return fmt.Errorf("helper must use the coder job worktree")
		}
	}
	return nil
}

// The provider receipt is authoritative; stop before executing further tools
// when a helper exhausts its cumulative usage allowance.
type helperTokenBudgetKey struct{}
