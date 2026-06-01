package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

const sessionsV2LifecyclePrefix = "/v2/sessions/"

type primarySessionV2Authority struct {
	Principal identity.Principal
	Execution pebblestore.SessionExecutionV2Record
	Placement pebblestore.TopologyRuntimePlacementRecord
	Binding   pebblestore.TopologyWorkspaceBindingRecord
}

func (s *Server) handlePrimarySessionV2ByID(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil || s.topology == nil {
		writeSessionsV2Error(w, errors.New("sessions v2 service is not configured"))
		return
	}
	sessionID, subpath, ok := parsePrimarySessionV2LifecyclePath(r.URL.Path)
	if !ok {
		writeSessionsV2Error(w, sessionV2BadRequest("invalid sessions v2 lifecycle path"))
		return
	}

	switch subpath {
	case "":
		s.handlePrimarySessionV2Get(w, r, sessionID)
	case "messages":
		s.handlePrimarySessionV2Messages(w, r, sessionID)
	case "metadata":
		s.handlePrimarySessionV2Metadata(w, r, sessionID)
	case "mode":
		s.handlePrimarySessionV2Mode(w, r, sessionID)
	case "preference":
		s.handlePrimarySessionV2Preference(w, r, sessionID)
	case "codex":
		s.handlePrimarySessionV2Codex(w, r, sessionID)
	case "plans/active":
		s.handlePrimarySessionV2ActivePlan(w, r, sessionID)
	case "plans":
		s.handlePrimarySessionV2Plans(w, r, sessionID)
	case "permissions":
		s.handlePrimarySessionV2Permissions(w, r, sessionID)
	case "permissions/resolve_all":
		s.handlePrimarySessionV2PermissionResolveAll(w, r, sessionID)
	case "usage":
		s.handlePrimarySessionV2Usage(w, r, sessionID)
	case "run":
		s.handlePrimarySessionV2Run(w, r, sessionID)
	case "run/stream":
		s.handlePrimarySessionV2RunStream(w, r, sessionID)
	default:
		if strings.HasPrefix(subpath, "plans/") {
			s.handlePrimarySessionV2PlanByID(w, r, sessionID, strings.TrimPrefix(subpath, "plans/"))
			return
		}
		if strings.HasPrefix(subpath, "permissions/") && strings.HasSuffix(subpath, "/resolve") {
			s.handlePrimarySessionV2PermissionResolve(w, r, sessionID, strings.TrimSuffix(strings.TrimPrefix(subpath, "permissions/"), "/resolve"))
			return
		}
		writeSessionsV2Error(w, sessionV2BadRequest("unknown sessions v2 lifecycle path %q", subpath))
	}
}

func parsePrimarySessionV2LifecyclePath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, sessionsV2LifecyclePrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, sessionsV2LifecyclePrefix)
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

