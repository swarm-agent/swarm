package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const RoutedSessionsPath = "/v3/sessions:routed"

type routedSessionMediaRequest struct {
	StagingID string `json:"staging_id"`
	Modality  string `json:"modality,omitempty"`
	FileType  string `json:"file_type,omitempty"`
}

type routedSessionStartRequest struct {
	Input           string                      `json:"input"`
	ClientRequestID string                      `json:"client_request_id,omitempty"`
	IdempotencyKey  string                      `json:"idempotency_key,omitempty"`
	AgentName       string                      `json:"agent_name,omitempty"`
	Metadata        map[string]any              `json:"metadata,omitempty"`
	Media           []routedSessionMediaRequest `json:"media,omitempty"`
	StagingIDs      []string                    `json:"staging_ids,omitempty"`
}

type routedSessionStartResult struct {
	Session    pebblestore.SessionSnapshot
	Projection sessionruntime.SessionProjection
	Message    *pebblestore.MessageSnapshot
	Mutation   sessionruntime.SessionMutationResult
	Replayed   bool
	EnqueueJob *sessionV3ExecutorJob
}

type sessionCreateRequest struct {
	Title                    string         `json:"title"`
	WorkspacePath            string         `json:"workspace_path"`
	HostWorkspacePath        string         `json:"host_workspace_path"`
	RuntimeWorkspacePath     string         `json:"runtime_workspace_path"`
	WorkspaceBindingID       string         `json:"workspace_binding_id,omitempty"`
	WorkspaceName            string         `json:"workspace_name"`
	Mode                     string         `json:"mode"`
	AgentName                string         `json:"agent_name"`
	WorktreeMode             string         `json:"worktree_mode,omitempty"`
	WorktreeUseCurrentBranch *bool          `json:"worktree_use_current_branch,omitempty"`
	WorktreeBaseBranch       string         `json:"worktree_base_branch,omitempty"`
	WorktreeBranchName       string         `json:"worktree_branch_name,omitempty"`
	Metadata                 map[string]any `json:"metadata"`
	Preference               struct {
		Provider    string `json:"provider"`
		Model       string `json:"model"`
		Thinking    string `json:"thinking"`
		ServiceTier string `json:"service_tier,omitempty"`
		ContextMode string `json:"context_mode,omitempty"`
	} `json:"preference"`
}

func (s *Server) decodeSessionCreateRequest(r *http.Request) (sessionCreateRequest, identity.Principal, bool, error) {
	var req sessionCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		return sessionCreateRequest{}, identity.Principal{}, false, err
	}
	req.WorkspacePath = strings.TrimSpace(req.WorkspacePath)
	req.HostWorkspacePath = strings.TrimSpace(req.HostWorkspacePath)
	req.RuntimeWorkspacePath = strings.TrimSpace(req.RuntimeWorkspacePath)
	req.WorkspaceBindingID = strings.TrimSpace(req.WorkspaceBindingID)
	req.WorkspaceName = strings.TrimSpace(req.WorkspaceName)
	principal, principalOK := PrincipalFromRequest(r)
	return req, principal, principalOK, nil
}

func (s *Server) validateAccountSessionWorkspaceBinding(principal identity.Principal, binding pebblestore.TopologyWorkspaceBindingRecord, selectedRuntimeSwarmID, contextLabel string) error {
	contextLabel = strings.TrimSpace(contextLabel)
	if contextLabel == "" {
		contextLabel = "session"
	}
	if !principal.Valid() {
		return identity.ErrPrincipalRequired
	}
	if strings.TrimSpace(selectedRuntimeSwarmID) == "" {
		return errors.New(contextLabel + " selected runtime swarm id is required")
	}
	if strings.TrimSpace(binding.BindingID) == "" {
		return errors.New(contextLabel + " workspace binding is missing binding id")
	}
	if !strings.EqualFold(strings.TrimSpace(binding.AccountScopeID), strings.TrimSpace(principal.AccountScopeID)) {
		return errors.New(contextLabel + " workspace binding account scope does not match principal")
	}
	if strings.TrimSpace(binding.UserID) != "" && !strings.EqualFold(strings.TrimSpace(binding.UserID), strings.TrimSpace(principal.UserID)) {
		return errors.New(contextLabel + " workspace binding user does not match principal")
	}
	if strings.TrimSpace(binding.State) != "" && !strings.EqualFold(strings.TrimSpace(binding.State), pebblestore.TopologyWorkspaceBindingStateBound) {
		return errors.New(contextLabel + " workspace binding is not bound")
	}
	if !strings.EqualFold(strings.TrimSpace(binding.DestinationRuntimeSwarmID), strings.TrimSpace(selectedRuntimeSwarmID)) {
		return errors.New(contextLabel + " workspace binding does not match selected runtime swarm id")
	}
	if strings.TrimSpace(binding.DestinationWorkspacePath) == "" {
		return errors.New(contextLabel + " workspace binding is missing destination workspace path")
	}
	if strings.TrimSpace(binding.AttestedByHostSwarmID) == "" || !strings.EqualFold(strings.TrimSpace(binding.AttestedByHostSwarmID), strings.TrimSpace(binding.DestinationAuthorityHostSwarmID)) {
		return errors.New(contextLabel + " workspace binding attesting host does not match authority host")
	}
	return nil
}

