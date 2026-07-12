package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type manageSessionsDeployApproved struct {
	Action              string                         `json:"action"`
	ManifestVersion     int                            `json:"manifest_version"`
	ManifestDigest      string                         `json:"manifest_digest"`
	ParentSessionID     string                         `json:"parent_session_id,omitempty"`
	AccountScopeID      string                         `json:"account_scope_id,omitempty"`
	UserID              string                         `json:"user_id,omitempty"`
	SelectedProposalIDs []string                       `json:"selected_proposal_ids"`
	Proposals           []manageSessionsDeployProposal `json:"proposals"`
}

type manageSessionsDeployResult struct {
	ProposalID string         `json:"proposal_id"`
	SessionID  string         `json:"session_id,omitempty"`
	Title      string         `json:"title,omitempty"`
	Mode       string         `json:"mode"`
	Agent      string         `json:"agent"`
	Workspace  string         `json:"workspace_path"`
	Worktree   bool           `json:"worktree"`
	Status     string         `json:"status"`
	Error      string         `json:"error,omitempty"`
	Navigation map[string]any `json:"navigation,omitempty"`
}

func parseApprovedManageSessionsDeploy(raw string) (manageSessionsDeployApproved, error) {
	var approved manageSessionsDeployApproved
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &approved); err != nil {
		return approved, fmt.Errorf("approved session deployment manifest invalid: %w", err)
	}
	if approved.Action != "deploy" || approved.ManifestVersion != manageSessionsDeployManifestVersion {
		return approved, errors.New("approved session deployment manifest version or action is invalid")
	}
	if len(approved.Proposals) == 0 || len(approved.Proposals) > manageSessionsDeployMaxProposals {
		return approved, errors.New("approved session deployment manifest has invalid proposal count")
	}
	if len(approved.SelectedProposalIDs) == 0 {
		return approved, errors.New("session deployment requires at least one selected proposal")
	}
	return approved, nil
}