func (s *Server) requirePrimarySessionV2Authority(r *http.Request, sessionID string, requireWrite bool) (primarySessionV2Authority, error) {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil || s.topology == nil {
		return primarySessionV2Authority{}, errors.New("sessions v2 service is not configured")
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		return primarySessionV2Authority{}, identity.ErrPrincipalRequired
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return primarySessionV2Authority{}, sessionV2BadRequest("session id is required")
	}

	execution, executionOK, err := s.sessions.Store().GetSessionExecutionV2(sessionID)
	if err != nil {
		return primarySessionV2Authority{}, err
	}
	if !executionOK {
		return primarySessionV2Authority{}, sessionV2AuthorityNotFound("sessions v2 execution for %q was not found", sessionID)
	}
	if strings.TrimSpace(execution.SessionID) != sessionID {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 execution session id mismatch")
	}
	if strings.TrimSpace(execution.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return primarySessionV2Authority{}, sessionV2AccessDenied("sessions v2 execution account scope does not match principal")
	}
	if strings.TrimSpace(execution.ExecutionClass) != sessionruntime.SessionExecutionClassPrimary {
		return primarySessionV2Authority{}, sessionV2InvalidClass("sessions v2 lifecycle only supports primary execution")
	}

	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return primarySessionV2Authority{}, err
	}
	localSwarmID := strings.TrimSpace(localNode.SwarmID)
	if !localOK || localSwarmID == "" {
		return primarySessionV2Authority{}, sessionV2AuthorityNotFound("sessions v2 primary local node identity is required")
	}
	if strings.TrimSpace(execution.RuntimeSwarmID) != localSwarmID || strings.TrimSpace(execution.AuthorityHostSwarmID) != localSwarmID {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 execution is not owned by this primary runtime")
	}
	if strings.TrimSpace(execution.RuntimeKind) != pebblestore.TopologyRuntimeKindHost || strings.TrimSpace(execution.AuthorityContainerID) != "" {
		return primarySessionV2Authority{}, sessionV2InvalidClass("sessions v2 primary execution must target host runtime authority")
	}
	if execution.PlacementGeneration <= 0 || execution.BindingGeneration <= 0 || strings.TrimSpace(execution.WorkspaceBindingID) == "" {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 execution authority identity is incomplete")
	}

	placement, placementOK, err := s.topology.GetRuntimePlacementForAccount(principal.AccountScopeID, execution.RuntimeSwarmID)
	if err != nil {
		return primarySessionV2Authority{}, err
	}
	if !placementOK {
		return primarySessionV2Authority{}, sessionV2AuthorityNotFound("sessions v2 runtime placement for %q was not found", execution.RuntimeSwarmID)
	}
	if strings.TrimSpace(placement.RuntimeSwarmID) != localSwarmID || strings.TrimSpace(placement.AuthorityHostSwarmID) != localSwarmID {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 primary placement is not local self-authority")
	}
	if strings.TrimSpace(placement.State) != pebblestore.TopologyRuntimePlacementStateActive {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 primary placement is not active")
	}
	if strings.TrimSpace(placement.RuntimeKind) != pebblestore.TopologyRuntimeKindHost || strings.TrimSpace(placement.AuthorityContainerID) != "" {
		return primarySessionV2Authority{}, sessionV2InvalidClass("sessions v2 primary placement must be host self-placement")
	}
	if placement.PlacementGeneration != execution.PlacementGeneration {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 primary placement generation mismatch")
	}

	binding, bindingOK, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, execution.WorkspaceBindingID)
	if err != nil {
		return primarySessionV2Authority{}, err
	}
	if !bindingOK {
		return primarySessionV2Authority{}, sessionV2AuthorityNotFound("sessions v2 workspace binding %q was not found", execution.WorkspaceBindingID)
	}
	if strings.TrimSpace(binding.State) != pebblestore.TopologyWorkspaceBindingStateBound {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 workspace binding is not bound")
	}
	if strings.TrimSpace(binding.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return primarySessionV2Authority{}, sessionV2AccessDenied("sessions v2 workspace binding account scope does not match principal")
	}
	if strings.TrimSpace(binding.BindingID) != strings.TrimSpace(execution.WorkspaceBindingID) {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 workspace binding id mismatch")
	}
	if strings.TrimSpace(binding.DestinationRuntimeSwarmID) != localSwarmID || strings.TrimSpace(binding.DestinationAuthorityHostSwarmID) != localSwarmID {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 workspace binding destination is not local primary self-authority")
	}
	if strings.TrimSpace(binding.DestinationRuntimeKind) != pebblestore.TopologyRuntimeKindHost || strings.TrimSpace(binding.DestinationContainerID) != "" {
		return primarySessionV2Authority{}, sessionV2InvalidClass("sessions v2 workspace binding destination must be host runtime")
	}
	if strings.TrimSpace(binding.AttestedByHostSwarmID) != strings.TrimSpace(placement.AuthorityHostSwarmID) {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 workspace binding attesting host does not match authority host")
	}
	if binding.PlacementGeneration != execution.PlacementGeneration || binding.BindingGeneration != execution.BindingGeneration {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 workspace binding generation mismatch")
	}
	if strings.TrimSpace(binding.SourceWorkspaceID) != strings.TrimSpace(execution.SourceWorkspaceID) || binding.SourceWorkspaceGeneration != execution.SourceWorkspaceGeneration || strings.TrimSpace(binding.SourceWorkspacePath) != strings.TrimSpace(execution.SourceWorkspacePath) || strings.TrimSpace(binding.SourceWorkspaceName) != strings.TrimSpace(execution.SourceWorkspaceName) {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 workspace binding source identity mismatch")
	}
	if strings.TrimSpace(binding.DestinationWorkspacePath) != strings.TrimSpace(execution.RuntimeWorkspacePath) {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 workspace binding destination workspace path mismatch")
	}
	accessMode := strings.TrimSpace(binding.AccessMode)
	if requireWrite {
		if accessMode != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || !binding.Writable {
			return primarySessionV2Authority{}, sessionV2AccessDenied("sessions v2 workspace binding is read-only")
		}
	} else if accessMode != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite && accessMode != pebblestore.TopologyWorkspaceBindingAccessModeReadOnly {
		return primarySessionV2Authority{}, sessionV2StaleAuthority("sessions v2 workspace binding access mode is invalid")
	}

	return primarySessionV2Authority{Principal: principal, Execution: execution, Placement: placement, Binding: binding}, nil
}