func (s *Server) createSessionFromRequest(req sessionCreateRequest, principal identity.Principal, principalOK bool, overrideMetadata map[string]any, allowWorktree bool) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, string, string, error) {
	return s.createSessionFromRequestWithSessionID(req, overrideMetadata, allowWorktree, "", principal, principalOK)
}

func (s *Server) createSessionFromRequestWithSessionID(req sessionCreateRequest, overrideMetadata map[string]any, allowWorktree bool, sessionIDOverride string, principal identity.Principal, principalOK bool) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, string, string, error) {
	createMetadata := mergeSessionCreateMetadata(req.Metadata, overrideMetadata)
	workspacePath := strings.TrimSpace(req.HostWorkspacePath)
	if workspacePath == "" && strings.TrimSpace(req.WorkspaceBindingID) != "" && principalOK && principal.Valid() && s != nil && s.topology != nil {
		binding, ok, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, req.WorkspaceBindingID)
		if err != nil {
			return pebblestore.SessionSnapshot{}, nil, "", "", err
		}
		if !ok {
			return pebblestore.SessionSnapshot{}, nil, "", "", fmt.Errorf("session workspace binding %q was not found", strings.TrimSpace(req.WorkspaceBindingID))
		}
		workspacePath = strings.TrimSpace(binding.DestinationWorkspacePath)
		if strings.TrimSpace(req.WorkspaceName) == "" {
			req.WorkspaceName = firstNonEmpty(strings.TrimSpace(binding.SourceWorkspaceName), baseNameForPath(workspacePath))
		}
	}
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(req.WorkspacePath)
	}
	createOptions := sessionruntime.CreateSessionOptions{
		Title:         req.Title,
		WorkspacePath: workspacePath,
		WorkspaceName: req.WorkspaceName,
		Mode:          req.Mode,
		Preference: &pebblestore.ModelPreference{
			Provider:    req.Preference.Provider,
			Model:       req.Preference.Model,
			Thinking:    req.Preference.Thinking,
			ServiceTier: req.Preference.ServiceTier,
			ContextMode: req.Preference.ContextMode,
		},
	}
	if principalOK && principal.Valid() {
		createOptions.UserID = principal.UserID
		createOptions.AccountScopeID = principal.AccountScopeID
	}
	requestedWorktreeMode := strings.TrimSpace(req.WorktreeMode)
	requestedWorktreeBaseBranch := strings.TrimSpace(req.WorktreeBaseBranch)
	requestedWorktreeBranchName := strings.TrimSpace(req.WorktreeBranchName)
	requestedWorktreeUseCurrentBranch := req.WorktreeUseCurrentBranch
	if s.agents == nil {
		return pebblestore.SessionSnapshot{}, nil, "", "", errors.New("agent service not configured")
	}
	var profile pebblestore.AgentProfile
	var profileErr error
	if principalOK && principal.Valid() {
		profile, profileErr = s.agents.ResolveAgentForAccount(principal.AccountScopeID, strings.TrimSpace(req.AgentName))
	} else {
		profile, profileErr = s.agents.ResolveAgent(strings.TrimSpace(req.AgentName))
	}
	if profileErr != nil {
		return pebblestore.SessionSnapshot{}, nil, "", "", profileErr
	}
	agentName := strings.TrimSpace(profile.Name)
	if agentName == "" {
		agentName = "swarm"
	}
	modeWarning := ""
	if !pebblestore.AgentExitPlanModeEnabled(profile) {
		setting := pebblestore.AgentProfileRuntimeMode(profile)
		if setting == "" || setting == pebblestore.AgentRuntimeModePlanAuto {
			return pebblestore.SessionSnapshot{}, nil, "", "", errors.New(agentName + " has plan mode disabled but no runtime_mode is configured")
		}
		if sessionruntime.NormalizeMode(req.Mode) != setting {
			modeWarning = "agent " + strconv.Quote(agentName) + " has plan mode disabled; ignoring requested session mode " + strconv.Quote(sessionruntime.NormalizeMode(req.Mode)) + " and using runtime mode " + strconv.Quote(setting)
		}
		createOptions.Mode = setting
	}
	sessionID := strings.TrimSpace(sessionIDOverride)
	if sessionID == "" {
		sessionID = sessionruntime.NewSessionID()
	}
	createOptions.SessionID = sessionID
	createOptions.Metadata = mergeSessionCreateMetadata(map[string]any{
		"workspace_id":  worktreeruntime.WorkspaceIdentityForSession(sessionID),
		"runtime_state": "standby",
		"title_pending": true,
		"agent_name":    agentName,
		"agent_mode":    strings.TrimSpace(profile.Mode),
	}, createMetadata)
	warning := ""
	if allowWorktree {
		nextWarning, worktreeErr := s.applySessionCreateWorktree(&createOptions, sessionID, requestedWorktreeMode, requestedWorktreeUseCurrentBranch, requestedWorktreeBaseBranch, requestedWorktreeBranchName, principal, principalOK)
		if worktreeErr != nil {
			return pebblestore.SessionSnapshot{}, nil, "", "", worktreeErr
		}
		if runruntime.NormalizeRunWorktreeMode(requestedWorktreeMode) == runruntime.RunWorktreeModeOn && createOptions.Worktree == nil {
			return pebblestore.SessionSnapshot{}, nil, "", "", fmt.Errorf("worktree_mode on did not allocate a worktree: session_id=%q create_workspace_path=%q worktree_service_configured=%t", sessionID, strings.TrimSpace(createOptions.WorkspacePath), s != nil && s.worktrees != nil)
		}
		warning = nextWarning
	}
	session, event, err := s.sessions.CreateSessionWithOptions(createOptions)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, "", "", err
	}
	if allowWorktree && s.worktrees != nil {
		session, event, err = sessionruntime.AttachCreatedWorktreeBranch(s.sessions, s.worktrees, session)
		if err != nil {
			return pebblestore.SessionSnapshot{}, nil, "", "", err
		}
		if runruntime.NormalizeRunWorktreeMode(requestedWorktreeMode) == runruntime.RunWorktreeModeOn {
			if missing := missingCanonicalWorktreeSessionFields(session); len(missing) > 0 {
				err := canonicalWorktreeSessionStateError(session, missing, createOptions)
				if cleanupErr := s.sessions.DeleteSession(session.ID); cleanupErr != nil {
					log.Printf("worktree session create rollback failed session_id=%q err=%v", session.ID, cleanupErr)
				}
				return pebblestore.SessionSnapshot{}, nil, "", "", err
			}
		}
	}
	return session, event, warning, modeWarning, nil
}

