package run

import (
	"strings"

	agent "swarm/packages/swarmd/internal/agent"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// InstructionProfileForTools is an ephemeral instruction projection, never a
// stored agent or permission authority. Runtime overlays may remove tools from a
// compiled role; its operational guidance must follow that final exposed list.
func InstructionProfileForTools(profile store.AgentProfile, definitions []provideriface.ToolDefinition) store.AgentProfile {
	allowed := make(map[string]bool, len(definitions))
	contract := &store.AgentToolContract{Preset: "custom", Tools: map[string]store.AgentToolConfig{}}
	for _, name := range storedV3BuiltinToolNames {
		contract.Tools[name] = store.AgentToolConfig{Enabled: store.BoolPtr(false)}
	}
	if profile.ToolContract != nil {
		for name := range profile.ToolContract.Tools {
			contract.Tools[canonicalToolName(name)] = store.AgentToolConfig{Enabled: store.BoolPtr(false)}
		}
	}
	for _, definition := range definitions {
		name := canonicalToolName(definition.Name)
		allowed[name] = true
		contract.Tools[name] = store.AgentToolConfig{Enabled: store.BoolPtr(true)}
	}
	restricted := func(names ...string) bool {
		for _, name := range names {
			if profileHasCapability(profile, name) && !allowed[name] {
				return true
			}
		}
		return false
	}
	switch {
	case strings.EqualFold(profile.Name, agent.SwarmAgentID) && restricted("task", "bash"):
		profile.Prompt = "You are Swarm. Own the supplied goal and acceptance criteria using only the tools exposed in this request. Preserve dependency/source readiness, exact results and unresolved blockers. Delegation and command execution are unavailable when their tools are absent; never infer success or bypass the imposed restriction. Keep work linked to its durable session and do not create workers or automations."
	case profile.Name == agent.WorkerCoderAgentID && restricted("bash", "task", "ask_user", "task_progress", "git_commit", "write", "edit"):
		profile.Prompt = "You are the retained worker Coder for the supplied job. Its brief, repository rules and acceptance contract determine work and validation phases. Use only exposed tools; if implementation, required checks, helper delegation or committing are unavailable, preserve completed work and report the exact pending step without claiming it ran. Do not bypass sandbox, permission or tool restrictions. Hand back precise outputs, commit state, checks and blockers."
	case agent.IsCoderAgentName(profile.Name) && restricted("bash", "task_progress", "git_commit", "write", "edit"):
		profile.Prompt = "You are the regular Coder for the supplied isolated implementation assignment. Use only exposed tools in the assigned worktree; preserve unrelated work, never delegate, and never provision or manage execution environments. Run required focused checks only when Bash is exposed; otherwise return pending checks marked 'not run; parent validation required'. Report exact source/commit state, commands, working directory, results and missing prerequisites. If a required edit, progress or commit operation is unavailable, report that blocker and the actual uncommitted state; never invent a successful commit."
	case agent.IsDesignerAgentName(profile.Name) && restricted(tool.SwarmDesignToolName):
		profile.Prompt = "You are Designer. swarm_design, your only way to make or edit designs, is unavailable in this request. Do not write HTML any other way or fabricate a design. Report to the caller that swarm_design is unavailable and that no design was made."
	}
	profile.ToolContract = contract
	return profile
}
