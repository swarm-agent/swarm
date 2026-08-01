package api

import (
	"errors"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// sessionsV3SessionIdentity is the canonical, durable identity projected for a
// session. Paths and binding facts come from the stored session snapshot rather
// than from a route request or mutable topology lookup.
type sessionsV3SessionIdentity struct {
	SessionID              string `json:"session_id"`
	Title                  string `json:"title"`
	WorkspaceID            string `json:"workspace_id,omitempty"`
	WorkspaceBindingID     string `json:"workspace_binding_id,omitempty"`
	SourceWorkspaceID      string `json:"source_workspace_id,omitempty"`
	SourceWorkspaceName    string `json:"source_workspace_name"`
	SourceWorkspacePath    string `json:"source_workspace_path"`
	RuntimeWorkspacePath   string `json:"runtime_workspace_path"`
	RuntimeSwarmID         string `json:"runtime_swarm_id,omitempty"`
	AuthorityHostSwarmID   string `json:"authority_host_swarm_id,omitempty"`
	WorktreeEnabled        bool   `json:"worktree_enabled"`
	WorktreeRootPath       string `json:"worktree_root_path,omitempty"`
	WorktreeBaseBranch     string `json:"worktree_base_branch,omitempty"`
	WorktreeBranch         string `json:"worktree_branch,omitempty"`
}

// sessionsV3RoutedStartResponse is the immediate response contract for a
// durable routed start. It deliberately contains the canonical session view
// plus the exact first user message and mutation facts supplied by the
// coordinator. It does not predict a worktree that has not been persisted.
type sessionsV3RoutedStartResponse struct {
	OK              bool                                  `json:"ok"`
	SessionID       string                                `json:"session_id"`
	Title           string                                `json:"title"`
	StartingMode    string                                `json:"starting_mode"`
	Session         pebblestore.SessionSnapshot           `json:"session"`
	SessionView     sessionsV3SessionView                 `json:"session_view"`
	FirstMessage    pebblestore.MessageSnapshot           `json:"first_message"`
	Projection      pebblestore.V3SessionProjection       `json:"projection"`
	CreateMutation  sessionruntime.SessionMutationResult  `json:"create_mutation"`
	MessageMutation sessionruntime.SessionMutationResult  `json:"message_mutation"`
}

func sessionsV3SessionIdentityFromSnapshot(session pebblestore.SessionSnapshot) (sessionsV3SessionIdentity, error) {
	if strings.TrimSpace(session.ID) == "" {
		return sessionsV3SessionIdentity{}, errors.New("session id is required")
	}
	sourcePath := firstNonEmpty(
		sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_path"),
		strings.TrimSpace(session.WorkspacePath),
	)
	runtimePath := firstNonEmpty(
		sessionsV3MetadataString(session.Metadata, "swarm_v3_runtime_workspace_path"),
		strings.TrimSpace(session.WorktreeRootPath),
		strings.TrimSpace(session.WorkspacePath),
	)
	return sessionsV3SessionIdentity{
		SessionID:            strings.TrimSpace(session.ID),
		Title:                strings.TrimSpace(session.Title),
		WorkspaceID:          sessionsV3MetadataString(session.Metadata, "workspace_id"),
		WorkspaceBindingID:   firstNonEmpty(sessionsV3MetadataString(session.Metadata, "swarm_v3_workspace_binding_id"), sessionsV3MetadataString(session.Metadata, "local_workspace_binding_id")),
		SourceWorkspaceID:    sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_id"),
		SourceWorkspaceName:  firstNonEmpty(sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_name"), strings.TrimSpace(session.WorkspaceName)),
		SourceWorkspacePath:  sourcePath,
		RuntimeWorkspacePath: runtimePath,
		RuntimeSwarmID:       sessionsV3MetadataString(session.Metadata, "swarm_v3_runtime_swarm_id"),
		AuthorityHostSwarmID: sessionsV3MetadataString(session.Metadata, "swarm_v3_authority_host_swarm_id"),
		WorktreeEnabled:      session.WorktreeEnabled,
		WorktreeRootPath:     strings.TrimSpace(session.WorktreeRootPath),
		WorktreeBaseBranch:   strings.TrimSpace(session.WorktreeBaseBranch),
		WorktreeBranch:       strings.TrimSpace(session.WorktreeBranch),
	}, nil
}

// buildSessionsV3RoutedStartResponse maps already-durable coordinator results
// into the immediate routed-start response. Callers must pass the post-message
// session snapshot and projection; no storage or topology state is inferred.
func (s *Server) buildSessionsV3RoutedStartResponse(
	view sessionsV3SessionView,
	session pebblestore.SessionSnapshot,
	firstMessage pebblestore.MessageSnapshot,
	projection pebblestore.V3SessionProjection,
	createMutation sessionruntime.SessionMutationResult,
	messageMutation sessionruntime.SessionMutationResult,
) (sessionsV3RoutedStartResponse, error) {
	identity, err := sessionsV3SessionIdentityFromSnapshot(session)
	if err != nil {
		return sessionsV3RoutedStartResponse{}, err
	}
	if identity.Title == "" {
		return sessionsV3RoutedStartResponse{}, errors.New("routed session title is required")
	}
	if identity.SourceWorkspacePath == "" || identity.RuntimeWorkspacePath == "" {
		return sessionsV3RoutedStartResponse{}, errors.New("routed session workspace paths are required")
	}
	if strings.TrimSpace(firstMessage.ID) == "" || strings.TrimSpace(firstMessage.SessionID) != identity.SessionID || strings.TrimSpace(firstMessage.Role) != "user" {
		return sessionsV3RoutedStartResponse{}, errors.New("first durable user message is required")
	}
	if strings.TrimSpace(projection.SessionID) != identity.SessionID || projection.LastEventSeq == 0 {
		return sessionsV3RoutedStartResponse{}, errors.New("routed session projection is required")
	}
	if strings.TrimSpace(createMutation.SessionID) != identity.SessionID || strings.TrimSpace(messageMutation.SessionID) != identity.SessionID {
		return sessionsV3RoutedStartResponse{}, errors.New("routed session mutations do not match session")
	}
	view.Identity = &identity
	return sessionsV3RoutedStartResponse{
		OK:              true,
		SessionID:       identity.SessionID,
		Title:           identity.Title,
		StartingMode:    strings.TrimSpace(session.Mode),
		Session:         session,
		SessionView:     view,
		FirstMessage:    firstMessage,
		Projection:      projection,
		CreateMutation:  sessionV3MutationResultResponse(createMutation),
		MessageMutation: sessionV3MutationResultResponse(messageMutation),
	}, nil
}
