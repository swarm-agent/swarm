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

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	modelruntime "swarm/packages/swarmd/internal/model"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

const WorkspaceOnboardingSessionsPath = "/v3/sessions:workspace-onboarding"

type workspaceOnboardingSessionStartRequest struct {
	Path                 string `json:"path"`
	ExpectedResolvedPath string `json:"expected_resolved_path"`
	ClientRequestID      string `json:"client_request_id"`
	Input                string `json:"input,omitempty"`
}

type workspaceOnboardingSessionStartResponse struct {
	OK           bool                                 `json:"ok"`
	SessionID    string                               `json:"session_id"`
	Repository   workspace.RepositoryState            `json:"repository"`
	Session      pebblestore.SessionSnapshot          `json:"session"`
	FirstMessage pebblestore.MessageSnapshot          `json:"first_message"`
	Projection   pebblestore.V3SessionProjection      `json:"projection"`
	Mutation     sessionruntime.SessionMutationResult `json:"mutation"`
	Replayed     bool                                 `json:"replayed"`
}

// handleWorkspaceOnboardingSessionStart is the only pre-admission conversational
// path. It binds one durable V3 session to one canonical unsaved directory in
// needs_assisted_setup state without creating workspace or topology authority.
func (s *Server) handleWorkspaceOnboardingSessionStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s == nil || s.workspace == nil || s.sessions == nil || s.agents == nil || s.agentModelSettings == nil || s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("workspace onboarding assistant is not configured"))
		return
	}
	var req workspaceOnboardingSessionStartRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	req.ExpectedResolvedPath = strings.TrimSpace(req.ExpectedResolvedPath)
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	req.Input = strings.TrimSpace(req.Input)
	if req.ClientRequestID == "" || len(req.ClientRequestID) > 256 {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required and must be 256 characters or fewer"))
		return
	}
	if req.Input == "" {
		req.Input = "Help me review this folder and prepare it as my first Swarm workspace."
	}

	repository, err := s.workspace.InspectOnboardingRepositoryForPrincipal(principal, req.Path, req.ExpectedResolvedPath)
	if err != nil {
		writeWorkspaceOnboardingError(w, repository, err)
		return
	}
	canonicalPath := strings.TrimSpace(repository.Path)
	if canonicalPath == "" {
		writeError(w, http.StatusBadRequest, errors.New("workspace onboarding canonical path is required"))
		return
	}

	sessionID := stableSessionsV3PrimarySessionID(principal, "workspace-onboarding:"+req.ClientRequestID)
	requestHash, err := workspaceOnboardingRequestHash(principal, req, canonicalPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if repository.State != workspace.RepositoryStateNeedsAssistedSetup {
		replay, found, replayErr := s.workspaceOnboardingReplay(principal, sessionID, req.ClientRequestID, requestHash)
		if replayErr != nil && !errors.Is(replayErr, sessionruntime.ErrSessionIdempotencyConflict) {
			writeRoutedSessionError(w, replayErr)
			return
		}
		if replayErr != nil || !found {
			writeWorkspaceOnboardingError(w, repository, errors.New("workspace onboarding assistance can start only for a non-repository folder containing existing files"))
			return
		}
		writeJSON(w, http.StatusOK, workspaceOnboardingSessionStartResponse{OK: true, SessionID: replay.Session.ID, Repository: repository, Session: replay.Session, FirstMessage: *replay.Message, Projection: replay.Projection, Mutation: replay.Mutation, Replayed: true})
		return
	}
	if replay, found, replayErr := s.workspaceOnboardingReplay(principal, sessionID, req.ClientRequestID, requestHash); replayErr != nil {
		writeRoutedSessionError(w, replayErr)
		return
	} else if found {
		writeJSON(w, http.StatusOK, workspaceOnboardingSessionStartResponse{OK: true, SessionID: replay.Session.ID, Repository: repository, Session: replay.Session, FirstMessage: *replay.Message, Projection: replay.Projection, Mutation: replay.Mutation, Replayed: true})
		return
	}

	now := time.Now().UnixMilli()
	modelProfile, err := s.sessionModelProfileSnapshotFromAccountDefault(identity.ContextWithPrincipal(r.Context(), principal), now)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("resolve configured Swarm Action model: %w", err))
		return
	}
	if modelProfile == nil || modelProfile.Source != pebblestore.SessionModelProfileSourceSwarmSettings || !modelProfile.UseAccountDefault || strings.TrimSpace(modelProfile.Action.Provider) == "" || strings.TrimSpace(modelProfile.Action.Model) == "" {
		writeError(w, http.StatusServiceUnavailable, errors.New("workspace onboarding requires a configured Swarm Action provider and model"))
		return
	}
	providerID := modelruntime.NormalizeProviderID(modelProfile.Action.Provider)
	if err := s.requireActiveWorkspaceOnboardingCredential(principal.AccountScopeID, providerID); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	if s.model == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("workspace onboarding model service is not configured"))
		return
	}
	catalog, catalogErr := s.model.GetCatalog(providerID, modelProfile.Action.Model)
	if catalogErr != nil || !catalog.Found {
		if catalogErr != nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("read configured Swarm Action model: %w", catalogErr))
		} else {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("configured Swarm Action model %q for provider %q is unavailable", modelProfile.Action.Model, providerID))
		}
		return
	}
	if s.providers == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("workspace onboarding provider registry is not configured"))
		return
	}
	providerRunner, runnerOK := s.providers.GetRunner(providerID)
	if !runnerOK || providerRunner == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("configured Swarm Action provider %q is not runnable", providerID))
		return
	}

	parentProfile := pebblestore.AgentProfile{Provider: providerID, Model: modelProfile.Action.Model, Thinking: modelProfile.Action.Thinking, AutoServiceTier: modelProfile.Action.ServiceTier, ContextMode: modelProfile.Action.ContextMode}
	registry, err := s.agents.SystemAgentRegistry()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	profile, err := registry.Materialize(agentruntime.WorkspaceOnboardingAgentID, parentProfile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	compiler, ok := s.runner.(sessionsV3StoredAgentToolContractCompiler)
	if !ok || compiler == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("v3 tool contract compiler is not configured"))
		return
	}
	if _, _, err := compiler.CompileStoredV3AgentToolContract(principal.AccountScopeID, profile); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}

	if _, verifyErr := s.workspace.InspectAssistedRepositoryForPrincipal(principal, canonicalPath, req.ExpectedResolvedPath); verifyErr != nil {
		writeWorkspaceOnboardingError(w, repository, verifyErr)
		return
	}

	metadata := map[string]any{
		"agent_name": agentruntime.WorkspaceOnboardingAgentID, "resolved_agent_name": agentruntime.WorkspaceOnboardingAgentID,
		"agent_mode": profile.Mode, "runtime_mode": profile.RuntimeMode, "default_session_mode": pebblestore.AgentProfileDefaultSessionMode(profile),
		"exit_plan_mode_enabled": false, "agent_profile": cloneSessionsV3AgentProfile(profile),
		"workspace_onboarding": true, "owner_transport": "workspace_onboarding_api", "pre_admission": true, "background": true, "launch_mode": "background",
		"workspace_onboarding_path": canonicalPath, "workspace_onboarding_expected_path": canonicalPath,
		"workspace_onboarding_request_hash": requestHash, "routed_start_request_hash": requestHash, "title_locked": true, "title_pending": false,
	}
	candidate := pebblestore.SessionSnapshot{
		ID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		WorkspacePath: canonicalPath, WorkspaceName: filepath.Base(canonicalPath), Title: "Prepare first workspace", Mode: sessionruntime.ModeAuto,
		Preference:   pebblestore.ModelPreference{Provider: providerID, Model: modelProfile.Action.Model, Thinking: modelProfile.Action.Thinking, ServiceTier: modelProfile.Action.ServiceTier, ContextMode: modelProfile.Action.ContextMode, UpdatedAt: now},
		ModelProfile: modelProfile, Metadata: metadata, CreatedAt: now, UpdatedAt: now,
	}
	message := pebblestore.MessageSnapshot{Role: "user", Content: req.Input}
	runID := stableSessionsV3PrimaryRunID(sessionID, "workspace-onboarding:"+req.ClientRequestID)
	runIntent := &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentPendingExecutor}
	createHash, err := workspaceOnboardingCreateHash(candidate, message, runIntent, requestHash)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	mutation, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: req.ClientRequestID, IdempotencyKey: req.ClientRequestID, PayloadHash: createHash, RequestHash: createHash, Kind: sessionruntime.SessionMutationCreateSession, Session: &candidate, Message: &message, RunIntent: runIntent, NowUnixMs: now})
	if err != nil {
		writeRoutedSessionError(w, err)
		return
	}
	created := candidate
	if mutation.Session != nil {
		created = *mutation.Session
	}
	responseMessage := message
	if mutation.Message != nil {
		responseMessage = *mutation.Message
	}
	var enqueue *sessionV3ExecutorJob
	if !mutation.Replayed && mutation.RunIntent != nil && mutation.RunIntent.Status == sessionruntime.RunIntentPendingExecutor && s.v3SessionExecutor != nil {
		enqueue = &sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: mutation.RunIntent.RunID, EpochID: mutation.RunIntent.EpochID}
	}
	writeJSON(w, http.StatusOK, workspaceOnboardingSessionStartResponse{OK: true, SessionID: sessionID, Repository: repository, Session: created, FirstMessage: responseMessage, Projection: mutation.Projection, Mutation: sessionV3MutationResultResponse(mutation), Replayed: mutation.Replayed})
	if enqueue != nil {
		s.v3SessionExecutor.EnqueueRun(*enqueue)
	}
}