func (s *Service) executeManageSessionsDeploy(ctx context.Context, parentSessionID string, call tool.Call, approvedArguments string, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	if s == nil || s.sessions == nil || s.agents == nil || s.workspace == nil {
		return "", errors.New("session deployment services are not configured")
	}
	approved, err := parseApprovedManageSessionsDeploy(approvedArguments)
	if err != nil {
		return "", err
	}
	parent, ok, err := s.sessions.GetSession(parentSessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("parent session not found")
		}
		return "", err
	}
	if approved.ParentSessionID != "" && approved.ParentSessionID != parent.ID || approved.AccountScopeID != "" && approved.AccountScopeID != parent.AccountScopeID || approved.UserID != "" && approved.UserID != parent.UserID {
		return "", errors.New("approved session deployment identity binding does not match the calling session")
	}
	selected := make(map[string]struct{}, len(approved.SelectedProposalIDs))
	for _, id := range approved.SelectedProposalIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return "", errors.New("session deployment selection contains an empty proposal id")
		}
		if _, duplicate := selected[id]; duplicate {
			return "", fmt.Errorf("session deployment selection repeats proposal %q", id)
		}
		selected[id] = struct{}{}
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, SessionID: parent.ID, AccountScopeSource: identity.AccountScopeSourceSession}
	profilesState, err := s.agents.ListStateForAccount(parent.AccountScopeID, 2000)
	if err != nil {
		return "", err
	}
	profiles := map[string]pebblestore.AgentProfile{}
	for _, profile := range profilesState.Profiles {
		profiles[strings.ToLower(strings.TrimSpace(profile.Name))] = profile
	}
	active, activeFound := profiles[strings.ToLower(strings.TrimSpace(profilesState.ActivePrimary))]
	if !activeFound || !active.Enabled || active.Mode != "primary" {
		return "", errors.New("active primary agent is missing, disabled, or invalid")
	}
	caller, callerErr := sessionV3AgentProfileFromMetadataMap(parent.Metadata)
	if callerErr != nil {
		caller = active
	}
	callerContract, _, err := s.CompileStoredV3AgentToolContract(parent.AccountScopeID, caller)
	if err != nil {
		return "", fmt.Errorf("resolve calling agent capability: %w", err)
	}
	canDelegate := callerContract.Tools["task"].Enabled
	for i := range approved.Proposals {
		proposal := approved.Proposals[i]
		profile, found := profiles[strings.ToLower(strings.TrimSpace(proposal.AgentName))]
		if !found || !profile.Enabled || (profile.Mode != "primary" && profile.Mode != "subagent") {
			return "", fmt.Errorf("proposal %q agent binding is no longer valid", proposal.ID)
		}
		if !strings.EqualFold(profile.Name, active.Name) && !canDelegate {
			return "", fmt.Errorf("proposal %q alternate agent requires calling primary task/delegation capability", proposal.ID)
		}
		executionMode, _, resolveErr := s.resolveExecutionMode(proposal.Mode, profile)
		if resolveErr != nil {
			return "", fmt.Errorf("proposal %q execution mode: %w", proposal.ID, resolveErr)
		}
		preference := applyAgentPreferenceOverridesForMode(parent.Preference, profile, proposal.Mode)
		scope, scopeErr := s.workspace.ScopeForPathForPrincipal(principal, proposal.WorkspacePath)
		if scopeErr != nil || !scope.Matched || scope.WorkspaceID != proposal.WorkspaceID || scope.WorkspaceGeneration != proposal.WorkspaceGeneration {
			return "", fmt.Errorf("proposal %q workspace binding is no longer valid", proposal.ID)
		}
		proposal.AgentName, proposal.AgentMode, proposal.RuntimeMode = profile.Name, profile.Mode, executionMode
		proposal.Provider, proposal.Model, proposal.Thinking = preference.Provider, preference.Model, preference.Thinking
		proposal.WorkspaceID, proposal.WorkspaceGeneration = scope.WorkspaceID, scope.WorkspaceGeneration
		proposal.WorkspacePath, proposal.WorkspaceName = scope.WorkspacePath, scope.WorkspaceName
		approved.Proposals[i] = proposal
	}
	manifest := manageSessionsDeployManifest{ManifestVersion: approved.ManifestVersion, Action: approved.Action, ParentSessionID: parent.ID, AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, Proposals: approved.Proposals}
	// Rebind the digest after re-resolving every trust field. The client may edit only
	// user-authorized fields; agent/runtime/model/workspace authority remains server-owned.
	digest, err := manageSessionsDeployDigest(manifest)
	if err != nil {
		return "", fmt.Errorf("bind approved session deployment manifest: %w", err)
	}
	proposals := make([]manageSessionsDeployProposal, 0, len(selected))
	for _, proposal := range approved.Proposals {
		if _, wanted := selected[proposal.ID]; wanted {
			proposals = append(proposals, proposal)
			delete(selected, proposal.ID)
		}
	}
	if len(selected) != 0 {
		return "", errors.New("session deployment selection references an unknown proposal")
	}
	type prepared struct {
		proposal manageSessionsDeployProposal
		profile  pebblestore.AgentProfile
		session  pebblestore.SessionSnapshot
		runID    string
	}
	ready := make([]prepared, 0, len(proposals))
	for _, proposal := range proposals {
		profile, found := profiles[strings.ToLower(strings.TrimSpace(proposal.AgentName))]
		if !found || !profile.Enabled || profile.Mode != proposal.AgentMode {
			return "", fmt.Errorf("proposal %q agent binding is no longer valid", proposal.ID)
		}
		scope, scopeErr := s.workspace.ScopeForPathForPrincipal(principal, proposal.WorkspacePath)
		if scopeErr != nil || !scope.Matched || scope.WorkspaceID != proposal.WorkspaceID || scope.WorkspaceGeneration != proposal.WorkspaceGeneration {
			return "", fmt.Errorf("proposal %q workspace binding is no longer valid", proposal.ID)
		}
		sessionID := deterministicDeployID(digest, proposal.ID, "session")
		runID := "session-deploy-run:" + deterministicDeployID(digest, proposal.ID, "run")
		workspacePath, workspaceName := scope.WorkspacePath, scope.WorkspaceName
		var allocation worktreeruntime.Allocation
		existing, exists, getErr := s.sessions.GetSession(sessionID)
		if getErr != nil {
			return "", getErr
		}
		if exists {
			if mapString(existing.Metadata, "deployment_manifest_digest") != digest || mapString(existing.Metadata, "deployment_proposal_id") != proposal.ID {
				return "", fmt.Errorf("proposal %q deterministic session id is already bound to another deployment", proposal.ID)
			}
			workspacePath, workspaceName = existing.WorkspacePath, existing.WorkspaceName
			allocation = worktreeruntime.Allocation{WorkspacePath: existing.WorktreeRootPath, BaseBranch: existing.WorktreeBaseBranch, BranchName: existing.WorktreeBranch}
		} else if proposal.ManagedWorktree {
			if s.worktrees == nil {
				return "", fmt.Errorf("proposal %q requires the managed worktree service", proposal.ID)
			}
			baseBranch := strings.TrimSpace(proposal.WorktreeBaseBranch)
			if baseBranch == "" {
				baseBranch = parent.WorktreeBranch
			}
			allocation, err = s.worktrees.AllocateTaskWorkspace(workspacePath, baseBranch, sessionID)
			if err != nil {
				return "", fmt.Errorf("proposal %q allocate managed worktree: %w", proposal.ID, err)
			}
			workspacePath, workspaceName = allocation.WorkspacePath, filepath.Base(allocation.WorkspacePath)
		}
		now := time.Now().UnixMilli()
		title := strings.TrimSpace(proposal.Title)
		if title == "" {
			title = "New session"
		}
		metadata := map[string]any{"agent_profile": profile, "agent_name": profile.Name, "runtime_mode": proposal.RuntimeMode, "parent_session_id": parent.ID, "lineage_kind": "session_deploy", "deployment_manifest_digest": digest, "deployment_proposal_id": proposal.ID, "workspace_id": proposal.WorkspaceID}
		snapshot := pebblestore.SessionSnapshot{ID: sessionID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, WorkspacePath: workspacePath, WorkspaceName: workspaceName, Title: title, Mode: proposal.Mode, Preference: pebblestore.ModelPreference{Provider: proposal.Provider, Model: proposal.Model, Thinking: proposal.Thinking}, Metadata: metadata, CreatedAt: now, UpdatedAt: now, WorktreeEnabled: proposal.ManagedWorktree, WorktreeRootPath: allocation.WorkspacePath, WorktreeBaseBranch: allocation.BaseBranch, WorktreeBranch: allocation.BranchName}
		ready = append(ready, prepared{proposal: proposal, profile: profile, session: snapshot, runID: runID})
	}
	if apply == nil {
		return "", errors.New("session deployment requires the canonical V3 mutation publisher")
	}
	results := make([]manageSessionsDeployResult, len(ready))
	for i, item := range ready {
		results[i] = manageSessionsDeployResult{ProposalID: item.proposal.ID, SessionID: item.session.ID, Title: item.session.Title, Mode: item.proposal.Mode, Agent: item.profile.Name, Workspace: item.session.WorkspacePath, Worktree: item.proposal.ManagedWorktree, Status: "created", Navigation: deploySessionNavigation(item.session)}
		createKey := "session-deploy:create:" + digest + ":" + item.proposal.ID
		created, createErr := apply(sessionruntime.SessionMutationInput{SessionID: item.session.ID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, ClientRequestID: createKey, IdempotencyKey: createKey, PayloadHash: createKey, RequestHash: createKey, Kind: sessionruntime.SessionMutationCreateSession, Session: &item.session, NowUnixMs: time.Now().UnixMilli()})
		if createErr != nil {
			results[i].Status = "error"
			results[i].Error = createErr.Error()
			continue
		}
		message := pebblestore.MessageSnapshot{ID: deterministicDeployID(digest, item.proposal.ID, "message"), SessionID: item.session.ID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, Role: "user", Content: item.proposal.Prompt, Metadata: map[string]any{"source": "session_deploy"}, CreatedAt: time.Now().UnixMilli()}
		intent := pebblestore.V3SessionRunIntent{SessionID: item.session.ID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, RunID: item.runID, Status: sessionruntime.RunIntentPendingExecutor}
		messageKey := "session-deploy:message:" + digest + ":" + item.proposal.ID
		appended, appendErr := apply(sessionruntime.SessionMutationInput{SessionID: item.session.ID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, ClientRequestID: messageKey, IdempotencyKey: messageKey, PayloadHash: messageKey, RequestHash: messageKey, Kind: sessionruntime.SessionMutationAppendMessage, Message: &message, RunIntent: &intent, NowUnixMs: time.Now().UnixMilli()})
		if appendErr != nil {
			results[i].Status = "error"
			results[i].Error = appendErr.Error()
			continue
		}
		if created.Replayed || appended.Replayed {
			results[i].Status = "replayed"
		}
		if appended.Replayed && appended.RunIntent != nil && appended.RunIntent.Status == sessionruntime.RunIntentPendingExecutor {
			results[i].Status = "created"
		}
	}
	for i, item := range ready {
		if results[i].Status == "error" || results[i].Status == "replayed" {
			continue
		}
		go func() {
			_, _ = s.RunTurnWithOptions(context.Background(), item.session.ID, RunOptions{Prompt: item.proposal.Prompt, AgentName: item.profile.Name, TrustedAgentProfile: &item.profile, PermissionSessionID: item.session.ID, RunID: item.runID, Principal: principal, ApplySessionMutation: apply, SkipInitialUserMessage: true})
		}()
		results[i].Status = "started"
	}
	payload := map[string]any{"tool": "manage_sessions", "action": "deploy", "manifest_digest": digest, "selected_count": len(results), "results": results}
	raw, err := json.Marshal(payload)
	return string(raw), err
}

func deterministicDeployID(digest, proposalID, kind string) string {
	sum := sha256.Sum256([]byte(digest + "\x00" + proposalID + "\x00" + kind))
	raw := hex.EncodeToString(sum[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", raw[:8], raw[8:12], raw[12:16], raw[16:20], raw[20:32])
}

func deploySessionNavigation(session pebblestore.SessionSnapshot) map[string]any {
	slug := strings.ToLower(strings.Trim(filepath.Base(session.WorkspacePath), " /"))
	if slug == "" {
		slug = "workspace"
	}
	return map[string]any{"kind": "session", "session_id": session.ID, "workspace_path": session.WorkspacePath, "workspace_name": session.WorkspaceName, "workspace_slug": slug, "href": "/" + slug + "/" + session.ID}
}
