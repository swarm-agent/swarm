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

func (s *Service) ResolveAgentToolContract(profile pebblestore.AgentProfile) (ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return s.compileResolvedAgentToolContract("", profile)
}

func (s *Service) ResolveAgentToolContractForAccount(accountScopeID string, profile pebblestore.AgentProfile) (ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return s.resolveAgentToolContractForAccount(strings.TrimSpace(accountScopeID), strings.TrimSpace(profile.Name))
}

func (s *Service) resolveAgentToolContractForAccount(accountScopeID, name string) (ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	if s == nil || s.agents == nil {
		return ResolvedAgentToolContract{}, nil, nil, fmt.Errorf("agent service is not configured")
	}
	profile, contract, err := s.agents.ResolveToolContractProfileForAccount(accountScopeID, name)
	if err != nil {
		return ResolvedAgentToolContract{}, nil, nil, err
	}
	profile.ToolContract = contract
	return s.compileResolvedAgentToolContract(accountScopeID, profile)
}

func (s *Service) compileResolvedAgentToolContract(accountScopeID string, profile pebblestore.AgentProfile) (ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	knownTools := s.knownRunToolNamesForAccount(accountScopeID)
	if len(knownTools) == 0 {
		return ResolvedAgentToolContract{}, nil, nil, fmt.Errorf("tool runtime is not configured")
	}
	if profile.ToolContract == nil {
		return ResolvedAgentToolContract{}, nil, nil, fmt.Errorf("agent %q tool_contract is not configured", strings.TrimSpace(profile.Name))
	}

	contract := profile.ToolContract
	activePreset := strings.TrimSpace(contract.Preset)
	inheritPolicy := contract.InheritPolicy
	resolved := ResolvedAgentToolContract{
		RuntimeMode:   strings.TrimSpace(profile.RuntimeMode),
		RawPreset:     activePreset,
		InheritPolicy: inheritPolicy,
		Tools:         make(map[string]ResolvedAgentTool, len(knownTools)),
	}
	for name := range knownTools {
		resolved.Tools[name] = ResolvedAgentTool{Enabled: false, Source: "tool_contract.default"}
	}
	if err := applyNamedAgentPreset(resolved.Tools, knownTools, activePreset); err != nil {
		return ResolvedAgentToolContract{}, nil, nil, err
	}
	applyExplicitAgentTools(resolved.Tools, contract.Tools, "tool_contract")

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
	enable := func(names ...string) {
		for _, name := range names {
			name = canonicalToolName(name)
			if name == "" {
				continue
			}
			target[name] = ResolvedAgentTool{Enabled: true, Source: "preset:" + preset}
		}
	}
	switch preset {
	case "read_only":
		enable("read", "search", "list", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode")
	case "integration_builder":
		enable("read", "search", "list", "websearch", "webfetch", "manage_integrations")
	case "read_write":
		enable("read", "search", "list", "write", "edit", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode")
	case "bash_git_only":
		enable("read", "search", "list", "bash", "skill_use", "plan_manage", "ask_user", "exit_plan_mode")
		target["bash"] = ResolvedAgentTool{
			Enabled:      true,
			Source:       "preset:" + preset,
			BashPrefixes: []string{"git status", "git diff", "git log", "git show"},
		}
	case "background_commit":
		enable("read", "search", "list", "git_status", "git_diff", "git_add", "git_commit")
	default:
		return fmt.Errorf("unsupported tool contract preset %q", preset)
	}
	return nil
}

func applyExplicitAgentTools(target map[string]ResolvedAgentTool, tools map[string]pebblestore.AgentToolConfig, sourcePrefix string) {
	for rawName, cfg := range tools {
		name := resolveExplicitAgentToolName(target, rawName)
		if name == "" {
			continue
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
	return name
}
