package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type delegatedExecutionLifecycleService interface {
	GetLease(string, string, string) (environments.DeploymentLease, bool, error)
	AcquirePreparedLease(context.Context, lifecycle.AcquirePreparedLeaseRequest) (environments.DeploymentLease, error)
	ReleaseDeployment(context.Context, lifecycle.ReleaseDeploymentRequest) (*lifecycle.ReleaseDeploymentResult, error)
}

// Keep this trusted handoff contract tied to the actual daemon service, rather
// than an interface implemented only by a test fixture.
var _ delegatedExecutionLifecycleService = (*lifecycle.DeploymentManager)(nil)

// delegatedExecutionParent proves a current direct Task child using durable
// lineage, not caller-supplied parent metadata. Nested delegation is not admitted.
func (s *Server) delegatedExecutionParent(p identity.Principal, task *pebblestore.ProjectTaskRecord, child pebblestore.SessionSnapshot) (pebblestore.SessionSnapshot, error) {
	fail := errors.New("managed execution delegated lineage is not current")
	generation, found, err := s.sessions.GetDelegatedChildGenerationBySession(p.AccountScopeID, child.ID)
	if err != nil || !found || generation.State != pebblestore.DelegatedChildGenerationActive || generation.SessionID != child.ID || generation.ParentSessionID == "" || generation.ParentSessionID != task.SessionID {
		return pebblestore.SessionSnapshot{}, fail
	}
	lineage, found, err := s.sessions.GetDelegatedChildLineage(p.AccountScopeID, generation.LogicalTaskID)
	if err != nil || !found || lineage.CurrentSessionID != child.ID || lineage.CurrentGeneration != generation.Generation {
		return pebblestore.SessionSnapshot{}, fail
	}
	if !child.WorktreeEnabled || child.WorktreeRootPath == "" || generation.WorkspacePath != child.WorktreeRootPath || child.Metadata["swarm_v3_worktree_owner_session_id"] != child.ID || child.Metadata["swarm_v3_runtime_workspace_path"] != child.WorktreeRootPath {
		return pebblestore.SessionSnapshot{}, fail
	}
	if task.SourceWorkspace.Path != "" && child.Metadata["swarm_v3_source_workspace_path"] != task.SourceWorkspace.Path {
		return pebblestore.SessionSnapshot{}, fail
	}
	parent, found, err := s.sessions.GetSession(generation.ParentSessionID)
	if err != nil || !found || parent.AccountScopeID != p.AccountScopeID || parent.UserID != p.UserID || child.AccountScopeID != p.AccountScopeID || child.UserID != p.UserID {
		return pebblestore.SessionSnapshot{}, fail
	}
	if err := verifyProjectTaskSession(task, parent, p.AccountScopeID); err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	task.EnsureTaskAttempts()
	attempt := task.ActiveAttempt()
	if task.Archived || attempt == nil || attempt.ID != task.ActiveAttemptID || attempt.SessionID != parent.ID {
		return pebblestore.SessionSnapshot{}, fail
	}
	for _, key := range []string{"project_id", "task_id", "task_attempt_id"} {
		childValue, childOK := child.Metadata[key].(string)
		parentValue, parentOK := parent.Metadata[key].(string)
		if !childOK || !parentOK || childValue == "" || childValue != parentValue {
			return pebblestore.SessionSnapshot{}, fail
		}
	}
	return parent, nil
}

func selectedExecution(snapshot pebblestore.SessionSnapshot) (tool.ManagedExecutionBinding, error) {
	var binding tool.ManagedExecutionBinding
	raw, err := json.Marshal(snapshot.Metadata[tool.ManagedExecutionMetadataKey])
	if err != nil || json.Unmarshal(raw, &binding) != nil || binding.WorkspaceID == "" || binding.LeaseID == "" {
		return binding, errors.New("managed execution selection unavailable; local fallback forbidden")
	}
	return binding, nil
}

