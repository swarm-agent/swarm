package api

import (
	"context"
	"fmt"

	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
)

type taskReceiptBoundary struct {
	manageDeploymentLifecycleService
	lease     environments.DeploymentLease
	submitted int
}

func (f *taskReceiptBoundary) GetLease(string, string, string) (environments.DeploymentLease, bool, error) {
	return f.lease, true, nil
}
func (f *taskReceiptBoundary) Submit(_ context.Context, req lifecycle.SubmitOperationRequest) (*environments.EnvironmentOperation, error) {
	f.submitted++
	return &environments.EnvironmentOperation{Action: req.Action, LeaseID: req.LeaseID}, nil
}

type taskAttachmentLifecycleFixture struct {
	manageDeploymentLifecycleService
	dep     environments.Deployment
	leases  map[string]environments.DeploymentLease
	effects int
}

func (f *taskAttachmentLifecycleFixture) GetDeployment(string, string, string) (environments.Deployment, bool, error) {
	return f.dep, true, nil
}
func (f *taskAttachmentLifecycleFixture) GetLease(_, _, id string) (environments.DeploymentLease, bool, error) {
	l, ok := f.leases[id]
	return l, ok, nil
}
func (f *taskAttachmentLifecycleFixture) AcquirePreparedLease(_ context.Context, req lifecycle.AcquirePreparedLeaseRequest) (environments.DeploymentLease, error) {
	id := fmt.Sprintf("receipt-%d", len(f.leases)+1)
	kind, consumer := environments.ConsumerTypeCustom, req.Attribution.Actor
	if req.Attribution.SessionID != "" {
		kind, consumer = environments.ConsumerTypeSession, req.Attribution.SessionID
	}
	l := environments.DeploymentLease{ID: id, AccountScopeID: req.Source.AccountScopeID, WorkspaceID: req.Source.WorkspaceID, DeploymentID: req.Source.DeploymentID, EnvironmentID: req.Source.EnvironmentID, ConsumerType: kind, ConsumerID: consumer, PreparedSource: &req.Source, TaskBinding: req.Binding, Active: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	f.leases[id] = l
	return l, nil
}
func (f *taskAttachmentLifecycleFixture) Submit(_ context.Context, req lifecycle.SubmitOperationRequest) (*environments.EnvironmentOperation, error) {
	f.effects++
	if req.Action == "release" {
		l := f.leases[req.LeaseID]
		l.Active = false
		f.leases[l.ID] = l
	}
	return &environments.EnvironmentOperation{Action: req.Action, LeaseID: req.LeaseID}, nil
}

type taskAttachmentDefinitionFixture struct {
	manageEnvironmentStore
	env environments.Environment
}

func (f *taskAttachmentDefinitionFixture) Get(string, string, string) (environments.Environment, bool, error) {
	return f.env, true, nil
}

type taskPreparationFixture struct {
	manageDeploymentLifecycleService
	op      environments.EnvironmentOperation
	lease   environments.DeploymentLease
	effects int
	request lifecycle.SubmitOperationRequest
}

func (f *taskPreparationFixture) Get(context.Context, string, string, string) (environments.EnvironmentOperation, bool, error) {
	return f.op, true, nil
}
func (f *taskPreparationFixture) GetLease(string, string, string) (environments.DeploymentLease, bool, error) {
	return f.lease, true, nil
}
func (f *taskPreparationFixture) Submit(_ context.Context, r lifecycle.SubmitOperationRequest) (*environments.EnvironmentOperation, error) {
	f.effects++
	f.request = r
	return &environments.EnvironmentOperation{Action: r.Action, LeaseID: r.LeaseID}, nil
}
