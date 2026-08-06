package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
)

const BackgroundRouterSessionsPath = "/v3/sessions:background-router"

type backgroundRouterSessionContextKey struct{}

func isBackgroundRouterSessionRequest(r *http.Request) bool {
	return r != nil && r.Context().Value(backgroundRouterSessionContextKey{}) == true
}

// backgroundRouterSessionStartRequest is the dedicated public contract for a
// background Router session. Managed worktree isolation is intentionally not a
// client option: this endpoint always authorizes and requires it.
type backgroundRouterSessionStartRequest struct {
	Input                string                      `json:"input"`
	ClientRequestID      string                      `json:"client_request_id,omitempty"`
	IdempotencyKey       string                      `json:"idempotency_key,omitempty"`
	AgentName            string                      `json:"agent_name,omitempty"`
	Metadata             map[string]any              `json:"metadata,omitempty"`
	PlanModeRequested    *bool                       `json:"plan_mode_requested"`
	WorkspacePath        string                      `json:"workspace_path"`
	HostWorkspacePath    string                      `json:"host_workspace_path,omitempty"`
	RuntimeWorkspacePath string                      `json:"runtime_workspace_path,omitempty"`
	WorkspaceBindingID   string                      `json:"workspace_binding_id"`
	SwarmID              string                      `json:"swarm_id"`
	TargetKind           string                      `json:"target_kind"`
	TargetRelationship   string                      `json:"target_relationship"`
	Media                []routedSessionMediaRequest `json:"media,omitempty"`
	StagingIDs           []string                    `json:"staging_ids,omitempty"`
}

// handleBackgroundRouterSessionStart preserves the canonical routed-session
// transaction while giving background Router sessions a distinct API contract.
// It has no todo/AI-task fallback and cannot disable managed worktree isolation.
func (s *Server) handleBackgroundRouterSessionStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}

	var req backgroundRouterSessionStartRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	metadata := cloneSessionsV3Metadata(req.Metadata)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metadata["background"] = true
	metadata["launch_mode"] = "background"
	metadata["background_router_session"] = true
	metadata["owner_transport"] = "background_router_api"
	managedWorktreeRequested := true
	canonical := routedSessionStartRequest{
		Input:                    req.Input,
		ClientRequestID:          req.ClientRequestID,
		IdempotencyKey:           req.IdempotencyKey,
		AgentName:                req.AgentName,
		Metadata:                 metadata,
		ManagedWorktreeRequested: &managedWorktreeRequested,
		PlanModeRequested:        req.PlanModeRequested,
		WorkspacePath:            req.WorkspacePath,
		HostWorkspacePath:        req.HostWorkspacePath,
		RuntimeWorkspacePath:     req.RuntimeWorkspacePath,
		WorkspaceBindingID:       req.WorkspaceBindingID,
		SwarmID:                  req.SwarmID,
		TargetKind:               req.TargetKind,
		TargetRelationship:       req.TargetRelationship,
		Media:                    req.Media,
		StagingIDs:               req.StagingIDs,
	}
	body, err := json.Marshal(canonical)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	canonicalRequest := r.Clone(context.WithValue(r.Context(), backgroundRouterSessionContextKey{}, true))
	canonicalRequest.Body = io.NopCloser(bytes.NewReader(body))
	canonicalRequest.ContentLength = int64(len(body))
	s.handleRoutedSessionStart(w, canonicalRequest)
}
