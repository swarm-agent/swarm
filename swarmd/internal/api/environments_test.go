package api

import (
	"context"

	"errors"

	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/environments/provider"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type testMockConnectionStore struct {
	conns map[string]environments.Connection
}

func (m *testMockConnectionStore) Get(accountScopeID, workspaceID, connectionID string) (environments.Connection, bool, error) {
	c, ok := m.conns[accountScopeID+":"+workspaceID+":"+connectionID]
	return c, ok, nil
}

func (m *testMockConnectionStore) List(accountScopeID, workspaceID string, limit int) ([]environments.Connection, error) {
	var res []environments.Connection
	for _, c := range m.conns {
		if c.AccountScopeID == accountScopeID && c.WorkspaceID == workspaceID {
			res = append(res, c)
		}
	}
	return res, nil
}

func (m *testMockConnectionStore) Save(conn environments.Connection) (environments.Connection, error) {
	key := conn.AccountScopeID + ":" + conn.WorkspaceID + ":" + conn.ID
	m.conns[key] = conn
	return conn, nil
}

func (m *testMockConnectionStore) Delete(accountScopeID, workspaceID, connectionID string) (bool, error) {
	key := accountScopeID + ":" + workspaceID + ":" + connectionID
	if _, ok := m.conns[key]; ok {
		delete(m.conns, key)
		return true, nil
	}
	return false, nil
}

type testMockEnvironmentStore struct {
	envs map[string]environments.Environment
}

func (m *testMockEnvironmentStore) Get(accountScopeID, workspaceID, environmentID string) (environments.Environment, bool, error) {
	e, ok := m.envs[accountScopeID+":"+workspaceID+":"+environmentID]
	return e, ok, nil
}

func (m *testMockEnvironmentStore) List(accountScopeID, workspaceID string, limit int) ([]environments.Environment, error) {
	var res []environments.Environment
	for _, e := range m.envs {
		if e.AccountScopeID == accountScopeID && e.WorkspaceID == workspaceID {
			res = append(res, e)
		}
	}
	return res, nil
}

func (m *testMockEnvironmentStore) Save(env environments.Environment) (environments.Environment, error) {
	key := env.AccountScopeID + ":" + env.WorkspaceID + ":" + env.ID
	m.envs[key] = env
	return env, nil
}

func (m *testMockEnvironmentStore) Delete(accountScopeID, workspaceID, environmentID string) (bool, error) {
	key := accountScopeID + ":" + workspaceID + ":" + environmentID
	if _, ok := m.envs[key]; ok {
		delete(m.envs, key)
		return true, nil
	}
	return false, nil
}

type testMockDeploymentManager struct {
	deps   map[string]environments.Deployment
	leases map[string]environments.DeploymentLease
}

func (m *testMockDeploymentManager) EnsureDeployment(ctx context.Context, req lifecycle.EnsureDeploymentRequest) (*lifecycle.EnsureDeploymentResult, error) {
	dep := environments.Deployment{
		ID:             "dep-1",
		AccountScopeID: req.AccountScopeID,
		WorkspaceID:    req.WorkspaceID,
		EnvironmentID:  req.EnvironmentID,
		ConnectionID:   req.ConnectionID,
		Name:           req.DeploymentName,
		Status:         environments.DeploymentStatusRunning,
		Health:         environments.HealthStatusHealthy,
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cont-12345",
			Endpoint:    "http://127.0.0.1:18080",
		},
	}
	lease := environments.DeploymentLease{
		ID:             "lease-1",
		AccountScopeID: req.AccountScopeID,
		WorkspaceID:    req.WorkspaceID,
		DeploymentID:   dep.ID,
		EnvironmentID:  req.EnvironmentID,
		ConsumerType:   req.ConsumerType,
		ConsumerID:     req.ConsumerID,
		Active:         true,
	}
	m.deps[dep.ID] = dep
	m.leases[dep.ID] = lease
	return &lifecycle.EnsureDeploymentResult{Deployment: dep, Lease: lease, Reused: false}, nil
}

func (m *testMockDeploymentManager) DeployDeployment(ctx context.Context, req lifecycle.DeployDeploymentRequest) (*lifecycle.DeployDeploymentResult, error) {
	return nil, nil
}

func (m *testMockDeploymentManager) ReleaseDeployment(ctx context.Context, req lifecycle.ReleaseDeploymentRequest) (*lifecycle.ReleaseDeploymentResult, error) {
	for k, l := range m.leases {
		if l.ID == req.LeaseID {
			l.Active = false
			l.ReleaseReason = req.Reason
			m.leases[k] = l
			dep := m.deps[l.DeploymentID]
			return &lifecycle.ReleaseDeploymentResult{Deployment: dep, Lease: l}, nil
		}
	}
	return nil, errors.New("lease not found")
}

