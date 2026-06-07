package run

import (
	"fmt"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type ResolvedAgentTool struct {
	Enabled      bool     `json:"enabled"`
	BashPrefixes []string `json:"bash_prefixes,omitempty"`
	Source       string   `json:"source,omitempty"`
}

type ResolvedAgentToolContract struct {
	RuntimeMode      string                       `json:"runtime_mode"`
	RawPreset        string                       `json:"raw_preset,omitempty"`
	InheritPolicy    bool                         `json:"inherit_policy,omitempty"`
	AvailableTools   []string                     `json:"available_tools,omitempty"`
	UnavailableTools []string                     `json:"unavailable_tools,omitempty"`
	Tools            map[string]ResolvedAgentTool `json:"tools,omitempty"`
}

func (s *Service) ListAgentToolDefinitions() []tool.Definition {
	return s.listAgentToolDefinitionsForAccount("")
}

func (s *Service) ListAgentToolDefinitionsForAccount(accountScopeID string) []tool.Definition {
	return s.listAgentToolDefinitionsForAccount(strings.TrimSpace(accountScopeID))
}

func (s *Service) listAgentToolDefinitionsForAccount(accountScopeID string) []tool.Definition {
	if s == nil || s.tools == nil {
		return nil
	}
	definitions := s.tools.Definitions()
	customDefinitions := s.customAgentToolDefinitionsForAccount(accountScopeID)
	out := make([]tool.Definition, 0, len(definitions)+len(customDefinitions))
	out = append(out, definitions...)
	out = append(out, customDefinitions...)
	return out
}

func (s *Service) customAgentToolDefinitions() []tool.Definition {
	return s.customAgentToolDefinitionsForAccount("")
}

func (s *Service) customAgentToolDefinitionsForAccount(accountScopeID string) []tool.Definition {
	customTools, err := s.listCustomAgentToolsForRun(accountScopeID)
	if err != nil {
		runRequestDebugEvent("custom_tool_inventory_error", map[string]any{
			"stage": "definitions",
			"error": err.Error(),
		})
		return nil
	}
	if len(customTools) == 0 {
		return nil
	}
	definitions := make([]tool.Definition, 0, len(customTools))
	for _, customTool := range customTools {
		name := canonicalToolName(customTool.Name)
		if name == "" {
			continue
		}
		description := strings.TrimSpace(customTool.Description)
		if description == "" {
			description = fmt.Sprintf("Custom agent tool (%s)", strings.TrimSpace(customTool.Kind))
		}
		definitions = append(definitions, tool.Definition{
			Type:        "function",
			Name:        name,
			Description: description,
			Parameters: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		})
	}
	return definitions
}

func (s *Service) customAgentToolNameSet() map[string]struct{} {
	return s.customAgentToolNameSetForAccount("")
}

func (s *Service) customAgentToolNameSetForAccount(accountScopeID string) map[string]struct{} {
	customTools, err := s.listCustomAgentToolsForRun(accountScopeID)
	if err != nil {
		runRequestDebugEvent("custom_tool_inventory_error", map[string]any{
			"stage": "name_set",
			"error": err.Error(),
		})
		return nil
	}
	if len(customTools) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(customTools))
	for _, customTool := range customTools {
		name := canonicalToolName(customTool.Name)
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Service) listCustomAgentToolsForRun(accountScopeID string) ([]pebblestore.AgentCustomToolDefinition, error) {
	if s == nil || s.agents == nil {
		return nil, nil
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID != "" {
		return s.agents.ListCustomToolsForAccount(accountScopeID, 2000)
	}
	return s.agents.ListCustomTools(2000)
}

func (s *Service) canonicalAgentToolNameSetForAccount(accountScopeID string) map[string]struct{} {
	definitions := s.ListAgentToolDefinitionsForAccount(accountScopeID)
	if len(definitions) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		name := canonicalToolName(definition.Name)
		if name == "" {
			continue
		}
		known[name] = struct{}{}
	}
	if len(known) == 0 {
		return nil
	}
	return known
}

func (s *Service) ResolveAgentToolContract(profile pebblestore.AgentProfile) (ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return s.resolveAgentToolContractForAccount("", profile)
}

func (s *Service) ResolveAgentToolContractForAccount(accountScopeID string, profile pebblestore.AgentProfile) (ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return s.resolveAgentToolContractForAccount(strings.TrimSpace(accountScopeID), profile)
}

func (s *Service) resolveAgentToolContractForAccount(accountScopeID string, profile pebblestore.AgentProfile) (ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	knownTools := s.canonicalAgentToolNameSetForAccount(accountScopeID)
	if len(knownTools) == 0 {
		return ResolvedAgentToolContract{}, nil, nil, fmt.Errorf("agent tool inventory is not configured")
	}
	if profile.ToolScope != nil {
		return ResolvedAgentToolContract{}, nil, nil, fmt.Errorf("legacy tool_scope is not supported for agent tool hydration; use tool_contract")
	}
	if profile.ToolContract == nil {
		return ResolvedAgentToolContract{}, nil, nil, fmt.Errorf("agent tool_contract is required for tool hydration")
	}

	resolved := ResolvedAgentToolContract{
		RuntimeMode: pebblestore.AgentProfileRuntimeMode(profile),
		Tools:       make(map[string]ResolvedAgentTool, len(knownTools)),
	}
	for name := range knownTools {
		resolved.Tools[name] = ResolvedAgentTool{Enabled: false, Source: "default"}
	}

	activePreset := strings.TrimSpace(profile.ToolContract.Preset)
	inheritPolicy := profile.ToolContract.InheritPolicy
	if err := applyNamedAgentPreset(resolved.Tools, knownTools, activePreset); err != nil {
		return ResolvedAgentToolContract{}, nil, nil, err
	}
	if err := applyExplicitAgentTools(resolved.Tools, profile.ToolContract.Tools, "tool_contract"); err != nil {
		return ResolvedAgentToolContract{}, nil, nil, err
	}

	resolved.RawPreset = activePreset
	resolved.InheritPolicy = inheritPolicy

	policyRules := make([]permission.PolicyRule, 0, len(knownTools)+8)
	disabled := make(map[string]bool, len(knownTools))
	for name, state := range resolved.Tools {
		name = canonicalToolName(name)
		if name == "" {
			continue
		}
		if state.Enabled && name == "bash" && len(state.BashPrefixes) > 0 {
			for _, prefix := range state.BashPrefixes {
				policyRules = append(policyRules, permission.PolicyRule{
					Kind:     permission.PolicyRuleKindBashPrefix,
					Decision: permission.PolicyDecisionAllow,
					Tool:     "bash",
					Pattern:  prefix,
				})
			}
			continue
		}
		if !state.Enabled {
			disabled[name] = true
			policyRules = append(policyRules, permission.PolicyRule{
				Kind:     permission.PolicyRuleKindTool,
				Decision: permission.PolicyDecisionDeny,
				Tool:     name,
			})
			continue
		}
		policyRules = append(policyRules, permission.PolicyRule{
			Kind:     permission.PolicyRuleKindTool,
			Decision: permission.PolicyDecisionAllow,
			Tool:     name,
		})
	}

	for name, state := range resolved.Tools {
		if state.Enabled {
			resolved.AvailableTools = append(resolved.AvailableTools, name)
			continue
		}
		resolved.UnavailableTools = append(resolved.UnavailableTools, name)
	}
	sort.Strings(resolved.AvailableTools)
	sort.Strings(resolved.UnavailableTools)

	compiled := permission.NormalizePolicy(permission.Policy{Version: 1, Rules: policyRules})
	if inheritPolicy && s != nil && s.permissions != nil {
		current, err := s.permissions.CurrentPolicy()
		if err != nil {
			return ResolvedAgentToolContract{}, nil, nil, err
		}
		merged := mergePermissionPolicies(&compiled, &current)
		compiled = merged
	}
	if len(disabled) == 0 {
		disabled = nil
	}
	return resolved, &compiled, disabled, nil
}

func applyNamedAgentPreset(target map[string]ResolvedAgentTool, knownTools map[string]struct{}, preset string) error {
	preset = strings.ToLower(strings.TrimSpace(preset))
	if preset == "" || preset == "custom" {
		return nil
	}
	for name := range knownTools {
		target[name] = ResolvedAgentTool{Enabled: false, Source: "preset:" + preset}
	}
	enable := func(names ...string) error {
		for _, rawName := range names {
			name := canonicalToolName(rawName)
			if name == "" {
				continue
			}
			if _, ok := knownTools[name]; !ok {
				return fmt.Errorf("tool contract preset %q references unknown tool %q", preset, rawName)
			}
			target[name] = ResolvedAgentTool{Enabled: true, Source: "preset:" + preset}
		}
		return nil
	}
	switch preset {
	case "read_only":
		return enable("read", "search", "list", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode")
	case "integration_builder":
		return enable("read", "search", "list", "websearch", "webfetch", "manage_integrations")
	case "read_write":
		return enable("read", "search", "list", "write", "edit", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode")
	case "bash_git_only":
		if err := enable("read", "search", "list", "bash", "skill_use", "plan_manage", "ask_user", "exit_plan_mode"); err != nil {
			return err
		}
		target["bash"] = ResolvedAgentTool{
			Enabled:      true,
			Source:       "preset:" + preset,
			BashPrefixes: []string{"git status", "git diff", "git log", "git show"},
		}
	case "background_commit":
		return enable("read", "search", "list", "git_status", "git_diff", "git_add", "git_commit")
	default:
		return fmt.Errorf("unsupported tool contract preset %q", preset)
	}
	return nil
}

func applyExplicitAgentTools(target map[string]ResolvedAgentTool, tools map[string]pebblestore.AgentToolConfig, sourcePrefix string) error {
	for rawName, cfg := range tools {
		name := resolveExplicitAgentToolName(target, rawName)
		if name == "" {
			return fmt.Errorf("tool contract references unknown tool %q", strings.TrimSpace(rawName))
		}
		state := target[name]
		if cfg.Enabled != nil {
			state.Enabled = *cfg.Enabled
		}
		if len(cfg.BashPrefixes) > 0 {
			state.Enabled = true
			state.BashPrefixes = append([]string(nil), cfg.BashPrefixes...)
		} else if !state.Enabled {
			state.BashPrefixes = nil
		}
		state.Source = sourcePrefix + "." + name
		target[name] = state
	}
	return nil
}

func resolveExplicitAgentToolName(target map[string]ResolvedAgentTool, rawName string) string {
	name := canonicalToolName(rawName)
	if name == "" {
		return ""
	}
	if _, ok := target[name]; ok {
		return name
	}
	if strings.Contains(name, "_") {
		hyphenated := strings.ReplaceAll(name, "_", "-")
		if _, ok := target[hyphenated]; ok {
			return hyphenated
		}
	}
	if strings.Contains(name, "-") {
		underscored := strings.ReplaceAll(name, "-", "_")
		if _, ok := target[underscored]; ok {
			return underscored
		}
	}
	return ""
}