// validateDelegatedParentReceipt is repeated by ValidateTaskEnvironmentLease at
// the provider boundary. Revoking or replacing the parent selection invalidates
// its delegated consumer even when that consumer receipt has not yet expired.
func (s *Server) validateDelegatedParentReceipt(ctx context.Context, lease environments.DeploymentLease, task *pebblestore.ProjectTaskRecord, p identity.Principal) error {
	child, found, err := s.sessions.GetSession(lease.ConsumerID)
	if err != nil || !found {
		return errors.New("managed child unavailable")
	}
	parent, err := s.delegatedExecutionParent(p, task, child)
	if err != nil {
		return err
	}
	selection, err := selectedExecution(parent)
	if err != nil {
		return err
	}
	b := lease.TaskBinding
	if b.ParentLeaseID == "" || selection.LeaseID != b.ParentLeaseID || selection.WorkspaceID != lease.PreparedSource.WorkspaceID || s.deployments == nil {
		return errors.New("managed parent selection changed")
	}
	reader, ok := s.deployments.(interface {
		GetLease(string, string, string) (environments.DeploymentLease, bool, error)
	})
	if !ok {
		return errors.New("managed receipt service unavailable")
	}
	root, found, err := reader.GetLease(p.AccountScopeID, selection.WorkspaceID, selection.LeaseID)
	if err != nil || !found || root.ID != b.ParentLeaseID || root.AccountScopeID != p.AccountScopeID || root.WorkspaceID != selection.WorkspaceID || !root.IsHeld(time.Now().UnixMilli()) || root.ConsumerType != environments.ConsumerTypeSession || root.ConsumerID != parent.ID || root.TaskBinding == nil || root.TaskBinding.ParentLeaseID != "" || root.PreparedSource == nil || *root.PreparedSource != *lease.PreparedSource {
		return errors.New("managed parent receipt unavailable or expired")
	}
	expected := *b
	expected.ParentLeaseID = ""
	if *root.TaskBinding != expected {
		return errors.New("managed parent task binding changed")
	}
	return s.ValidateTaskEnvironmentLease(ctx, root)
}

// BindDelegatedTaskExecution runs only from the trusted Task launch path, after
// lineage creation and before the child run. It grants a receipt, not management.
func (s *Server) BindDelegatedTaskExecution(ctx context.Context, parentID, childID string) error {
	parent, found, err := s.sessions.GetSession(parentID)
	if err != nil || !found {
		return errors.New("managed parent unavailable")
	}
	selection, err := selectedExecution(parent)
	if err != nil {
		return err
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID}
	service, ok := s.deployments.(delegatedExecutionLifecycleService)
	if !ok {
		return errors.New("managed execution handoff service unavailable")
	}
	root, found, err := service.GetLease(p.AccountScopeID, selection.WorkspaceID, selection.LeaseID)
	if err != nil || !found || root.ID != selection.LeaseID || root.AccountScopeID != p.AccountScopeID || root.WorkspaceID != selection.WorkspaceID || root.TaskBinding == nil || root.TaskBinding.ParentLeaseID != "" || root.PreparedSource == nil || root.ConsumerType != environments.ConsumerTypeSession || root.ConsumerID != parent.ID || !root.IsHeld(time.Now().UnixMilli()) {
		return errors.New("managed parent receipt invalid")
	}
	if err := s.ValidateTaskEnvironmentLease(ctx, root); err != nil {
		return err
	}
	task, err := s.authorizeTaskEnvironment(p, "", root.TaskBinding.ProjectID, root.TaskBinding.TaskID)
	if err != nil {
		return err
	}
	if err := s.validateTaskExecutionConsumer(p, task, childID); err != nil {
		return err
	}
	child, found, err := s.sessions.GetSession(childID)
	if err != nil || !found {
		return errors.New("managed child unavailable")
	}
	admittedParent, err := s.delegatedExecutionParent(p, task, child)
	if err != nil || admittedParent.ID != parentID {
		return errors.New("managed handoff parent mismatch")
	}
	binding := *root.TaskBinding
	binding.ParentLeaseID = root.ID
	ttl := root.ExpiresAt - time.Now().UnixMilli()
	if ttl <= 0 {
		return errors.New("managed parent receipt expired")
	}
	receipt, err := service.AcquirePreparedLease(ctx, lifecycle.AcquirePreparedLeaseRequest{Source: *root.PreparedSource, Binding: &binding, Attribution: environments.OperationAttribution{Actor: p.UserID, SessionID: childID}, TTLMillis: ttl})
	if err != nil {
		return err
	}
	return s.bindOrReleaseTaskExecution(ctx, p, task, childID, receipt)
}