func (m *testMockDeploymentManager) DestroyDeployment(ctx context.Context, req lifecycle.DestroyDeploymentRequest) error {
	delete(m.deps, req.DeploymentID)
	delete(m.leases, req.DeploymentID)
	return nil
}

func (m *testMockDeploymentManager) StopDeployment(ctx context.Context, accountScopeID, workspaceID, deploymentID string) error {
	if d, ok := m.deps[deploymentID]; ok {
		d.Status = environments.DeploymentStatusStopped
		m.deps[deploymentID] = d
		return nil
	}
	return errors.New("deployment not found")
}

func (m *testMockDeploymentManager) StartDeployment(ctx context.Context, accountScopeID, workspaceID, deploymentID string) error {
	if d, ok := m.deps[deploymentID]; ok {
		d.Status = environments.DeploymentStatusRunning
		m.deps[deploymentID] = d
		return nil
	}
	return errors.New("deployment not found")
}

func (m *testMockDeploymentManager) InspectDeployment(ctx context.Context, accountScopeID, workspaceID, deploymentID string) (*environments.Deployment, error) {
	d, ok := m.deps[deploymentID]
	if !ok {
		return nil, errors.New("deployment not found")
	}
	return &d, nil
}

func (m *testMockDeploymentManager) ResolveAccess(ctx context.Context, accountScopeID, workspaceID, deploymentID string) (*provider.DeploymentAccess, error) {
	return nil, nil
}

func (m *testMockDeploymentManager) Exec(ctx context.Context, accountScopeID, workspaceID, deploymentID string, req provider.ExecRequest) (*provider.ExecResult, error) {
	return nil, nil
}

func (m *testMockDeploymentManager) GetDeployment(accountScopeID, workspaceID, deploymentID string) (environments.Deployment, bool, error) {
	d, ok := m.deps[deploymentID]
	return d, ok, nil
}

func (m *testMockDeploymentManager) ListDeployments(accountScopeID, workspaceID string, limit int) ([]environments.Deployment, error) {
	var res []environments.Deployment
	for _, d := range m.deps {
		if d.AccountScopeID == accountScopeID && d.WorkspaceID == workspaceID {
			res = append(res, d)
		}
	}
	return res, nil
}

func (m *testMockDeploymentManager) ListDeploymentsByEnvironment(accountScopeID, workspaceID, environmentID string, limit int) ([]environments.Deployment, error) {
	var res []environments.Deployment
	for _, d := range m.deps {
		if d.AccountScopeID == accountScopeID && d.WorkspaceID == workspaceID && d.EnvironmentID == environmentID {
			res = append(res, d)
		}
	}
	return res, nil
}

func (m *testMockDeploymentManager) GetActiveLease(accountScopeID, workspaceID, deploymentID string) (environments.DeploymentLease, bool, error) {
	l, ok := m.leases[deploymentID]
	if ok && l.Active {
		return l, true, nil
	}
	return environments.DeploymentLease{}, false, nil
}

func (m *testMockDeploymentManager) Submit(ctx context.Context, req lifecycle.SubmitOperationRequest) (*environments.EnvironmentOperation, error) {
	opID := "op-" + req.Action + "-1"
	depID := req.DeploymentID
	if req.Action == "ensure" || req.Action == "deploy" {
		depID = "dep-1"
		dep := environments.Deployment{
			ID:             depID,
			AccountScopeID: req.AccountScopeID,
			WorkspaceID:    req.WorkspaceID,
			EnvironmentID:  req.EnvironmentID,
			ConnectionID:   req.ConnectionID,
			Name:           req.DeploymentName,
			Status:         environments.DeploymentStatusRunning,
			Health:         environments.HealthStatusHealthy,
		}
		lease := environments.DeploymentLease{
			ID:             "lease-1",
			AccountScopeID: req.AccountScopeID,
			WorkspaceID:    req.WorkspaceID,
			DeploymentID:   depID,
			EnvironmentID:  req.EnvironmentID,
			ConsumerType:   req.ConsumerType,
			ConsumerID:     req.ConsumerID,
			Active:         true,
		}
		m.deps[depID] = dep
		m.leases[depID] = lease
	} else if req.Action == "stop" {
		if d, ok := m.deps[depID]; ok {
			d.Status = environments.DeploymentStatusStopped
			m.deps[depID] = d
		}
	} else if req.Action == "start" {
		if d, ok := m.deps[depID]; ok {
			d.Status = environments.DeploymentStatusRunning
			m.deps[depID] = d
		}
	} else if req.Action == "release" {
		if l, ok := m.leases[depID]; ok {
			l.Active = false
			m.leases[depID] = l
		}
	} else if req.Action == "destroy" {
		delete(m.deps, depID)
		delete(m.leases, depID)
	}

	op := &environments.EnvironmentOperation{
		OperationID:    opID,
		AccountScopeID: req.AccountScopeID,
		WorkspaceID:    req.WorkspaceID,
		Action:         req.Action,
		EnvironmentID:  req.EnvironmentID,
		DeploymentID:   depID,
		LeaseID:        req.LeaseID,
		Attribution:    req.Attribution,
		Status:         environments.OperationStatusRunning,
		CreatedAt:      time.Now().UnixMilli(),
		Deadline:       time.Now().UnixMilli() + 300000,
	}
	return op, nil
}

