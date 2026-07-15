package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const sessionsV3TUIPrefix = "/v3/tui/sessions/"

type sessionsV3TUICreateRequest struct {
	SessionID                string                      `json:"session_id,omitempty"`
	ClientRequestID          string                      `json:"client_request_id,omitempty"`
	IdempotencyKey           string                      `json:"idempotency_key,omitempty"`
	CWDPath                  string                      `json:"cwd_path"`
	Title                    string                      `json:"title,omitempty"`
	Mode                     string                      `json:"mode,omitempty"`
	AgentName                string                      `json:"agent_name,omitempty"`
	Preference               pebblestore.ModelPreference `json:"preference,omitempty"`
	WorktreeMode             string                      `json:"worktree_mode,omitempty"`
	WorktreeUseCurrentBranch *bool                       `json:"worktree_use_current_branch,omitempty"`
	WorktreeBaseBranch       string                      `json:"worktree_base_branch,omitempty"`
	WorktreeBranchName       string                      `json:"worktree_branch_name,omitempty"`
	Metadata                 map[string]any              `json:"metadata,omitempty"`
}

type sessionsV3TUIRebindRequest struct {
	ClientRequestID    string `json:"client_request_id,omitempty"`
	IdempotencyKey     string `json:"idempotency_key,omitempty"`
	CWDPath            string `json:"cwd_path"`
	WorkspacePath      string `json:"workspace_path"`
	WorkspaceBindingID string `json:"workspace_binding_id"`
	SwarmID            string `json:"swarm_id"`
}

func (s *Server) handleSessionsV3TUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := s.sessionsV3TUIPrincipal(w, r)
	if !ok {
		return
	}
	s.handleSessionsV3TUICreate(w, r, principal)
}

func (s *Server) handleSessionV3TUIByID(w http.ResponseWriter, r *http.Request) {
	principal, principalOK := s.sessionsV3TUIPrincipal(w, r)
	if !principalOK {
		return
	}
	sessionID, subpath, ok := parseSessionsV3TUIPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("invalid sessions v3 tui path"))
		return
	}
	switch subpath {
	case "":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		s.handleSessionV3TUIOpen(w, r, principal, sessionID)
	case "rebind":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleSessionV3TUIRebind(w, r, principal, sessionID)
	default:
		writeError(w, http.StatusBadRequest, errors.New("unknown sessions v3 tui path"))
	}
}

func (s *Server) sessionsV3TUIPrincipal(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return identity.Principal{}, false
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return identity.Principal{}, false
	}
	return principal, true
}

