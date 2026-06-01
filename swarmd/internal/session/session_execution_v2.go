package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const (
	SessionExecutionClassPrimary        = "primary"
	SessionExecutionClassLocalContainer = "local_container"

	SessionExecutionTUICWDSourceIDPrefix    = "tui-cwd:"
	SessionExecutionTUICWDSourceGeneration  = int64(1)
	SessionExecutionTUICWDBindingGeneration = int(1)
)

type SessionExecution struct {
	SessionID                 string `json:"session_id,omitempty"`
	ExecutionClass            string `json:"execution_class"`
	RuntimeSwarmID            string `json:"runtime_swarm_id"`
	RuntimeKind               string `json:"runtime_kind"`
	AuthorityHostSwarmID      string `json:"authority_host_swarm_id"`
	AuthorityContainerID      string `json:"authority_container_id,omitempty"`
	WorkspaceBindingID        string `json:"workspace_binding_id"`
	SourceWorkspaceID         string `json:"source_workspace_id"`
	SourceWorkspaceGeneration int64  `json:"source_workspace_generation"`
	SourceWorkspaceName       string `json:"source_workspace_name,omitempty"`
	SourceWorkspacePath       string `json:"source_workspace_path"`
	RuntimeWorkspacePath      string `json:"runtime_workspace_path"`
	PlacementGeneration       int    `json:"placement_generation"`
	BindingGeneration         int    `json:"binding_generation"`
	CreatedAt                 int64  `json:"created_at,omitempty"`
	UpdatedAt                 int64  `json:"updated_at,omitempty"`
}

type SessionsV2CreateRequest struct {
	SwarmID                  string                      `json:"swarm_id"`
	WorkspaceBindingID       string                      `json:"workspace_binding_id"`
	WorkspacePath            string                      `json:"workspace_path,omitempty"`
	Title                    string                      `json:"title,omitempty"`
	Mode                     string                      `json:"mode,omitempty"`
	AgentName                string                      `json:"agent_name,omitempty"`
	WorktreeMode             string                      `json:"worktree_mode,omitempty"`
	WorktreeUseCurrentBranch *bool                       `json:"worktree_use_current_branch,omitempty"`
	WorktreeBaseBranch       string                      `json:"worktree_base_branch,omitempty"`
	WorktreeBranchName       string                      `json:"worktree_branch_name,omitempty"`
	Preference               pebblestore.ModelPreference `json:"preference,omitempty"`
	Metadata                 map[string]any              `json:"metadata,omitempty"`
}

type SessionsV2CreateCommand struct {
	Principal identity.Principal
	Request   SessionsV2CreateRequest
	Execution SessionExecution
}