func (m *testMockDeploymentManager) Get(ctx context.Context, accountScopeID, workspaceID, operationID string) (environments.EnvironmentOperation, bool, error) {
	return environments.EnvironmentOperation{
		OperationID:    operationID,
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Status:         environments.OperationStatusRunning,
	}, true, nil
}

func (m *testMockDeploymentManager) History(ctx context.Context, q environments.OperationHistoryQuery) (environments.OperationHistoryPage, error) {
	return environments.OperationHistoryPage{
		Operations: []environments.EnvironmentOperation{
			{OperationID: "op-1", AccountScopeID: q.AccountScopeID, WorkspaceID: q.WorkspaceID, Action: "exec", Status: environments.OperationStatusSucceeded},
		},
		DailyTotals: []environments.DailyOperationCounts{
			{Date: "2026-09-21", TotalOps: 1, Succeeded: 1},
		},
		Summary: environments.EnvironmentSummary{
			AccountScopeID: q.AccountScopeID,
			WorkspaceID:    q.WorkspaceID,
			TotalOps:       1,
			SucceededOps:   1,
		},
	}, nil
}

func (m *testMockDeploymentManager) Summary(ctx context.Context, accountScopeID, workspaceID string) (environments.EnvironmentSummary, error) {
	return environments.EnvironmentSummary{
		AccountScopeID:    accountScopeID,
		WorkspaceID:       workspaceID,
		ActiveDeployments: len(m.deps),
		TotalOps:          1,
	}, nil
}

func (m *testMockDeploymentManager) Cancel(ctx context.Context, req lifecycle.CancelOperationRequest) (*environments.EnvironmentOperation, error) {
	return &environments.EnvironmentOperation{
		OperationID:    req.OperationID,
		AccountScopeID: req.AccountScopeID,
		WorkspaceID:    req.WorkspaceID,
		Status:         environments.OperationStatusCancelled,
	}, nil
}

func (m *testMockDeploymentManager) CancelOwner(ctx context.Context, req lifecycle.CancelOwnerRequest) (int, error) {
	return 1, nil
}

type testMockWorkspaceSettingsStore struct {
	settings environments.WorkspaceSettings
}

func (m *testMockWorkspaceSettingsStore) GetWorkspaceSettings(accountScopeID, workspaceID string) (environments.WorkspaceSettings, bool, error) {
	return m.settings, true, nil
}

func (m *testMockWorkspaceSettingsStore) UpdateWorkspaceSettings(accountScopeID, workspaceID string, defaultTestEnvironmentID, defaultConnectionID *string) (pebblestore.WorkspaceEntry, error) {
	if defaultTestEnvironmentID != nil {
		m.settings.DefaultTestEnvironmentID = *defaultTestEnvironmentID
	}
	if defaultConnectionID != nil {
		m.settings.DefaultConnectionID = *defaultConnectionID
	}
	return pebblestore.WorkspaceEntry{}, nil
}

type testMockProviderRegistry struct {
	providers map[environments.ConnectionKind]provider.DeploymentProvider
}

func (m *testMockProviderRegistry) Get(kind environments.ConnectionKind) (provider.DeploymentProvider, bool) {
	p, ok := m.providers[kind]
	return p, ok
}

type testMockProvider struct {
	kind environments.ConnectionKind
}

func (p *testMockProvider) Kind() environments.ConnectionKind {
	return p.kind
}

func (p *testMockProvider) ValidateConnection(ctx context.Context, conn *environments.Connection) error {
	return nil
}

func (p *testMockProvider) Capabilities(ctx context.Context, conn *environments.Connection) (environments.ConnectionCapabilities, error) {
	return environments.ConnectionCapabilities{
		SupportsDocker:      true,
		SupportsDirectMount: true,
		RemoteOS:            "linux",
	}, nil
}

func (p *testMockProvider) Start(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	return nil
}

func (p *testMockProvider) Stop(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	return nil
}

func (p *testMockProvider) ResolveAccess(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) (*provider.DeploymentAccess, error) {
	return nil, nil
}

func (p *testMockProvider) Exec(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
	return nil, nil
}
