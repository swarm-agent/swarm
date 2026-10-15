package api

import (
	"swarm/packages/swarmd/internal/identity"

	runruntime "swarm/packages/swarmd/internal/run"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Only scope and cheap validation are exposed, not the execution compiler.
type capabilityScopeOnly struct {
	runService
	resolve func(pebblestore.SessionSnapshot, identity.Principal) (tool.WorkspaceScope, error)
}

func (r capabilityScopeOnly) ResolveRuntimeWorkspaceScope(s pebblestore.SessionSnapshot, p identity.Principal) (tool.WorkspaceScope, error) {
	return r.resolve(s, p)
}

func (r capabilityScopeOnly) ValidateStoredV3AgentToolContract(account string, profile pebblestore.AgentProfile) error {
	return r.runService.(interface {
		ValidateStoredV3AgentToolContract(string, pebblestore.AgentProfile) error
	}).ValidateStoredV3AgentToolContract(account, profile)
}

type capabilityPoisonExecution struct{ capabilityScopeOnly }

func (capabilityPoisonExecution) CompileStoredV3AgentToolContract(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, map[string]bool, error) {
	panic("capability read compiled execution tools")
}
func (capabilityPoisonExecution) ComposeRuntimeInstructions(tool.WorkspaceScope, string, bool, pebblestore.AgentProfile, string) string {
	panic("capability read composed prompts")
}
func (capabilityPoisonExecution) ComposeDurableRunStateInstructions(string, string, string, *runruntime.RunPlanCheckpointContext) (string, error) {
	panic("capability read composed durable instructions")
}
