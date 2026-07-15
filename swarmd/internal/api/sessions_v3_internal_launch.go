package api

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// LaunchV3Session is the internal API-owned boundary for trusted control-plane
// callers that need to create a canonical durable V3 session and dispatch its
// first turn without routing back through HTTP.
func (s *Server) LaunchV3Session(ctx context.Context, req runruntime.V3SessionLaunchRequest) (runruntime.V3SessionLaunchResult, error) {
	if s == nil || s.sessions == nil || s.v3SessionExecutor == nil {
		return runruntime.V3SessionLaunchResult{}, errors.New("sessions v3 launch service is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return runruntime.V3SessionLaunchResult{}, ctx.Err()
	default:
	}
	principal := req.Principal
	principal.Type = identity.PrincipalTypeUser
	principal.UserID = strings.TrimSpace(principal.UserID)
	principal.AccountScopeID = strings.TrimSpace(principal.AccountScopeID)
	principal.SessionID = strings.TrimSpace(req.ParentSessionID)
	if !principal.Valid() {
		return runruntime.V3SessionLaunchResult{}, identity.ErrPrincipalRequired
	}
	for name, value := range map[string]string{
		"session id": req.SessionID, "run id": req.RunID,
		"create client request id":  req.CreateClientRequestID,
		"message client request id": req.MessageClientRequestID,
		"message id":                req.MessageID, "prompt": req.Prompt,
		"agent name": req.AgentName, "source workspace id": req.SourceWorkspaceID,
		"source workspace path": req.SourceWorkspacePath,
	} {
		if strings.TrimSpace(value) == "" {
			return runruntime.V3SessionLaunchResult{}, fmt.Errorf("v3 session launch %s is required", name)
		}
	}
	binding, err := s.resolveSessionsV3LaunchBinding(principal, req)
	if err != nil {
		return runruntime.V3SessionLaunchResult{}, err
	}
	resolvedAgent, err := s.resolveSessionsV3PrimaryCreateAgent(principal, req.AgentName)
	if err != nil {
		return runruntime.V3SessionLaunchResult{}, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "New Session"
	}
	now := time.Now().UnixMilli()
	metadata := sessionsV3CreateServerMetadata(map[string]any{
		"parent_session_id":          strings.TrimSpace(req.ParentSessionID),
		"lineage_kind":               "session_deploy",
		"deployment_manifest_digest": strings.TrimSpace(req.DeploymentManifestDigest),
		"deployment_proposal_id":     strings.TrimSpace(req.DeploymentProposalID),
	}, resolvedAgent, binding)
	session := pebblestore.SessionSnapshot{
		ID:             strings.TrimSpace(req.SessionID),
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  binding.SourceWorkspacePath,
		WorkspaceName:  binding.SourceWorkspaceName,
		Title:          title,
		Mode:           sessionruntime.NormalizeMode(req.Mode),
		Preference:     normalizeSessionsV3ModelPreference(req.Preference),
		Metadata:       metadata,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if session.WorkspaceName == "" {
		session.WorkspaceName = filepath.Base(session.WorkspacePath)
	}
	if existing, exists, getErr := s.sessions.GetSession(session.ID); getErr != nil {
		return runruntime.V3SessionLaunchResult{}, getErr
	} else if exists {
		if sessionsV3MetadataString(existing.Metadata, "deployment_manifest_digest") != strings.TrimSpace(req.DeploymentManifestDigest) || sessionsV3MetadataString(existing.Metadata, "deployment_proposal_id") != strings.TrimSpace(req.DeploymentProposalID) {
			return runruntime.V3SessionLaunchResult{}, errors.New("deterministic deployed session id is bound to another deployment")
		}
		session = existing
	} else if req.ManagedWorktree {
		allocation, allocationErr := s.resolveSessionsV3CreateWorktree(principal, binding.SourceWorkspacePath, session.ID, nil, req.WorktreeBaseBranch, req.WorktreeBranch, "")
		if allocationErr != nil {
			return runruntime.V3SessionLaunchResult{}, allocationErr
		}
		session.WorkspacePath = strings.TrimSpace(allocation.WorkspacePath)
		session.WorktreeEnabled = true
		session.WorktreeRootPath = strings.TrimSpace(allocation.WorkspacePath)
		session.WorktreeBaseBranch = strings.TrimSpace(allocation.BaseBranch)
		session.WorktreeBranch = strings.TrimSpace(allocation.BranchName)
		session.Metadata["workspace_id"] = strings.TrimSpace(allocation.WorkspaceID)
		session.Metadata["swarm_v3_runtime_workspace_path"] = strings.TrimSpace(allocation.WorkspacePath)
	}
	_, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: session.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: strings.TrimSpace(req.CreateClientRequestID), IdempotencyKey: strings.TrimSpace(req.CreateClientRequestID),
		PayloadHash: strings.TrimSpace(req.CreateClientRequestID), RequestHash: strings.TrimSpace(req.CreateClientRequestID),
		Kind: sessionruntime.SessionMutationCreateSession, Session: &session, NowUnixMs: now,
	})
	if err != nil {
		return runruntime.V3SessionLaunchResult{}, err
	}
	message := pebblestore.MessageSnapshot{ID: strings.TrimSpace(req.MessageID), Role: "user", Content: req.Prompt, Metadata: map[string]any{"source": "session_deploy"}, CreatedAt: now}
	intent := pebblestore.V3SessionRunIntent{RunID: strings.TrimSpace(req.RunID), Status: sessionruntime.RunIntentPendingExecutor, ParentSessionID: strings.TrimSpace(req.ParentSessionID)}
	messageResult, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: session.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: strings.TrimSpace(req.MessageClientRequestID), IdempotencyKey: strings.TrimSpace(req.MessageClientRequestID),
		PayloadHash: strings.TrimSpace(req.MessageClientRequestID), RequestHash: strings.TrimSpace(req.MessageClientRequestID),
		Kind: sessionruntime.SessionMutationAppendMessage, Message: &message, RunIntent: &intent, NowUnixMs: now,
	})
	if err != nil {
		return runruntime.V3SessionLaunchResult{}, err
	}
	result := runruntime.V3SessionLaunchResult{Session: session, Replayed: messageResult.Replayed}
	if messageResult.Replayed {
		return result, nil
	}
	if messageResult.RunIntent == nil || messageResult.RunIntent.Status != sessionruntime.RunIntentPendingExecutor {
		return result, errors.New("v3 session launch did not commit a pending executor intent")
	}
	result.Enqueued = s.v3SessionExecutor.EnqueueRun(sessionV3ExecutorJob{Principal: principal, SessionID: session.ID, RunID: messageResult.RunIntent.RunID, ParentSessionID: strings.TrimSpace(req.ParentSessionID)})
	if !result.Enqueued {
		reason := "v3 session executor rejected the committed deployment run"
		_, statusErr := s.v3SessionExecutor.recordRunStatus(sessionV3ExecutorJob{Principal: principal, SessionID: session.ID, RunID: messageResult.RunIntent.RunID, ParentSessionID: strings.TrimSpace(req.ParentSessionID)}, sessionruntime.RunIntentFailed, reason, "session.run.failed")
		if statusErr != nil {
			return result, fmt.Errorf("%s: record terminal state: %w", reason, statusErr)
		}
		return result, errors.New(reason)
	}
	return result, nil
}