func (s *Server) applySessionCreateWorktree(createOptions *sessionruntime.CreateSessionOptions, sessionID, rawRequestedMode string, requestedUseCurrentBranch *bool, requestedBaseBranch, requestedBranchName string, principal identity.Principal, principalOK bool) (string, error) {
	if createOptions == nil {
		return "", nil
	}
	requestedMode := runruntime.NormalizeRunWorktreeMode(rawRequestedMode)
	if strings.TrimSpace(rawRequestedMode) != "" && requestedMode == "" {
		return "", errors.New("unsupported worktree_mode " + strconv.Quote(strings.TrimSpace(rawRequestedMode)))
	}
	if s == nil || s.worktrees == nil {
		if requestedMode == runruntime.RunWorktreeModeOn {
			return "", errors.New("worktree service not configured")
		}
		return "", nil
	}
	if !principalOK || !principal.Valid() {
		switch requestedMode {
		case "", runruntime.RunWorktreeModeInherit, runruntime.RunWorktreeModeOff:
			return "", nil
		default:
			return "", identity.ErrPrincipalRequired
		}
	}
	config, cfgErr := s.worktrees.GetConfigForPrincipal(principal, createOptions.WorkspacePath)
	if cfgErr != nil {
		return "", cfgErr
	}
	switch requestedMode {
	case "", runruntime.RunWorktreeModeInherit:
		if !config.Enabled {
			return "", nil
		}
		return s.allocateSessionCreateDetachedWorkspace(createOptions, sessionID, func() (worktreeruntime.Allocation, error) {
			return s.worktrees.AllocateDetachedWorkspaceForPrincipal(principal, createOptions.WorkspacePath, sessionID)
		})
	case runruntime.RunWorktreeModeOff:
		return "", nil
	case runruntime.RunWorktreeModeOn:
		baseBranch := strings.TrimSpace(requestedBaseBranch)
		if requestedUseCurrentBranch != nil && *requestedUseCurrentBranch {
			baseBranch = ""
		}
		if requestedUseCurrentBranch != nil && !*requestedUseCurrentBranch && baseBranch == "" {
			return "", errors.New("worktree_base_branch is required when worktree_use_current_branch is false")
		}
		branchName := strings.TrimSpace(requestedBranchName)
		return s.allocateSessionCreateDetachedWorkspace(createOptions, sessionID, func() (worktreeruntime.Allocation, error) {
			return s.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(principal, createOptions.WorkspacePath, sessionID, baseBranch, branchName)
		})
	default:
		return "", errors.New("unsupported worktree_mode " + strconv.Quote(strings.TrimSpace(rawRequestedMode)))
	}
}

