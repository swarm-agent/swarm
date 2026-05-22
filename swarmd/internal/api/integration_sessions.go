package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	integrationBuilderSessionSource = "integration_builder"
	integrationBuilderScope         = "swarm"
	integrationBuilderWorkspaceName = "Integrations"
	integrationBuilderWorkspacePart = "integrations"

	integrationSessionContextKeyWorkspaceID    = "integration_workspace_id"
	integrationSessionContextKeyDisplayName    = "integration_workspace_display_name"
	integrationSessionContextKeyPackID         = "integration_pack_id"
	integrationSessionContextKeyDraftVersionID = "integration_draft_version_id"
)

type integrationSessionCreateRequest struct {
	Title       string                         `json:"title"`
	Mode        string                         `json:"mode"`
	AgentName   string                         `json:"agent_name"`
	WorkspaceID string                         `json:"workspace_id,omitempty"`
	PackID      string                         `json:"pack_id,omitempty"`
	VersionID   string                         `json:"version_id,omitempty"`
	Metadata    map[string]any                 `json:"metadata"`
	Preference  integrationSessionPreferenceIn `json:"preference"`
}

type integrationSessionPreferenceIn struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	ServiceTier string `json:"service_tier,omitempty"`
	ContextMode string `json:"context_mode,omitempty"`
}

type integrationWorkspaceOpenRequest struct {
	WorkspaceID    string                         `json:"workspace_id"`
	DisplayName    string                         `json:"display_name"`
	PackID         string                         `json:"pack_id"`
	DraftVersionID string                         `json:"draft_version_id"`
	Metadata       map[string]any                 `json:"metadata"`
	CreateChild    bool                           `json:"create_child"`
	NewChild       bool                           `json:"new_child"`
	Title          string                         `json:"title"`
	Mode           string                         `json:"mode"`
	Preference     integrationSessionPreferenceIn `json:"preference"`
}

type integrationWorkspaceSessionRequest struct {
	Action     string                         `json:"action"`
	SessionID  string                         `json:"session_id"`
	Title      string                         `json:"title"`
	Mode       string                         `json:"mode"`
	Metadata   map[string]any                 `json:"metadata"`
	Preference integrationSessionPreferenceIn `json:"preference"`
}