func (s *Server) requireActiveWorkspaceOnboardingCredential(accountScopeID, providerID string) error {
	if s == nil || s.auth == nil {
		return errors.New("workspace onboarding auth service is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	providerID = modelruntime.NormalizeProviderID(providerID)
	if accountScopeID == "" || providerID == "" {
		return errors.New("workspace onboarding requires an account-scoped active provider credential")
	}
	credentials, err := s.auth.ListCredentialsForAccount(accountScopeID, providerID, "", 200)
	if err != nil {
		return fmt.Errorf("read workspace onboarding provider credentials: %w", err)
	}
	for _, credential := range credentials.Records {
		if credential.Active {
			return nil
		}
	}
	return fmt.Errorf("workspace onboarding requires an active credential for configured Swarm Action provider %q", providerID)
}

func writeWorkspaceOnboardingError(w http.ResponseWriter, repository workspace.RepositoryState, err error) {
	status := http.StatusConflict
	if errors.Is(err, identity.ErrPrincipalRequired) || errors.Is(err, identity.ErrProductIdentityRequired) {
		status = http.StatusUnauthorized
	}
	writeJSON(w, status, map[string]any{"ok": false, "code": "workspace_onboarding_unavailable", "error": err.Error(), "repository": repository})
}

func workspaceOnboardingRequestHash(principal identity.Principal, req workspaceOnboardingSessionStartRequest, canonicalPath string) (string, error) {
	raw, err := json.Marshal(struct {
		AccountScopeID, UserID, ClientRequestID, Input, Path string
	}{principal.AccountScopeID, principal.UserID, req.ClientRequestID, req.Input, canonicalPath})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func workspaceOnboardingCreateHash(session pebblestore.SessionSnapshot, message pebblestore.MessageSnapshot, intent *pebblestore.V3SessionRunIntent, requestHash string) (string, error) {
	raw, err := json.Marshal(struct {
		Session pebblestore.SessionSnapshot
		Message pebblestore.MessageSnapshot
		Intent  *pebblestore.V3SessionRunIntent
		Request string
	}{session, message, intent, requestHash})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) workspaceOnboardingReplay(principal identity.Principal, sessionID, requestID, requestHash string) (routedSessionStartResult, bool, error) {
	result, found, err := s.routedSessionReplay(principal, sessionID, requestID, requestHash)
	if err != nil || !found {
		return routedSessionStartResult{}, found, err
	}
	if !sessionV3MetadataBool(result.Session.Metadata, "workspace_onboarding") || sessionV3MetadataString(result.Session.Metadata, "workspace_onboarding_path") != result.Session.WorkspacePath {
		return routedSessionStartResult{}, false, errors.New("workspace onboarding replay authority is stale")
	}
	providerID := modelruntime.NormalizeProviderID(result.Session.Preference.Provider)
	if result.Session.ModelProfile != nil {
		providerID = modelruntime.NormalizeProviderID(result.Session.ModelProfile.Action.Provider)
	}
	if err := s.requireActiveWorkspaceOnboardingCredential(principal.AccountScopeID, providerID); err != nil {
		return routedSessionStartResult{}, false, err
	}
	return result, true, nil
}