func (s *Server) allocateSessionCreateDetachedWorkspace(createOptions *sessionruntime.CreateSessionOptions, sessionID string, allocate func() (worktreeruntime.Allocation, error)) (string, error) {
	if createOptions == nil || allocate == nil {
		return "", nil
	}
	if strings.TrimSpace(createOptions.WorkspaceName) == "" {
		createOptions.WorkspaceName = filepath.Base(strings.TrimSpace(createOptions.WorkspacePath))
	}
	allocation, allocErr := allocate()
	if allocErr != nil {
		return "", allocErr
	}
	applySessionCreateWorktreeAllocation(createOptions, allocation)
	return "", nil
}

func applySessionCreateWorktreeAllocation(createOptions *sessionruntime.CreateSessionOptions, allocation worktreeruntime.Allocation) {
	if createOptions == nil {
		return
	}
	createOptions.WorkspacePath = allocation.WorkspacePath
	createOptions.Worktree = &sessionruntime.CreateSessionWorktree{RootPath: allocation.WorkspacePath, BaseBranch: allocation.BaseBranch, BranchName: allocation.BranchName, WorkspaceID: allocation.WorkspaceID}
	if baseCommit := strings.TrimSpace(allocation.BaseCommit); baseCommit != "" {
		if createOptions.Metadata == nil {
			createOptions.Metadata = make(map[string]any)
		}
		createOptions.Metadata["base_commit"] = baseCommit
	}
}

func missingCanonicalWorktreeSessionFields(session pebblestore.SessionSnapshot) []string {
	missing := make([]string, 0, 4)
	if !session.WorktreeEnabled {
		missing = append(missing, "worktree_enabled")
	}
	if strings.TrimSpace(session.WorktreeRootPath) == "" {
		missing = append(missing, "worktree_root_path")
	}
	if strings.TrimSpace(session.WorktreeBranch) == "" {
		missing = append(missing, "worktree_branch")
	}
	if strings.TrimSpace(session.WorkspacePath) == "" {
		missing = append(missing, "workspace_path")
	}
	return missing
}

func canonicalWorktreeSessionStateError(session pebblestore.SessionSnapshot, missing []string, createOptions sessionruntime.CreateSessionOptions) error {
	worktreePresent := createOptions.Worktree != nil
	createRoot, createBranch := "", ""
	if createOptions.Worktree != nil {
		createRoot = strings.TrimSpace(createOptions.Worktree.RootPath)
		createBranch = strings.TrimSpace(createOptions.Worktree.BranchName)
	}
	return fmt.Errorf("worktree_mode on did not create canonical worktree session state: missing=%s session_id=%q session_workspace_path=%q session_worktree_enabled=%t session_worktree_root_path=%q session_worktree_branch=%q create_workspace_path=%q create_worktree_present=%t create_worktree_root_path=%q create_worktree_branch=%q", strings.Join(missing, ","), session.ID, strings.TrimSpace(session.WorkspacePath), session.WorktreeEnabled, strings.TrimSpace(session.WorktreeRootPath), strings.TrimSpace(session.WorktreeBranch), strings.TrimSpace(createOptions.WorkspacePath), worktreePresent, createRoot, createBranch)
}

