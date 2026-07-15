package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

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

func (s *Server) createSessionFromRequestWithSessionID(req sessionCreateRequest, overrideMetadata map[string]any, allowWorktree bool, sessionIDOverride string, principalAndOK ...any) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, string, string, error) {
	principal := identity.Principal{}
	principalOK := false
	if len(principalAndOK) >= 2 {
		if typed, ok := principalAndOK[0].(identity.Principal); ok {
			principal = typed
		}
		if typed, ok := principalAndOK[1].(bool); ok {
			principalOK = typed
		}
	}
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
	createOptions.WorkspacePath = allocation.WorkspacePath
	createOptions.Worktree = &sessionruntime.CreateSessionWorktree{RootPath: allocation.WorkspacePath, BaseBranch: allocation.BaseBranch, BranchName: allocation.BranchName, WorkspaceID: allocation.WorkspaceID}
	return "", nil
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
