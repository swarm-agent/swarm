package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// AITaskPreparation is the complete, strict one-shot preparer response. The
// unexported deployment implementation consumes this only after validation.
type AITaskPreparation struct {
	Title    string `json:"title"`
	Prompt   string `json:"prompt"`
	Mode     string `json:"mode"`
	Worktree bool   `json:"worktree"`
}

func ParseAITaskPreparation(raw string) (AITaskPreparation, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &fields); err != nil {
		return AITaskPreparation{}, fmt.Errorf("AI task preparation must be one JSON object: %w", err)
	}
	for key := range fields {
		switch key {
		case "title", "prompt", "mode", "worktree":
		default:
			return AITaskPreparation{}, fmt.Errorf("AI task preparation rejects field %q", key)
		}
	}
	if len(fields) != 4 {
		return AITaskPreparation{}, fmt.Errorf("AI task preparation requires title, prompt, mode, and worktree")
	}
	var out AITaskPreparation
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &out); err != nil {
		return AITaskPreparation{}, err
	}
	out.Title, out.Prompt, out.Mode = strings.TrimSpace(out.Title), strings.TrimSpace(out.Prompt), strings.ToLower(strings.TrimSpace(out.Mode))
	if out.Title == "" || out.Prompt == "" {
		return AITaskPreparation{}, fmt.Errorf("AI task preparation title and prompt are required")
	}
	if out.Mode != sessionruntime.ModePlan && out.Mode != sessionruntime.ModeAuto {
		return AITaskPreparation{}, fmt.Errorf("AI task preparation mode must be plan or auto")
	}
	// Worktree placement is server-owned policy for queued AI tasks, not model
	// authority. Normalize the preparer field so a stale or noncompliant model
	// response cannot route deployment into the base workspace.
	out.Worktree = true
	return out, nil
}

// ResolveAITaskPreparer compiles the reserved preparer from the account's
// configured Swarm profile. Missing or disabled Swarm/model configuration fails
// explicitly instead of falling back to browser or provider defaults.
func (s *Service) ExecutePreparedAITask(ctx context.Context, parentSessionID, accountScopeID, workspacePath, taskID string, preparation AITaskPreparation, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	if s == nil || s.aiTaskBinder == nil {
		return "", fmt.Errorf("AI task binder is not configured")
	}
	workspacePath, taskID = strings.TrimSpace(workspacePath), strings.TrimSpace(taskID)
	if workspacePath == "" || taskID == "" {
		return "", fmt.Errorf("AI task workspace and id are required")
	}
	if _, err := ParseAITaskPreparation(marshalAITaskPreparation(preparation)); err != nil {
		return "", err
	}
	// Queued AI tasks always deploy through the managed-worktree branch of the
	// canonical session deploy path. The manifest builder resolves the user's
	// worktree base branch and branch family for both plan and auto modes.
	arguments, _ := json.Marshal(map[string]any{"action": "deploy", "proposals": []map[string]any{{"title": preparation.Title, "prompt": preparation.Prompt, "mode": preparation.Mode, "agent": agentruntime.SwarmAgentID, "workspace_path": workspacePath, "worktree": true}}})
	call := tool.Call{Name: "manage_sessions", Arguments: string(arguments)}
	manifest, err := s.buildManageSessionsDeployManifest(parentSessionID, call)
	if err != nil {
		return "", err
	}
	approved, _ := json.Marshal(manifest.ApprovedArguments)
	parent, _, _ := s.sessions.GetSession(parentSessionID)
	return s.executeManageSessionsDeployBound(ctx, parentSessionID, call, string(approved), apply, &AITaskDeployBinding{AccountScopeID: accountScopeID, WorkspacePath: workspacePath, TaskID: taskID, PreparationSessionID: parentSessionID, PreparationRunID: mapString(parent.Metadata, "ai_task_preparation_run_id")})
}

func marshalAITaskPreparation(value any) string { raw, _ := json.Marshal(value); return string(raw) }

func (s *Service) ResolveAITaskPreparer(accountScopeID string) (pebblestore.AgentProfile, error) {
	if s == nil || s.agents == nil {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent service is not configured")
	}
	state, err := s.agents.ListStateForAccount(strings.TrimSpace(accountScopeID), 2000)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	var swarm pebblestore.AgentProfile
	for _, profile := range state.Profiles {
		if strings.EqualFold(profile.Name, agentruntime.SwarmAgentID) {
			swarm = profile
			break
		}
	}
	if strings.TrimSpace(swarm.Name) == "" {
		swarm, err = s.agents.ResolveSystemAgent(agentruntime.SwarmAgentID, pebblestore.AgentProfile{})
	}
	if err != nil || !swarm.Enabled {
		return pebblestore.AgentProfile{}, fmt.Errorf("configured Swarm profile is missing or disabled")
	}
	preparer, err := s.agents.ResolveSystemAgent(agentruntime.AITaskPreparerAgentID, swarm)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	if strings.TrimSpace(preparer.Provider) == "" || strings.TrimSpace(preparer.Model) == "" {
		return pebblestore.AgentProfile{}, fmt.Errorf("configured Swarm auto model is missing")
	}
	return preparer, nil
}