// handleRoutedSessionStart is the canonical Desktop new-session transaction.
// It resolves all non-durable routing authority before entering the V3 mutation
// boundary and never allocates a managed worktree; routed worktree facts remain
// intent until the dedicated allocator realizes them.
func (s *Server) handleRoutedSessionStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req routedSessionStartRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey, r.Header.Get("Idempotency-Key")))
	req.Input = strings.TrimSpace(req.Input)
	req.AgentName = strings.TrimSpace(req.AgentName)
	if req.AgentName == "" {
		req.AgentName = "swarm"
	}
	media, stagingIDs, err := normalizeRoutedSessionMedia(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cleanup := len(stagingIDs) > 0
	defer func() {
		if cleanup && s != nil && s.mediaStaging != nil {
			if _, cleanupErr := s.mediaStaging.CleanupAbandoned(principal.AccountScopeID, stagingIDs, time.Now().UnixMilli()); cleanupErr != nil {
				log.Printf("routed session abandoned media cleanup failed account_scope_id=%q err=%v", principal.AccountScopeID, cleanupErr)
			}
		}
	}()
	if req.Input == "" {
		writeError(w, http.StatusBadRequest, errors.New("routed session input is required"))
		return
	}
	if clientRequestID == "" || len(clientRequestID) > 256 {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required and must be 256 characters or fewer"))
		return
	}
	if err := validateSessionsV3CreateMetadata(req.Metadata); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	requestHash, err := routedSessionRequestHash(req, clientRequestID, media)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sessionID := stableSessionsV3PrimarySessionID(principal, "routed:"+clientRequestID)
	if replay, found, replayErr := s.routedSessionReplay(principal, sessionID, clientRequestID, requestHash); replayErr != nil {
		writeRoutedSessionError(w, replayErr)
		return
	} else if found {
		response, responseErr := s.routedSessionResponse(principal, replay)
		if responseErr != nil {
			writeRoutedSessionError(w, responseErr)
			return
		}
		cleanup = false
		writeJSON(w, http.StatusOK, response)
		return
	}

	decision, err := s.routeSessionOnce(r.Context(), principal, req.Input)
	if err != nil {
		writeRoutedSessionError(w, err)
		return
	}
	now := time.Now().UnixMilli()
	resolvedAgent, err := s.resolveSessionsV3PrimaryCreateAgent(principal, req.AgentName)
	if err != nil {
		writeRoutedSessionError(w, err)
		return
	}
	modelProfile, err := s.sessionModelProfileSnapshotFromAccountDefault(identity.ContextWithPrincipal(r.Context(), principal), now)
	if err != nil {
		writeRoutedSessionError(w, err)
		return
	}
	mode := sessionruntime.NormalizeMode(decision.Result.Mode)
	if modelProfile != nil && mode == sessionruntime.ModePlan && modelProfile.Plan == nil {
		writeRoutedSessionError(w, errors.New("Router selected Plan but the account default has Plan disabled"))
		return
	}
	candidate := pebblestore.SessionSnapshot{ID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: decision.Workspace.WorkspacePath, WorkspaceName: decision.Workspace.WorkspaceName, Title: strings.TrimSpace(decision.Result.Title), Mode: mode, ModelProfile: modelProfile, CreatedAt: now, UpdatedAt: now}
	if preference, preferenceOK := sessionsV3ProfilePreference(candidate); preferenceOK {
		candidate.Preference = normalizeSessionsV3ModelPreference(preference)
	} else {
		writeRoutedSessionError(w, errors.New("routed session mode has no configured model assignment"))
		return
	}
	canonical, err := s.CanonicalizeSessionDeploy(runruntime.SessionDeployCanonicalizeInput{Principal: principal, WorkspacePath: decision.Workspace.WorkspacePath, AgentProfile: resolvedAgent.Profile, ModelProfile: modelProfile, RuntimeMode: mode, Metadata: req.Metadata})
	if err != nil {
		writeRoutedSessionError(w, err)
		return
	}
	candidate.WorkspacePath = canonical.SourceWorkspacePath
	candidate.WorkspaceName = firstNonEmpty(canonical.SourceWorkspaceName, decision.Workspace.WorkspaceName)
	createRequest := sessionCreateRequest{Metadata: cloneSessionsV3Metadata(canonical.Metadata)}
	if err := applyRoutedSessionRouterTitle(&createRequest, decision.Result.Title); err != nil {
		writeRoutedSessionError(w, err)
		return
	}
	candidate.Title = createRequest.Title
	candidate.Metadata = createRequest.Metadata
	candidate.Metadata["routed_start"] = true
	candidate.Metadata["routed_start_request_hash"] = requestHash
	candidate.Metadata["routed_worktree_requested"] = decision.Result.Worktree
	if decision.Result.WorktreeName != nil {
		candidate.Metadata["routed_worktree_name"] = strings.TrimSpace(*decision.Result.WorktreeName)
	}

	var mediaPlan runruntime.PreSessionMediaBindingPlan
	var mediaBytes map[string][]byte
	if len(media) > 0 {
		mediaPlan, mediaBytes, err = s.prepareRoutedSessionMedia(principal, candidate, media)
		if err != nil {
			writeRoutedSessionError(w, err)
			return
		}
	}
	mediaReferences := make([]pebblestore.SessionMediaReference, 0, len(mediaPlan.Bindings))
	stagingBindings := make([]pebblestore.MediaStagingBinding, 0, len(mediaPlan.Bindings))
	materializedAssetIDs := make([]string, 0, len(mediaPlan.Bindings))
	mediaCommitted := false
	defer func() {
		if mediaCommitted || s == nil || s.sessions == nil || s.sessions.Store() == nil {
			return
		}
		for _, assetID := range materializedAssetIDs {
			if _, cleanupErr := s.sessions.Store().DeleteUnreferencedSessionMediaAsset(principal.AccountScopeID, sessionID, assetID); cleanupErr != nil {
				log.Printf("routed session media rollback failed session_id=%q asset_id=%q err=%v", sessionID, assetID, cleanupErr)
			}
		}
	}()
	for _, binding := range mediaPlan.Bindings {
		payload := mediaBytes[binding.StagingID]
		asset, _, assetErr := s.sessions.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{AccountScopeID: principal.AccountScopeID, SessionID: sessionID, Modality: binding.Metadata.Modality, DeclaredMIMEType: binding.Metadata.DetectedMIMEType, FileType: binding.Metadata.FileType, ContractHash: mediaPlan.ContractHash, ProviderID: mediaPlan.ProviderID, Model: mediaPlan.Model, MaxBytes: binding.Metadata.Size, MaxCount: len(mediaPlan.Bindings), Reader: bytes.NewReader(payload), NowUnixMs: now})
		if assetErr != nil {
			writeRoutedSessionError(w, assetErr)
			return
		}
		mediaReferences = append(mediaReferences, binding.Reference)
		materializedAssetIDs = append(materializedAssetIDs, asset.ID)
		stagingBindings = append(stagingBindings, pebblestore.MediaStagingBinding{StagingID: binding.StagingID, AuthorityAssetID: asset.ID, DigestSHA256: asset.DigestSHA256})
	}
	message := pebblestore.MessageSnapshot{Role: "user", Content: req.Input, Media: mediaReferences}
	messageKey := "routed-message:" + clientRequestID
	runStatus, blockedReason := s.sessionsV3PrimaryRunIntentStatus(principal, candidate, sessionsV3MessageRequest{})
	runIntent := &pebblestore.V3SessionRunIntent{RunID: stableSessionsV3PrimaryRunID(sessionID, messageKey), Status: runStatus, BlockedReason: blockedReason}
	createHash, err := routedSessionCreateHash(candidate, message, runIntent, requestHash)
	if err != nil {
		writeRoutedSessionError(w, err)
		return
	}
	mutation, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: createHash, RequestHash: createHash, Kind: sessionruntime.SessionMutationCreateSession, Session: &candidate, Message: &message, RunIntent: runIntent, NowUnixMs: now})
	if err != nil {
		writeRoutedSessionError(w, err)
		return
	}
	if len(stagingBindings) > 0 {
		if _, _, err = s.mediaStaging.Bind(pebblestore.BindMediaStagingInput{AccountScopeID: principal.AccountScopeID, SessionID: sessionID, Bindings: stagingBindings, NowUnixMs: now}); err != nil {
			// The durable create/message mutation already committed. Return its
			// canonical success instead of misreporting a failed routed start;
			// staging reconciliation remains safely replayable by session/asset ID.
			log.Printf("routed session staging bind reconciliation required session_id=%q err=%v", sessionID, err)
		}
	}
	cleanup = false
	mediaCommitted = true
	created := candidate
	if mutation.Session != nil {
		created = *mutation.Session
	}
	var enqueueJob *sessionV3ExecutorJob
	if !mutation.Replayed && mutation.RunIntent != nil && mutation.RunIntent.Status == sessionruntime.RunIntentPendingExecutor && s.v3SessionExecutor != nil {
		enqueueJob = &sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: mutation.RunIntent.RunID, EpochID: mutation.RunIntent.EpochID}
	}
	result := routedSessionStartResult{Session: created, Projection: mutation.Projection, Message: mutation.Message, Mutation: mutation, Replayed: mutation.Replayed, EnqueueJob: enqueueJob}
	response, responseErr := s.routedSessionResponse(principal, result)
	if responseErr != nil {
		// The atomic mutation is already durable. Surface the canonical mapping
		// failure without presenting a partial legacy success contract.
		writeRoutedSessionError(w, responseErr)
		if enqueueJob != nil {
			s.v3SessionExecutor.EnqueueRun(*enqueueJob)
		}
		return
	}
	writeJSON(w, http.StatusOK, response)
	if enqueueJob != nil {
		s.v3SessionExecutor.EnqueueRun(*enqueueJob)
	}
}