func (s *Service) CreateFromExecutionV2(ctx context.Context, cmd SessionsV2CreateCommand) (pebblestore.SessionSnapshot, SessionExecution, *pebblestore.EventEnvelope, string, string, error) {
	if s == nil || s.store == nil {
		return pebblestore.SessionSnapshot{}, SessionExecution{}, nil, "", "", errors.New("session service is not configured")
	}
	if !cmd.Principal.Valid() {
		return pebblestore.SessionSnapshot{}, SessionExecution{}, nil, "", "", identity.ErrPrincipalRequired
	}
	execution := normalizeSessionExecutionV2(cmd.Execution)
	if err := validateSessionExecutionV2(execution); err != nil {
		return pebblestore.SessionSnapshot{}, SessionExecution{}, nil, "", "", err
	}
	if strings.TrimSpace(cmd.Request.SwarmID) != execution.RuntimeSwarmID {
		return pebblestore.SessionSnapshot{}, SessionExecution{}, nil, "", "", errors.New("sessions v2 request swarm_id does not match execution runtime")
	}
	if strings.TrimSpace(cmd.Request.WorkspaceBindingID) != execution.WorkspaceBindingID {
		return pebblestore.SessionSnapshot{}, SessionExecution{}, nil, "", "", errors.New("sessions v2 request workspace_binding_id does not match execution binding")
	}
	if execution.WorkspaceBindingID == "" && strings.TrimSpace(cmd.Request.WorkspacePath) != execution.RuntimeWorkspacePath {
		return pebblestore.SessionSnapshot{}, SessionExecution{}, nil, "", "", errors.New("sessions v2 request workspace_path does not match tui cwd execution")
	}
	if err := validateCreateFromExecutionV2Metadata(cmd.Request.Metadata); err != nil {
		return pebblestore.SessionSnapshot{}, SessionExecution{}, nil, "", "", err
	}

	preference, err := normalizeSessionPreferenceValue(normalizeSessionPreference(&cmd.Request.Preference))
	if err != nil {
		return pebblestore.SessionSnapshot{}, SessionExecution{}, nil, "", "", fmt.Errorf("normalize session preference: %w", err)
	}
	if strings.TrimSpace(preference.Provider) == "" || strings.TrimSpace(preference.Model) == "" || strings.TrimSpace(preference.Thinking) == "" {
		return pebblestore.SessionSnapshot{}, SessionExecution{}, nil, "", "", errors.New("session execution preference is required")
	}

	mode := NormalizeMode(cmd.Request.Mode)
	modeWarning := ""
	agentName := strings.TrimSpace(cmd.Request.AgentName)
	agentMode := ""
	if agentName == "" {
		agentName = "swarm"
	}

	sessionID := NewSessionID()
	now := time.Now().UnixMilli()
	execution.SessionID = sessionID
	execution.CreatedAt = now
	execution.UpdatedAt = now

	metadata := sessionExecutionV2Metadata(cmd.Request.Metadata, execution)
	metadata["workspace_id"] = worktreeruntime.WorkspaceIdentityForSession(sessionID)
	metadata["runtime_state"] = "standby"
	metadata["title_pending"] = true
	metadata["agent_name"] = agentName
	if strings.TrimSpace(agentMode) != "" {
		metadata["agent_mode"] = strings.TrimSpace(agentMode)
	}

	workspacePath := execution.RuntimeWorkspacePath
	workspaceName := execution.SourceWorkspaceName
	if workspaceName == "" {
		workspaceName = filepath.Base(execution.SourceWorkspacePath)
	}
	title := strings.TrimSpace(cmd.Request.Title)
	if title == "" {
		title = "New Session"
	}

	session := pebblestore.SessionSnapshot{
		ID:             sessionID,
		UserID:         strings.TrimSpace(cmd.Principal.UserID),
		AccountScopeID: strings.TrimSpace(cmd.Principal.AccountScopeID),
		WorkspacePath:  workspacePath,
		WorkspaceName:  workspaceName,
		Title:          title,
		Mode:           mode,
		Preference:     preference,
		Metadata:       metadata,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.CreateSessionWithExecutionV2(session, sessionExecutionV2RecordFromExecution(cmd.Principal, execution)); err != nil {
		return pebblestore.SessionSnapshot{}, SessionExecution{}, nil, "", "", fmt.Errorf("persist session execution v2: %w", err)
	}
	payload, err := json.Marshal(session)
	if err != nil {
		return pebblestore.SessionSnapshot{}, SessionExecution{}, nil, "", "", err
	}
	var env *pebblestore.EventEnvelope
	if s.events != nil {
		appended, err := s.events.Append("session:"+session.ID, "session.created", session.ID, payload, "", "")
		if err != nil {
			return pebblestore.SessionSnapshot{}, SessionExecution{}, nil, "", "", err
		}
		env = &appended
	}
	return session, execution, env, "", modeWarning, nil
}

func sessionExecutionV2RecordFromExecution(principal identity.Principal, execution SessionExecution) pebblestore.SessionExecutionV2Record {
	return pebblestore.SessionExecutionV2Record{
		SessionID:                 execution.SessionID,
		UserID:                    strings.TrimSpace(principal.UserID),
		AccountScopeID:            strings.TrimSpace(principal.AccountScopeID),
		ExecutionClass:            execution.ExecutionClass,
		RuntimeSwarmID:            execution.RuntimeSwarmID,
		RuntimeKind:               execution.RuntimeKind,
		AuthorityHostSwarmID:      execution.AuthorityHostSwarmID,
		AuthorityContainerID:      execution.AuthorityContainerID,
		WorkspaceBindingID:        execution.WorkspaceBindingID,
		SourceWorkspaceID:         execution.SourceWorkspaceID,
		SourceWorkspaceGeneration: execution.SourceWorkspaceGeneration,
		SourceWorkspaceName:       execution.SourceWorkspaceName,
		SourceWorkspacePath:       execution.SourceWorkspacePath,
		RuntimeWorkspacePath:      execution.RuntimeWorkspacePath,
		PlacementGeneration:       execution.PlacementGeneration,
		BindingGeneration:         execution.BindingGeneration,
		CreatedAt:                 execution.CreatedAt,
		UpdatedAt:                 execution.UpdatedAt,
	}
}

func normalizeSessionExecutionV2(execution SessionExecution) SessionExecution {
	execution.SessionID = strings.TrimSpace(execution.SessionID)
	execution.ExecutionClass = strings.TrimSpace(execution.ExecutionClass)
	execution.RuntimeSwarmID = strings.TrimSpace(execution.RuntimeSwarmID)
	execution.RuntimeKind = strings.TrimSpace(execution.RuntimeKind)
	execution.AuthorityHostSwarmID = strings.TrimSpace(execution.AuthorityHostSwarmID)
	execution.AuthorityContainerID = strings.TrimSpace(execution.AuthorityContainerID)
	execution.WorkspaceBindingID = strings.TrimSpace(execution.WorkspaceBindingID)
	execution.SourceWorkspaceID = strings.TrimSpace(execution.SourceWorkspaceID)
	execution.SourceWorkspaceName = strings.TrimSpace(execution.SourceWorkspaceName)
	execution.SourceWorkspacePath = strings.TrimSpace(execution.SourceWorkspacePath)
	execution.RuntimeWorkspacePath = strings.TrimSpace(execution.RuntimeWorkspacePath)
	return execution
}

func validateSessionExecutionV2(execution SessionExecution) error {
	if execution.ExecutionClass != SessionExecutionClassPrimary && execution.ExecutionClass != SessionExecutionClassLocalContainer {
		return fmt.Errorf("unsupported sessions v2 execution class %q", execution.ExecutionClass)
	}
	if execution.RuntimeSwarmID == "" || execution.RuntimeKind == "" || execution.AuthorityHostSwarmID == "" {
		return errors.New("sessions v2 execution runtime and authority identity are required")
	}
	if isPrimaryTUICWDSessionExecution(execution) {
		if execution.SourceWorkspacePath == "" || execution.RuntimeWorkspacePath == "" || execution.SourceWorkspacePath != execution.RuntimeWorkspacePath {
			return errors.New("sessions v2 tui cwd execution workspace identity is required")
		}
		if execution.SourceWorkspaceGeneration != SessionExecutionTUICWDSourceGeneration || execution.PlacementGeneration <= 0 || execution.BindingGeneration != SessionExecutionTUICWDBindingGeneration {
			return errors.New("sessions v2 tui cwd execution generations are required")
		}
		return nil
	}
	if execution.WorkspaceBindingID == "" || execution.SourceWorkspaceID == "" || execution.SourceWorkspacePath == "" || execution.RuntimeWorkspacePath == "" {
		return errors.New("sessions v2 execution workspace identity is required")
	}
	if execution.SourceWorkspaceGeneration <= 0 || execution.PlacementGeneration <= 0 || execution.BindingGeneration <= 0 {
		return errors.New("sessions v2 execution generations are required")
	}
	return nil
}

func isPrimaryTUICWDSessionExecution(execution SessionExecution) bool {
	return execution.ExecutionClass == SessionExecutionClassPrimary &&
		execution.WorkspaceBindingID == "" &&
		strings.HasPrefix(execution.SourceWorkspaceID, SessionExecutionTUICWDSourceIDPrefix)
}

func validateCreateFromExecutionV2Metadata(metadata map[string]any) error {
	for key := range metadata {
		if sessionV2MetadataKeyIsReserved(key) || sessionV2KeyLooksLikeAuthority(key) {
			return fmt.Errorf("sessions v2 metadata must not include routing authority key %q", key)
		}
	}
	return nil
}

func sessionExecutionV2Metadata(base map[string]any, execution SessionExecution) map[string]any {
	metadata := make(map[string]any, len(base)+18)
	for key, value := range base {
		metadata[key] = value
	}
	metadata["swarm_v2_execution_class"] = execution.ExecutionClass
	metadata["swarm_v2_runtime_swarm_id"] = execution.RuntimeSwarmID
	metadata["swarm_v2_runtime_kind"] = execution.RuntimeKind
	metadata["swarm_v2_authority_host_swarm_id"] = execution.AuthorityHostSwarmID
	metadata["swarm_v2_authority_container_id"] = execution.AuthorityContainerID
	metadata["swarm_v2_workspace_binding_id"] = execution.WorkspaceBindingID
	metadata["swarm_v2_source_workspace_id"] = execution.SourceWorkspaceID
	metadata["swarm_v2_source_workspace_generation"] = fmt.Sprintf("%d", execution.SourceWorkspaceGeneration)
	metadata["swarm_v2_source_workspace_name"] = execution.SourceWorkspaceName
	metadata["swarm_v2_placement_generation"] = execution.PlacementGeneration
	metadata["swarm_v2_binding_generation"] = execution.BindingGeneration
	metadata["local_workspace_binding_id"] = execution.WorkspaceBindingID
	return metadata
}

func sessionV2MetadataKeyIsReserved(key string) bool {
	key = strings.TrimSpace(key)
	return strings.HasPrefix(key, "swarm_v2_") || key == "local_workspace_binding_id" || key == "workspace_id" || key == "runtime_state" || key == "title_pending" || key == "agent_name" || key == "agent_mode"
}

func sessionV2KeyLooksLikeAuthority(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return false
	}
	needles := []string{"routing", "backend", "next_hop", "nexthop", "target", "workspace_path", "workspace_name", "hosted_session", "managed_host", "swarm_routed"}
	for _, needle := range needles {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return false
}
