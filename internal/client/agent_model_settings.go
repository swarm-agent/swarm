package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const agentModelSettingsPath = "/v1/agent-model-settings"

// AgentModelAssignment is the canonical daemon-owned model selection shared by
// Swarm modes and compiled system agents.
type AgentModelAssignment struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	ServiceTier string `json:"service_tier,omitempty"`
	ContextMode string `json:"context_mode,omitempty"`
}

type SwarmAgentModelAssignments struct {
	Action AgentModelAssignment `json:"action"`
	Plan   AgentModelAssignment `json:"plan"`
}

type SystemAgentModelAssignments struct {
	Compact  AgentModelAssignment `json:"compact"`
	Finder   AgentModelAssignment `json:"finder"`
	Coder    AgentModelAssignment `json:"coder"`
	Designer AgentModelAssignment `json:"designer"`
	Router   AgentModelAssignment `json:"router"`
}

type AgentModelSettings struct {
	Swarm        SwarmAgentModelAssignments  `json:"swarm"`
	SystemAgents SystemAgentModelAssignments `json:"system_agents"`
	UpdatedAt    int64                       `json:"updated_at"`
}

type AgentModelSettingsSwarmPatch struct {
	Action AgentModelAssignment `json:"action"`
	Plan   AgentModelAssignment `json:"plan"`
}

type AgentModelSettingsSystemAgentsPatch struct {
	Compact  *AgentModelAssignment `json:"compact,omitempty"`
	Finder   *AgentModelAssignment `json:"finder,omitempty"`
	Coder    *AgentModelAssignment `json:"coder,omitempty"`
	Designer *AgentModelAssignment `json:"designer,omitempty"`
	Router   *AgentModelAssignment `json:"router,omitempty"`
}

type AgentModelSettingsPatch struct {
	Swarm        *AgentModelSettingsSwarmPatch        `json:"swarm,omitempty"`
	SystemAgents *AgentModelSettingsSystemAgentsPatch `json:"system_agents,omitempty"`
}

type agentModelSettingsResponse struct {
	AgentModelSettings AgentModelSettings `json:"agent_model_settings"`
}

func (c *API) GetAgentModelSettings(ctx context.Context) (AgentModelSettings, error) {
	var response agentModelSettingsResponse
	if err := c.getJSON(ctx, agentModelSettingsPath, &response, true); err != nil {
		return AgentModelSettings{}, err
	}
	return response.AgentModelSettings, nil
}

func (c *API) PatchAgentModelSettings(ctx context.Context, patch AgentModelSettingsPatch) (AgentModelSettings, error) {
	status, body, err := c.request(ctx, http.MethodPatch, agentModelSettingsPath, patch, true)
	if err != nil {
		return AgentModelSettings{}, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return AgentModelSettings{}, decodeAPIError(status, body)
	}
	var response agentModelSettingsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return AgentModelSettings{}, fmt.Errorf("decode %s response: %w", agentModelSettingsPath, err)
	}
	return response.AgentModelSettings, nil
}
