package api

import (
	"context"
	"errors"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Preparation cannot substitute an arbitrary project member or mutable dev HEAD.
// Once assigned, the current isolated task tree is the source authority.
func (s *Server) validateTaskEnvironmentCommit(task *pebblestore.ProjectTaskRecord, commit string) error {
	if commit == "" || s.worktrees == nil {
		return errors.New("task source Git inspection unavailable")
	}
	root := task.SourceWorkspace.Path
	if task.SessionID != "" {
		child, found, err := s.sessions.GetSession(task.SessionID)
		if err != nil || !found || !child.WorktreeEnabled {
			return errors.New("task source session unavailable or not isolated")
		}
		if err := verifyProjectTaskSession(task, child, child.AccountScopeID); err != nil {
			return err
		}
		validator, ok := s.worktrees.(interface {
			ValidateSessionRepositoryLane(string, string, string, string) error
		})
		if !ok {
			return errors.New("task source ownership validator unavailable")
		}
		if err := validator.ValidateSessionRepositoryLane(root, child.WorktreeRootPath, child.ID, child.WorktreeBranch); err != nil {
			return err
		}
		root = child.WorktreeRootPath
	}
	if root == "" {
		return errors.New("task source root required")
	}
	state, err := s.worktrees.InspectTaskWorkspace(root)
	if err != nil || !state.Clean || state.HeadCommit != commit {
		return errors.New("task source changed or dirty; commit, rebuild and explicitly reassign attachment")
	}
	return nil
}

func (s *Server) prepareTaskEnvironment(ctx context.Context, p identity.Principal, sessionID string, task *pebblestore.ProjectTaskRecord, req tool.TaskEnvironmentRequest) (tool.TaskEnvironmentResult, error) {
	var result tool.TaskEnvironmentResult
	if s.environments == nil || s.deployments == nil || req.EnvironmentID == "" || req.WorkspaceID == "" {
		return result, errors.New("explicit environment_id and workspace_id required")
	}
	env, found, err := s.environments.Get(p.AccountScopeID, req.WorkspaceID, req.EnvironmentID)
	if err != nil || !found || env.Build == nil {
		return result, errors.New("task preparation requires a managed committed build")
	}
	build := environments.ImageBuildResult{Product: env.Build.Product, Recipe: env.Build.Recipe}
	if req.Action != "build" {
		if req.BuildOperationID == "" {
			return result, errors.New("explicit successful build_operation_id required")
		}
		op, found, err := s.deployments.Get(ctx, p.AccountScopeID, req.WorkspaceID, req.BuildOperationID)
		if err != nil || !found || op.EnvironmentID != env.ID || op.Action != environments.OperationActionBuild || op.Status != environments.OperationStatusSucceeded || op.Result.Build == nil {
			return result, errors.New("exact successful managed build required")
		}
		build = *op.Result.Build
	}
	source := environments.PreparedDeploymentSource{AccountScopeID: p.AccountScopeID, WorkspaceID: req.WorkspaceID, EnvironmentID: env.ID, Build: build}
	if err := s.validateTaskEnvironmentSource(p, task, source); err != nil {
		return result, err
	}
	kind, consumer := environments.ConsumerTypeCustom, p.UserID
	if sessionID != "" {
		kind, consumer = environments.ConsumerTypeSession, sessionID
	}
	op, err := s.deployments.Submit(ctx, lifecycle.SubmitOperationRequest{AccountScopeID: p.AccountScopeID, WorkspaceID: req.WorkspaceID, EnvironmentID: env.ID, Action: req.Action, EnvOverrides: req.EnvOverrides, IdempotencyKey: req.IdempotencyKey, BuildOperationID: req.BuildOperationID, ConsumerType: kind, ConsumerID: consumer, TTLMillis: req.TTLMillis, Attribution: environments.OperationAttribution{Actor: p.UserID, SessionID: sessionID}})
	if err != nil {
		return result, err
	}
	public := *op
	public.LeaseID = ""
	result.Operation = &public
	result.TaskRevision = task.Revision
	return result, nil
}

// Preparation receipts precede attachments, but only their original session may
// inspect/cancel them. Execution operations always require attachment receipts.
func (s *Server) taskPreparationOperation(ctx context.Context, p identity.Principal, sessionID string, task *pebblestore.ProjectTaskRecord, req tool.TaskEnvironmentRequest) (tool.TaskEnvironmentResult, error) {
	var result tool.TaskEnvironmentResult
	if s.deployments == nil || sessionID == "" || req.WorkspaceID == "" || req.OperationID == "" {
		return result, errors.New("own explicit preparation operation required")
	}
	op, found, err := s.deployments.Get(ctx, p.AccountScopeID, req.WorkspaceID, req.OperationID)
	if err != nil || !found || op.Attribution.SessionID != sessionID || op.Attribution.Actor != p.UserID {
		return result, errors.New("preparation operation belongs to another consumer")
	}
	if attempt := task.ActiveAttempt(); attempt != nil && op.CreatedAt < attempt.CreatedAt {
		return result, errors.New("preparation operation predates current task attempt")
	}
	if req.Action == "release_preparation" {
		if (op.Action != "ensure" && op.Action != "deploy") || op.Status != environments.OperationStatusSucceeded || op.LeaseID == "" {
			return result, errors.New("successful own deployment preparation required")
		}
		reader, ok := s.deployments.(interface {
			GetLease(string, string, string) (environments.DeploymentLease, bool, error)
		})
		if !ok {
			return result, errors.New("lease service unavailable")
		}
		lease, found, err := reader.GetLease(p.AccountScopeID, req.WorkspaceID, op.LeaseID)
		if err != nil || !found || lease.Shared || lease.TaskBinding != nil || lease.ConsumerType != environments.ConsumerTypeSession || lease.ConsumerID != sessionID || lease.DeploymentID != op.DeploymentID {
			return result, errors.New("preparation lease ownership mismatch")
		}
		released, err := s.deployments.Submit(ctx, lifecycle.SubmitOperationRequest{Action: "release", AccountScopeID: p.AccountScopeID, WorkspaceID: req.WorkspaceID, DeploymentID: lease.DeploymentID, LeaseID: lease.ID, Attribution: environments.OperationAttribution{Actor: p.UserID, SessionID: sessionID}, Reason: "preparation handed to task consumers"})
		if err != nil {
			return result, err
		}
		public := *released
		public.LeaseID = ""
		result.Operation, result.TaskRevision = &public, task.Revision
		return result, nil
	}
	var build environments.ImageBuildResult
	switch op.Action {
	case "build":
		if op.BuildDefinition == nil {
			return result, errors.New("captured build source unavailable")
		}
		build.Product, build.Recipe = op.BuildDefinition.Product, op.BuildDefinition.Recipe
	case "release":
		// Owner-only release status carries no execution grant and remains useful
		// after handing the deployment to a different attachment consumer.
		if req.Action != "get_operation" {
			return result, errors.New("release status is read-only")
		}
		op.LeaseID = ""
		result.Operation, result.TaskRevision = &op, task.Revision
		return result, nil
	case "ensure", "deploy":
		d, found, err := s.deployments.GetDeployment(p.AccountScopeID, req.WorkspaceID, op.DeploymentID)
		if err != nil {
			return result, err
		}
		if !found || d.Build == nil {
			// A queued/running preparation may not have a deployment yet. Its
			// authenticated owner can inspect/cancel it without runtime access.
			if op.Status.IsTerminal() {
				return result, errors.New("completed preparation deployment unavailable")
			}
			if req.Action == "cancel_operation" {
				cancelled, e := s.deployments.Cancel(ctx, lifecycle.CancelOperationRequest{AccountScopeID: p.AccountScopeID, WorkspaceID: req.WorkspaceID, OperationID: op.OperationID, Reason: "task preparation cancellation"})
				if e != nil {
					return result, e
				}
				op = *cancelled
			}
			op.LeaseID = ""
			result.Operation, result.TaskRevision = &op, task.Revision
			return result, nil
		}
		build = *d.Build
	default:
		return result, errors.New("execution inspection requires own attachment receipt")
	}
	if err := s.validateTaskEnvironmentSource(p, task, environments.PreparedDeploymentSource{AccountScopeID: p.AccountScopeID, WorkspaceID: req.WorkspaceID, Build: build}); err != nil {
		return result, err
	}
	if req.Action == "cancel_operation" {
		cancelled, err := s.deployments.Cancel(ctx, lifecycle.CancelOperationRequest{AccountScopeID: p.AccountScopeID, WorkspaceID: req.WorkspaceID, OperationID: op.OperationID, Reason: "task preparation cancellation"})
		if err != nil {
			return result, err
		}
		op = *cancelled
	}
	// A preparation receipt is not execution authority; require explicit attach/acquire.
	op.LeaseID = ""
	result.Operation, result.TaskRevision = &op, task.Revision
	return result, nil
}
