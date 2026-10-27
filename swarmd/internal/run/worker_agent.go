package run

import (
	"fmt"
	"sort"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// ApplyWorkerAgent overlays only user-owned instructions and tools. The role's
// identity remains authoritative for code-owned runtime restrictions.
func (s *Service) ApplyWorkerAgent(account string, w store.WorkerRecord, profile store.AgentProfile) (store.AgentProfile, error) {
	name := strings.TrimSpace(w.Agent)
	if name == "" {
		return profile, nil
	}
	if s == nil || s.agents == nil {
		return profile, fmt.Errorf("agent service is not configured")
	}
	if _, system := agentruntime.CanonicalSystemAgentID(name); system || strings.HasPrefix(strings.ToLower(name), "system-") {
		return profile, fmt.Errorf("worker agent %q must be a non-system custom agent", name)
	}
	custom, found, err := s.agents.GetProfileForAccount(account, name)
	if err != nil {
		return profile, err
	}
	if !found {
		return profile, fmt.Errorf("worker agent %q does not exist in this account", name)
	}
	if !custom.Enabled {
		return profile, fmt.Errorf("worker agent %q is disabled", name)
	}
	if custom.Protected {
		return profile, fmt.Errorf("worker agent %q must be a non-system custom agent", name)
	}
	if custom.ToolContract == nil {
		return profile, fmt.Errorf("worker agent %q tool_contract is not configured", name)
	}
	if w.Role == store.WorkerRoleReviewer {
		// Inspect the custom contract before role overrides hide task. Reviewer
		// fixes are a separate code-owned execution mode, not a permission bypass.
		check := custom
		check.Mode = ""
		resolved, _, err := s.resolveValidatedStoredV3AgentToolStates(account, check)
		if err != nil {
			return profile, err
		}
		writes := map[string]bool{"write": true, "edit": true, "git_add": true, "git_commit": true, "task": true}
		tools, err := s.listCustomAgentToolsForRun(account)
		if err != nil {
			return profile, err
		}
		for _, tool := range tools {
			if tool.Effect == store.AgentCustomToolEffectWrite {
				writes[tool.Name] = true
			}
		}
		denied := []string{}
		for tool := range writes {
			if resolved.Tools[tool].Enabled {
				denied = append(denied, tool)
			}
		}
		sort.Strings(denied)
		if len(denied) > 0 {
			return profile, fmt.Errorf("reviewer worker agent %q enables write tools: %s", name, strings.Join(denied, ", "))
		}
	}
	expected, _ := workerExecutionAgentID(w.Role, nil)
	if profile.Name != expected && !(w.Role == store.WorkerRoleReviewer && profile.Name == "system-reviewer-fixer") {
		return profile, fmt.Errorf("worker role does not match session agent %q", profile.Name)
	}
	if profile.Name == "system-reviewer-fixer" {
		return profile, nil
	}
	// Preserve the code-owned role identity, but persist a narrowing scope so
	// role defaults cannot grant tools outside the accepted custom contract.
	resolved, _, err := s.resolveValidatedStoredV3AgentToolStates(account, custom)
	if err != nil {
		return profile, err
	}
	if profile.ToolScope == nil {
		profile.ToolScope = &store.AgentToolScope{}
	}
	for name, state := range resolved.Tools {
		if !state.Enabled {
			profile.ToolScope.DenyTools = append(profile.ToolScope.DenyTools, name)
		}
	}
	sort.Strings(profile.ToolScope.DenyTools)
	profile.ToolContract = store.CloneAgentToolContract(custom.ToolContract)
	if strings.TrimSpace(custom.Prompt) != "" {
		profile.Prompt = strings.TrimSpace(profile.Prompt + "\n\n" + custom.Prompt)
	}
	if err := s.ValidateStoredV3AgentToolContract(account, profile); err != nil {
		return profile, err
	}
	return profile, nil
}

// ValidateWorkerAgent uses the same role and tool resolution as job admission.
func (s *Service) ValidateWorkerAgent(account string, w store.WorkerRecord) error {
	if strings.TrimSpace(w.Agent) == "" {
		return nil
	}
	if s == nil || s.agents == nil {
		return fmt.Errorf("agent service is not configured")
	}
	profile, err := s.agents.ResolveSystemAgent(workerRoleAgentID(w.Role), store.AgentProfile{})
	if err != nil {
		return err
	}
	_, err = s.ApplyWorkerAgent(account, w, profile)
	return err
}