func normalizeRoutedSessionMedia(req routedSessionStartRequest) ([]routedSessionMediaRequest, []string, error) {
	if len(req.Media) > 0 && len(req.StagingIDs) > 0 {
		return nil, nil, errors.New("provide media or staging_ids, not both")
	}
	media := append([]routedSessionMediaRequest(nil), req.Media...)
	for _, stagingID := range req.StagingIDs {
		media = append(media, routedSessionMediaRequest{StagingID: stagingID})
	}
	if len(media) > pebblestore.MediaStagingDefaultMaxCount {
		return nil, nil, errors.New("routed session staged media count limit exceeded")
	}
	ids := make([]string, 0, len(media))
	seen := make(map[string]struct{}, len(media))
	for index := range media {
		media[index].StagingID = strings.TrimSpace(media[index].StagingID)
		media[index].Modality = strings.ToLower(strings.TrimSpace(media[index].Modality))
		media[index].FileType = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(media[index].FileType), "."))
		if media[index].StagingID == "" {
			return nil, nil, errors.New("routed session staging_id is required")
		}
		if _, duplicate := seen[media[index].StagingID]; duplicate {
			return nil, nil, fmt.Errorf("duplicate routed session staging_id %q", media[index].StagingID)
		}
		seen[media[index].StagingID] = struct{}{}
		ids = append(ids, media[index].StagingID)
	}
	return media, ids, nil
}