func (s *Server) handleIntegrationBuilderSessions(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusUnauthorized, errors.New("authenticated account principal is required"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		limit := 100
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, ok := parsePositiveInt(raw)
			if !ok {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			limit = parsed
		}
		scanLimit := 10000
		if limit > scanLimit {
			scanLimit = limit
		}
		sessions, err := s.sessions.ListSessionsForAccount(principal.AccountScopeID, scanLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		filtered := make([]pebblestore.SessionSnapshot, 0, len(sessions))
		for _, session := range sessions {
			if isIntegrationBuilderSession(session) {
				filtered = append(filtered, session)
				if len(filtered) >= limit {
					break
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions": filtered})
	case http.MethodPost:
		var req integrationSessionCreateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		session, event, err := s.createIntegrationBuilderChildSession(principal, integrationBuilderSessionCreateOptions{
			Title:      req.Title,
			Mode:       req.Mode,
			Preference: req.Preference,
			Metadata:   req.Metadata,
			Context: integrationWorkspaceSessionContext{
				WorkspaceID:    req.WorkspaceID,
				PackID:         req.PackID,
				DraftVersionID: req.VersionID,
			},
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleIntegrationWorkspaces(w http.ResponseWriter, r *http.Request) {
	if s.integrations == nil {
		writeError(w, http.StatusInternalServerError, errors.New("integration service not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusUnauthorized, errors.New("authenticated account principal is required"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit := 100
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, ok := parsePositiveInt(raw)
			if !ok {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			limit = parsed
		}
		workspaces, err := s.integrations.ListWorkspacesForAccount(principal.AccountScopeID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspaces": workspaces, "count": len(workspaces)})
	case http.MethodPost:
		var req integrationWorkspaceOpenRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		workspace, session, children, err := s.openIntegrationWorkspace(principal, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace": workspace, "session": session, "sessions": children})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleIntegrationWorkspaceByID(w http.ResponseWriter, r *http.Request) {
	if s.integrations == nil {
		writeError(w, http.StatusInternalServerError, errors.New("integration service not configured"))
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/integrations/workspaces/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		writeError(w, http.StatusBadRequest, errors.New("workspace_id is required"))
		return
	}
	parts := strings.Split(rest, "/")
	workspaceID := strings.TrimSpace(parts[0])
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, errors.New("workspace_id is required"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusUnauthorized, errors.New("authenticated account principal is required"))
		return
	}
	if len(parts) == 1 {
		s.handleIntegrationWorkspaceOpen(w, r, principal, workspaceID)
		return
	}
	if parts[1] != "sessions" {
		writeError(w, http.StatusNotFound, errors.New("integration workspace route not found"))
		return
	}
	if len(parts) == 2 {
		s.handleIntegrationWorkspaceSessions(w, r, principal, workspaceID)
		return
	}
	if len(parts) == 3 {
		s.handleIntegrationWorkspaceSessionSwitch(w, r, principal, workspaceID, strings.TrimSpace(parts[2]))
		return
	}
	writeError(w, http.StatusNotFound, errors.New("integration workspace route not found"))
}

func (s *Server) handleIntegrationWorkspaceOpen(w http.ResponseWriter, r *http.Request, principal identity.Principal, workspaceID string) {
	switch r.Method {
	case http.MethodGet:
		workspace, session, children, err := s.integrationWorkspaceSnapshot(principal, workspaceID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace": workspace, "session": session, "sessions": children})
	case http.MethodPost:
		var req integrationWorkspaceOpenRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		req.WorkspaceID = firstNonEmpty(strings.TrimSpace(req.WorkspaceID), workspaceID)
		workspace, session, children, err := s.openIntegrationWorkspace(principal, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace": workspace, "session": session, "sessions": children})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleIntegrationWorkspaceSessions(w http.ResponseWriter, r *http.Request, principal identity.Principal, workspaceID string) {
	switch r.Method {
	case http.MethodGet:
		limit := 100
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, ok := parsePositiveInt(raw)
			if !ok {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			limit = parsed
		}
		children, err := s.integrationWorkspaceChildSessions(principal, workspaceID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace_id": workspaceID, "sessions": children, "count": len(children)})
	case http.MethodPost:
		var req integrationWorkspaceSessionRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action == "" {
			action = "new"
		}
		switch action {
		case "new", "create":
			workspace, ok, err := s.integrations.GetWorkspaceForAccount(principal.AccountScopeID, workspaceID)
			if err != nil || !ok {
				writeError(w, http.StatusBadRequest, notFoundOrErr("integration workspace", workspaceID, ok, err))
				return
			}
			session, join, event, err := s.createIntegrationWorkspaceChildSession(principal, workspace, req)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if event != nil && s.hub != nil {
				s.hub.Publish(*event)
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace": workspace, "workspace_session": join, "session": session})
		case "attach":
			join, session, err := s.attachIntegrationWorkspaceSession(principal, workspaceID, req.SessionID, req.Title, req.Metadata)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace_session": join, "session": session})
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported workspace session action %q", req.Action))
		}
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleIntegrationWorkspaceSessionSwitch(w http.ResponseWriter, r *http.Request, principal identity.Principal, workspaceID, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	join, ok, err := s.integrations.GetWorkspaceSessionForAccount(principal.AccountScopeID, workspaceID, sessionID)
	if err != nil || !ok {
		writeError(w, http.StatusBadRequest, notFoundOrErr("integration workspace session", sessionID, ok, err))
		return
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		writeError(w, http.StatusBadRequest, notFoundOrErr("session", sessionID, ok, err))
		return
	}
	if strings.TrimSpace(session.AccountScopeID) != principal.AccountScopeID {
		writeError(w, http.StatusForbidden, errors.New("session does not belong to account"))
		return
	}
	if r.Method == http.MethodDelete {
		if err := s.integrations.DeleteWorkspaceSessionForAccount(principal.AccountScopeID, workspaceID, sessionID); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": sessionID, "workspace_session": join})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace_session": join, "session": session})
}

type integrationBuilderSessionCreateOptions struct {
	Title      string
	Mode       string
	Preference integrationSessionPreferenceIn
	Metadata   map[string]any
	Context    integrationWorkspaceSessionContext
}

type integrationWorkspaceSessionContext struct {
	WorkspaceID    string
	DisplayName    string
	PackID         string
	DraftVersionID string
}

func (s *Server) openIntegrationWorkspace(principal identity.Principal, req integrationWorkspaceOpenRequest) (pebblestore.IntegrationWorkspaceRecord, *pebblestore.SessionSnapshot, []integrationWorkspaceChildSession, error) {
	workspace, err := s.integrations.UpsertWorkspaceContext(pebblestore.IntegrationWorkspaceRecord{
		AccountScopeID: principal.AccountScopeID,
		WorkspaceID:    req.WorkspaceID,
		DisplayName:    req.DisplayName,
		PackID:         req.PackID,
		DraftVersionID: req.DraftVersionID,
		Metadata:       stringMapFromAny(req.Metadata),
	})
	if err != nil {
		return pebblestore.IntegrationWorkspaceRecord{}, nil, nil, err
	}
	children, err := s.integrationWorkspaceChildSessions(principal, workspace.WorkspaceID, 100)
	if err != nil {
		return pebblestore.IntegrationWorkspaceRecord{}, nil, nil, err
	}
	var selected *pebblestore.SessionSnapshot
	if (req.CreateChild || req.NewChild) || len(children) == 0 {
		sessionReq := integrationWorkspaceSessionRequest{Title: req.Title, Mode: req.Mode, Preference: req.Preference}
		session, _, event, err := s.createIntegrationWorkspaceChildSession(principal, workspace, sessionReq)
		if err != nil {
			return pebblestore.IntegrationWorkspaceRecord{}, nil, nil, err
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		selected = &session
		children, err = s.integrationWorkspaceChildSessions(principal, workspace.WorkspaceID, 100)
		if err != nil {
			return pebblestore.IntegrationWorkspaceRecord{}, nil, nil, err
		}
	} else if len(children) > 0 {
		selected = &children[0].Session
	}
	workspace, _, _ = s.integrations.GetWorkspaceForAccount(principal.AccountScopeID, workspace.WorkspaceID)
	return workspace, selected, children, nil
}

func (s *Server) integrationWorkspaceSnapshot(principal identity.Principal, workspaceID string) (pebblestore.IntegrationWorkspaceRecord, *pebblestore.SessionSnapshot, []integrationWorkspaceChildSession, error) {
	workspace, ok, err := s.integrations.GetWorkspaceForAccount(principal.AccountScopeID, workspaceID)
	if err != nil || !ok {
		return pebblestore.IntegrationWorkspaceRecord{}, nil, nil, notFoundOrErr("integration workspace", workspaceID, ok, err)
	}
	children, err := s.integrationWorkspaceChildSessions(principal, workspace.WorkspaceID, 100)
	if err != nil {
		return pebblestore.IntegrationWorkspaceRecord{}, nil, nil, err
	}
	var selected *pebblestore.SessionSnapshot
	if len(children) > 0 {
		selected = &children[0].Session
	}
	return workspace, selected, children, nil
}

type integrationWorkspaceChildSession struct {
	Join    pebblestore.IntegrationWorkspaceSessionRecord `json:"workspace_session"`
	Session pebblestore.SessionSnapshot                   `json:"session"`
}

func (s *Server) integrationWorkspaceChildSessions(principal identity.Principal, workspaceID string, limit int) ([]integrationWorkspaceChildSession, error) {
	if s.sessions == nil {
		return nil, errors.New("session service not configured")
	}
	joins, err := s.integrations.ListWorkspaceSessionsForAccount(principal.AccountScopeID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	children := make([]integrationWorkspaceChildSession, 0, len(joins))
	for _, join := range joins {
		session, ok, err := s.sessions.GetSession(join.SessionID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if strings.TrimSpace(session.AccountScopeID) != principal.AccountScopeID {
			continue
		}
		children = append(children, integrationWorkspaceChildSession{Join: join, Session: session})
	}
	return children, nil
}

func (s *Server) createIntegrationWorkspaceChildSession(principal identity.Principal, workspace pebblestore.IntegrationWorkspaceRecord, req integrationWorkspaceSessionRequest) (pebblestore.SessionSnapshot, pebblestore.IntegrationWorkspaceSessionRecord, *pebblestore.EventEnvelope, error) {
	session, event, err := s.createIntegrationBuilderChildSession(principal, integrationBuilderSessionCreateOptions{
		Title:      firstNonEmpty(strings.TrimSpace(req.Title), workspace.DisplayName),
		Mode:       req.Mode,
		Preference: req.Preference,
		Metadata:   req.Metadata,
		Context: integrationWorkspaceSessionContext{
			WorkspaceID:    workspace.WorkspaceID,
			DisplayName:    workspace.DisplayName,
			PackID:         workspace.PackID,
			DraftVersionID: workspace.DraftVersionID,
		},
	})
	if err != nil {
		return pebblestore.SessionSnapshot{}, pebblestore.IntegrationWorkspaceSessionRecord{}, nil, err
	}
	join, err := s.integrations.AttachWorkspaceSession(pebblestore.IntegrationWorkspaceSessionRecord{
		AccountScopeID: principal.AccountScopeID,
		WorkspaceID:    workspace.WorkspaceID,
		SessionID:      session.ID,
		Title:          session.Title,
		Metadata:       integrationWorkspaceSessionMetadata(workspace),
		UpdatedAt:      time.UnixMilli(session.UpdatedAt).UTC(),
		CreatedAt:      time.UnixMilli(session.CreatedAt).UTC(),
	})
	if err != nil {
		return pebblestore.SessionSnapshot{}, pebblestore.IntegrationWorkspaceSessionRecord{}, nil, err
	}
	return session, join, event, nil
}

func (s *Server) createIntegrationBuilderChildSession(principal identity.Principal, options integrationBuilderSessionCreateOptions) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	if s.sessions == nil {
		return pebblestore.SessionSnapshot{}, nil, errors.New("session service not configured")
	}
	workspacePath, err := integrationBuilderWorkspacePath()
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	mode := sessionruntime.NormalizeMode(options.Mode)
	if strings.TrimSpace(options.Mode) == "" {
		mode = sessionruntime.ModePlan
	}
	metadata := mergeSessionCreateMetadata(integrationBuilderSessionMetadata(), options.Metadata)
	metadata = mergeSessionCreateMetadata(metadata, integrationWorkspaceContextMetadata(options.Context))
	session, event, err := s.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		Title:          firstNonEmpty(strings.TrimSpace(options.Title), "New integration"),
		WorkspacePath:  workspacePath,
		WorkspaceName:  integrationBuilderWorkspaceName,
		Mode:           mode,
		Preference: &pebblestore.ModelPreference{
			Provider:    strings.TrimSpace(options.Preference.Provider),
			Model:       strings.TrimSpace(options.Preference.Model),
			Thinking:    strings.TrimSpace(options.Preference.Thinking),
			ServiceTier: strings.TrimSpace(options.Preference.ServiceTier),
			ContextMode: strings.TrimSpace(options.Preference.ContextMode),
		},
		Metadata: metadata,
	})
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	return session, event, nil
}

func (s *Server) attachIntegrationWorkspaceSession(principal identity.Principal, workspaceID, sessionID, title string, metadata map[string]any) (pebblestore.IntegrationWorkspaceSessionRecord, pebblestore.SessionSnapshot, error) {
	if s.sessions == nil {
		return pebblestore.IntegrationWorkspaceSessionRecord{}, pebblestore.SessionSnapshot{}, errors.New("session service not configured")
	}
	workspace, ok, err := s.integrations.GetWorkspaceForAccount(principal.AccountScopeID, workspaceID)
	if err != nil || !ok {
		return pebblestore.IntegrationWorkspaceSessionRecord{}, pebblestore.SessionSnapshot{}, notFoundOrErr("integration workspace", workspaceID, ok, err)
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return pebblestore.IntegrationWorkspaceSessionRecord{}, pebblestore.SessionSnapshot{}, notFoundOrErr("session", sessionID, ok, err)
	}
	if strings.TrimSpace(session.AccountScopeID) != principal.AccountScopeID {
		return pebblestore.IntegrationWorkspaceSessionRecord{}, pebblestore.SessionSnapshot{}, errors.New("session does not belong to account")
	}
	updatedMetadata := mergeSessionCreateMetadata(session.Metadata, integrationWorkspaceContextMetadata(integrationWorkspaceSessionContext{WorkspaceID: workspace.WorkspaceID, DisplayName: workspace.DisplayName, PackID: workspace.PackID, DraftVersionID: workspace.DraftVersionID}))
	updatedSession, event, err := s.sessions.UpdateMetadata(session.ID, updatedMetadata)
	if err != nil {
		return pebblestore.IntegrationWorkspaceSessionRecord{}, pebblestore.SessionSnapshot{}, err
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	join, err := s.integrations.AttachWorkspaceSession(pebblestore.IntegrationWorkspaceSessionRecord{
		AccountScopeID: principal.AccountScopeID,
		WorkspaceID:    workspace.WorkspaceID,
		SessionID:      updatedSession.ID,
		Title:          firstNonEmpty(strings.TrimSpace(title), updatedSession.Title),
		Metadata:       stringMapFromAny(metadata),
		UpdatedAt:      time.Now().UTC(),
	})
	if err != nil {
		return pebblestore.IntegrationWorkspaceSessionRecord{}, pebblestore.SessionSnapshot{}, err
	}
	return join, updatedSession, nil
}

func integrationBuilderWorkspacePath() (string, error) {
	return appstorage.DataDir("global-sessions", integrationBuilderWorkspacePart)
}

func integrationBuilderSessionMetadata() map[string]any {
	return map[string]any{
		"source":          integrationBuilderSessionSource,
		"session_source":  integrationBuilderSessionSource,
		"scope":           integrationBuilderScope,
		"workspace_scope": integrationBuilderScope,
		"title_pending":   true,
	}
}

func integrationWorkspaceContextMetadata(ctx integrationWorkspaceSessionContext) map[string]any {
	out := map[string]any{}
	if value := strings.TrimSpace(ctx.WorkspaceID); value != "" {
		out[integrationSessionContextKeyWorkspaceID] = value
		out["integration_workspace_id"] = value
	}
	if value := strings.TrimSpace(ctx.DisplayName); value != "" {
		out[integrationSessionContextKeyDisplayName] = value
	}
	if value := strings.TrimSpace(ctx.PackID); value != "" {
		out[integrationSessionContextKeyPackID] = value
	}
	if value := strings.TrimSpace(ctx.DraftVersionID); value != "" {
		out[integrationSessionContextKeyDraftVersionID] = value
	}
	return out
}

func integrationWorkspaceSessionMetadata(workspace pebblestore.IntegrationWorkspaceRecord) map[string]string {
	out := map[string]string{}
	if workspace.PackID != "" {
		out["pack_id"] = workspace.PackID
	}
	if workspace.DraftVersionID != "" {
		out["draft_version_id"] = workspace.DraftVersionID
	}
	if workspace.DisplayName != "" {
		out["workspace_display_name"] = workspace.DisplayName
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isIntegrationBuilderSession(session pebblestore.SessionSnapshot) bool {
	return sessionMetadataEquals(session.Metadata, "source", integrationBuilderSessionSource) ||
		sessionMetadataEquals(session.Metadata, "session_source", integrationBuilderSessionSource)
}

func (s *Server) applyIntegrationBuilderRunContext(principal identity.Principal, sessionID string, req *sessionRunRequestAdapter) (runIntegrationContext, error) {
	if s == nil || s.sessions == nil || req == nil {
		return runIntegrationContext{}, nil
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return runIntegrationContext{}, err
	}
	if principal.Valid() && strings.TrimSpace(session.AccountScopeID) != principal.AccountScopeID {
		return runIntegrationContext{}, errors.New("session does not belong to account")
	}
	if !isIntegrationBuilderSession(session) {
		return runIntegrationContext{}, nil
	}
	if strings.TrimSpace(req.AgentName()) == "" {
		req.SetAgentName(agentruntime.IntegrationBuilderAgentID)
	}
	req.SetInstructions(appendIntegrationWorkspaceInstructions(req.Instructions(), session.Metadata))
	return runIntegrationContext{IntegrationFlow: true}, nil
}

type runIntegrationContext struct {
	IntegrationFlow bool
}

type sessionRunRequestAdapter struct {
	agentName       func() string
	setAgentName    func(string)
	instructions    func() string
	setInstructions func(string)
}

func (a *sessionRunRequestAdapter) AgentName() string            { return a.agentName() }
func (a *sessionRunRequestAdapter) SetAgentName(value string)    { a.setAgentName(value) }
func (a *sessionRunRequestAdapter) Instructions() string         { return a.instructions() }
func (a *sessionRunRequestAdapter) SetInstructions(value string) { a.setInstructions(value) }

func appendIntegrationWorkspaceInstructions(existing string, metadata map[string]any) string {
	workspaceID := metadataString(metadata, integrationSessionContextKeyWorkspaceID)
	packID := metadataString(metadata, integrationSessionContextKeyPackID)
	draftVersionID := metadataString(metadata, integrationSessionContextKeyDraftVersionID)
	displayName := metadataString(metadata, integrationSessionContextKeyDisplayName)
	if workspaceID == "" && packID == "" && draftVersionID == "" {
		return strings.TrimSpace(existing)
	}
	lines := []string{"Integration workspace context:"}
	if workspaceID != "" {
		lines = append(lines, "- workspace_id: "+workspaceID)
	}
	if displayName != "" {
		lines = append(lines, "- workspace_display_name: "+displayName)
	}
	if packID != "" {
		lines = append(lines, "- pack_id: "+packID)
	}
	if draftVersionID != "" {
		lines = append(lines, "- draft_version_id: "+draftVersionID)
	}
	lines = append(lines, "Use this selected integration context automatically when calling manage-integrations or explaining next steps.")
	block := strings.Join(lines, "\n")
	if strings.TrimSpace(existing) == "" {
		return block
	}
	return strings.TrimSpace(existing) + "\n\n" + block
}

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func stringMapFromAny(values map[string]any) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, value := range values {
		key = strings.TrimSpace(key)
		text := strings.TrimSpace(fmt.Sprint(value))
		if key == "" || text == "" {
			continue
		}
		out[key] = text
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sessionMetadataEquals(metadata map[string]any, key, want string) bool {
	value, ok := metadata[key]
	if !ok {
		return false
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(text), strings.TrimSpace(want))
}

func parsePositiveInt(raw string) (int, bool) {
	value := 0
	for _, ch := range strings.TrimSpace(raw) {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		value = value*10 + int(ch-'0')
	}
	return value, value > 0
}

func notFoundOrErr(resource, id string, ok bool, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s %q not found", resource, id)
	}
	return nil
}