func (s *Server) handlePrimarySessionV2Get(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2Authority(r, sessionID, false); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !ok {
		writeSessionNotFound(w)
		return
	}
	s.writeSessionSnapshot(w, session)
}

func (s *Server) handlePrimarySessionV2Messages(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2Authority(r, sessionID, requireWrite); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		afterSeq, limit, ok := parseAfterSeqAndLimit(w, r, 500)
		if !ok {
			return
		}
		messages, err := s.sessions.ListMessages(sessionID, afterSeq, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "messages": messages})
	case http.MethodPost:
		var req struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		message, updatedSession, event, err := s.sessions.AppendMessage(sessionID, req.Role, req.Content, nil)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message, "session": updatedSession})
	}
}

func parseAfterSeqAndLimit(w http.ResponseWriter, r *http.Request, defaultLimit int) (uint64, int, bool) {
	afterSeq := uint64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("after_seq must be an unsigned integer"))
			return 0, 0, false
		}
		afterSeq = parsed
	}
	limit := defaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return 0, 0, false
		}
		limit = parsed
	}
	return afterSeq, limit, true
}

func parseSessionsV2PositiveLimit(w http.ResponseWriter, r *http.Request, defaultLimit int) (int, bool) {
	limit := defaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return 0, false
		}
		limit = parsed
	}
	return limit, true
}

func (s *Server) handlePrimarySessionV2Metadata(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2Authority(r, sessionID, requireWrite); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if r.Method == http.MethodGet {
		session, ok, err := s.sessions.GetSession(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeSessionNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "metadata": session.Metadata, "updated_at": session.UpdatedAt})
		return
	}
	var req struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateSessionsV2Metadata(req.Metadata); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	session, event, err := s.sessions.UpdateMetadata(sessionID, req.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session})
}

func (s *Server) handlePrimarySessionV2Mode(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	authority, err := s.requirePrimarySessionV2Authority(r, sessionID, requireWrite)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if r.Method == http.MethodGet {
		mode, err := s.sessions.GetMode(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "mode": mode})
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	profile, profileErr := s.agents.ResolvePrimaryForAccount(authority.Principal.AccountScopeID, "")
	if profileErr != nil {
		writeError(w, http.StatusBadRequest, profileErr)
		return
	}
	requestedMode := sessionruntime.NormalizeMode(req.Mode)
	modeWarning := ""
	if !pebblestore.AgentExitPlanModeEnabled(profile) {
		setting, ok := pebblestore.AgentExecutionSetting(profile)
		if !ok {
			agentName := strings.TrimSpace(profile.Name)
			if agentName == "" {
				agentName = "active primary agent"
			}
			writeError(w, http.StatusBadRequest, fmt.Errorf("%s has plan mode disabled but no execution_setting is configured", agentName))
			return
		}
		if requestedMode != setting {
			modeWarning = fmt.Sprintf("active primary agent %q has plan mode disabled; ignoring requested session mode %q and using execution setting %q", strings.TrimSpace(profile.Name), requestedMode, setting)
		}
		req.Mode = setting
	}
	session, event, err := s.sessions.SetMode(sessionID, req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "mode": session.Mode, "updated_at": session.UpdatedAt, "warning": modeWarning})
}