func (s *Server) handleSessionsV3TUICreate(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
	var req sessionsV3TUICreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
		return
	}
	cwdPath, err := canonicalSessionsV3TUIPath("cwd_path", req.CWDPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateSessionsV3CreateMetadata(req.Metadata); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	requestedWorktreeMode, err := validateSessionsV3CreateWorktreeRequest(req.WorktreeMode, req.WorktreeUseCurrentBranch, req.WorktreeBaseBranch, req.WorktreeBranchName, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runtimeSwarmID := strings.TrimSpace(localNode.SwarmID)
	if !localOK || runtimeSwarmID == "" {
		writeError(w, http.StatusBadRequest, errors.New("sessions v3 tui local node identity is required"))
		return
	}
	resolvedAgent, err := s.resolveSessionsV3PrimaryCreateAgent(principal, req.AgentName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = stableSessionsV3PrimarySessionID(principal, clientRequestID)
	}
	workspaceName := filepath.Base(cwdPath)
	if workspaceName == "." || workspaceName == string(filepath.Separator) || strings.TrimSpace(workspaceName) == "" {
		workspaceName = "directory"
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "New Session"
	}
	now := time.Now().UnixMilli()
	metadata := sessionsV3TUICreateServerMetadata(req.Metadata, resolvedAgent, runtimeSwarmID, cwdPath)
	session := pebblestore.SessionSnapshot{
		ID:             sessionID,
		UserID:         strings.TrimSpace(principal.UserID),
		AccountScopeID: strings.TrimSpace(principal.AccountScopeID),
		WorkspacePath:  cwdPath,
		WorkspaceName:  workspaceName,
		Title:          title,
		Mode:           sessionruntime.NormalizeMode(req.Mode),
		Preference:     normalizeSessionsV3ModelPreference(req.Preference),
		Metadata:       metadata,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	payloadHash, err := sessionsV3TUICreatePayloadHash(sessionID, req, cwdPath, title, metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.handleSessionsV3CreateReplay(w, principal, sessionID, clientRequestID, payloadHash, session) {
		return
	}
	if requestedWorktreeMode == runruntime.RunWorktreeModeOn {
		allocation, err := s.allocateSessionsV3CreateWorktree(principal, cwdPath, sessionID, req.WorktreeUseCurrentBranch, req.WorktreeBaseBranch, req.WorktreeBranchName)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		session.WorkspacePath = strings.TrimSpace(allocation.WorkspacePath)
		session.WorktreeEnabled = true
		session.WorktreeRootPath = strings.TrimSpace(allocation.WorkspacePath)
		session.WorktreeBaseBranch = strings.TrimSpace(allocation.BaseBranch)
		session.WorktreeBranch = strings.TrimSpace(allocation.BranchName)
		if session.Metadata == nil {
			session.Metadata = make(map[string]any, 4)
		}
		session.Metadata["workspace_id"] = strings.TrimSpace(allocation.WorkspaceID)
		session.Metadata["swarm_v3_tui_worktree_path"] = strings.TrimSpace(allocation.WorkspacePath)
	} else {
		session.WorktreeBranch = sessionruntime.DetectCurrentBranch(session.WorkspacePath)
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &session,
		NowUnixMs:       now,
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict, "result": result})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	hydrated, found, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeError(w, http.StatusInternalServerError, errors.New("created sessions v3 tui projection was not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session":    hydrated.Session,
		"projection": hydrated.Projection,
		"messages":   hydrated.Messages,
		"events":     hydrated.Events,
		"mutation":   result,
	})
}

func (s *Server) handleSessionV3TUIOpen(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	paths, err := canonicalSessionsV3TUIWorksetPaths(sessionsV3TUIWorksetScope{
		WorkspacePath: r.URL.Query().Get("workspace_path"),
		CWDPath:       r.URL.Query().Get("cwd_path"),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	hydrated, found, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found || !sessionsV3TUISessionVisibleForPaths(hydrated.Session, principal, paths) {
		writeSessionNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, sessionsV3HydratedResponse(hydrated))
}

func (s *Server) handleSessionV3TUIRebind(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	var req sessionsV3TUIRebindRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
		return
	}
	cwdPath, err := canonicalSessionsV3TUIPath("cwd_path", req.CWDPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	workspacePath, err := canonicalSessionsV3TUIPath("workspace_path", req.WorkspacePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if filepath.Clean(cwdPath) != filepath.Clean(workspacePath) {
		writeError(w, http.StatusBadRequest, errors.New("tui directory rebind requires workspace_path to match cwd_path"))
		return
	}
	hydrated, found, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	session := hydrated.Session
	if !sessionsV3TUIDirectorySessionMatchesCWD(session, cwdPath) {
		writeError(w, http.StatusBadRequest, errors.New("session is not a tui directory session for the requested cwd_path"))
		return
	}
	binding, err := s.resolveSessionsV3PrimaryBinding(principal, sessionsV3CreateRequest{
		WorkspacePath:      workspacePath,
		WorkspaceBindingID: req.WorkspaceBindingID,
		SwarmID:            req.SwarmID,
		TargetKind:         "host",
		TargetRelationship: "self",
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if filepath.Clean(binding.SourceWorkspacePath) != filepath.Clean(cwdPath) {
		writeError(w, http.StatusBadRequest, errors.New("workspace binding source must match tui cwd_path"))
		return
	}
	agent, err := sessionsV3ResolvedAgentIdentityFromMetadata(session.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	metadata := sessionsV3CreateServerMetadata(session.Metadata, agent, binding)
	metadata["swarm_v3_tui_directory_session"] = false
	metadata["swarm_v3_tui_original_cwd_path"] = cwdPath
	delete(metadata, "swarm_v3_tui_cwd_path")
	session.WorkspacePath = binding.SourceWorkspacePath
	session.WorkspaceName = binding.SourceWorkspaceName
	if strings.TrimSpace(session.WorkspaceName) == "" {
		session.WorkspaceName = filepath.Base(session.WorkspacePath)
	}
	session.Metadata = metadata
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, "session.tui.rebind", map[string]any{
		"cwd_path":             cwdPath,
		"workspace_path":       binding.SourceWorkspacePath,
		"workspace_binding_id": binding.WorkspaceBindingID,
		"swarm_id":             binding.RuntimeSwarmID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	now := time.Now().UnixMilli()
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdateMetadata,
		EventType:       "session.tui.rebound",
		Session:         &session,
		NowUnixMs:       now,
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict, "result": result})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, found, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session":    updated.Session,
		"projection": updated.Projection,
		"messages":   updated.Messages,
		"events":     updated.Events,
		"mutation":   result,
	})
}

func parseSessionsV3TUIPath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, sessionsV3TUIPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, sessionsV3TUIPrefix)
	if rest == "" || strings.HasPrefix(rest, "/") || strings.HasSuffix(rest, "/") || strings.Contains(rest, "//") {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return "", "", false
		}
	}
	sessionID := strings.TrimSpace(parts[0])
	if sessionID == "" {
		return "", "", false
	}
	if len(parts) == 1 {
		return sessionID, "", true
	}
	return sessionID, strings.Join(parts[1:], "/"), true
}

func canonicalSessionsV3TUIPath(kind, value string) (string, error) {
	paths, err := appendCanonicalSessionsV3WorksetPath(nil, map[string]struct{}{}, kind, value)
	if err != nil {
		return "", err
	}
	if len(paths) != 1 {
		return "", errors.New("tui " + kind + " is required")
	}
	return paths[0], nil
}

func sessionsV3TUICreateServerMetadata(clientMetadata map[string]any, agent sessionsV3ResolvedAgentIdentity, runtimeSwarmID, cwdPath string) map[string]any {
	metadata := cloneSessionsV3Metadata(clientMetadata)
	if metadata == nil {
		metadata = make(map[string]any, 16)
	}
	metadata["agent_name"] = agent.Name
	metadata["resolved_agent_name"] = agent.ResolvedName
	metadata["agent_mode"] = agent.Mode
	metadata["runtime_mode"] = agent.RuntimeMode
	metadata["exit_plan_mode_enabled"] = agent.ExitPlanModeEnabled
	metadata["agent_profile"] = cloneSessionsV3AgentProfile(agent.Profile)
	metadata["swarm_v3_execution_class"] = "primary"
	metadata["swarm_v3_runtime_swarm_id"] = strings.TrimSpace(runtimeSwarmID)
	metadata["swarm_v3_runtime_kind"] = pebblestore.TopologyRuntimeKindHost
	metadata["swarm_v3_authority_host_swarm_id"] = strings.TrimSpace(runtimeSwarmID)
	metadata["swarm_v3_tui_directory_session"] = true
	metadata["swarm_v3_tui_cwd_path"] = strings.TrimSpace(cwdPath)
	if agent.ToolContractPreset != "" {
		metadata["tool_contract_preset"] = agent.ToolContractPreset
	}
	return metadata
}

func sessionsV3ResolvedAgentIdentityFromMetadata(metadata map[string]any) (sessionsV3ResolvedAgentIdentity, error) {
	profile, err := sessionV3AgentProfileFromMetadata(metadata)
	if err != nil {
		return sessionsV3ResolvedAgentIdentity{}, err
	}
	name := strings.TrimSpace(firstNonEmpty(sessionsV3MetadataString(metadata, "agent_name"), profile.Name))
	if name == "" {
		return sessionsV3ResolvedAgentIdentity{}, errors.New("session agent metadata is missing agent_name")
	}
	resolvedName := strings.TrimSpace(firstNonEmpty(sessionsV3MetadataString(metadata, "resolved_agent_name"), name))
	mode := strings.TrimSpace(firstNonEmpty(sessionsV3MetadataString(metadata, "agent_mode"), profile.Mode))
	if mode == "" {
		return sessionsV3ResolvedAgentIdentity{}, errors.New("session agent metadata is missing agent_mode")
	}
	runtimeMode := strings.TrimSpace(firstNonEmpty(sessionsV3MetadataString(metadata, "runtime_mode"), profile.RuntimeMode))
	if runtimeMode == "" {
		return sessionsV3ResolvedAgentIdentity{}, errors.New("session agent metadata is missing runtime_mode")
	}
	exitPlanModeEnabled := false
	if profile.ExitPlanModeEnabled != nil {
		exitPlanModeEnabled = *profile.ExitPlanModeEnabled
	}
	if raw, ok := metadata["exit_plan_mode_enabled"].(bool); ok {
		exitPlanModeEnabled = raw
	}
	return sessionsV3ResolvedAgentIdentity{
		Name:                name,
		ResolvedName:        resolvedName,
		Mode:                mode,
		RuntimeMode:         runtimeMode,
		ExitPlanModeEnabled: exitPlanModeEnabled,
		ToolContractPreset:  sessionsV3MetadataString(metadata, "tool_contract_preset"),
		Profile:             profile,
	}, nil
}

func sessionsV3TUICreatePayloadHash(sessionID string, req sessionsV3TUICreateRequest, cwdPath, title string, metadata map[string]any) (string, error) {
	canonical := struct {
		Operation                string                      `json:"operation"`
		SessionID                string                      `json:"session_id"`
		Title                    string                      `json:"title"`
		CWDPath                  string                      `json:"cwd_path"`
		Mode                     string                      `json:"mode"`
		AgentName                string                      `json:"agent_name,omitempty"`
		Preference               pebblestore.ModelPreference `json:"preference"`
		WorktreeMode             string                      `json:"worktree_mode,omitempty"`
		WorktreeUseCurrentBranch *bool                       `json:"worktree_use_current_branch,omitempty"`
		WorktreeBaseBranch       string                      `json:"worktree_base_branch,omitempty"`
		WorktreeBranchName       string                      `json:"worktree_branch_name,omitempty"`
		Metadata                 map[string]any              `json:"metadata,omitempty"`
	}{
		Operation:                sessionruntime.SessionMutationCreateSession,
		SessionID:                strings.TrimSpace(sessionID),
		Title:                    strings.TrimSpace(title),
		CWDPath:                  strings.TrimSpace(cwdPath),
		Mode:                     sessionruntime.NormalizeMode(req.Mode),
		AgentName:                strings.TrimSpace(req.AgentName),
		Preference:               normalizeSessionsV3ModelPreference(req.Preference),
		WorktreeMode:             runruntime.NormalizeRunWorktreeMode(req.WorktreeMode),
		WorktreeUseCurrentBranch: req.WorktreeUseCurrentBranch,
		WorktreeBaseBranch:       strings.TrimSpace(req.WorktreeBaseBranch),
		WorktreeBranchName:       strings.TrimSpace(req.WorktreeBranchName),
		Metadata:                 cloneSessionsV3Metadata(metadata),
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal sessions v3 tui create payload hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func sessionsV3TUISessionVisibleForPaths(session pebblestore.SessionSnapshot, principal identity.Principal, paths []string) bool {
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return false
	}
	if strings.TrimSpace(session.UserID) == "" || strings.TrimSpace(session.UserID) != strings.TrimSpace(principal.UserID) {
		return false
	}
	candidates := []string{
		strings.TrimSpace(session.WorkspacePath),
		strings.TrimSpace(session.WorktreeRootPath),
		sessionsV3MetadataString(session.Metadata, "swarm_v3_tui_cwd_path"),
		sessionsV3MetadataString(session.Metadata, "swarm_v3_tui_worktree_path"),
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		for _, path := range paths {
			if strings.TrimSpace(path) != "" && filepath.Clean(candidate) == filepath.Clean(path) {
				return true
			}
		}
	}
	return false
}

func sessionsV3TUIDirectorySessionMatchesCWD(session pebblestore.SessionSnapshot, cwdPath string) bool {
	if strings.TrimSpace(session.WorkspacePath) == "" || filepath.Clean(session.WorkspacePath) != filepath.Clean(cwdPath) {
		return false
	}
	if raw, ok := session.Metadata["swarm_v3_tui_directory_session"].(bool); ok && raw {
		metadataCWD := sessionsV3MetadataString(session.Metadata, "swarm_v3_tui_cwd_path")
		return metadataCWD != "" && filepath.Clean(metadataCWD) == filepath.Clean(cwdPath)
	}
	return false
}