func (s *Server) resolveSessionsV3LaunchBinding(principal identity.Principal, req runruntime.V3SessionLaunchRequest) (sessionsV3PrimaryBinding, error) {
	if s == nil || s.topology == nil {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary topology is not configured")
	}
	bindings, err := s.topology.ListWorkspaceBindingsForAccount(principal.AccountScopeID, 100000)
	if err != nil {
		return sessionsV3PrimaryBinding{}, err
	}
	for _, record := range bindings {
		if strings.TrimSpace(req.WorkspaceBindingID) != "" && strings.TrimSpace(record.BindingID) != strings.TrimSpace(req.WorkspaceBindingID) {
			continue
		}
		if strings.TrimSpace(record.SourceWorkspaceID) != strings.TrimSpace(req.SourceWorkspaceID) || record.SourceWorkspaceGeneration != req.SourceWorkspaceGeneration {
			continue
		}
		if filepath.Clean(strings.TrimSpace(record.SourceWorkspacePath)) != filepath.Clean(strings.TrimSpace(req.SourceWorkspacePath)) {
			continue
		}
		return s.resolveSessionsV3PrimaryBinding(principal, sessionsV3CreateRequest{
			WorkspacePath: req.SourceWorkspacePath, WorkspaceBindingID: record.BindingID,
			SwarmID: record.DestinationRuntimeSwarmID, TargetKind: "host", TargetRelationship: "self",
			HostWorkspacePath: record.SourceWorkspacePath, RuntimeWorkspacePath: record.DestinationWorkspacePath,
		})
	}
	return sessionsV3PrimaryBinding{}, errors.New("v3 session launch could not resolve the canonical self workspace binding")
}
