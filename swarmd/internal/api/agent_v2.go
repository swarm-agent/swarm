package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) handleCustomToolsV2(w http.ResponseWriter, r *http.Request) {
	if s.agents == nil {
		writeError(w, http.StatusInternalServerError, errors.New("agent service not configured"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	limit, err := parsePositiveLimit(r, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	customTools, err := s.agents.ListCustomTools(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"custom_tools": customTools,
	})
}

func (s *Server) handleCustomToolByNameV2(w http.ResponseWriter, r *http.Request) {
	if s.agents == nil {
		writeError(w, http.StatusInternalServerError, errors.New("agent service not configured"))
		return
	}
	const prefix = "/v2/custom-tools/"
	name := strings.TrimSpace(strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/"))
	if name == "" {
		writeError(w, http.StatusNotFound, errors.New("custom tool path is required"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		definition, ok, err := s.agents.GetCustomTool(name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("custom tool not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"custom_tool": definition,
		})
	case http.MethodPut:
		var req struct {
			Name        string `json:"name"`
			Kind        string `json:"kind"`
			Description string `json:"description"`
			Command     string `json:"command"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if trimmed := strings.TrimSpace(req.Name); trimmed != "" && !strings.EqualFold(trimmed, name) {
			writeError(w, http.StatusBadRequest, errors.New("custom tool name in path must match payload name"))
			return
		}
		stored, err := s.agents.PutCustomTool(pebblestore.AgentCustomToolDefinition{
			Name:        name,
			Kind:        req.Kind,
			Description: req.Description,
			Command:     req.Command,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"custom_tool": stored,
		})
	case http.MethodDelete:
		deleted, err := s.agents.DeleteCustomTool(name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !deleted {
			writeError(w, http.StatusNotFound, errors.New("custom tool not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"deleted": pebblestore.NormalizeAgentCustomToolName(name),
		})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleAgentsV2(w http.ResponseWriter, r *http.Request) {
	if s.agents == nil {
		writeError(w, http.StatusInternalServerError, errors.New("agent service not configured"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	limit, err := parsePositiveLimit(r, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	state, err := s.agents.ListState(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                        true,
		"state":                     state,
		"provider_defaults_preview": s.providerDefaultsPreviewForState(state),
	})
}

func (s *Server) handleAgentDefaultsRestoreV2(w http.ResponseWriter, r *http.Request) {
	if s.agents == nil {
		writeError(w, http.StatusInternalServerError, errors.New("agent service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	state, _, event, err := s.agents.RestoreDefaults()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	state, err = s.applyProviderDefaultsToBuiltIns(state)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                        true,
		"profiles":                  state.Profiles,
		"active_primary":            state.ActivePrimary,
		"active_subagent":           state.ActiveSubagent,
		"version":                   state.Version,
		"provider_defaults_preview": s.providerDefaultsPreviewForState(state),
	})
}

func (s *Server) handleAgentDefaultsResetV2(w http.ResponseWriter, r *http.Request) {
	if s.agents == nil {
		writeError(w, http.StatusInternalServerError, errors.New("agent service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	state, version, event, err := s.agents.ResetDefaults()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                        true,
		"profiles":                  state.Profiles,
		"active_primary":            state.ActivePrimary,
		"active_subagent":           state.ActiveSubagent,
		"version":                   version,
		"provider_defaults_preview": s.providerDefaultsPreviewForState(state),
	})
}

func (s *Server) handleAgentByNameV2(w http.ResponseWriter, r *http.Request) {
	if s.agents == nil {
		writeError(w, http.StatusInternalServerError, errors.New("agent service not configured"))
		return
	}
	const prefix = "/v2/agents/"
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if rest == "" {
		writeError(w, http.StatusNotFound, errors.New("agent path is required"))
		return
	}
	segments := strings.Split(rest, "/")
	for _, segment := range segments {
		if strings.TrimSpace(segment) == "" {
			writeError(w, http.StatusNotFound, errors.New("agent path is invalid"))
			return
		}
	}

	if len(segments) == 2 && strings.EqualFold(segments[0], "active") && strings.EqualFold(segments[1], "primary") {
		if r.Method != http.MethodPut {
			methodNotAllowed(w)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		activePrimary, version, event, err := s.agents.ActivatePrimary(req.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":             true,
			"active_primary": activePrimary,
			"version":        version,
		})
		return
	}

	if len(segments) == 2 && strings.EqualFold(segments[1], "tool-contract") {
		name := strings.TrimSpace(segments[0])
		if name == "" {
			writeError(w, http.StatusBadRequest, errors.New("agent name is required"))
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		profile, ok, err := s.agents.GetProfile(name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("agent not found"))
			return
		}
		if s.runner == nil {
			writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
			return
		}
		resolved, compiledPolicy, _, err := s.runner.ResolveAgentToolContract(profile)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                true,
			"agent":             strings.TrimSpace(profile.Name),
			"raw_tool_contract": profile.ToolContract,
			"resolved":          resolved,
			"compiled_policy":   compiledPolicy,
		})
		return
	}

	if len(segments) == 3 && strings.EqualFold(segments[1], "custom-tools") {
		agentName := strings.TrimSpace(segments[0])
		toolName := strings.TrimSpace(segments[2])
		if agentName == "" || toolName == "" {
			writeError(w, http.StatusBadRequest, errors.New("agent name and custom tool name are required"))
			return
		}
		switch r.Method {
		case http.MethodPut:
			profile, version, event, err := s.agents.AssignCustomTool(agentName, toolName)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if event != nil && s.hub != nil {
				s.hub.Publish(*event)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":        true,
				"profile":   profile,
				"tool_name": pebblestore.NormalizeAgentCustomToolName(toolName),
				"version":   version,
			})
		case http.MethodDelete:
			profile, version, event, err := s.agents.UnassignCustomTool(agentName, toolName)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if event != nil && s.hub != nil {
				s.hub.Publish(*event)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":        true,
				"profile":   profile,
				"tool_name": pebblestore.NormalizeAgentCustomToolName(toolName),
				"version":   version,
			})
		default:
			methodNotAllowed(w)
		}
		return
	}

	if len(segments) != 1 {
		writeError(w, http.StatusNotFound, errors.New("agent path is invalid"))
		return
	}
	name := strings.TrimSpace(segments[0])
	if name == "" {
		writeError(w, http.StatusBadRequest, errors.New("agent name is required"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		profile, ok, err := s.agents.GetProfile(name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("agent not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"profile": profile,
		})
	case http.MethodPut:
		var req struct {
			Mode                string                                  `json:"mode"`
			Description         string                                  `json:"description"`
			Provider            *string                                 `json:"provider"`
			Model               *string                                 `json:"model"`
			Thinking            *string                                 `json:"thinking"`
			Prompt              string                                  `json:"prompt"`
			ExecutionSetting    string                                  `json:"execution_setting"`
			ExitPlanModeEnabled *bool                                   `json:"exit_plan_mode_enabled"`
			ToolContract        *pebblestore.AgentToolContract          `json:"tool_contract"`
			Enabled             *bool                                   `json:"enabled"`
			CustomTools         []pebblestore.AgentCustomToolDefinition `json:"custom_tools"`
			AssignCustomTools   []string                                `json:"assign_custom_tools"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		provider := ""
		if req.Provider != nil {
			provider = *req.Provider
		}
		model := ""
		if req.Model != nil {
			model = *req.Model
		}
		thinking := ""
		if req.Thinking != nil {
			thinking = *req.Thinking
		}
		storedCustomTools := make([]pebblestore.AgentCustomToolDefinition, 0, len(req.CustomTools))
		for _, definition := range req.CustomTools {
			stored, err := s.agents.PutCustomTool(definition)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			storedCustomTools = append(storedCustomTools, stored)
		}
		profile, version, event, err := s.agents.Upsert(agentruntime.UpsertInput{
			Name:                name,
			Mode:                req.Mode,
			Description:         req.Description,
			Provider:            provider,
			Model:               model,
			Thinking:            thinking,
			ProviderSet:         req.Provider != nil,
			ModelSet:            req.Model != nil,
			ThinkingSet:         req.Thinking != nil,
			Prompt:              req.Prompt,
			ExecutionSetting:    req.ExecutionSetting,
			ExitPlanModeEnabled: req.ExitPlanModeEnabled,
			ToolContract:        req.ToolContract,
			Enabled:             req.Enabled,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		assignedCustomTools := normalizeUniqueCustomToolNames(req.AssignCustomTools)
		if len(assignedCustomTools) == 0 && len(storedCustomTools) > 0 {
			assignedCustomTools = make([]string, 0, len(storedCustomTools))
			for _, definition := range storedCustomTools {
				assignedCustomTools = append(assignedCustomTools, definition.Name)
			}
		}
		for _, toolName := range assignedCustomTools {
			profile, version, event, err = s.agents.AssignCustomTool(name, toolName)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if event != nil && s.hub != nil {
				s.hub.Publish(*event)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                    true,
			"profile":               profile,
			"version":               version,
			"custom_tools":          storedCustomTools,
			"assigned_custom_tools": assignedCustomTools,
		})
	case http.MethodDelete:
		result, version, event, err := s.agents.Delete(name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":             true,
			"deleted":        result.Deleted,
			"active_primary": result.ActivePrimary,
			"version":        version,
		})
	default:
		methodNotAllowed(w)
	}
}

func parsePositiveLimit(r *http.Request, defaultLimit int) (int, error) {
	limit := defaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return 0, errors.New("limit must be a positive integer")
		}
		limit = parsed
	}
	return limit, nil
}

func normalizeUniqueCustomToolNames(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		name := pebblestore.NormalizeAgentCustomToolName(value)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