func (s *Server) handlePrimarySessionV2Preference(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2Authority(r, sessionID, requireWrite); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if r.Method == http.MethodGet {
		pref, err := s.sessions.GetSessionPreference(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resolved, err := s.model.ResolvePreference(pref)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, resolved)
		return
	}
	var req struct {
		Provider    *string `json:"provider,omitempty"`
		Model       *string `json:"model,omitempty"`
		Thinking    *string `json:"thinking,omitempty"`
		ServiceTier *string `json:"service_tier,omitempty"`
		ContextMode *string `json:"context_mode,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	pref, event, err := s.sessions.SetSessionPreference(sessionID, sessionruntime.SessionPreferenceUpdate{Provider: req.Provider, Model: req.Model, Thinking: req.Thinking, ServiceTier: req.ServiceTier, ContextMode: req.ContextMode})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	resolved, err := s.model.ResolvePreference(pref)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, resolved)
}

func (s *Server) handlePrimarySessionV2Codex(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2Authority(r, sessionID, requireWrite); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if r.Method == http.MethodGet {
		config, err := s.sessions.GetCodexConfig(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, s.codexSessionConfigResponse(sessionID, config))
		return
	}
	var req struct {
		ServiceTier *string `json:"service_tier,omitempty"`
		ContextMode *string `json:"context_mode,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	config, event, err := s.sessions.SetCodexConfig(sessionID, sessionruntime.SessionCodexConfigUpdate{ServiceTier: req.ServiceTier, ContextMode: req.ContextMode})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, s.codexSessionConfigResponse(sessionID, config))
}

func (s *Server) handlePrimarySessionV2ActivePlan(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2Authority(r, sessionID, requireWrite); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if r.Method == http.MethodGet {
		plan, ok, err := s.sessions.GetActivePlan(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "has_active": false, "active_plan": nil})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "has_active": true, "active_plan": plan})
		return
	}
	var req struct {
		PlanID string `json:"plan_id"`
		ID     string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	planID := strings.TrimSpace(req.PlanID)
	if planID == "" {
		planID = strings.TrimSpace(req.ID)
	}
	plan, event, err := s.sessions.SetActivePlan(sessionID, planID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "active_plan": plan})
}

func (s *Server) handlePrimarySessionV2PlanByID(w http.ResponseWriter, r *http.Request, sessionID, tail string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2Authority(r, sessionID, false); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if strings.HasSuffix(tail, "/history") {
		planID := strings.TrimSpace(strings.TrimSuffix(tail, "/history"))
		if planID == "" || strings.Contains(planID, "/") {
			writeError(w, http.StatusBadRequest, errors.New("plan id is required"))
			return
		}
		limit, ok := parseSessionsV2PositiveLimit(w, r, 100)
		if !ok {
			return
		}
		revisions, err := s.sessions.ListPlanRevisions(sessionID, planID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "plan_id": planID, "count": len(revisions), "revisions": revisions})
		return
	}
	planID := strings.TrimSpace(tail)
	if planID == "" || strings.Contains(planID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("plan id is required"))
		return
	}
	plan, ok, err := s.sessions.GetPlan(sessionID, planID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "plan not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "plan": plan})
}

