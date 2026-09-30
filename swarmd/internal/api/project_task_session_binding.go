package api

import (
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Follow-ups retain source authority from the task reservation, not project order
// or the allocated runtime path. Canonicalization crosses the same account,
// placement and binding validation boundary as POST /v3/sessions.
func (s *Server) projectTaskSessionBinding(p identity.Principal, task *pebblestore.ProjectTaskRecord) (runruntime.SessionWorkspaceCanonicalization, error) {
	binding, err := s.CanonicalizeSessionWorkspace(runruntime.SessionWorkspaceCanonicalizeInput{
		Principal: p, WorkspaceID: task.SourceWorkspace.WorkspaceID, WorkspaceGeneration: task.SourceWorkspace.WorkspaceGeneration,
	})
	if err != nil {
		return binding, fmt.Errorf("task session workspace binding: %w", err)
	}
	if binding.SourceWorkspacePath != task.SourceWorkspace.Path {
		return binding, errors.New("task session binding source differs from reserved workspace")
	}
	return binding, nil
}

func projectTaskBindingMetadata(metadata map[string]any, binding runruntime.SessionWorkspaceCanonicalization) map[string]any {
	metadata = cloneSessionsV3Metadata(metadata)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metadata["swarm_v3_execution_class"] = "primary"
	metadata["swarm_v3_runtime_swarm_id"] = binding.RuntimeSwarmID
	metadata["swarm_v3_runtime_kind"] = pebblestore.TopologyRuntimeKindHost
	metadata["swarm_v3_authority_host_swarm_id"] = binding.AuthorityHostSwarmID
	metadata["swarm_v3_workspace_binding_id"] = binding.WorkspaceBindingID
	metadata["local_workspace_binding_id"] = binding.WorkspaceBindingID
	metadata["swarm_v3_source_workspace_name"] = binding.WorkspaceName
	metadata["swarm_v3_placement_generation"] = binding.PlacementGeneration
	metadata["swarm_v3_binding_generation"] = binding.BindingGeneration
	// Do not replace runtime/source paths or Git lineage with topology paths.
	return metadata
}

// Older failed launches may already own a durable isolated session but lack
// routing metadata. Repair only that omission after ownership/Git verification;
// a conflicting retained binding is an error, never authority to adopt a target.
func (s *Server) reconcileProjectTaskSessionBinding(p identity.Principal, task *pebblestore.ProjectTaskRecord, owned *pebblestore.SessionSnapshot) error {
	binding, err := s.projectTaskSessionBinding(p, task)
	if err != nil {
		return err
	}
	for _, key := range []string{"swarm_v3_workspace_binding_id", "local_workspace_binding_id"} {
		if retained := sessionsV3MetadataString(owned.Metadata, key); retained != "" && retained != binding.WorkspaceBindingID {
			return errors.New("task session retained workspace binding does not match canonical source")
		}
	}
	if retained := sessionsV3MetadataString(owned.Metadata, "swarm_v3_runtime_swarm_id"); retained != "" && retained != binding.RuntimeSwarmID {
		return errors.New("task session retained runtime does not match canonical source")
	}
	if sessionsV3MetadataString(owned.Metadata, "swarm_v3_workspace_binding_id") != "" && sessionsV3MetadataString(owned.Metadata, "local_workspace_binding_id") != "" {
		return nil
	}
	owned.Metadata = projectTaskBindingMetadata(owned.Metadata, binding)
	key := fmt.Sprintf("project-task:binding:%s:%s:%s", task.ProjectID, task.ID, owned.ID)
	_, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: owned.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
		Kind: sessionruntime.SessionMutationUpdateMetadata, Session: owned,
	})
	return err
}

func isProjectTaskFollowup(task *pebblestore.ProjectTaskRecord) bool {
	return strings.TrimSpace(task.ActiveAttemptID) != "" && task.ActiveAttemptID != "initial"
}
