package api

import (
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// CanonicalizeSessionWorkspace binds a model-authored workspace identity to the
// current account catalog and local topology before a session mutation may use it.
func (s *Server) CanonicalizeSessionWorkspace(input runruntime.SessionWorkspaceCanonicalizeInput) (runruntime.SessionWorkspaceCanonicalization, error) {
	if s == nil || s.workspace == nil || s.topology == nil {
		return runruntime.SessionWorkspaceCanonicalization{}, errors.New("session workspace routing is not configured")
	}
	if !input.Principal.Valid() {
		return runruntime.SessionWorkspaceCanonicalization{}, errors.New("session workspace principal is required")
	}
	workspace, ok, err := s.workspace.GetByWorkspaceIDForPrincipal(input.Principal, strings.TrimSpace(input.WorkspaceID))
	if err != nil {
		return runruntime.SessionWorkspaceCanonicalization{}, err
	}
	if !ok {
		return runruntime.SessionWorkspaceCanonicalization{}, fmt.Errorf("workspace %q was not found for this account", strings.TrimSpace(input.WorkspaceID))
	}
	if input.WorkspaceGeneration > 0 && input.WorkspaceGeneration != workspace.WorkspaceGeneration {
		return runruntime.SessionWorkspaceCanonicalization{}, fmt.Errorf("workspace generation is stale: expected %d, current %d", input.WorkspaceGeneration, workspace.WorkspaceGeneration)
	}
	localNode, ok, err := s.swarmLocalNode()
	if err != nil {
		return runruntime.SessionWorkspaceCanonicalization{}, err
	}
	if !ok || strings.TrimSpace(localNode.SwarmID) == "" {
		return runruntime.SessionWorkspaceCanonicalization{}, errors.New("sessions v3 primary local node identity is required")
	}
	bindings, err := s.topology.ListWorkspaceBindingsForAccount(input.Principal.AccountScopeID, 100000)
	if err != nil {
		return runruntime.SessionWorkspaceCanonicalization{}, err
	}
	var selected *pebblestore.TopologyWorkspaceBindingRecord
	for i := range bindings {
		candidate := bindings[i]
		if strings.TrimSpace(candidate.SourceWorkspaceID) != strings.TrimSpace(workspace.WorkspaceID) || candidate.SourceWorkspaceGeneration != workspace.WorkspaceGeneration || strings.TrimSpace(candidate.DestinationRuntimeSwarmID) != strings.TrimSpace(localNode.SwarmID) || strings.TrimSpace(candidate.State) != pebblestore.TopologyWorkspaceBindingStateBound {
			continue
		}
		if selected != nil && strings.TrimSpace(selected.BindingID) != strings.TrimSpace(candidate.BindingID) {
			return runruntime.SessionWorkspaceCanonicalization{}, errors.New("workspace has multiple canonical local bindings")
		}
		copy := candidate
		selected = &copy
	}
	if selected == nil {
		return runruntime.SessionWorkspaceCanonicalization{}, errors.New("workspace is missing its canonical local binding")
	}
	binding, err := s.resolveSessionsV3PrimaryBinding(input.Principal, sessionsV3CreateRequest{
		WorkspacePath: workspace.Path, WorkspaceBindingID: selected.BindingID, SwarmID: localNode.SwarmID,
		TargetKind: "host", TargetRelationship: "self",
	})
	if err != nil {
		return runruntime.SessionWorkspaceCanonicalization{}, err
	}
	workspaceName := firstNonEmptyString(binding.SourceWorkspaceName, workspace.Name)
	if strings.TrimSpace(workspaceName) == "" {
		return runruntime.SessionWorkspaceCanonicalization{}, errors.New("workspace canonical name is required")
	}
	return runruntime.SessionWorkspaceCanonicalization{
		WorkspaceID: binding.SourceWorkspaceID, WorkspaceGeneration: binding.SourceWorkspaceGeneration,
		WorkspaceState: workspace.State, WorkspaceName: workspaceName,
		SourceWorkspacePath: binding.SourceWorkspacePath, RuntimeWorkspacePath: binding.RuntimeWorkspacePath,
		WorkspaceBindingID: binding.WorkspaceBindingID, RuntimeSwarmID: binding.RuntimeSwarmID,
		PlacementGeneration: binding.PlacementGeneration, BindingGeneration: binding.BindingGeneration,
		AuthorityHostSwarmID: binding.RuntimeSwarmID,
	}, nil
}

// CanonicalizeSessionDeploy resolves the same topology and agent metadata used by
// POST /v3/sessions. The manage-sessions deploy tool must not construct this
// server-owned session authority itself.
func (s *Server) CanonicalizeSessionDeploy(input runruntime.SessionDeployCanonicalizeInput) (runruntime.SessionDeployCanonicalization, error) {
	if s == nil || s.topology == nil {
		return runruntime.SessionDeployCanonicalization{}, errors.New("sessions v3 server topology is not configured")
	}
	if !input.Principal.Valid() {
		return runruntime.SessionDeployCanonicalization{}, errors.New("sessions v3 deploy principal is required")
	}
	localNode, ok, err := s.swarmLocalNode()
	if err != nil {
		return runruntime.SessionDeployCanonicalization{}, err
	}
	if !ok || strings.TrimSpace(localNode.SwarmID) == "" {
		return runruntime.SessionDeployCanonicalization{}, errors.New("sessions v3 primary local node identity is required")
	}
	workspaceBindingID := strings.TrimSpace(input.WorkspaceBindingID)
	if workspaceBindingID == "" {
		bindings, listErr := s.topology.ListWorkspaceBindingsBySourcePathForAccount(input.Principal.AccountScopeID, strings.TrimSpace(input.WorkspacePath), 100)
		if listErr != nil {
			return runruntime.SessionDeployCanonicalization{}, listErr
		}
		for _, candidate := range bindings {
			if strings.TrimSpace(candidate.SourceWorkspacePath) == strings.TrimSpace(input.WorkspacePath) && strings.TrimSpace(candidate.DestinationRuntimeSwarmID) == strings.TrimSpace(localNode.SwarmID) && strings.TrimSpace(candidate.State) == pebblestore.TopologyWorkspaceBindingStateBound {
				if workspaceBindingID != "" && workspaceBindingID != strings.TrimSpace(candidate.BindingID) {
					return runruntime.SessionDeployCanonicalization{}, errors.New("workspace has multiple canonical local bindings")
				}
				workspaceBindingID = strings.TrimSpace(candidate.BindingID)
			}
		}
	}
	if workspaceBindingID == "" {
		return runruntime.SessionDeployCanonicalization{}, errors.New("workspace is missing its canonical local binding")
	}
	binding, err := s.resolveSessionsV3PrimaryBinding(input.Principal, sessionsV3CreateRequest{
		WorkspacePath:      strings.TrimSpace(input.WorkspacePath),
		WorkspaceBindingID: workspaceBindingID,
		SwarmID:            strings.TrimSpace(localNode.SwarmID),
		TargetKind:         "host",
		TargetRelationship: "self",
	})
	if err != nil {
		return runruntime.SessionDeployCanonicalization{}, err
	}
	resolvedAgent, err := s.resolveSessionsV3PrimaryCreateAgent(input.Principal, input.AgentProfile.Name)
	if err != nil {
		return runruntime.SessionDeployCanonicalization{}, err
	}
	modelProfile := cloneSessionsV3ModelProfileSnapshotPointer(input.ModelProfile)
	metadata := sessionsV3ModelProfileMetadata(sessionsV3CreateServerMetadata(input.Metadata, resolvedAgent, binding), modelProfile)
	return runruntime.SessionDeployCanonicalization{
		Metadata:                  metadata,
		SourceWorkspaceID:         binding.SourceWorkspaceID,
		SourceWorkspaceGeneration: binding.SourceWorkspaceGeneration,
		SourceWorkspaceName:       binding.SourceWorkspaceName,
		SourceWorkspacePath:       binding.SourceWorkspacePath,
		RuntimeWorkspacePath:      binding.RuntimeWorkspacePath,
	}, nil
}

// EnqueueSessionDeployRun enters the same durable executor used after a normal
// V3 message append. A false result means no run was accepted for execution.
func cloneSessionsV3ModelProfileSnapshotPointer(profile *pebblestore.SessionModelProfileSnapshot) *pebblestore.SessionModelProfileSnapshot {
	if profile == nil {
		return nil
	}
	cloned := cloneSessionsV3ModelProfileSnapshot(*profile)
	return &cloned
}

func (s *Server) EnqueueSessionDeployRun(principal identity.Principal, sessionID, runID, parentSessionID string) bool {
	if s == nil || s.v3SessionExecutor == nil {
		return false
	}
	return s.v3SessionExecutor.EnqueueRun(sessionV3DeployExecutorJob(principal, sessionID, runID, parentSessionID))
}

func sessionV3DeployExecutorJob(principal identity.Principal, sessionID, runID, parentSessionID string) sessionV3ExecutorJob {
	return sessionV3ExecutorJob{
		Principal:       principal,
		SessionID:       strings.TrimSpace(sessionID),
		RunID:           strings.TrimSpace(runID),
		ParentSessionID: strings.TrimSpace(parentSessionID),
	}
}
