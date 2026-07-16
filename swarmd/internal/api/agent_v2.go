package api

import (
	"errors"
	"io"
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
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("product identity required"))
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
	customTools, err := s.agents.ListCustomToolsForAccount(principal.AccountScopeID, limit)
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
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("product identity required"))
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
		definition, ok, err := s.agents.GetCustomToolForAccount(principal.AccountScopeID, name)
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
		stored, err := s.agents.PutCustomToolForAccount(principal.AccountScopeID, pebblestore.AgentCustomToolDefinition{
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
		deleted, err := s.agents.DeleteCustomToolForAccount(principal.AccountScopeID, name)
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
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("product identity required"))
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
	state, err := s.agents.ListStateForAccount(principal.AccountScopeID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	state.Profiles, err = s.publicAgentProfiles(state.Profiles)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("view")), "summary") {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    true,
			"state": compactAgentStateForDesktop(state),
		})
		return
	}
	toolInventory, err := s.agentToolInventoryForAccount(principal.AccountScopeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                        true,
		"state":                     state,
		"provider_defaults_preview": s.providerDefaultsPreviewForState(state),
		"tool_inventory":            toolInventory,
	})
}

func (s *Server) publicAgentProfiles(profiles []pebblestore.AgentProfile) ([]pebblestore.AgentProfile, error) {
	out := make([]pebblestore.AgentProfile, 0, len(profiles)+8)
	contexts := make(map[string]pebblestore.AgentProfile, len(profiles))
	for _, profile := range profiles {
		name := strings.ToLower(strings.TrimSpace(profile.Name))
		contexts[name] = profile
		if !agentruntime.IsReservedSystemAgentName(name) {
			out = append(out, profile)
		}
	}
	registry, err := s.agents.SystemAgentRegistry()
	if err != nil {
		return nil, err
	}
	for _, id := range registry.IDs() {
		context := pebblestore.AgentProfile{}
		if id == agentruntime.SwarmAgentID {
			context = contexts[agentruntime.SwarmAgentID]
		}
		profile, err := registry.Materialize(id, context)
		if err != nil {
			return nil, err
		}
		profile.Protected = true
		out = append(out, profile)
	}
	return out, nil
}

func compactAgentStateForDesktop(state agentruntime.State) map[string]any {
	profiles := make([]compactAgentProfileForDesktop, 0, len(state.Profiles))
	for _, profile := range state.Profiles {
		profile = pebblestore.NormalizeAgentProfile(profile)
		profiles = append(profiles, compactAgentProfileForDesktop{
			Name:               profile.Name,
			Mode:               profile.Mode,
			Protected:          profile.Protected,
			Provider:           profile.Provider,
			Model:              profile.Model,
			Thinking:           profile.Thinking,
			ModelMode:          profile.ModelMode,
			PlanProvider:       profile.PlanProvider,
			PlanModel:          profile.PlanModel,
			PlanThinking:       profile.PlanThinking,
			PlanServiceTier:    profile.PlanServiceTier,
			AutoProvider:       profile.AutoProvider,
			AutoModel:          profile.AutoModel,
			AutoThinking:       profile.AutoThinking,
			AutoServiceTier:    profile.AutoServiceTier,
			RuntimeMode:        pebblestore.AgentProfileRuntimeMode(profile),
			DefaultSessionMode: pebblestore.AgentProfileDefaultSessionMode(profile),
			Enabled:            profile.Enabled,
		})
	}
	return map[string]any{
		"profiles":        profiles,
		"active_primary":  state.ActivePrimary,
		"active_subagent": state.ActiveSubagent,
		"version":         state.Version,
	}
}