func (s *Server) handlePrimarySessionV2Plans(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2Authority(r, sessionID, requireWrite); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if r.Method == http.MethodGet {
		limit, ok := parseSessionsV2PositiveLimit(w, r, 100)
		if !ok {
			return
		}
		plans, activeID, err := s.sessions.ListPlans(sessionID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "active_plan_id": activeID, "count": len(plans), "plans": plans})
		return
	}
	var req struct {
		ID            string `json:"id"`
		PlanID        string `json:"plan_id"`
		Title         string `json:"title"`
		Plan          string `json:"plan"`
		Status        string `json:"status"`
		ApprovalState string `json:"approval_state"`
		UpdateSummary string `json:"update_summary"`
		UpdateScope   string `json:"update_scope"`
		Scope         string `json:"scope"`
		UpdateKind    string `json:"update_kind"`
		Checkpoint    bool   `json:"checkpoint"`
		Activate      *bool  `json:"activate"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	planID := strings.TrimSpace(req.PlanID)
	if planID == "" {
		planID = strings.TrimSpace(req.ID)
	}
	activate := true
	if req.Activate != nil {
		activate = *req.Activate
	}
	updateScope := strings.TrimSpace(req.UpdateScope)
	if updateScope == "" {
		updateScope = strings.TrimSpace(req.Scope)
	}
	plan, event, err := s.sessions.SavePlanWithMetadata(sessionID, planID, req.Title, req.Plan, req.Status, req.ApprovalState, activate, sessionruntime.PlanSaveMetadata{UpdateSummary: req.UpdateSummary, UpdateScope: updateScope, UpdateKind: req.UpdateKind, Checkpoint: req.Checkpoint})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "plan": plan})
}

func (s *Server) handlePrimarySessionV2Permissions(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	if _, err := s.requirePrimarySessionV2Authority(r, sessionID, false); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	limit, ok := parseSessionsV2PositiveLimit(w, r, 200)
	if !ok {
		return
	}
	var permissions []pebblestore.PermissionRecord
	var err error
	switch status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))); status {
	case "", "all":
		permissions, err = s.perm.ListPermissions(sessionID, limit)
	case pebblestore.PermissionStatusPending:
		permissions, err = s.perm.ListPending(sessionID, limit)
	default:
		writeError(w, http.StatusBadRequest, errors.New("unsupported permission status"))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "count": len(permissions), "permissions": permissions})
}

func (s *Server) handlePrimarySessionV2PermissionResolve(w http.ResponseWriter, r *http.Request, sessionID, permissionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	permissionID = strings.Trim(permissionID, "/")
	if permissionID == "" || strings.Contains(permissionID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("permission id is required"))
		return
	}
	if _, err := s.requirePrimarySessionV2Authority(r, sessionID, true); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	var req struct {
		Action            string          `json:"action"`
		Reason            string          `json:"reason"`
		ApprovedArguments json.RawMessage `json:"approved_arguments,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, savedRule, err := s.perm.ResolveWithPolicyAndArguments(sessionID, permissionID, req.Action, req.Reason, string(req.ApprovedArguments))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "permission": record, "saved_rule": savedRule})
}

