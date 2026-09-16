package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type testMockConnectionStore struct {
	conns map[string]environments.Connection
}

func newTestMockConnectionStore() *testMockConnectionStore {
	return &testMockConnectionStore{conns: make(map[string]environments.Connection)}
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

func newTestMockEnvironmentStore() *testMockEnvironmentStore {
	return &testMockEnvironmentStore{envs: make(map[string]environments.Environment)}
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

func newTestMockDeploymentManager() *testMockDeploymentManager {
	return &testMockDeploymentManager{
		deps:   make(map[string]environments.Deployment),
		leases: make(map[string]environments.DeploymentLease),
	}
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

func (p *testMockProvider) Deploy(ctx context.Context, req provider.DeployRequest) (*provider.DeployResult, error) {
	return nil, nil
}

func (p *testMockProvider) Inspect(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) (*provider.InspectResult, error) {
	return nil, nil
}

func (p *testMockProvider) Start(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	return nil
}

func (p *testMockProvider) Stop(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	return nil
}

func (p *testMockProvider) Destroy(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	return nil
}

func (p *testMockProvider) ResolveAccess(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) (*provider.DeploymentAccess, error) {
	return nil, nil
}

func (p *testMockProvider) Exec(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
	return nil, nil
}

func setupTestServer() (*Server, *testMockConnectionStore, *testMockEnvironmentStore, *testMockDeploymentManager, *testMockWorkspaceSettingsStore) {
	connStore := newTestMockConnectionStore()
	envStore := newTestMockEnvironmentStore()
	depMgr := newTestMockDeploymentManager()
	wsSettings := &testMockWorkspaceSettingsStore{
		settings: environments.WorkspaceSettings{
			WorkspaceID:    "ws-1",
			AccountScopeID: "acc-1",
		},
	}
	provReg := &testMockProviderRegistry{
		providers: map[environments.ConnectionKind]provider.DeploymentProvider{
			environments.ConnectionKindLocalDocker: &testMockProvider{kind: environments.ConnectionKindLocalDocker},
			environments.ConnectionKindSSH:         &testMockProvider{kind: environments.ConnectionKindSSH},
		},
	}

	srv := &Server{}
	srv.SetEnvironmentServices(connStore, envStore, depMgr, wsSettings, provReg)
	return srv, connStore, envStore, depMgr, wsSettings
}

func authedRequest(method, url string, body any) *http.Request {
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	ctx := identity.ContextWithPrincipal(req.Context(), identity.Principal{
		Type:           identity.PrincipalTypeUser,
		AccountScopeID: "acc-1",
		UserID:         "user-1",
	})
	return req.WithContext(ctx)
}

func TestConnectionsAPI(t *testing.T) {
	srv, _, _, _, _ := setupTestServer()

	// 1. List initially empty
	req := authedRequest(http.MethodGet, "/v1/connections?workspace_id=ws-1", nil)
	w := httptest.NewRecorder()
	srv.handleConnections(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	if count, ok := listResp["count"].(float64); !ok || count != 0 {
		t.Fatalf("expected 0 connections, got %v", listResp["count"])
	}

	// 2. Create local docker connection
	createReq := authedRequest(http.MethodPost, "/v1/connections", map[string]any{
		"action":       "create",
		"workspace_id": "ws-1",
		"name":         "Local Docker Daemon",
		"kind":         "local_docker",
		"socket_path":  "/var/run/docker.sock",
	})
	w = httptest.NewRecorder()
	srv.handleConnections(w, createReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on create, got %d: %s", w.Code, w.Body.String())
	}
	var createResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &createResp)
	connMap := createResp["connection"].(map[string]any)
	connID := connMap["id"].(string)
	if connID == "" {
		t.Fatal("expected non-empty connection ID")
	}

	// 3. Get connection by ID
	getReq := authedRequest(http.MethodGet, "/v1/connections?workspace_id=ws-1&id="+connID, nil)
	w = httptest.NewRecorder()
	srv.handleConnections(w, getReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on get, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Check connection
	checkReq := authedRequest(http.MethodPost, "/v1/connections", map[string]any{
		"action":       "check",
		"workspace_id": "ws-1",
		"id":           connID,
	})
	w = httptest.NewRecorder()
	srv.handleConnections(w, checkReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on check, got %d: %s", w.Code, w.Body.String())
	}
	var checkResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &checkResp)
	if checkResp["healthy"] != true {
		t.Fatalf("expected healthy true, got %v", checkResp)
	}

	// 5. Delete connection
	delReq := authedRequest(http.MethodPost, "/v1/connections", map[string]any{
		"action":       "delete",
		"workspace_id": "ws-1",
		"id":           connID,
	})
	w = httptest.NewRecorder()
	srv.handleConnections(w, delReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on delete, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEnvironmentsAPI(t *testing.T) {
	srv, _, _, _, _ := setupTestServer()

	// 1. Create environment
	createReq := authedRequest(http.MethodPost, "/v1/environments", map[string]any{
		"action":       "create",
		"workspace_id": "ws-1",
		"environment": map[string]any{
			"id":   "env-test-1",
			"name": "Integration Testbench",
			"mode": "deployable",
			"role": "testing",
			"container": map[string]any{
				"image": "golang:1.24",
			},
			"provisioning": map[string]any{
				"strategy": map[string]any{
					"kind": "local_mount",
					"local_mount": map[string]any{
						"container_path": "/workspace",
					},
				},
			},
			"deployment_policy": map[string]any{
				"reuse":            true,
				"max_instances":    1,
				"release_behavior": "restart",
			},
		},
	})
	w := httptest.NewRecorder()
	srv.handleEnvironments(w, createReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on env create, got %d: %s", w.Code, w.Body.String())
	}

	// 2. List environments
	listReq := authedRequest(http.MethodGet, "/v1/environments?workspace_id=ws-1", nil)
	w = httptest.NewRecorder()
	srv.handleEnvironments(w, listReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on env list, got %d: %s", w.Code, w.Body.String())
	}
	var listResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	if count, ok := listResp["count"].(float64); !ok || count != 1 {
		t.Fatalf("expected 1 environment, got %v", listResp["count"])
	}

	// 3. Set default test environment
	setDefaultReq := authedRequest(http.MethodPost, "/v1/environments", map[string]any{
		"action":                      "set_default_test_environment",
		"workspace_id":                "ws-1",
		"default_test_environment_id": "env-test-1",
	})
	w = httptest.NewRecorder()
	srv.handleEnvironments(w, setDefaultReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on set default, got %d: %s", w.Code, w.Body.String())
	}

	// Verify default is set in GET response
	listReq = authedRequest(http.MethodGet, "/v1/environments?workspace_id=ws-1", nil)
	w = httptest.NewRecorder()
	srv.handleEnvironments(w, listReq)
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	settingsMap := listResp["settings"].(map[string]any)
	if settingsMap["default_test_environment_id"] != "env-test-1" {
		t.Fatalf("expected default_test_environment_id env-test-1, got %v", settingsMap["default_test_environment_id"])
	}

	// 4. Delete environment
	delReq := authedRequest(http.MethodPost, "/v1/environments", map[string]any{
		"action":       "delete",
		"workspace_id": "ws-1",
		"id":           "env-test-1",
	})
	w = httptest.NewRecorder()
	srv.handleEnvironments(w, delReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on env delete, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI(t *testing.T) {
	srv, _, _, _, _ := setupTestServer()

	// 1. Ensure deployment
	ensureReq := authedRequest(http.MethodPost, "/v1/deployments", map[string]any{
		"action":          "ensure",
		"workspace_id":    "ws-1",
		"environment_id":  "env-test-1",
		"consumer_type":   "session",
		"consumer_id":     "sess-123",
		"deployment_name": "Test Deployment",
	})
	w := httptest.NewRecorder()
	srv.handleDeployments(w, ensureReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on ensure, got %d: %s", w.Code, w.Body.String())
	}
	var ensureResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &ensureResp)
	depMap := ensureResp["deployment"].(map[string]any)
	depID := depMap["id"].(string)

	// 2. List deployments
	listReq := authedRequest(http.MethodGet, "/v1/deployments?workspace_id=ws-1", nil)
	w = httptest.NewRecorder()
	srv.handleDeployments(w, listReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on list, got %d: %s", w.Code, w.Body.String())
	}
	var listResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	if count, ok := listResp["count"].(float64); !ok || count != 1 {
		t.Fatalf("expected 1 deployment, got %v", listResp["count"])
	}
	activeLeases := listResp["active_leases"].(map[string]any)
	if _, ok := activeLeases[depID]; !ok {
		t.Fatalf("expected active lease for %s", depID)
	}

	// 3. Stop deployment
	stopReq := authedRequest(http.MethodPost, "/v1/deployments", map[string]any{
		"action":        "stop",
		"workspace_id":  "ws-1",
		"deployment_id": depID,
	})
	w = httptest.NewRecorder()
	srv.handleDeployments(w, stopReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on stop, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Release deployment
	relReq := authedRequest(http.MethodPost, "/v1/deployments", map[string]any{
		"action":         "release",
		"workspace_id":   "ws-1",
		"deployment_id":  depID,
		"release_reason": "test complete",
	})
	w = httptest.NewRecorder()
	srv.handleDeployments(w, relReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on release, got %d: %s", w.Code, w.Body.String())
	}

	// 5. Destroy deployment
	destroyReq := authedRequest(http.MethodPost, "/v1/deployments", map[string]any{
		"action":        "destroy",
		"workspace_id":  "ws-1",
		"deployment_id": depID,
	})
	w = httptest.NewRecorder()
	srv.handleDeployments(w, destroyReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on destroy, got %d: %s", w.Code, w.Body.String())
	}
}