func routedSessionRequestHash(req routedSessionStartRequest, clientRequestID string, media []routedSessionMediaRequest) (string, error) {
	raw, err := json.Marshal(struct {
		Input, ClientRequestID, AgentName string
		Metadata map[string]any
		Media []routedSessionMediaRequest
	}{req.Input, clientRequestID, req.AgentName, cloneSessionsV3Metadata(req.Metadata), media})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func routedSessionCreateHash(session pebblestore.SessionSnapshot, message pebblestore.MessageSnapshot, runIntent *pebblestore.V3SessionRunIntent, requestHash string) (string, error) {
	raw, err := json.Marshal(map[string]any{"operation": "routed_session_start", "request_hash": requestHash, "session": session, "message": message, "run_intent": runIntent})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) routedSessionReplay(principal identity.Principal, sessionID, clientRequestID, requestHash string) (routedSessionStartResult, bool, error) {
	if s == nil || s.sessions == nil {
		return routedSessionStartResult{}, false, errors.New("sessions v3 service is not configured")
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil || !found {
		return routedSessionStartResult{}, false, err
	}
	if session.AccountScopeID != principal.AccountScopeID || sessionsV3MetadataString(session.Metadata, "routed_start_request_hash") != requestHash {
		return routedSessionStartResult{}, false, sessionruntime.ErrSessionIdempotencyConflict
	}
	projection, ok, err := s.sessions.GetSessionProjection(sessionID)
	if err != nil {
		return routedSessionStartResult{}, false, err
	}
	if !ok {
		return routedSessionStartResult{}, false, errors.New("routed session replay projection is missing")
	}
	record, ok, err := s.sessions.Store().GetV3SessionOperationIdempotencyRecord(principal.AccountScopeID, sessionID, sessionruntime.SessionMutationCreateSession, clientRequestID)
	if err != nil {
		return routedSessionStartResult{}, false, err
	}
	if !ok {
		return routedSessionStartResult{}, false, errors.New("routed session replay idempotency record is missing")
	}
	if strings.TrimSpace(record.Result.MessageID) == "" {
		return routedSessionStartResult{}, false, errors.New("routed session replay idempotency record is missing the first message")
	}
	afterSeq := uint64(0)
	if record.Result.FirstSeq > 0 {
		afterSeq = record.Result.FirstSeq - 1
	}
	messages, err := s.sessions.ListSessionMessages(sessionID, afterSeq, 1)
	if err != nil {
		return routedSessionStartResult{}, false, err
	}
	if len(messages) != 1 || messages[0].ID != record.Result.MessageID || !strings.EqualFold(messages[0].Role, "user") {
		return routedSessionStartResult{}, false, errors.New("routed session idempotency record is incomplete")
	}
	primarySeq := record.Result.LastSeq
	if primarySeq == 0 {
		primarySeq = record.Result.FirstSeq
	}
	mutation := sessionruntime.SessionMutationResult{
		SessionID:       sessionID,
		PrimarySeq:      primarySeq,
		FirstSeq:        record.Result.FirstSeq,
		LastSeq:         record.Result.LastSeq,
		EventIDs:        append([]string(nil), record.Result.EventIDs...),
		PayloadHash:     record.Result.PayloadHash,
		ResponseVersion: record.Result.ResponseVersion,
		ResponseStatus:  record.Result.ResponseStatus,
		ResponseBody:    append(json.RawMessage(nil), record.Result.ResponseBody...),
		Conflict:        record.Result.Conflict,
		Error:           record.Result.Error,
		Idempotency:     record,
		Projection:      projection,
		Session:         &session,
		Message:         &messages[0],
		Replayed:        true,
	}
	if primarySeq != 0 {
		if event, found, eventErr := s.sessions.Store().GetV3SessionEvent(sessionID, primarySeq); eventErr != nil {
			return routedSessionStartResult{}, false, eventErr
		} else if found {
			mutation.Event = event
		}
	}
	if strings.TrimSpace(record.Result.RunID) != "" {
		if runIntent, found, runErr := s.sessions.Store().GetV3SessionRunIntent(sessionID, record.Result.RunID); runErr != nil {
			return routedSessionStartResult{}, false, runErr
		} else if found {
			mutation.RunIntent = &runIntent
		}
	}
	message := messages[0]
	return routedSessionStartResult{Session: session, Projection: projection, Message: &message, Mutation: mutation, Replayed: true}, true, nil
}

func (s *Server) routedSessionResponse(principal identity.Principal, result routedSessionStartResult) (sessionsV3RoutedStartResponse, error) {
	if result.Message == nil {
		return sessionsV3RoutedStartResponse{}, errors.New("routed session first durable message is missing")
	}
	view, err := s.buildSessionsV3SessionView(principal, result.Session, result.Projection, nil, false)
	if err != nil {
		return sessionsV3RoutedStartResponse{}, err
	}
	return s.buildSessionsV3RoutedStartResponse(view, result.Session, *result.Message, result.Projection, result.Mutation, result.Replayed)
}

func writeRoutedSessionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrPrincipalRequired):
		writeError(w, http.StatusUnauthorized, err)
	case errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict), errors.Is(err, pebblestore.ErrMediaStagingConflict), errors.Is(err, pebblestore.ErrMediaStagingNotConsumable):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}

