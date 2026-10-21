package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func (s *Server) bindOrReleaseTaskExecution(ctx context.Context, p identity.Principal, task *pebblestore.ProjectTaskRecord, sessionID string, lease environments.DeploymentLease) error {
	if err := s.bindTaskExecutionSession(ctx, p, task, sessionID, lease); err != nil {
		cleanup, ok := s.deployments.(interface {
			ReleaseDeployment(context.Context, lifecycle.ReleaseDeploymentRequest) (*lifecycle.ReleaseDeploymentResult, error)
		})
		if !ok {
			return errors.Join(err, errors.New("receipt cleanup unavailable"))
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cleanupCancel()
		_, cleanupErr := cleanup.ReleaseDeployment(cleanupCtx, lifecycle.ReleaseDeploymentRequest{AccountScopeID: p.AccountScopeID, WorkspaceID: lease.WorkspaceID, LeaseID: lease.ID, Attribution: environments.OperationAttribution{Actor: p.UserID, SessionID: sessionID}, Reason: "execution_binding_failed"})
		return errors.Join(err, cleanupErr)
	}
	return nil
}

// Execution admission is separate from management admission. A task Coder
// consumes its current task receipt here; its own manage_environments calls go
// through management admission.
func (s *Server) validateTaskExecutionConsumer(p identity.Principal, task *pebblestore.ProjectTaskRecord, sessionID string) error {
	caller, found, err := s.sessions.GetSession(sessionID)
	if err != nil || !found || caller.AccountScopeID != p.AccountScopeID || caller.UserID != p.UserID {
		return errors.New("task execution consumer ownership mismatch")
	}
	var profile pebblestore.AgentProfile
	raw, _ := json.Marshal(caller.Metadata["agent_profile"])
	coder := json.Unmarshal(raw, &profile) == nil && agentruntime.IsCoderAgentName(profile.Name)
	if !coder && tool.EnvironmentToolAllowed(caller.Metadata, "manage_environments") {
		_, err := s.authorizeTaskEnvironment(identity.Principal{Type: p.Type, AccountScopeID: p.AccountScopeID, UserID: p.UserID}, sessionID, task.ProjectID, task.ID)
		return err
	}
	if !coder {
		return errors.New("task execution requires current Coder consumer")
	}
	if task.SessionID != sessionID {
		_, err := s.delegatedExecutionParent(p, task, caller)
		return err
	}
	if err := verifyProjectTaskSession(task, caller, p.AccountScopeID); err != nil {
		return err
	}
	task.EnsureTaskAttempts()
	a := task.ActiveAttempt()
	if task.Archived || a == nil || a.SessionID != sessionID || a.ID != task.ActiveAttemptID {
		return errors.New("task execution attempt is stale")
	}
	return nil
}

func (s *Server) bindTaskExecutionSession(ctx context.Context, p identity.Principal, task *pebblestore.ProjectTaskRecord, sessionID string, lease environments.DeploymentLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Read projection first: a concurrent session update is rejected by the
	// canonical V3 sequence CAS, rather than replacing unrelated metadata.
	projection, found, err := s.sessions.Store().GetV3SessionProjection(sessionID)
	if err != nil || !found {
		return errors.New("execution session projection unavailable")
	}
	next, found, err := s.sessions.GetSession(sessionID)
	if err != nil || !found {
		return errors.New("execution session unavailable")
	}
	if err := s.validateTaskExecutionConsumer(p, task, sessionID); err != nil {
		return err
	}
	metadata := make(map[string]any, len(next.Metadata)+1)
	for key, value := range next.Metadata {
		metadata[key] = value
	}
	metadata[tool.ManagedExecutionMetadataKey] = tool.ManagedExecutionBinding{WorkspaceID: lease.WorkspaceID, LeaseID: lease.ID}
	next.Metadata, next.UpdatedAt = metadata, time.Now().UnixMilli()
	key := "managed-execution:" + lease.ID
	result, err := s.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: sessionID, AccountScopeID: p.AccountScopeID, UserID: p.UserID,
		Kind: pebblestore.V3SessionMutationUpdateMetadata, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key,
		Session: &next, ExpectedLastEventSeq: &projection.LastEventSeq, NowUnixMs: next.UpdatedAt,
	})
	if err == nil && (result.Error != nil || result.Conflict != nil || result.Session == nil) {
		return errors.New("managed execution binding rejected by canonical session mutation")
	}
	return err
}