func (s *Server) handlePrimarySessionV2PermissionResolveAll(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	if _, err := s.requirePrimarySessionV2Authority(r, sessionID, true); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
		Limit  int    `json:"limit"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolved, err := s.perm.ResolveAll(sessionID, req.Action, req.Reason, req.Limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "count": len(resolved), "resolved": resolved})
}

func (s *Server) handlePrimarySessionV2Usage(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2Authority(r, sessionID, false); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	limit, ok := parseSessionsV2PositiveLimit(w, r, 50)
	if !ok {
		return
	}
	summary, hasSummary, err := s.sessions.GetUsageSummary(sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	turns, err := s.sessions.ListTurnUsage(sessionID, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var summaryPayload any
	if hasSummary {
		summaryPayload = summary
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "has_usage_summary": hasSummary, "usage_summary": summaryPayload, "turn_usage_records": turns})
}

func (s *Server) handlePrimarySessionV2Run(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	authority, err := s.requirePrimarySessionV2Authority(r, sessionID, true)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
		return
	}
	if s.isShuttingDown() {
		writeError(w, http.StatusServiceUnavailable, errors.New("daemon is shutting down"))
		return
	}
	var req runruntime.RunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	integrationCtx, err := s.applyIntegrationBuilderRunContext(authority.Principal, sessionID, &sessionRunRequestAdapter{agentName: func() string { return req.AgentName }, setAgentName: func(value string) { req.AgentName = value }, instructions: func() string { return req.Instructions }, setInstructions: func(value string) { req.Instructions = value }})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.beginActiveRun()
	defer s.endActiveRun()
	result, err := s.runner.RunTurn(identity.ContextWithPrincipal(r.Context(), authority.Principal), sessionID, req, runruntime.RunStartMeta{IntegrationFlow: integrationCtx.IntegrationFlow, Principal: authority.Principal})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, runruntime.ErrSessionAlreadyActive) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	if s.hub != nil {
		for _, event := range result.Events {
			s.hub.Publish(event)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handlePrimarySessionV2RunStream(w http.ResponseWriter, r *http.Request, sessionID string) {
	authority, err := s.requirePrimarySessionV2Authority(r, sessionID, false)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handlePrimarySessionV2RunStreamWebsocket(w, r, sessionID, authority.Principal)
	case http.MethodPost:
		s.handlePrimarySessionV2RunStreamControl(w, r, sessionID, authority.Principal)
	default:
		writeError(w, http.StatusUpgradeRequired, errors.New("run stream requires websocket upgrade (GET) or control POST"))
	}
}

func (s *Server) handlePrimarySessionV2RunStreamWebsocket(w http.ResponseWriter, r *http.Request, sessionID string, principal identity.Principal) {
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
		return
	}
	if s.runStreams == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run stream manager not configured"))
		return
	}
	if s.isShuttingDown() {
		writeError(w, http.StatusServiceUnavailable, errors.New("daemon is shutting down"))
		return
	}
	conn, err := transportws.Accept(w, r)
	if err != nil {
		log.Printf("sessions v2 run stream websocket accept failed session_id=%s remote_addr=%s path=%s err=%v", sessionID, strings.TrimSpace(r.RemoteAddr), r.URL.Path, err)
		if errors.Is(err, transportws.ErrUpgradeRequired) {
			writeError(w, http.StatusUpgradeRequired, errors.New("websocket upgrade required"))
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer conn.Close()
	raw, err := conn.ReadText()
	if err != nil {
		log.Printf("sessions v2 run stream websocket initial read failed session_id=%s remote_addr=%s err=%v", sessionID, strings.TrimSpace(r.RemoteAddr), err)
		return
	}
	inbound, err := decodeRunStreamInbound(raw)
	if err != nil {
		log.Printf("sessions v2 run stream websocket decode failed session_id=%s remote_addr=%s err=%v", sessionID, strings.TrimSpace(r.RemoteAddr), err)
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
		return
	}
	switch inbound.Type {
	case "run.start", "start":
		if _, err := s.requirePrimarySessionV2Authority(r, sessionID, true); err != nil {
			s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
			return
		}
		s.handleRunStreamStart(conn, sessionID, inbound, principal)
	case "run.resume", "resume":
		s.handleRunStreamResume(conn, sessionID, inbound)
	case "run.stop", "stop":
		if _, err := s.requirePrimarySessionV2Authority(r, sessionID, true); err != nil {
			s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
			return
		}
		s.handleRunStreamStop(conn, sessionID, inbound)
	default:
		log.Printf("sessions v2 run stream websocket unsupported message session_id=%s remote_addr=%s type=%q", sessionID, strings.TrimSpace(r.RemoteAddr), inbound.Type)
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: fmt.Sprintf("unsupported run stream message type %q", inbound.Type)})
	}
}

func (s *Server) handlePrimarySessionV2RunStreamControl(w http.ResponseWriter, r *http.Request, sessionID string, principal identity.Principal) {
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
		return
	}
	if s.runStreams == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run stream manager not configured"))
		return
	}
	var inbound runStreamInboundMessage
	if err := decodeJSON(r, &inbound); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	inbound.Type = strings.ToLower(strings.TrimSpace(inbound.Type))
	inbound.RunRequest = inbound.RunRequest.Normalized()
	inbound.RunID = strings.TrimSpace(inbound.RunID)
	switch inbound.Type {
	case "run.start", "start":
		if _, err := s.requirePrimarySessionV2Authority(r, sessionID, true); err != nil {
			writeSessionsV2Error(w, err)
			return
		}
		state, err := s.runStreams.newRun(sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		started := s.startRunStreamExecution(state.runID, sessionID, inbound, principal)
		if startErr := <-started; startErr != nil {
			status := http.StatusBadRequest
			if errors.Is(startErr, runruntime.ErrSessionAlreadyActive) {
				status = http.StatusConflict
			}
			writeError(w, status, startErr)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "session_id": sessionID, "run_id": state.runID, "status": "accepted", "background": inbound.RunRequest.Background, "target_kind": strings.TrimSpace(inbound.RunRequest.TargetKind), "target_name": strings.TrimSpace(inbound.RunRequest.TargetName), "owner_transport": "background_api"})
	case "run.stop", "stop":
		if _, err := s.requirePrimarySessionV2Authority(r, sessionID, true); err != nil {
			writeSessionsV2Error(w, err)
			return
		}
		if inbound.RunID == "" {
			writeError(w, http.StatusBadRequest, errors.New("run_id is required for stop"))
			return
		}
		s.runStreams.setStopReason(inbound.RunID, "run stopped by user")
		if err := s.runner.StopSessionRun(sessionID, inbound.RunID, "run stopped by user"); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "run_id": inbound.RunID, "status": "stop_requested"})
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported run stream message type %q", inbound.Type))
	}
}