type compactAgentProfileForDesktop struct {
	Name               string `json:"name"`
	Mode               string `json:"mode"`
	Protected          bool   `json:"protected,omitempty"`
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	Thinking           string `json:"thinking"`
	ModelMode          string `json:"model_mode,omitempty"`
	PlanProvider       string `json:"plan_provider,omitempty"`
	PlanModel          string `json:"plan_model,omitempty"`
	PlanThinking       string `json:"plan_thinking,omitempty"`
	PlanServiceTier    string `json:"plan_service_tier,omitempty"`
	AutoProvider       string `json:"auto_provider,omitempty"`
	AutoModel          string `json:"auto_model,omitempty"`
	AutoThinking       string `json:"auto_thinking,omitempty"`
	AutoServiceTier    string `json:"auto_service_tier,omitempty"`
	RuntimeMode        string `json:"runtime_mode,omitempty"`
	DefaultSessionMode string `json:"default_session_mode,omitempty"`
	Enabled            bool   `json:"enabled"`
}

func (s *Server) handleAgentDefaultsRestoreV2(w http.ResponseWriter, r *http.Request) {
	if s.agents == nil {
		writeError(w, http.StatusInternalServerError, errors.New("agent service not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("product identity required"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		UtilityProvider   *string `json:"utility_provider"`
		UtilityModel      *string `json:"utility_model"`
		UtilityThinking   *string `json:"utility_thinking"`
		OverwriteExplicit *bool   `json:"overwrite_explicit"`
	}
	if r.Body != nil && r.Body != http.NoBody {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			if err := decodeJSONBytes(body, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
	}
	hasUtilityOverride := req.UtilityProvider != nil || req.UtilityModel != nil || req.UtilityThinking != nil || req.OverwriteExplicit != nil
	var state agentruntime.State
	var err error
	if hasUtilityOverride {
		state, err = s.agents.ListStateForAccount(principal.AccountScopeID, 2000)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		provider := ""
		model := ""
		thinking := ""
		if req.UtilityProvider != nil {
			provider = *req.UtilityProvider
		}
		if req.UtilityModel != nil {
			model = *req.UtilityModel
		}
		if req.UtilityThinking != nil {
			thinking = *req.UtilityThinking
		}
		overwriteExplicit := req.OverwriteExplicit != nil && *req.OverwriteExplicit
		state, err = s.applyUtilityAIToBuiltInsForAccount(principal.AccountScopeID, state, provider, model, thinking, overwriteExplicit)
	} else {
		state, _, _, err = s.agents.RestoreDefaultsForAccount(principal.AccountScopeID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		state, err = s.applyProviderDefaultsToBuiltInsForAccount(principal.AccountScopeID, state)
	}
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
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("product identity required"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	state, version, _, err := s.agents.ResetDefaultsForAccount(principal.AccountScopeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
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
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("product identity required"))
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
		activePrimary, version, _, err := s.agents.ActivatePrimaryForAccount(principal.AccountScopeID, req.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
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
		profile, _, err := s.agents.ResolveToolContractProfileForAccount(principal.AccountScopeID, name)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
			}
			writeError(w, status, err)
			return
		}
		if s.runner == nil {
			writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
			return
		}
		resolved, compiledPolicy, _, err := s.runner.ResolveAgentToolContractForAccount(principal.AccountScopeID, profile)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		toolInventory, err := s.agentToolInventoryForAccount(principal.AccountScopeID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                true,
			"agent":             strings.TrimSpace(profile.Name),
			"raw_tool_contract": profile.ToolContract,
			"resolved":          resolved,
			"compiled_policy":   compiledPolicy,
			"tool_inventory":    toolInventory,
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
			profile, version, _, err := s.agents.AssignCustomToolForAccount(principal.AccountScopeID, agentName, toolName)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":        true,
				"profile":   profile,
				"tool_name": pebblestore.NormalizeAgentCustomToolName(toolName),
				"version":   version,
			})
		case http.MethodDelete:
			profile, version, _, err := s.agents.UnassignCustomToolForAccount(principal.AccountScopeID, agentName, toolName)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
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
		profile, ok, err := s.agents.GetProfileForAccount(principal.AccountScopeID, name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("agent not found"))
			return
		}
		if _, system := agentruntime.CanonicalSystemAgentID(profile.Name); system {
			profile.Protected = true
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
			ModelMode           string                                  `json:"model_mode"`
			PlanProvider        *string                                 `json:"plan_provider"`
			PlanModel           *string                                 `json:"plan_model"`
			PlanThinking        *string                                 `json:"plan_thinking"`
			PlanServiceTier     *string                                 `json:"plan_service_tier"`
			AutoProvider        *string                                 `json:"auto_provider"`
			AutoModel           *string                                 `json:"auto_model"`
			AutoThinking        *string                                 `json:"auto_thinking"`
			AutoServiceTier     *string                                 `json:"auto_service_tier"`
			Prompt              string                                  `json:"prompt"`
			RuntimeMode         string                                  `json:"runtime_mode"`
			DefaultSessionMode  string                                  `json:"default_session_mode"`
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
		planProvider := ""
		if req.PlanProvider != nil {
			planProvider = *req.PlanProvider
		}
		planModel := ""
		if req.PlanModel != nil {
			planModel = *req.PlanModel
		}
		planThinking := ""
		if req.PlanThinking != nil {
			planThinking = *req.PlanThinking
		}
		planServiceTier := ""
		if req.PlanServiceTier != nil {
			planServiceTier = *req.PlanServiceTier
		}
		autoProvider := ""
		if req.AutoProvider != nil {
			autoProvider = *req.AutoProvider
		}
		autoModel := ""
		if req.AutoModel != nil {
			autoModel = *req.AutoModel
		}
		autoThinking := ""
		if req.AutoThinking != nil {
			autoThinking = *req.AutoThinking
		}
		autoServiceTier := ""
		if req.AutoServiceTier != nil {
			autoServiceTier = *req.AutoServiceTier
		}
		storedCustomTools := make([]pebblestore.AgentCustomToolDefinition, 0, len(req.CustomTools))
		for _, definition := range req.CustomTools {
			stored, err := s.agents.PutCustomToolForAccount(principal.AccountScopeID, definition)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			storedCustomTools = append(storedCustomTools, stored)
		}
		profile, version, _, err := s.agents.UpsertForAccount(principal.AccountScopeID, agentruntime.UpsertInput{
			Name:                name,
			Mode:                req.Mode,
			Description:         req.Description,
			Provider:            provider,
			Model:               model,
			Thinking:            thinking,
			ProviderSet:         req.Provider != nil,
			ModelSet:            req.Model != nil,
			ThinkingSet:         req.Thinking != nil,
			ModelMode:           req.ModelMode,
			PlanProvider:        planProvider,
			PlanModel:           planModel,
			PlanThinking:        planThinking,
			PlanServiceTier:     planServiceTier,
			PlanProviderSet:     req.PlanProvider != nil,
			PlanModelSet:        req.PlanModel != nil,
			PlanThinkingSet:     req.PlanThinking != nil,
			PlanServiceTierSet:  req.PlanServiceTier != nil,
			AutoProvider:        autoProvider,
			AutoModel:           autoModel,
			AutoThinking:        autoThinking,
			AutoServiceTier:     autoServiceTier,
			AutoProviderSet:     req.AutoProvider != nil,
			AutoModelSet:        req.AutoModel != nil,
			AutoThinkingSet:     req.AutoThinking != nil,
			AutoServiceTierSet:  req.AutoServiceTier != nil,
			Prompt:              req.Prompt,
			RuntimeMode:         req.RuntimeMode,
			DefaultSessionMode:  req.DefaultSessionMode,
			ExecutionSetting:    req.ExecutionSetting,
			ExitPlanModeEnabled: req.ExitPlanModeEnabled,
			ToolContract:        req.ToolContract,
			Enabled:             req.Enabled,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		assignedCustomTools := normalizeUniqueCustomToolNames(req.AssignCustomTools)
		if len(assignedCustomTools) == 0 && len(storedCustomTools) > 0 {
			assignedCustomTools = make([]string, 0, len(storedCustomTools))
			for _, definition := range storedCustomTools {
				assignedCustomTools = append(assignedCustomTools, definition.Name)
			}
		}
		for _, toolName := range assignedCustomTools {
			profile, version, _, err = s.agents.AssignCustomToolForAccount(principal.AccountScopeID, name, toolName)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
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
		result, version, _, err := s.agents.DeleteForAccount(principal.AccountScopeID, name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
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

func isIntegrationFlowRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("flow"))) {
	case "integration", "integrations":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("integration_flow"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
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
