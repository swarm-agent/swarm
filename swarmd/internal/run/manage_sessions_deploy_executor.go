package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
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
	if s == nil || s.sessions == nil || s.agents == nil || s.workspace == nil || s.v3Launcher == nil {
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
	// Rebind the digest after re-resolving every trust field. Client edits are
	// limited to user-authorized fields; resolved authority remains server-owned.
	digest, err := manageSessionsDeployDigest(manifest)
	if err != nil {
		return "", fmt.Errorf("bind approved session deployment manifest: %w", err)
	}
	if strings.TrimSpace(approved.ManifestDigest) == "" || !strings.EqualFold(strings.TrimSpace(approved.ManifestDigest), digest) {
		return "", errors.New("approved session deployment manifest digest is stale or invalid")
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
		proposal  manageSessionsDeployProposal
		profile   pebblestore.AgentProfile
		sessionID string
		runID     string
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
		if proposal.ManagedWorktree && strings.TrimSpace(proposal.WorktreeBranch) == "" {
			return "", fmt.Errorf("proposal %q requires a canonical worktree branch", proposal.ID)
		}
		ready = append(ready, prepared{proposal: proposal, profile: profile, sessionID: sessionID, runID: runID})
	}
	results := make([]manageSessionsDeployResult, len(ready))
	for i, item := range ready {
		launch, launchErr := s.v3Launcher.LaunchV3Session(ctx, V3SessionLaunchRequest{
			Principal: principal, SessionID: item.sessionID, RunID: item.runID,
			CreateClientRequestID:  "session-deploy:create:" + digest + ":" + item.proposal.ID,
			MessageClientRequestID: "session-deploy:message:" + digest + ":" + item.proposal.ID,
			MessageID:              deterministicDeployID(digest, item.proposal.ID, "message"),
			Title:                  item.proposal.Title, Prompt: item.proposal.Prompt, Mode: item.proposal.Mode,
			AgentName: item.profile.Name, Preference: pebblestore.ModelPreference{Provider: item.proposal.Provider, Model: item.proposal.Model, Thinking: item.proposal.Thinking},
			SourceWorkspaceID: item.proposal.WorkspaceID, SourceWorkspaceGeneration: item.proposal.WorkspaceGeneration,
			SourceWorkspacePath: item.proposal.WorkspacePath, SourceWorkspaceName: item.proposal.WorkspaceName,
			WorkspaceBindingID: item.proposal.WorkspaceBindingID, ManagedWorktree: item.proposal.ManagedWorktree,
			WorktreeBaseBranch: item.proposal.WorktreeBaseBranch, WorktreeBranch: item.proposal.WorktreeBranch,
			ParentSessionID: parent.ID, DeploymentManifestDigest: digest, DeploymentProposalID: item.proposal.ID,
		})
		results[i] = manageSessionsDeployResult{ProposalID: item.proposal.ID, SessionID: item.sessionID, Mode: item.proposal.Mode, Agent: item.profile.Name, Worktree: item.proposal.ManagedWorktree}
		if launchErr != nil {
			results[i].Status = "error"
			results[i].Error = launchErr.Error()
			continue
		}
		results[i].SessionID, results[i].Title, results[i].Workspace = launch.Session.ID, launch.Session.Title, launch.Session.WorkspacePath
		results[i].Navigation = deploySessionNavigation(launch.Session)
		if launch.Replayed {
			results[i].Status = "replayed"
		} else if launch.Enqueued {
			results[i].Status = "started"
		} else {
			results[i].Status = "created"
		}
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
	workspacePath := firstNonEmptyString(mapString(session.Metadata, "swarm_v3_source_workspace_path"), session.WorkspacePath)
	workspaceName := firstNonEmptyString(mapString(session.Metadata, "swarm_v3_source_workspace_name"), session.WorkspaceName)
	slug := strings.ToLower(strings.Trim(workspaceName, " /"))
	if slug == "" {
		slug = "workspace"
	}
	return map[string]any{"kind": "session", "session_id": session.ID, "workspace_path": workspacePath, "workspace_name": workspaceName, "workspace_slug": slug, "href": "/" + slug + "/" + session.ID}
}
