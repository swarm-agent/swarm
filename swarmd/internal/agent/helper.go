package agent

import pebblestore "swarm/packages/swarmd/internal/store/pebble"

const HelperAgentID = "system-readonly-helper"

// HelperAgentProfileForParent preserves the already resolved Finder model but
// never inherits the caller's mutable tool authority.
func HelperAgentProfileForParent(parent pebblestore.AgentProfile) pebblestore.AgentProfile {
	p := FinderAgentProfileForParent(parent)
	p.Name = HelperAgentID
	p.Prompt = "You are a read-only helper inside a Coder job. Inspect the assigned question and return findings directly. You cannot modify files, commit, deliver, or delegate. Bash permits read-only execution only."
	p.ToolContract = &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{}}
	for _, name := range []string{"read", "search", "find", "list", "bash"} {
		p.ToolContract.Tools[name] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(true)}
	}
	return p
}
