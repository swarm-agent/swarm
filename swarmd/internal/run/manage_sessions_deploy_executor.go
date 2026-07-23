package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type AITaskDeployBinding struct {
	UserID               string
	AccountScopeID       string
	WorkspacePath        string
	TaskID               string
	PreparationSessionID string
	PreparationRunID     string
}

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
	return s.executeManageSessionsDeployBound(ctx, parentSessionID, call, approvedArguments, apply, nil)
}

func (s *Service) executeManageSessionsDeployBound(ctx context.Context, parentSessionID string, call tool.Call, approvedArguments string, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), aiTask *AITaskDeployBinding) (string, error) {
	if s == nil || s.sessions == nil || s.agents == nil || s.workspace == nil || s.sessionDeployCanonicalize == nil || s.sessionDeployEnqueue == nil {
		return "", errors.New("session deployment canonical V3 services are not configured")
	}
	approved, err := parseApprovedManageSessionsDeploy(approvedArguments)
	if err != nil {
		return "", err
	}
	var parent pebblestore.SessionSnapshot
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID != "" {
		var ok bool
		parent, ok, err = s.sessions.GetSession(parentSessionID)
		if err != nil || !ok {
			if err == nil {
				err = errors.New("parent session not found")
			}
			return "", err
		}
	} else {
		if aiTask == nil {
			return "", errors.New("session deployment requires a calling session")
		}
		parent = pebblestore.SessionSnapshot{UserID: strings.TrimSpace(aiTask.UserID), AccountScopeID: strings.TrimSpace(aiTask.AccountScopeID), WorkspacePath: strings.TrimSpace(aiTask.WorkspacePath)}
	}
	if aiTask != nil && (strings.TrimSpace(aiTask.UserID) != strings.TrimSpace(parent.UserID) || strings.TrimSpace(aiTask.AccountScopeID) != strings.TrimSpace(parent.AccountScopeID) || strings.TrimSpace(aiTask.WorkspacePath) != strings.TrimSpace(parent.WorkspacePath)) {
		return "", errors.New("AI task deployment binding does not match the authorized origin")
	}
	if approved.ParentSessionID != "" && approved.ParentSessionID != parent.ID || approved.AccountScopeID != "" && approved.AccountScopeID != parent.AccountScopeID || approved.UserID != "" && approved.UserID != parent.UserID {
		return "", errors.New("approved session deployment identity binding does not match the deployment principal")
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
	if parent.ID == "" {
		principal.AccountScopeSource = identity.AccountScopeSourceServerState
	}
	profilesState, err := s.agents.ListStateForAccount(parent.AccountScopeID, 2000)
	if err != nil {
		return "", err
	}
	profiles := map[string]pebblestore.AgentProfile{}
	for _, profile := range profilesState.Profiles {
		profiles[strings.ToLower(strings.TrimSpace(profile.Name))] = profile
	}
	activeName := strings.TrimSpace(profilesState.ActivePrimary)
	activeResolution, activeFound, err := s.resolveManageSessionsDeployAgent(profiles, activeName)
	if err != nil {
		return "", fmt.Errorf("resolve active primary agent: %w", err)
	}
	active := activeResolution.ExecutionProfile
	if !activeFound || !active.Enabled || active.Mode != agentruntime.ModePrimary {
		return "", errors.New("active primary agent is missing, disabled, or invalid")
	}
	caller, callerErr := sessionV3AgentProfileFromMetadataMap(parent.Metadata)
	if callerErr != nil || parent.ID == "" {
		caller = active
	}
	callerContract, _, err := s.CompileStoredV3AgentToolContract(parent.AccountScopeID, caller)
	if err != nil {
		return "", fmt.Errorf("resolve calling agent capability: %w", err)
	}
	canDelegate := callerContract.Tools["task"].Enabled
	for i := range approved.Proposals {
		proposal := approved.Proposals[i]
		var resolution manageSessionsDeployAgentResolution
		var found bool
		var agentErr error
		if aiTask != nil {
			resolution, found, agentErr = s.resolveQueuedAITaskDeployAgent(profiles, activeName)
		} else {
			resolution, found, agentErr = s.resolveManageSessionsDeployAgent(profiles, proposal.AgentName)
		}
		if agentErr != nil {
			return "", fmt.Errorf("proposal %q agent resolution: %w", proposal.ID, agentErr)
		}
		profile := resolution.ExecutionProfile
		if !found || !profile.Enabled || (profile.Mode != agentruntime.ModePrimary && profile.Mode != agentruntime.ModeSubagent) {
			return "", fmt.Errorf("proposal %q agent binding is no longer valid", proposal.ID)
		}
		if aiTask == nil {
			if err := validateManageSessionsDeployAgent(active, profile, canDelegate); err != nil {
				return "", fmt.Errorf("proposal %q: %w", proposal.ID, err)
			}
		} else if profile.Mode != agentruntime.ModePrimary || pebblestore.AgentProfileRuntimeMode(profile) != pebblestore.AgentRuntimeModePlanAuto {
			return "", fmt.Errorf("proposal %q queued AI task requires an enabled plan/auto primary agent", proposal.ID)
		}
		executionMode, _, resolveErr := s.resolveExecutionMode(proposal.Mode, profile)
		if resolveErr != nil {
			return "", fmt.Errorf("proposal %q execution mode: %w", proposal.ID, resolveErr)
		}
		preference := resolution.preferenceForMode(parent.Preference, proposal.Mode)
		scope, scopeErr := s.workspace.ScopeForPathForPrincipal(principal, proposal.WorkspacePath)
		if scopeErr != nil || !scope.Matched || scope.WorkspaceID != proposal.WorkspaceID || scope.WorkspaceGeneration != proposal.WorkspaceGeneration {
			return "", fmt.Errorf("proposal %q workspace binding is no longer valid", proposal.ID)
		}
		proposal.AgentName, proposal.AgentMode, proposal.RuntimeMode = profile.Name, profile.Mode, executionMode
		proposal.Provider, proposal.Model, proposal.Thinking = preference.Provider, preference.Model, preference.Thinking
		proposal.ServiceTier, proposal.ContextMode = preference.ServiceTier, preference.ContextMode
		proposal.WorkspaceID, proposal.WorkspaceGeneration = scope.WorkspaceID, scope.WorkspaceGeneration
		proposal.WorkspacePath, proposal.WorkspaceName = scope.WorkspacePath, scope.WorkspaceName
		approved.Proposals[i] = proposal
	}
	manifest := manageSessionsDeployManifest{ManifestVersion: approved.ManifestVersion, Action: approved.Action, ParentSessionID: parent.ID, AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, Proposals: approved.Proposals}
	// Rebind the digest after re-resolving every trust field. Client edits are
	// limited to user-authorized fields; resolved authority remains server-owned.
	digest, err := manageSessionsDeployDigest(manifest)
	if aiTask != nil {
		digest = aiTaskDeploymentDigest(aiTask.AccountScopeID, aiTask.WorkspacePath, aiTask.TaskID)
	}
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
		resolution, found, agentErr := s.resolveManageSessionsDeployAgent(profiles, proposal.AgentName)
		if agentErr != nil {
			return "", fmt.Errorf("proposal %q agent resolution: %w", proposal.ID, agentErr)
		}
		profile := resolution.ExecutionProfile
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
			branchName := strings.TrimSpace(proposal.WorktreeBranch)
			if branchName == "" {
				return "", fmt.Errorf("proposal %q requires a canonical worktree branch", proposal.ID)
			}
			allocation, err = s.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(principal, scope.WorkspacePath, sessionID, proposal.WorktreeBaseBranch, branchName)
			if err != nil {
				return "", fmt.Errorf("proposal %q allocate managed worktree: %w", proposal.ID, err)
			}
			workspacePath = allocation.WorkspacePath
			workspaceName = scope.WorkspaceName
		}
		now := time.Now().UnixMilli()
		title := strings.TrimSpace(proposal.Title)
		if title == "" {
			title = "New Session"
		}
		lineageMetadata := sessionDeployCreationMetadata(parent.ID, scope.WorkspacePath, digest, proposal.ID)
		if aiTask != nil {
			lineageMetadata["ai_task_id"] = strings.TrimSpace(aiTask.TaskID)
			lineageMetadata["ai_task_workspace_path"] = strings.TrimSpace(aiTask.WorkspacePath)
			lineageMetadata["ai_task_preparation_session_id"] = strings.TrimSpace(aiTask.PreparationSessionID)
			lineageMetadata["ai_task_preparation_run_id"] = strings.TrimSpace(aiTask.PreparationRunID)
		}
		canonical, canonicalErr := s.sessionDeployCanonicalize(SessionDeployCanonicalizeInput{Principal: principal, WorkspacePath: scope.WorkspacePath, WorkspaceBindingID: proposal.WorkspaceBindingID, AgentProfile: profile, RuntimeMode: proposal.RuntimeMode, Metadata: lineageMetadata})
		if canonicalErr != nil {
			return "", fmt.Errorf("proposal %q resolve canonical V3 session metadata: %w", proposal.ID, canonicalErr)
		}
		if canonical.SourceWorkspaceID != proposal.WorkspaceID || canonical.SourceWorkspaceGeneration != proposal.WorkspaceGeneration {
			return "", fmt.Errorf("proposal %q canonical V3 workspace binding changed", proposal.ID)
		}
		metadata := canonical.Metadata
		if metadata == nil {
			return "", fmt.Errorf("proposal %q canonical V3 session metadata is empty", proposal.ID)
		}
		workspaceName = firstNonEmptyString(canonical.SourceWorkspaceName, workspaceName)
		if proposal.ManagedWorktree {
			metadata["workspace_id"] = allocation.WorkspaceID
			metadata["swarm_v3_source_workspace_path"] = canonical.SourceWorkspacePath
			metadata["swarm_v3_runtime_workspace_path"] = workspacePath
		} else {
			workspacePath = canonical.RuntimeWorkspacePath
		}
		snapshot := pebblestore.SessionSnapshot{ID: sessionID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, WorkspacePath: workspacePath, WorkspaceName: workspaceName, Title: title, Mode: proposal.Mode, Preference: pebblestore.ModelPreference{Provider: proposal.Provider, Model: proposal.Model, Thinking: proposal.Thinking, ServiceTier: proposal.ServiceTier, ContextMode: proposal.ContextMode}, Metadata: metadata, CreatedAt: now, UpdatedAt: now, WorktreeEnabled: proposal.ManagedWorktree, WorktreeRootPath: allocation.WorkspacePath, WorktreeBaseBranch: allocation.BaseBranch, WorktreeBranch: allocation.BranchName}
		if !proposal.ManagedWorktree {
			snapshot.WorktreeBranch = sessionruntime.DetectCurrentBranch(snapshot.WorkspacePath)
		}
		ready = append(ready, prepared{proposal: proposal, profile: profile, session: snapshot, runID: runID})
	}
	if apply == nil {
		return "", errors.New("session deployment requires the canonical V3 mutation publisher")
	}
	results := make([]manageSessionsDeployResult, len(ready))
	replayedPending := make([]bool, len(ready))
	for i, item := range ready {
		results[i] = manageSessionsDeployResult{ProposalID: item.proposal.ID, SessionID: item.session.ID, Title: item.session.Title, Mode: item.proposal.Mode, Agent: item.profile.Name, Workspace: item.session.WorkspacePath, Worktree: item.proposal.ManagedWorktree, Status: "created", Navigation: deploySessionNavigation(item.session)}
		createKey := "session-deploy:create:" + digest + ":" + item.proposal.ID
		_, createErr := apply(sessionruntime.SessionMutationInput{SessionID: item.session.ID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, ClientRequestID: createKey, IdempotencyKey: createKey, PayloadHash: createKey, RequestHash: createKey, Kind: sessionruntime.SessionMutationCreateSession, Session: &item.session, NowUnixMs: time.Now().UnixMilli()})
		if createErr == nil && aiTask != nil && s.aiTaskBinder != nil {
			_ = s.aiTaskBinder.AppendAITaskAudit(aiTask.AccountScopeID, aiTask.WorkspacePath, aiTask.TaskID, pebblestore.AITaskAuditRecord{StageKey: "000002_final_session", Stage: "final_session", FinalSessionID: item.session.ID, FinalRunID: item.runID, Disposition: "created_or_reused", CreatedAt: time.Now().UnixMilli()})
		}
		if createErr != nil {
			results[i].Status = "error"
			results[i].Error = createErr.Error()
			continue
		}
		message := pebblestore.MessageSnapshot{ID: deterministicDeployID(digest, item.proposal.ID, "message"), SessionID: item.session.ID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, Role: "user", Content: item.proposal.Prompt, Metadata: map[string]any{"source": "session_deploy"}, CreatedAt: time.Now().UnixMilli()}
		intentParentSessionID := parent.ID
		if intentParentSessionID == "" {
			intentParentSessionID = item.session.ID
		}
		intent := sessionDeployRunIntent(item.session.ID, item.runID, intentParentSessionID, parent.UserID, parent.AccountScopeID)
		messageKey := "session-deploy:message:" + digest + ":" + item.proposal.ID
		appended, appendErr := apply(sessionruntime.SessionMutationInput{SessionID: item.session.ID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, ClientRequestID: messageKey, IdempotencyKey: messageKey, PayloadHash: messageKey, RequestHash: messageKey, Kind: sessionruntime.SessionMutationAppendMessage, Message: &message, RunIntent: &intent, NowUnixMs: time.Now().UnixMilli()})
		if appendErr == nil && aiTask != nil && s.aiTaskBinder != nil {
			_ = s.aiTaskBinder.AppendAITaskAudit(aiTask.AccountScopeID, aiTask.WorkspacePath, aiTask.TaskID, pebblestore.AITaskAuditRecord{StageKey: "000002_final_run_intent", Stage: "final_run_intent", FinalSessionID: item.session.ID, FinalRunID: item.runID, Disposition: "created_or_reused", CreatedAt: time.Now().UnixMilli()})
		}
		if appendErr != nil {
			results[i].Status = "error"
			results[i].Error = appendErr.Error()
			continue
		}
		if appended.Replayed {
			results[i].Status = "replayed"
			replayedPending[i] = appended.RunIntent != nil && appended.RunIntent.Status == sessionruntime.RunIntentPendingExecutor
		}
		if !appended.Replayed && (appended.RunIntent == nil || appended.RunIntent.Status != sessionruntime.RunIntentPendingExecutor) {
			results[i].Status = "error"
			results[i].Error = "canonical V3 message mutation did not persist a pending executor run intent"
		}
	}
	for i, item := range ready {
		if results[i].Status == "error" || (results[i].Status == "replayed" && !replayedPending[i]) {
			continue
		}
		if aiTask != nil {
			if parent.ID != "" {
				parentMetadata := cloneStringAnyMap(parent.Metadata)
				parentMetadata["ai_task_final_session_id"] = item.session.ID
				parentMetadata["ai_task_final_run_id"] = item.runID
				linkedParent := parent
				linkedParent.Metadata = parentMetadata
				linkKey := "ai-task:preparation:link:" + strings.TrimSpace(aiTask.TaskID)
				if _, linkErr := apply(sessionruntime.SessionMutationInput{SessionID: parent.ID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, ClientRequestID: linkKey, IdempotencyKey: linkKey, PayloadHash: linkKey, RequestHash: linkKey, Kind: sessionruntime.SessionMutationUpdateMetadata, Session: &linkedParent, NowUnixMs: time.Now().UnixMilli()}); linkErr != nil {
					results[i].Status, results[i].Error = "error", linkErr.Error()
					continue
				}
			}
			if s.aiTaskBinder == nil {
				results[i].Status, results[i].Error = "error", "AI task binder is not configured"
				continue
			}
			if results[i].Status != "replayed" {
				if _, bindErr := s.aiTaskBinder.BindAITaskLifecycle(aiTask.AccountScopeID, aiTask.WorkspacePath, aiTask.TaskID, "preparing", "in_progress", item.proposal.Mode, item.proposal.ManagedWorktree, item.session.ID, item.session.Title, item.runID, "", ""); bindErr != nil {
					results[i].Status, results[i].Error = "error", bindErr.Error()
					continue
				}
			}
		}
		enqueueParentSessionID := parent.ID
		if enqueueParentSessionID == "" {
			enqueueParentSessionID = item.session.ID
		}
		if !s.sessionDeployEnqueue(principal, item.session.ID, item.runID, enqueueParentSessionID) {
			if results[i].Status == "replayed" {
				continue
			}
			results[i].Status = "error"
			results[i].Error = "canonical V3 session executor rejected the deployed run"
			if aiTask != nil && s.aiTaskBinder != nil {
				_, _ = s.aiTaskBinder.BindAITaskLifecycle(aiTask.AccountScopeID, aiTask.WorkspacePath, aiTask.TaskID, "in_progress", "failed", item.proposal.Mode, item.proposal.ManagedWorktree, item.session.ID, item.session.Title, item.runID, "", results[i].Error)
			}
			continue
		}
		results[i].Status = "started"
		if aiTask != nil && s.aiTaskBinder != nil {
			_ = s.aiTaskBinder.AppendAITaskAudit(aiTask.AccountScopeID, aiTask.WorkspacePath, aiTask.TaskID, pebblestore.AITaskAuditRecord{StageKey: "000003_executor_enqueued", Stage: "executor_enqueued", FinalSessionID: item.session.ID, FinalRunID: item.runID, Disposition: "started", CreatedAt: time.Now().UnixMilli()})
		}
	}
	payload := map[string]any{"tool": "manage_sessions", "action": "deploy", "manifest_digest": digest, "selected_count": len(results), "results": results}
	raw, err := json.Marshal(payload)
	return string(raw), err
}

func cloneStringAnyMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input)+2)
	for key, value := range input {
		out[key] = value
	}
	return out
}

func sessionDeployCreationMetadata(parentSessionID, workspacePath, digest, proposalID string) map[string]any {
	metadata := map[string]any{
		"source":                     "desktop-v3",
		"workspace_path":             strings.TrimSpace(workspacePath),
		"deployment_manifest_digest": strings.TrimSpace(digest),
		"deployment_proposal_id":     strings.TrimSpace(proposalID),
	}
	if parentSessionID = strings.TrimSpace(parentSessionID); parentSessionID != "" {
		metadata["parent_session_id"] = parentSessionID
		metadata["lineage_kind"] = "session_deploy"
	}
	return metadata
}

func sessionDeployRunIntent(sessionID, runID, parentSessionID, userID, accountScopeID string) pebblestore.V3SessionRunIntent {
	return pebblestore.V3SessionRunIntent{
		SessionID:       strings.TrimSpace(sessionID),
		UserID:          strings.TrimSpace(userID),
		AccountScopeID:  strings.TrimSpace(accountScopeID),
		RunID:           strings.TrimSpace(runID),
		Status:          sessionruntime.RunIntentPendingExecutor,
		RunSessionID:    strings.TrimSpace(sessionID),
		ParentSessionID: strings.TrimSpace(parentSessionID),
	}
}

func aiTaskDeploymentDigest(accountScopeID, workspacePath, taskID string) string {
	sum := sha256.Sum256([]byte("ai-task-deploy\x00" + strings.TrimSpace(accountScopeID) + "\x00" + strings.TrimSpace(workspacePath) + "\x00" + strings.TrimSpace(taskID)))
	return hex.EncodeToString(sum[:])
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
