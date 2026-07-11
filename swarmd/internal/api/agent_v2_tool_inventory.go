package api

import (
	"sort"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type agentToolPresetDefinition struct {
	ID                string
	Label             string
	Description       string
	EnabledTools      []string
	DisabledByDefault []string
	BashPrefixes      []string
}

func (s *Server) agentToolInventoryForAccount(accountScopeID string) (map[string]any, error) {
	var definitions []tool.Definition
	if s != nil && s.runner != nil {
		definitions = s.runner.ListAgentToolDefinitionsForAccount(strings.TrimSpace(accountScopeID))
	}
	customTools := []pebblestore.AgentCustomToolDefinition{}
	if s != nil && s.agents != nil {
		listed, err := s.agents.ListCustomToolsForAccount(strings.TrimSpace(accountScopeID), 2000)
		if err != nil {
			return nil, err
		}
		customTools = listed
	}
	return agentToolInventoryMap(definitions, customTools), nil
}

func agentToolInventoryMap(definitions []tool.Definition, customTools []pebblestore.AgentCustomToolDefinition) map[string]any {
	tools := make([]map[string]any, 0, len(definitions)+len(customTools))
	seen := make(map[string]struct{}, len(definitions)+len(customTools))
	for _, definition := range definitions {
		displayName := strings.TrimSpace(definition.Name)
		name := agentToolCanonicalName(displayName)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		tools = append(tools, map[string]any{
			"name":          displayName,
			"contract_name": name,
			"description":   strings.TrimSpace(definition.Description),
			"group":         agentToolGroup(name),
			"kind":          "built_in",
		})
	}
	customToolMaps := make([]map[string]any, 0, len(customTools))
	for _, customTool := range customTools {
		customToolMaps = append(customToolMaps, map[string]any{
			"name":        strings.TrimSpace(customTool.Name),
			"kind":        strings.TrimSpace(customTool.Kind),
			"description": strings.TrimSpace(customTool.Description),
			"updated_at":  customTool.UpdatedAt,
		})
		name := agentToolCanonicalName(customTool.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		tools = append(tools, map[string]any{
			"name":          strings.TrimSpace(customTool.Name),
			"contract_name": name,
			"description":   strings.TrimSpace(customTool.Description),
			"group":         "custom",
			"kind":          "custom",
		})
	}
	sort.Slice(tools, func(i, j int) bool {
		left, _ := tools[i]["contract_name"].(string)
		right, _ := tools[j]["contract_name"].(string)
		return strings.TrimSpace(left) < strings.TrimSpace(right)
	})
	return map[string]any{
		"tools":        tools,
		"tool_count":   len(tools),
		"custom_tools": customToolMaps,
		"presets":      agentToolPresetInventory(),
	}
}

func agentToolCanonicalName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ask-user", "ask_user":
		return "ask_user"
	case "exit-plan-mode", "exit_plan_mode":
		return "exit_plan_mode"
	case "plan-manage", "plan_manage":
		return "plan_manage"
	case "skill-use", "skill_use":
		return "skill_use"
	case "manage-skill", "manage_skill":
		return "manage_skill"
	case "manage-agent", "manage_agent":
		return "manage_agent"
	case "manage-integrations", "manage_integrations":
		return "manage_integrations"
	case "manage-theme", "manage_theme":
		return "manage_theme"
	case "manage-worktree", "manage_worktree":
		return "manage_worktree"
	case "manage-sessions", "manage_sessions":
		return "manage_sessions"
	case "manage-todos", "manage_todos":
		return "manage_todos"
	case "manage-image", "manage_image":
		return "manage_image"
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

func agentToolGroup(name string) string {
	switch name {
	case "read", "search", "list", "websearch", "webfetch", "agentic_search":
		return "read"
	case "write", "edit", "bash", "git_status", "git_diff", "git_add", "git_commit":
		return "write"
	case "task", "ask_user", "exit_plan_mode", "plan_manage", "skill_use":
		return "control"
	case "manage_agent", "manage_skill", "manage_todos", "manage_worktree", "manage_theme", "manage_integrations", "manage_image":
		return "management"
	default:
		return "other"
	}
}

func agentToolPresetInventory() []map[string]any {
	presets := []agentToolPresetDefinition{
		{
			ID:                "custom",
			Label:             "Custom",
			Description:       "Fully custom tool contract controlled by explicit per-tool allow/block choices.",
			EnabledTools:      []string{},
			DisabledByDefault: []string{},
		},
		{
			ID:                "read_only",
			Label:             "Read only",
			Description:       "Inspect workspace files and web content without file mutation or shell execution.",
			EnabledTools:      []string{"read", "search", "list", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode"},
			DisabledByDefault: []string{"write", "edit", "bash", "task"},
		},
		{
			ID:                "integration_builder",
			Label:             "Integration builder",
			Description:       "Inspect local/web context and manage Integration Pack drafts without shell or file mutation tools.",
			EnabledTools:      []string{"read", "search", "list", "websearch", "webfetch", "manage_integrations"},
			DisabledByDefault: []string{"write", "edit", "bash", "task"},
		},
		{
			ID:                "read_write",
			Label:             "Read/write",
			Description:       "Inspect and edit workspace files without shell execution or delegation.",
			EnabledTools:      []string{"read", "search", "list", "write", "edit", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode"},
			DisabledByDefault: []string{"bash", "task"},
		},
		{
			ID:                "bash_git_only",
			Label:             "Git shell only",
			Description:       "Allow read tools plus bash restricted to git status/diff/log/show prefixes.",
			EnabledTools:      []string{"read", "search", "list", "bash", "skill_use", "plan_manage", "ask_user", "exit_plan_mode"},
			DisabledByDefault: []string{"write", "edit", "task"},
			BashPrefixes:      []string{"git status", "git diff", "git log", "git show"},
		},
		{
			ID:                "background_commit",
			Label:             "Background commit",
			Description:       "Allow only read/list/search plus git status/diff/add/commit tools for durable commits.",
			EnabledTools:      []string{"read", "search", "list", "git_status", "git_diff", "git_add", "git_commit"},
			DisabledByDefault: []string{"write", "edit", "bash", "task"},
		},
	}
	out := make([]map[string]any, 0, len(presets))
	for _, preset := range presets {
		entry := map[string]any{
			"id":                  preset.ID,
			"label":               preset.Label,
			"description":         preset.Description,
			"enabled_tools":       append([]string(nil), preset.EnabledTools...),
			"disabled_by_default": append([]string(nil), preset.DisabledByDefault...),
		}
		if len(preset.BashPrefixes) > 0 {
			entry["bash_prefixes"] = append([]string(nil), preset.BashPrefixes...)
		}
		out = append(out, entry)
	}
	return out
}