func (s *Server) prepareRoutedSessionMedia(principal identity.Principal, session pebblestore.SessionSnapshot, requests []routedSessionMediaRequest) (runruntime.PreSessionMediaBindingPlan, map[string][]byte, error) {
	if s == nil || s.mediaStaging == nil {
		return runruntime.PreSessionMediaBindingPlan{}, nil, errors.New("media staging service is not configured")
	}
	contract, err := s.routedSessionMediaContract(context.Background(), principal, session)
	if err != nil {
		return runruntime.PreSessionMediaBindingPlan{}, nil, err
	}
	staged := make([]runruntime.PreSessionMediaStagedMetadata, 0, len(requests))
	payloads := make(map[string][]byte, len(requests))
	for _, request := range requests {
		record, payload, readErr := s.mediaStaging.Read(principal.AccountScopeID, request.StagingID, time.Now().UnixMilli())
		if readErr != nil {
			return runruntime.PreSessionMediaBindingPlan{}, nil, readErr
		}
		modality := request.Modality
		if modality == "" {
			modality = routedSessionModality(record.DetectedMIMEType)
		}
		fileType := request.FileType
		if fileType == "" {
			if extensions, _ := mime.ExtensionsByType(record.DetectedMIMEType); len(extensions) > 0 {
				fileType = strings.TrimPrefix(extensions[0], ".")
			}
		}
		staged = append(staged, runruntime.PreSessionMediaStagedMetadata{StagingID: record.ID, AccountScopeID: record.AccountScopeID, Modality: modality, DeclaredMIMEType: record.DeclaredMIMEType, DetectedMIMEType: record.DetectedMIMEType, FileType: fileType, Size: record.Size, DigestSHA256: record.DigestSHA256})
		payloads[record.ID] = payload
	}
	plan, err := runruntime.PreparePreSessionMediaBindings(runruntime.PreSessionMediaBindingInput{AccountScopeID: principal.AccountScopeID, SessionID: session.ID, WorkspaceScope: session.WorkspacePath, Contract: contract, Staged: staged})
	return plan, payloads, err
}

func routedSessionModality(mimeType string) string {
	switch strings.Split(strings.ToLower(strings.TrimSpace(mimeType)), "/")[0] {
	case "image", "audio", "video":
		return strings.Split(strings.ToLower(strings.TrimSpace(mimeType)), "/")[0]
	default:
		return "document"
	}
}

// routedSessionMediaContract compiles the effective-model contract against the
// fully canonical candidate without requiring a durable session to exist.
func (s *Server) routedSessionMediaContract(ctx context.Context, principal identity.Principal, session pebblestore.SessionSnapshot) (provideriface.SessionMediaContract, error) {
	if s == nil || s.v3SessionExecutor == nil {
		return provideriface.SessionMediaContract{}, errors.New("v3 session executor is not configured")
	}
	e := s.v3SessionExecutor
	agentProfile, err := sessionV3AgentProfileFromMetadata(session.Metadata)
	if err != nil {
		return provideriface.SessionMediaContract{}, err
	}
	agentProfile, err = e.resolveSessionV3CurrentAgentToolContract(session.AccountScopeID, session.Metadata, agentProfile)
	if err != nil {
		return provideriface.SessionMediaContract{}, err
	}
	effectivePreference, err := resolveSessionV3EffectivePreference(session, agentProfile)
	if err != nil {
		return provideriface.SessionMediaContract{}, err
	}
	preference, _, err := e.resolveSessionV3ProviderPreference(effectivePreference)
	if err != nil {
		return provideriface.SessionMediaContract{}, err
	}
	catalogRecord, catalogMeta, err := e.sessionV3ModelCatalogRecord(preference)
	if err != nil {
		return provideriface.SessionMediaContract{}, err
	}
	scope, err := e.resolveSessionV3WorkspaceScope(session, principal)
	if err != nil {
		return provideriface.SessionMediaContract{}, err
	}
	if s.providers == nil {
		return provideriface.SessionMediaContract{}, errors.New("provider registry is not configured")
	}
	providerID := strings.ToLower(strings.TrimSpace(preference.Provider))
	providerRunner, _ := s.providers.GetRunner(providerID)
	var catalog *pebblestore.ModelCatalogRecord
	if record, ok := catalogRecord.(pebblestore.ModelCatalogRecord); ok {
		catalog = &record
	}
	contract := runruntime.CompileSessionMediaContract(runruntime.SessionMediaContractInput{ProviderID: providerID, Model: preference.Model, Catalog: catalog, CatalogMeta: catalogMeta, Adapter: runruntime.ResolveMediaAdapterDeclaration(identity.ContextWithPrincipal(ctx, principal), providerID, providerRunner), AgentAuthorized: runruntime.AgentProfileAuthorizesMedia(agentProfile), ExecutionMode: session.Mode, WorkspaceScope: scope.PrimaryPath, SessionScope: session.ID})
	if !runruntime.SessionMediaProviderEnabled(contract.ProviderID) {
		return provideriface.SessionMediaContract{}, errors.New("media admission is restricted to reviewed conversational provider surfaces")
	}
	return contract, nil
}
