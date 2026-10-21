package run

import (
	"context"

	"swarm-refactor/swarmtui/pkg/environments"

	"swarm/packages/swarmd/internal/environments/lifecycle"
)

type mockConnectionStore struct {
	connections map[string]environments.Connection
}

func (m *mockConnectionStore) Get(accountScopeID, workspaceID, connectionID string) (environments.Connection, bool, error) {
	conn, ok := m.connections[connectionID]
	return conn, ok, nil
}

func (m *mockConnectionStore) List(accountScopeID, workspaceID string, limit int) ([]environments.Connection, error) {
	var list []environments.Connection
	for _, c := range m.connections {
		list = append(list, c)
		if limit > 0 && len(list) >= limit {
			break
		}
	}
	return list, nil
}

type mockEnvironmentStore struct {
	envs []environments.Environment
}

func (m *mockEnvironmentStore) Get(accountScopeID, workspaceID, environmentID string) (environments.Environment, bool, error) {
	for _, e := range m.envs {
		if e.ID == environmentID {
			return e, true, nil
		}
	}
	return environments.Environment{}, false, nil
}

func (m *mockEnvironmentStore) List(accountScopeID, workspaceID string, limit int) ([]environments.Environment, error) {
	if limit > 0 && len(m.envs) > limit {
		return m.envs[:limit], nil
	}
	return m.envs, nil
}

type mockDeploymentService struct {
	resolveFn func(ctx context.Context, accountScopeID, workspaceID string, explicitConnectionID string, env *environments.Environment) (*environments.Connection, error)
	deps      []environments.Deployment
	leases    map[string]environments.DeploymentLease
}

func (m *mockDeploymentService) ResolveConnection(ctx context.Context, accountScopeID, workspaceID string, explicitConnectionID string, env *environments.Environment) (*environments.Connection, error) {
	if m.resolveFn != nil {
		return m.resolveFn(ctx, accountScopeID, workspaceID, explicitConnectionID, env)
	}
	return nil, lifecycle.ErrNoConnectionResolved
}

func (m *mockDeploymentService) ListDeployments(accountScopeID, workspaceID string, limit int) ([]environments.Deployment, error) {
	if limit > 0 && len(m.deps) > limit {
		return m.deps[:limit], nil
	}
	return m.deps, nil
}

func (m *mockDeploymentService) GetActiveLease(accountScopeID, workspaceID, deploymentID string) (environments.DeploymentLease, bool, error) {
	if m.leases != nil {
		l, ok := m.leases[deploymentID]
		return l, ok, nil
	}
	return environments.DeploymentLease{}, false, nil
}

type mockWorkspaceSettingsStore struct {
	settings map[string]environments.WorkspaceSettings
}

func (m *mockWorkspaceSettingsStore) GetWorkspaceSettings(accountScopeID, workspaceID string) (environments.WorkspaceSettings, bool, error) {
	if m.settings != nil {
		s, ok := m.settings[workspaceID]
		return s, ok, nil
	}
	return environments.WorkspaceSettings{}, false, nil
}

type testCtxMockProvider struct {
	kind environments.ConnectionKind
}

func (m *testCtxMockProvider) Kind() environments.ConnectionKind { return m.kind }

func (m *testCtxMockProvider) Capabilities(ctx context.Context, conn *environments.Connection) (environments.ConnectionCapabilities, error) {
	return environments.ConnectionCapabilities{SupportsDocker: true}, nil
}

func (m *testCtxMockProvider) Start(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	return nil
}
func (m *testCtxMockProvider) Stop(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	return nil
}
