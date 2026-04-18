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
	if s == nil || s.tools == nil {
		return nil
	}
	definitions := s.tools.Definitions()
	customDefinitions := s.customAgentToolDefinitions()
	out := make([]tool.Definition, 0, len(definitions)+len(customDefinitions))
	out = append(out, definitions...)
	out = append(out, customDefinitions...)
	return out
}

func (s *Service) customAgentToolDefinitions() []tool.Definition {
	customTools, err := s.listCustomAgentToolsForRun()
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
	customTools, err := s.listCustomAgentToolsForRun()
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

func (s *Service) listCustomAgentToolsForRun() ([]pebblestore.AgentCustomToolDefinition, error) {
	if s == nil || s.agents == nil {
		return nil, nil
	}
	return s.agents.ListCustomTools(2000)
}

func commitOnlyToolNames() []string {
	return []string{"git_status", "git_diff", "git_add", "git_commit"}
}

func allowCommitOnlyTools(profile pebblestore.AgentProfile) bool {
	return strings.EqualFold(strings.TrimSpace(profile.Name), "commit") && profile.Mode == "background"
}

func (s *Service) ResolveAgentToolContract(profile pebblestore.AgentProfile) (ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	knownTools := s.knownRunToolNames()
	customTools := s.customAgentToolNameSet()
	if len(knownTools) == 0 {
		return ResolvedAgentToolContract{}, nil, nil, fmt.Errorf("tool runtime is not configured")
	}

	resolved := ResolvedAgentToolContract{
		RuntimeMode: contractRuntimeMode(profile),
		Tools:       make(map[string]ResolvedAgentTool, len(knownTools)),
	}
	for name := range knownTools {
		resolved.Tools[name] = ResolvedAgentTool{Enabled: false, Source: "default"}
	}

	if pebblestore.AgentExitPlanModeEnabled(profile) {
		for name := range knownTools {
			if _, ok := customTools[name]; ok {
				continue
			}
			resolved.Tools[name] = ResolvedAgentTool{Enabled: true, Source: "plan_mode"}
		}
	} else {
		applyExecutionSettingBaseline(resolved.Tools, pebblestore.NormalizeAgentExecutionSetting(profile.ExecutionSetting))
	}

	activePreset := ""
	inheritPolicy := false
	if profile.ToolContract != nil {
		activePreset = strings.TrimSpace(profile.ToolContract.Preset)
		inheritPolicy = profile.ToolContract.InheritPolicy
		if err := applyNamedAgentPreset(resolved.Tools, knownTools, activePreset); err != nil {
			return ResolvedAgentToolContract{}, nil, nil, err
		}
		applyExplicitAgentTools(resolved.Tools, profile.ToolContract.Tools, "tool_contract")
	} else if profile.ToolScope != nil {
		activePreset = strings.TrimSpace(profile.ToolScope.Preset)
		inheritPolicy = profile.ToolScope.InheritPolicy
		if err := applyNamedAgentPreset(resolved.Tools, knownTools, activePreset); err != nil {
			return ResolvedAgentToolContract{}, nil, nil, err
		}
		applyLegacyToolScope(resolved.Tools, profile.ToolScope)
	}

	if !pebblestore.AgentExitPlanModeEnabled(profile) {
		state := resolved.Tools["exit_plan_mode"]
		state.Enabled = false
		state.BashPrefixes = nil
		state.Source = "plan_mode_disabled"
		resolved.Tools["exit_plan_mode"] = state
	}

	if !allowCommitOnlyTools(profile) {
		for _, name := range commitOnlyToolNames() {
			state := resolved.Tools[name]
			state.Enabled = false
			state.BashPrefixes = nil
			state.Source = "commit_only"
			resolved.Tools[name] = state
		}
	}

	resolved.RawPreset = activePreset
	resolved.InheritPolicy = inheritPolicy

	policyRules := make([]permission.PolicyRule, 0, len(knownTools)+8)
	disabled := make(map[string]bool, len(knownTools))
	emitAllowRules := !pebblestore.AgentExitPlanModeEnabled(profile)
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
		if emitAllowRules {
			policyRules = append(policyRules, permission.PolicyRule{
				Kind:     permission.PolicyRuleKindTool,
				Decision: permission.PolicyDecisionAllow,
				Tool:     name,
			})
		}
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

func contractRuntimeMode(profile pebblestore.AgentProfile) string {
	if pebblestore.AgentExitPlanModeEnabled(profile) {
		return "plan_auto"
	}
	if setting, ok := pebblestore.AgentExecutionSetting(profile); ok {
		return setting
	}
	return "unset"
}

func applyExecutionSettingBaseline(target map[string]ResolvedAgentTool, setting string) {
	enable := func(source string, names ...string) {
		for _, name := range names {
			name = canonicalToolName(name)
			if name == "" {
				continue
			}
			target[name] = ResolvedAgentTool{Enabled: true, Source: source}
		}
	}
	disable := func(source string, names ...string) {
		for _, name := range names {
			name = canonicalToolName(name)
			if name == "" {
				continue
			}
			target[name] = ResolvedAgentTool{Enabled: false, Source: source}
		}
	}

	switch setting {
	case pebblestore.AgentExecutionSettingRead:
		enable("execution_setting:read",
			"read", "search", "list", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode",
		)
		disable("execution_setting:read", "write", "edit", "bash", "task")
	case pebblestore.AgentExecutionSettingReadWrite:
		enable("execution_setting:readwrite",
			"read", "search", "list", "write", "edit", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode",
		)
		disable("execution_setting:readwrite", "bash", "task")
	}
}

func applyNamedAgentPreset(target map[string]ResolvedAgentTool, knownTools map[string]struct{}, preset string) error {
	preset = strings.ToLower(strings.TrimSpace(preset))
	if preset == "" {
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

func applyLegacyToolScope(target map[string]ResolvedAgentTool, scope *pebblestore.AgentToolScope) {
	if scope == nil {
		return
	}
	for _, name := range scope.AllowTools {
		name = canonicalToolName(name)
		if name == "" {
			continue
		}
		target[name] = ResolvedAgentTool{Enabled: true, Source: "legacy.tool_scope"}
	}
	for _, name := range scope.DenyTools {
		name = canonicalToolName(name)
		if name == "" {
			continue
		}
		target[name] = ResolvedAgentTool{Enabled: false, Source: "legacy.tool_scope"}
	}
	if len(scope.BashPrefixes) > 0 {
		target["bash"] = ResolvedAgentTool{
			Enabled:      true,
			BashPrefixes: append([]string(nil), scope.BashPrefixes...),
			Source:       "legacy.tool_scope.bash",
		}
	}
}
