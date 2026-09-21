package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// mockProvider implements provider.DeploymentProvider for lifecycle engine testing.
type mockProvider struct {
	mu           sync.Mutex
	kind         environments.ConnectionKind
	depStates    map[string]environments.DeploymentStatus
	deployCalls  int32
	startCalls   int32
	stopCalls    int32
	destroyCalls int32
	execCalls    int32
	accessCalls  int32

	deployErr  error
	startErr   error
	stopErr    error
	destroyErr error
	execErr    error
	inspectErr error

	deployFunc func(ctx context.Context, req provider.DeployRequest) (*provider.DeployResult, error)
}

func newMockProvider(kind environments.ConnectionKind) *mockProvider {
	return &mockProvider{
		kind:      kind,
		depStates: make(map[string]environments.DeploymentStatus),
	}
}

func (m *mockProvider) Kind() environments.ConnectionKind {
	return m.kind
}

func (m *mockProvider) ValidateConnection(ctx context.Context, conn *environments.Connection) error {
	return nil
}

func (m *mockProvider) Capabilities(ctx context.Context, conn *environments.Connection) (environments.ConnectionCapabilities, error) {
	return environments.ConnectionCapabilities{
		SupportsDocker:      true,
		SupportsDirectMount: true,
	}, nil
}

func (m *mockProvider) Deploy(ctx context.Context, req provider.DeployRequest) (*provider.DeployResult, error) {
	atomic.AddInt32(&m.deployCalls, 1)
	if m.deployErr != nil {
		return nil, m.deployErr
	}
	if m.deployFunc != nil {
		return m.deployFunc(ctx, req)
	}
	m.mu.Lock()
	if m.depStates == nil {
		m.depStates = make(map[string]environments.DeploymentStatus)
	}
	m.depStates[req.Deployment.ID] = environments.DeploymentStatusRunning
	m.mu.Unlock()
	return &provider.DeployResult{
		Runtime: environments.RuntimeMetadata{
			ContainerID:         "mock_ctr_" + req.Deployment.ID,
			ProviderResourceID:  "mock_res_" + req.Deployment.ID,
			Endpoint:            "http://127.0.0.1:18080",
			RemoteWorkspacePath: "/workspace",
			RuntimeIP:           "172.17.0.5",
			AssignedPorts: []environments.AssignedPort{
				{ContainerPort: 8080, HostPort: 18080, Protocol: "tcp", EndpointURL: "http://127.0.0.1:18080"},
			},
		},
		Health: environments.HealthStatusHealthy,
		Status: environments.DeploymentStatusRunning,
	}, nil
}

func (m *mockProvider) Inspect(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) (*provider.InspectResult, error) {
	if m.inspectErr != nil {
		return nil, m.inspectErr
	}
	m.mu.Lock()
	status := dep.Status
	if s, ok := m.depStates[dep.ID]; ok {
		status = s
	}
	m.mu.Unlock()
	return &provider.InspectResult{
		Status:  status,
		Health:  dep.Health,
		Runtime: dep.Runtime,
	}, nil
}

func (m *mockProvider) Start(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	atomic.AddInt32(&m.startCalls, 1)
	if m.startErr == nil {
		m.mu.Lock()
		if m.depStates == nil {
			m.depStates = make(map[string]environments.DeploymentStatus)
		}
		m.depStates[dep.ID] = environments.DeploymentStatusRunning
		m.mu.Unlock()
	}
	return m.startErr
}

func (m *mockProvider) Stop(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	atomic.AddInt32(&m.stopCalls, 1)
	if m.stopErr == nil {
		m.mu.Lock()
		if m.depStates == nil {
			m.depStates = make(map[string]environments.DeploymentStatus)
		}
		m.depStates[dep.ID] = environments.DeploymentStatusStopped
		m.mu.Unlock()
	}
	return m.stopErr
}

func (m *mockProvider) Destroy(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	atomic.AddInt32(&m.destroyCalls, 1)
	return m.destroyErr
}

func (m *mockProvider) ResolveAccess(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) (*provider.DeploymentAccess, error) {
	atomic.AddInt32(&m.accessCalls, 1)
	return &provider.DeploymentAccess{
		PrimaryEndpoint:     dep.Runtime.Endpoint,
		RemoteWorkspacePath: dep.Runtime.RemoteWorkspacePath,
		MappedPorts:         dep.Runtime.AssignedPorts,
		ExecSupported:       true,
		ContainerID:         dep.Runtime.ContainerID,
	}, nil
}

func (m *mockProvider) Exec(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
	atomic.AddInt32(&m.execCalls, 1)
	if m.execErr != nil {
		return nil, m.execErr
	}
	return &provider.ExecResult{
		ExitCode: 0,
		Stdout:   "mock output",
	}, nil
}

// testHarness initializes real Pebble stores, a mock provider, and a DeploymentManager.
type testHarness struct {
	store        *pebblestore.Store
	connections  *pebblestore.ConnectionStore
	environments *pebblestore.EnvironmentStore
	deployments  *pebblestore.DeploymentStore
	workspaces   *pebblestore.WorkspaceStore
	mockProv     *mockProvider
	manager      *DeploymentManager
}

func setupTestHarness(t *testing.T) *testHarness {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "lifecycle_test.pebble")
	store, err := pebblestore.Open(dbPath)
	if err != nil {
		t.Fatalf("open pebble store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	cs := pebblestore.NewConnectionStore(store)
	es := pebblestore.NewEnvironmentStore(store)
	ds := pebblestore.NewDeploymentStore(store)
	ws := pebblestore.NewWorkspaceStore(store)

	mockProv := newMockProvider(environments.ConnectionKindLocalDocker)
	registry := provider.NewRegistry()
	registry.Register(mockProv)

	manager := NewDeploymentManager(cs, es, ds, ws, registry)

	return &testHarness{
		store:        store,
		connections:  cs,
		environments: es,
		deployments:  ds,
		workspaces:   ws,
		mockProv:     mockProv,
		manager:      manager,
	}
}

func createTestConnection(t *testing.T, cs *pebblestore.ConnectionStore, accountScopeID, workspaceID, connID, name string) environments.Connection {
	t.Helper()
	conn := environments.Connection{
		ID:             connID,
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Name:           name,
		Kind:           environments.ConnectionKindLocalDocker,
		Capabilities: environments.ConnectionCapabilities{
			SupportsDocker:      true,
			SupportsDirectMount: true,
		},
		LocalDocker: &environments.LocalDockerConfig{
			SocketPath: "/var/run/docker.sock",
		},
	}
	saved, err := cs.Save(conn)
	if err != nil {
		t.Fatalf("save test connection %q: %v", connID, err)
	}
	return saved
}

func createTestEnvironment(t *testing.T, es *pebblestore.EnvironmentStore, accountScopeID, workspaceID, envID, preferredConnID string, reuse bool, maxInstances int, releaseBehavior environments.ReleaseBehavior) environments.Environment {
	t.Helper()
	env := environments.Environment{
		ID:                    envID,
		AccountScopeID:        accountScopeID,
		WorkspaceID:           workspaceID,
		Name:                  "Test Environment " + envID,
		Mode:                  environments.EnvironmentModeDeployable,
		Role:                  environments.EnvironmentRoleTesting,
		PreferredConnectionID: preferredConnID,
		Container: environments.ContainerDefinition{
			Image: "golang:1.26-alpine",
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindLocalMount,
				LocalMount: &environments.LocalMountConfig{
					HostPath:      "/workspace/repo",
					ContainerPath: "/workspace",
				},
			},
		},
		DeploymentPolicy: environments.DeploymentPolicy{
			Reuse:           reuse,
			MaxInstances:    maxInstances,
			ReleaseBehavior: releaseBehavior,
		},
	}
	saved, err := es.Save(env)
	if err != nil {
		t.Fatalf("save test environment %q: %v", envID, err)
	}
	return saved
}

// TestDeploymentManager_ConnectionPrecedence tests the 3-tier connection resolution:
// (1) explicitly supplied connection_id
// (2) Environment preferred_connection_id
// (3) workspace default_connection_id
func TestDeploymentManager_ConnectionPrecedence(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "acc-prec"
	workspaceID := "ws-prec"

	// Create workspace entry
	wsEntry, err := h.workspaces.AddForAccount(accountScope, "/workspaces/repo", "Repo")
	if err != nil {
		t.Fatalf("add workspace entry: %v", err)
	}
	workspaceID = wsEntry.WorkspaceID

	// 1. Create 3 connections: explicit, preferred, default
	connExplicit := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-explicit", "Explicit Conn")
	connPreferred := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-preferred", "Preferred Conn")
	connDefault := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-default", "Default Conn")

	// Set workspace default connection in workspace settings
	defaultEnv := ""
	defaultConn := connDefault.ID
	_, err = h.workspaces.UpdateWorkspaceSettings(accountScope, wsEntry.WorkspaceID, &defaultEnv, &defaultConn)
	if err != nil {
		t.Fatalf("update workspace settings: %v", err)
	}

	// Create environment with preferred connection
	envWithPref := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-pref", connPreferred.ID, true, 2, environments.ReleaseBehaviorNone)

	// Create environment with NO preferred connection
	envNoPref := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-nopref", "", true, 2, environments.ReleaseBehaviorNone)

	// Case 1: Tier 1 - Explicit connection overrides both preferred and default
	resolved1, err := h.manager.ResolveConnection(ctx, accountScope, workspaceID, connExplicit.ID, &envWithPref)
	if err != nil {
		t.Fatalf("Tier 1 resolution failed: %v", err)
	}
	if resolved1.ID != connExplicit.ID {
		t.Errorf("Tier 1: expected explicit %q, got %q", connExplicit.ID, resolved1.ID)
	}

	// Case 2: Tier 2 - Empty explicit connection falls back to Environment preferred_connection_id
	resolved2, err := h.manager.ResolveConnection(ctx, accountScope, workspaceID, "", &envWithPref)
	if err != nil {
		t.Fatalf("Tier 2 resolution failed: %v", err)
	}
	if resolved2.ID != connPreferred.ID {
		t.Errorf("Tier 2: expected preferred %q, got %q", connPreferred.ID, resolved2.ID)
	}

	// Case 3: Tier 3 - Empty explicit and empty preferred falls back to workspace default_connection_id
	resolved3, err := h.manager.ResolveConnection(ctx, accountScope, workspaceID, "", &envNoPref)
	if err != nil {
		t.Fatalf("Tier 3 resolution failed: %v", err)
	}
	if resolved3.ID != connDefault.ID {
		t.Errorf("Tier 3: expected default %q, got %q", connDefault.ID, resolved3.ID)
	}

	// Case 4: None resolved error when default is cleared and env has no preferred
	emptyConn := ""
	_, err = h.workspaces.UpdateWorkspaceSettings(accountScope, wsEntry.WorkspaceID, &defaultEnv, &emptyConn)
	if err != nil {
		t.Fatalf("clear workspace default: %v", err)
	}
	_, err = h.manager.ResolveConnection(ctx, accountScope, workspaceID, "", &envNoPref)
	if !errors.Is(err, ErrNoConnectionResolved) {
		t.Errorf("expected ErrNoConnectionResolved, got: %v", err)
	}

	// Case 5: Non-existent explicit connection returns ErrConnectionNotFound
	_, err = h.manager.ResolveConnection(ctx, accountScope, workspaceID, "non-existent-conn", &envWithPref)
	if !errors.Is(err, ErrConnectionNotFound) {
		t.Errorf("expected ErrConnectionNotFound for missing explicit conn, got: %v", err)
	}
}

// TestDeploymentManager_AtomicLeaseAndReuse tests:
// 1. Initial ensure_deployment creates a new deployment and acquires an exclusive lease.
// 2. While held, another ensure_deployment cannot reuse the leased deployment.
// 3. Once released, ensure_deployment with reuse=true reuses the existing deployment without provisioning a new one.
// 4. With reuse=false, a new deployment is provisioned even if unleased deployments exist.
func TestDeploymentManager_AtomicLeaseAndReuse(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "acc-reuse"
	workspaceID := "ws-reuse"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-reuse-1", "Local Docker")
	envReuse := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-reuse-true", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	// Step 1: Consumer A calls EnsureDeployment
	resA, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  envReuse.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-alpha",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment A failed: %v", err)
	}
	if resA.Reused {
		t.Errorf("expected Reused=false for first deployment, got true")
	}
	if !resA.Lease.Active {
		t.Errorf("expected active lease for Consumer A")
	}
	if resA.Deployment.Status != environments.DeploymentStatusBusy {
		t.Errorf("expected deployment status %q, got %q", environments.DeploymentStatusBusy, resA.Deployment.Status)
	}
	if atomic.LoadInt32(&h.mockProv.deployCalls) != 1 {
		t.Errorf("expected 1 provider deploy call, got %d", atomic.LoadInt32(&h.mockProv.deployCalls))
	}

	// Step 2: Consumer B calls EnsureDeployment while Consumer A holds the lease.
	// Since max_instances = 2, Consumer B cannot reuse dep A (it is leased), so a second deployment is provisioned.
	resB, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  envReuse.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-beta",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment B failed: %v", err)
	}
	if resB.Reused {
		t.Errorf("expected Reused=false since dep A was held, got true")
	}
	if resB.Deployment.ID == resA.Deployment.ID {
		t.Errorf("Consumer B should have received a new deployment, not dep A")
	}
	if atomic.LoadInt32(&h.mockProv.deployCalls) != 2 {
		t.Errorf("expected 2 provider deploy calls, got %d", atomic.LoadInt32(&h.mockProv.deployCalls))
	}

	// Step 3: Consumer A releases its lease.
	relA, err := h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		LeaseID:        resA.Lease.ID,
		Reason:         "session finished",
	})
	if err != nil {
		t.Fatalf("ReleaseDeployment A failed: %v", err)
	}
	if relA.Lease.Active {
		t.Errorf("expected lease A to be inactive after release")
	}
	if relA.Deployment.Status != environments.DeploymentStatusReady {
		t.Errorf("expected deployment status %q after release with none policy, got %q", environments.DeploymentStatusReady, relA.Deployment.Status)
	}

	// Step 4: Consumer C calls EnsureDeployment on the same environment.
	// Because dep A is now unleased, healthy, and reuse=true, Consumer C should REUSE dep A!
	resC, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  envReuse.ID,
		ConsumerType:   environments.ConsumerTypeTestRun,
		ConsumerID:     "test-run-gamma",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment C failed: %v", err)
	}
	if !resC.Reused {
		t.Errorf("expected Reused=true for Consumer C, got false")
	}
	if resC.Deployment.ID != resA.Deployment.ID {
		t.Errorf("expected Consumer C to reuse dep A (%q), got %q", resA.Deployment.ID, resC.Deployment.ID)
	}
	if resC.Deployment.Status != environments.DeploymentStatusBusy {
		t.Errorf("expected reused deployment status %q, got %q", environments.DeploymentStatusBusy, resC.Deployment.Status)
	}
	// Deploy call count should NOT have increased
	if atomic.LoadInt32(&h.mockProv.deployCalls) != 2 {
		t.Errorf("expected deploy calls to remain 2 (reused), got %d", atomic.LoadInt32(&h.mockProv.deployCalls))
	}

	// Step 5: Test environment with reuse=false
	envNoReuse := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-reuse-false", conn.ID, false, 5, environments.ReleaseBehaviorNone)

	resNR1, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  envNoReuse.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-1",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment NR1 failed: %v", err)
	}
	// Release NR1
	_, err = h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		LeaseID:        resNR1.Lease.ID,
	})
	if err != nil {
		t.Fatalf("ReleaseDeployment NR1 failed: %v", err)
	}

	// Now call EnsureDeployment again on envNoReuse: should NOT reuse NR1 even though NR1 is unleased!
	beforeCalls := atomic.LoadInt32(&h.mockProv.deployCalls)
	resNR2, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  envNoReuse.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-2",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment NR2 failed: %v", err)
	}
	if resNR2.Reused {
		t.Errorf("expected Reused=false when reuse policy is disabled")
	}
	if resNR2.Deployment.ID == resNR1.Deployment.ID {
		t.Errorf("expected new deployment, but got same ID as NR1")
	}
	if atomic.LoadInt32(&h.mockProv.deployCalls) != beforeCalls+1 {
		t.Errorf("expected new provider deploy call for reuse=false")
	}
}

func TestDeploymentManager_EnsureDeployment_StoppedContainerLifecycle(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "acc-stopped-test"
	workspaceID := "ws-stopped-test"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-stopped-1", "Local Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-stopped-reuse", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	// 1. Initial ensure provisions dep1
	res1, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-1",
	})
	if err != nil {
		t.Fatalf("initial ensure failed: %v", err)
	}

	// Release lease so dep1 is idle
	_, err = h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		LeaseID:        res1.Lease.ID,
	})
	if err != nil {
		t.Fatalf("release failed: %v", err)
	}

	// Simulate container stopping while idle
	h.mockProv.mu.Lock()
	h.mockProv.depStates[res1.Deployment.ID] = environments.DeploymentStatusStopped
	h.mockProv.mu.Unlock()

	// 2. Next EnsureDeployment should detect container is stopped, call prov.Start, and successfully reuse it
	startBefore := atomic.LoadInt32(&h.mockProv.startCalls)
	res2, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-2",
	})
	if err != nil {
		t.Fatalf("ensure after stopped container failed: %v", err)
	}
	if !res2.Reused {
		t.Errorf("expected res2 to be reused after successful restart")
	}
	if res2.Deployment.ID != res1.Deployment.ID {
		t.Errorf("expected res2 deployment ID %q, got %q", res1.Deployment.ID, res2.Deployment.ID)
	}
	if atomic.LoadInt32(&h.mockProv.startCalls) != startBefore+1 {
		t.Errorf("expected provider Start to be called to recover stopped container")
	}

	// 3. Now release again, and simulate start failure on reuse
	_, err = h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		LeaseID:        res2.Lease.ID,
	})
	if err != nil {
		t.Fatalf("release 2 failed: %v", err)
	}

	h.mockProv.mu.Lock()
	h.mockProv.depStates[res1.Deployment.ID] = environments.DeploymentStatusStopped
	h.mockProv.startErr = errors.New("cannot restart container")
	h.mockProv.mu.Unlock()

	// EnsureDeployment should fail to restart dep1, mark dep1 failed, and provision a new deployment (since max_instances=2)
	deployBefore := atomic.LoadInt32(&h.mockProv.deployCalls)
	res3, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-3",
	})
	if err != nil {
		t.Fatalf("ensure after unstartable container failed: %v", err)
	}
	if res3.Reused {
		t.Errorf("expected res3 to NOT be reused when restart failed")
	}
	if res3.Deployment.ID == res1.Deployment.ID {
		t.Errorf("expected new deployment, but got failed deployment ID")
	}
	if atomic.LoadInt32(&h.mockProv.deployCalls) != deployBefore+1 {
		t.Errorf("expected new provider deploy call")
	}

	// Verify dep1 was marked failed in store
	dep1, found, err := h.manager.GetDeployment(accountScope, workspaceID, res1.Deployment.ID)
	if err != nil || !found {
		t.Fatalf("get dep1 failed: %v", err)
	}
	if dep1.Status != environments.DeploymentStatusFailed {
		t.Errorf("expected dep1 status to be failed, got %q", dep1.Status)
	}
}

// TestDeploymentManager_ConcurrencySafety tests that concurrent callers cannot exceed max_instances
// or acquire the same deployment simultaneously.
func TestDeploymentManager_ConcurrencySafety(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "acc-conc"
	workspaceID := "ws-conc"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-conc", "Local Docker")
	// MaxInstances: 1, Reuse: false -> Exactly 1 concurrent caller should succeed!
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-conc-single", conn.ID, false, 1, environments.ReleaseBehaviorNone)

	concurrency := 10
	var wg sync.WaitGroup
	var successCount int32
	var rejectedCount int32

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		consumerID := fmt.Sprintf("consumer-%d", i)
		go func(cID string) {
			defer wg.Done()
			_, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
				AccountScopeID: accountScope,
				WorkspaceID:    workspaceID,
				EnvironmentID:  env.ID,
				ConsumerType:   environments.ConsumerTypeSession,
				ConsumerID:     cID,
			})
			if err == nil {
				atomic.AddInt32(&successCount, 1)
			} else {
				if errors.Is(err, ErrMaxInstancesReached) {
					atomic.AddInt32(&rejectedCount, 1)
				}
			}
		}(consumerID)
	}

	wg.Wait()

	if successCount != 1 {
		t.Errorf("expected exactly 1 success for max_instances=1, got %d", successCount)
	}
	if rejectedCount != int32(concurrency-1) {
		t.Errorf("expected %d rejections, got %d", concurrency-1, rejectedCount)
	}

	// Verify deployment count in store is exactly 1
	deps, err := h.deployments.ListByEnvironment(accountScope, workspaceID, env.ID, 100)
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	if len(deps) != 1 {
		t.Errorf("expected exactly 1 deployment in store, got %d", len(deps))
	}
}

// TestDeploymentManager_MaxInstancesEnforcement verifies capacity limit enforcement and clear error reporting.
func TestDeploymentManager_MaxInstancesEnforcement(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "acc-limit"
	workspaceID := "ws-limit"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-limit", "Local Docker")
	// MaxInstances: 2, Reuse: false
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-limit-2", conn.ID, false, 2, environments.ReleaseBehaviorNone)

	// Instance 1
	res1, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "cons-1",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment 1 failed: %v", err)
	}

	// Instance 2
	_, err = h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "cons-2",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment 2 failed: %v", err)
	}

	// Instance 3 -> Capacity exhausted (2/2)
	_, err = h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "cons-3",
	})
	if err == nil {
		t.Fatalf("expected error when capacity is exhausted, got nil")
	}
	if !errors.Is(err, ErrMaxInstancesReached) {
		t.Errorf("expected ErrMaxInstancesReached, got: %v", err)
	}

	// Release Instance 1
	_, err = h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		LeaseID:        res1.Lease.ID,
	})
	if err != nil {
		t.Fatalf("release instance 1: %v", err)
	}

	// Note: Because env.DeploymentPolicy.Reuse is false, Instance 1 is unleased but still an active deployment.
	// Capacity is still 2 active deployments.
	_, err = h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "cons-3",
	})
	if !errors.Is(err, ErrMaxInstancesReached) {
		t.Errorf("expected ErrMaxInstancesReached because 2 active instances still exist in store, got: %v", err)
	}

	// Destroy Instance 1
	err = h.manager.DestroyDeployment(ctx, DestroyDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		DeploymentID:   res1.Deployment.ID,
	})
	if err != nil {
		t.Fatalf("destroy instance 1: %v", err)
	}

	// Now active instances = 1 (less than MaxInstances 2). Provisioning instance 3 should now succeed!
	res3, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "cons-3",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment 3 should succeed after destroying instance 1, got: %v", err)
	}
	if res3.Deployment.ID == "" {
		t.Errorf("expected valid deployment ID for instance 3")
	}
}

// TestDeploymentManager_ReleaseBehavior verifies the 3 release policies:
// 1. none: keeps container running as-is; status transitions to ready; provider Stop/Destroy are NOT called.
// 2. restart: restarts container (Stop then Start called on provider); status transitions to ready.
// 3. recreate: destroys container and removes deployment; fresh instance provisioned on next lease.
func TestDeploymentManager_ReleaseBehavior(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "acc-rel"
	workspaceID := "ws-rel"
	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-rel", "Local Docker")

	// 1. Test ReleaseBehaviorNone
	t.Run("ReleaseBehaviorNone", func(t *testing.T) {
		envNone := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-rel-none", conn.ID, true, 2, environments.ReleaseBehaviorNone)

		res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
			AccountScopeID: accountScope,
			WorkspaceID:    workspaceID,
			EnvironmentID:  envNone.ID,
			ConsumerType:   environments.ConsumerTypeSession,
			ConsumerID:     "user-none",
		})
		if err != nil {
			t.Fatalf("EnsureDeployment none failed: %v", err)
		}

		stopBefore := atomic.LoadInt32(&h.mockProv.stopCalls)
		destroyBefore := atomic.LoadInt32(&h.mockProv.destroyCalls)

		rel, err := h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
			AccountScopeID: accountScope,
			WorkspaceID:    workspaceID,
			LeaseID:        res.Lease.ID,
		})
		if err != nil {
			t.Fatalf("ReleaseDeployment failed: %v", err)
		}
		if rel.ActionTaken != "retained" {
			t.Errorf("expected ActionTaken=retained, got %q", rel.ActionTaken)
		}
		if rel.Deployment.Status != environments.DeploymentStatusReady {
			t.Errorf("expected status %q, got %q", environments.DeploymentStatusReady, rel.Deployment.Status)
		}
		if atomic.LoadInt32(&h.mockProv.stopCalls) != stopBefore {
			t.Errorf("Stop should not be called for ReleaseBehaviorNone")
		}
		if atomic.LoadInt32(&h.mockProv.destroyCalls) != destroyBefore {
			t.Errorf("Destroy should not be called for ReleaseBehaviorNone")
		}
	})

	// 2. Test ReleaseBehaviorRestart
	t.Run("ReleaseBehaviorRestart", func(t *testing.T) {
		envRestart := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-rel-restart", conn.ID, true, 2, environments.ReleaseBehaviorRestart)

		res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
			AccountScopeID: accountScope,
			WorkspaceID:    workspaceID,
			EnvironmentID:  envRestart.ID,
			ConsumerType:   environments.ConsumerTypeSession,
			ConsumerID:     "user-restart",
		})
		if err != nil {
			t.Fatalf("EnsureDeployment restart failed: %v", err)
		}

		stopBefore := atomic.LoadInt32(&h.mockProv.stopCalls)
		startBefore := atomic.LoadInt32(&h.mockProv.startCalls)

		rel, err := h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
			AccountScopeID: accountScope,
			WorkspaceID:    workspaceID,
			LeaseID:        res.Lease.ID,
		})
		if err != nil {
			t.Fatalf("ReleaseDeployment failed: %v", err)
		}
		if rel.ActionTaken != "restarted" {
			t.Errorf("expected ActionTaken=restarted, got %q", rel.ActionTaken)
		}
		if rel.Deployment.Status != environments.DeploymentStatusReady {
			t.Errorf("expected status %q, got %q", environments.DeploymentStatusReady, rel.Deployment.Status)
		}
		if atomic.LoadInt32(&h.mockProv.stopCalls) != stopBefore+1 {
			t.Errorf("expected Stop to be called for ReleaseBehaviorRestart")
		}
		if atomic.LoadInt32(&h.mockProv.startCalls) != startBefore+1 {
			t.Errorf("expected Start to be called for ReleaseBehaviorRestart")
		}
	})

	// 3. Test ReleaseBehaviorRecreate
	t.Run("ReleaseBehaviorRecreate", func(t *testing.T) {
		envRecreate := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-rel-recreate", conn.ID, true, 2, environments.ReleaseBehaviorRecreate)

		res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
			AccountScopeID: accountScope,
			WorkspaceID:    workspaceID,
			EnvironmentID:  envRecreate.ID,
			ConsumerType:   environments.ConsumerTypeSession,
			ConsumerID:     "user-recreate",
		})
		if err != nil {
			t.Fatalf("EnsureDeployment recreate failed: %v", err)
		}

		destroyBefore := atomic.LoadInt32(&h.mockProv.destroyCalls)

		rel, err := h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
			AccountScopeID: accountScope,
			WorkspaceID:    workspaceID,
			LeaseID:        res.Lease.ID,
		})
		if err != nil {
			t.Fatalf("ReleaseDeployment failed: %v", err)
		}
		if rel.ActionTaken != "recreated" {
			t.Errorf("expected ActionTaken=recreated, got %q", rel.ActionTaken)
		}
		if atomic.LoadInt32(&h.mockProv.destroyCalls) != destroyBefore+1 {
			t.Errorf("expected Destroy to be called for ReleaseBehaviorRecreate")
		}

		// Deployment should be retained in store with terminated status after recreate release
		dep, found, err := h.manager.GetDeployment(accountScope, workspaceID, res.Deployment.ID)
		if err != nil {
			t.Fatalf("GetDeployment failed: %v", err)
		}
		if !found {
			t.Errorf("expected deployment record to be retained in store after recreate release")
		} else if dep.Status != environments.DeploymentStatusTerminated {
			t.Errorf("expected deployment status %q, got %q", environments.DeploymentStatusTerminated, dep.Status)
		}

		// Next EnsureDeployment should provision a fresh instance
		resNew, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
			AccountScopeID: accountScope,
			WorkspaceID:    workspaceID,
			EnvironmentID:  envRecreate.ID,
			ConsumerType:   environments.ConsumerTypeSession,
			ConsumerID:     "user-recreate-2",
		})
		if err != nil {
			t.Fatalf("EnsureDeployment next lease failed: %v", err)
		}
		if resNew.Reused {
			t.Errorf("expected Reused=false after recreate, got true")
		}
		if resNew.Deployment.ID == res.Deployment.ID {
			t.Errorf("expected new deployment ID, got old %q", res.Deployment.ID)
		}
	})
}

// TestDeploymentManager_DestroyDeployment verifies forcible termination and cleanup.
func TestDeploymentManager_DestroyDeployment(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "acc-dest"
	workspaceID := "ws-dest"
	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-dest", "Local Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-dest", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "consumer-dest",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment failed: %v", err)
	}

	destroyBefore := atomic.LoadInt32(&h.mockProv.destroyCalls)

	// Forcibly destroy deployment while active lease is held
	err = h.manager.DestroyDeployment(ctx, DestroyDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		DeploymentID:   res.Deployment.ID,
		Reason:         "user requested termination",
	})
	if err != nil {
		t.Fatalf("DestroyDeployment failed: %v", err)
	}

	if atomic.LoadInt32(&h.mockProv.destroyCalls) != destroyBefore+1 {
		t.Errorf("expected Destroy to be called on provider")
	}

	// Active lease should be released
	lease, foundLease, err := h.manager.GetLease(accountScope, workspaceID, res.Lease.ID)
	if err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if !foundLease {
		t.Errorf("expected lease record to remain in history")
	}
	if lease.Active {
		t.Errorf("expected lease to be deactivated after destroy")
	}

	// Deployment should be retained in store with terminated status
	dep, foundDep, err := h.manager.GetDeployment(accountScope, workspaceID, res.Deployment.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if !foundDep {
		t.Errorf("expected deployment to be retained in store after destroy")
	} else if dep.Status != environments.DeploymentStatusTerminated {
		t.Errorf("expected deployment status %q, got %q", environments.DeploymentStatusTerminated, dep.Status)
	}
}

// TestDeploymentManager_RuntimeAccessAndExec verifies ResolveAccess and Exec delegation.
func TestDeploymentManager_RuntimeAccessAndExec(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "acc-exec"
	workspaceID := "ws-exec"
	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-exec", "Local Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-exec", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "consumer-exec",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment failed: %v", err)
	}

	// Resolve access
	access, err := h.manager.ResolveAccess(ctx, accountScope, workspaceID, res.Deployment.ID)
	if err != nil {
		t.Fatalf("ResolveAccess failed: %v", err)
	}
	if access.PrimaryEndpoint != "http://127.0.0.1:18080" {
		t.Errorf("expected primary endpoint http://127.0.0.1:18080, got %q", access.PrimaryEndpoint)
	}
	if !access.ExecSupported {
		t.Errorf("expected ExecSupported=true")
	}

	// Exec command
	execRes, err := h.manager.Exec(ctx, accountScope, workspaceID, res.Deployment.ID, provider.ExecRequest{
		Command: []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if !execRes.Success() {
		t.Errorf("expected exec success, got exit code %d", execRes.ExitCode)
	}
	if execRes.Stdout != "mock output" {
		t.Errorf("expected 'mock output', got %q", execRes.Stdout)
	}

	// Exec on stopped/unusable deployment should fail
	_, _ = h.deployments.UpdateStatus(accountScope, workspaceID, res.Deployment.ID, environments.DeploymentStatusStopped, environments.HealthStatusUnknown, "stopped")
	_, err = h.manager.Exec(ctx, accountScope, workspaceID, res.Deployment.ID, provider.ExecRequest{
		Command: []string{"echo", "hello"},
	})
	if !errors.Is(err, ErrDeploymentUnusable) {
		t.Errorf("expected ErrDeploymentUnusable on stopped deployment, got: %v", err)
	}
}

// TestDeploymentManager_ValidationAndErrors tests input validation and error conditions.
func TestDeploymentManager_ValidationAndErrors(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "acc-val"
	workspaceID := "ws-val"

	// 1. Invalid EnsureDeploymentRequest
	_, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{})
	if err == nil {
		t.Fatalf("expected error for empty request")
	}

	// 2. Missing environment
	_, err = h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  "non-existent-env",
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "cons-1",
	})
	if !errors.Is(err, ErrEnvironmentNotFound) {
		t.Errorf("expected ErrEnvironmentNotFound, got: %v", err)
	}

	// 3. Connection does not support Docker
	connNoDocker := environments.Connection{
		ID:             "conn-nodocker",
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Name:           "No Docker Conn",
		Kind:           environments.ConnectionKindLocalDocker,
		Capabilities: environments.ConnectionCapabilities{
			SupportsDocker: false,
		},
		LocalDocker: &environments.LocalDockerConfig{},
	}
	_, _ = h.connections.Save(connNoDocker)
	envDocker := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-docker", connNoDocker.ID, true, 2, environments.ReleaseBehaviorNone)

	_, err = h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  envDocker.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "cons-1",
	})
	if err == nil || !strings.Contains(err.Error(), "does not support Docker") {
		t.Errorf("expected docker unsupported error, got: %v", err)
	}

	// 4. Provider deploy error marks deployment as failed
	connOK := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-deploy-fail", "Local Docker")
	envFail := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-deploy-fail", connOK.ID, true, 2, environments.ReleaseBehaviorNone)
	h.mockProv.deployErr = errors.New("simulated container launch failure")
	defer func() { h.mockProv.deployErr = nil }()

	_, err = h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  envFail.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "cons-fail",
	})
	if err == nil || !strings.Contains(err.Error(), "simulated container launch failure") {
		t.Errorf("expected simulated failure, got: %v", err)
	}

	// Check that failed deployment was recorded in store with status failed
	deps, _ := h.manager.ListDeploymentsByEnvironment(accountScope, workspaceID, envFail.ID, 10)
	if len(deps) == 0 {
		t.Fatalf("expected failed deployment to be recorded in store")
	}
	if deps[0].Status != environments.DeploymentStatusFailed {
		t.Errorf("expected status %q for failed deployment, got %q", environments.DeploymentStatusFailed, deps[0].Status)
	}

	// 5. ReleaseDeployment errors
	_, err = h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		LeaseID:        "non-existent-lease",
	})
	if !errors.Is(err, ErrLeaseNotFound) {
		t.Errorf("expected ErrLeaseNotFound, got: %v", err)
	}

	// 6. DestroyDeployment error for missing deployment
	err = h.manager.DestroyDeployment(ctx, DestroyDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		DeploymentID:   "non-existent-dep",
	})
	if !errors.Is(err, ErrDeploymentNotFound) {
		t.Errorf("expected ErrDeploymentNotFound, got: %v", err)
	}
}

// TestDeploymentManager_InspectDeployment verifies inspection updates runtime attributes in store.
func TestDeploymentManager_InspectDeployment(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "acc-insp"
	workspaceID := "ws-insp"
	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-insp", "Local Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-insp", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "cons-insp",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment failed: %v", err)
	}

	inspected, err := h.manager.InspectDeployment(ctx, accountScope, workspaceID, res.Deployment.ID)
	if err != nil {
		t.Fatalf("InspectDeployment failed: %v", err)
	}
	if inspected.Status != res.Deployment.Status {
		t.Errorf("expected status %q, got %q", res.Deployment.Status, inspected.Status)
	}
	if inspected.Runtime.ContainerID != res.Deployment.Runtime.ContainerID {
		t.Errorf("expected container id %q, got %q", res.Deployment.Runtime.ContainerID, inspected.Runtime.ContainerID)
	}
}

// TestDeploymentManager_DeployAndStop verifies explicit DeployDeployment and StopDeployment lifecycle.
func TestDeploymentManager_DeployAndStop(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "acc-depstop"
	workspaceID := "ws-depstop"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-ds", "Local Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-ds", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	// 1. Direct deploy without consumer (status should be ready, no lease)
	dRes, err := h.manager.DeployDeployment(ctx, DeployDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		DeploymentName: "custom-dep-name",
	})
	if err != nil {
		t.Fatalf("DeployDeployment failed: %v", err)
	}
	if dRes.Deployment.Status != environments.DeploymentStatusReady {
		t.Errorf("expected status ready for unleased deploy, got %q", dRes.Deployment.Status)
	}
	if dRes.Lease != nil {
		t.Errorf("expected no lease for deploy without consumer")
	}
	if dRes.Access == nil || dRes.Access.PrimaryEndpoint == "" {
		t.Errorf("expected access info to be populated")
	}

	// 2. Stop deployment
	stopBefore := atomic.LoadInt32(&h.mockProv.stopCalls)
	err = h.manager.StopDeployment(ctx, accountScope, workspaceID, dRes.Deployment.ID)
	if err != nil {
		t.Fatalf("StopDeployment failed: %v", err)
	}
	if atomic.LoadInt32(&h.mockProv.stopCalls) != stopBefore+1 {
		t.Errorf("expected provider Stop to be called")
	}

	// Verify status in store is stopped
	dep, found, err := h.manager.GetDeployment(accountScope, workspaceID, dRes.Deployment.ID)
	if err != nil || !found {
		t.Fatalf("get deployment: %v", err)
	}
	if dep.Status != environments.DeploymentStatusStopped {
		t.Errorf("expected status stopped, got %q", dep.Status)
	}

	// 2b. Start deployment
	startBefore := atomic.LoadInt32(&h.mockProv.startCalls)
	err = h.manager.StartDeployment(ctx, accountScope, workspaceID, dRes.Deployment.ID)
	if err != nil {
		t.Fatalf("StartDeployment failed: %v", err)
	}
	if atomic.LoadInt32(&h.mockProv.startCalls) != startBefore+1 {
		t.Errorf("expected provider Start to be called")
	}

	dep, found, err = h.manager.GetDeployment(accountScope, workspaceID, dRes.Deployment.ID)
	if err != nil || !found {
		t.Fatalf("get deployment after start: %v", err)
	}
	if dep.Status != environments.DeploymentStatusReady {
		t.Errorf("expected status ready after start, got %q", dep.Status)
	}

	// 3. Deploy with consumer (status should be busy, with lease)
	dResWithLease, err := h.manager.DeployDeployment(ctx, DeployDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "consumer-123",
	})
	if err != nil {
		t.Fatalf("DeployDeployment with consumer failed: %v", err)
	}
	if dResWithLease.Deployment.Status != environments.DeploymentStatusBusy {
		t.Errorf("expected status busy for leased deploy, got %q", dResWithLease.Deployment.Status)
	}
	if dResWithLease.Lease == nil || !dResWithLease.Lease.Active {
		t.Errorf("expected active lease for leased deploy")
	}
}

func TestDeploymentManager_WorkerAndCustomConsumers(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "acc-workers"
	workspaceID := "ws-workers"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-worker", "Local Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-worker", conn.ID, true, 5, environments.ReleaseBehaviorNone)

	// 1. Ensure deployment with Worker consumer
	workerMeta := map[string]string{
		"worker_id":   "worker-hourly-report",
		"schedule_id": "sched-daily-001",
		"run_id":      "run-xyz",
	}
	resWorker, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID:   accountScope,
		WorkspaceID:      workspaceID,
		EnvironmentID:    env.ID,
		ConsumerType:     environments.ConsumerTypeWorker,
		ConsumerID:       "worker-run-456",
		ConsumerMetadata: workerMeta,
	})
	if err != nil {
		t.Fatalf("EnsureDeployment for Worker failed: %v", err)
	}
	if resWorker.Lease.ConsumerType != environments.ConsumerTypeWorker {
		t.Errorf("expected lease ConsumerType %q, got %q", environments.ConsumerTypeWorker, resWorker.Lease.ConsumerType)
	}
	if resWorker.Lease.ConsumerID != "worker-run-456" {
		t.Errorf("expected lease ConsumerID 'worker-run-456', got %q", resWorker.Lease.ConsumerID)
	}
	if resWorker.Lease.ConsumerMetadata["worker_id"] != "worker-hourly-report" {
		t.Errorf("expected metadata worker_id preserved, got: %+v", resWorker.Lease.ConsumerMetadata)
	}
	if resWorker.Access == nil {
		t.Fatalf("expected non-nil access for Worker deployment")
	}

	// Exec command from Worker
	execRes, err := h.manager.Exec(ctx, accountScope, workspaceID, resWorker.Deployment.ID, provider.ExecRequest{
		Command: []string{"echo", "worker-output"},
	})
	if err != nil {
		t.Fatalf("Worker Exec failed: %v", err)
	}
	if !execRes.Success() {
		t.Errorf("expected worker exec success, got exit code %d", execRes.ExitCode)
	}

	// Release Worker lease
	_, err = h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		LeaseID:        resWorker.Lease.ID,
		Reason:         "worker run completed",
	})
	if err != nil {
		t.Fatalf("ReleaseDeployment for Worker failed: %v", err)
	}

	// 2. Ensure deployment with Custom consumer (should reuse the deployment)
	customMeta := map[string]string{
		"pipeline": "ci-nightly",
		"stage":    "integration-tests",
	}
	resCustom, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID:   accountScope,
		WorkspaceID:      workspaceID,
		EnvironmentID:    env.ID,
		ConsumerType:     environments.ConsumerTypeCustom,
		ConsumerID:       "custom-runner-789",
		ConsumerMetadata: customMeta,
	})
	if err != nil {
		t.Fatalf("EnsureDeployment for Custom consumer failed: %v", err)
	}
	if !resCustom.Reused {
		t.Errorf("expected deployment to be reused for Custom consumer, got reused=false")
	}
	if resCustom.Deployment.ID != resWorker.Deployment.ID {
		t.Errorf("expected same deployment ID %q, got %q", resWorker.Deployment.ID, resCustom.Deployment.ID)
	}
	if resCustom.Lease.ConsumerType != environments.ConsumerTypeCustom {
		t.Errorf("expected lease ConsumerType %q, got %q", environments.ConsumerTypeCustom, resCustom.Lease.ConsumerType)
	}
	if resCustom.Lease.ConsumerID != "custom-runner-789" {
		t.Errorf("expected lease ConsumerID 'custom-runner-789', got %q", resCustom.Lease.ConsumerID)
	}
	if resCustom.Lease.ConsumerMetadata["pipeline"] != "ci-nightly" {
		t.Errorf("expected custom metadata preserved, got: %+v", resCustom.Lease.ConsumerMetadata)
	}

	// Clean release
	_, err = h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		LeaseID:        resCustom.Lease.ID,
		Reason:         "custom pipeline finished",
	})
	if err != nil {
		t.Fatalf("ReleaseDeployment for Custom failed: %v", err)
	}
}

func TestDeploymentManager_LeaseTTLAndReapExpired(t *testing.T) {
	h := setupTestHarness(t)
	ctx := context.Background()
	accountScope := "test-account-ttl"
	workspaceID := "ws-ttl-1"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-ttl", "Local Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-ttl", conn.ID, true, 2, environments.ReleaseBehaviorRestart)

	// 1. Verify default 1-hour lease TTL is assigned when TTLMillis == 0
	res1, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-default-ttl",
		TTLMillis:      0, // not specified
	})
	if err != nil {
		t.Fatalf("EnsureDeployment with default TTL failed: %v", err)
	}
	if res1.Lease.ExpiresAt <= res1.Lease.AcquiredAt {
		t.Errorf("expected lease ExpiresAt > AcquiredAt, got %d <= %d", res1.Lease.ExpiresAt, res1.Lease.AcquiredAt)
	}
	expectedExpiry := res1.Lease.AcquiredAt + DefaultLeaseTTLMillis
	diff := res1.Lease.ExpiresAt - expectedExpiry
	if diff < -1000 || diff > 1000 {
		t.Errorf("expected lease ExpiresAt ~ %d (+-1s), got %d (diff: %d)", expectedExpiry, res1.Lease.ExpiresAt, diff)
	}

	// Clean up first lease
	_, err = h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		LeaseID:        res1.Lease.ID,
	})
	if err != nil {
		t.Fatalf("ReleaseDeployment failed: %v", err)
	}

	// 2. Test short TTL and ReapExpired
	resShort, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-short-ttl",
		TTLMillis:      10, // 10ms TTL
	})
	if err != nil {
		t.Fatalf("EnsureDeployment with short TTL failed: %v", err)
	}

	// Immediately should be held
	activeLease, isHeld, err := h.manager.GetActiveLease(accountScope, workspaceID, resShort.Deployment.ID)
	if err != nil || !isHeld || !activeLease.Active {
		t.Fatalf("expected lease to be held immediately: held=%v, err=%v", isHeld, err)
	}

	// Wait for TTL to expire
	time.Sleep(20 * time.Millisecond)

	// GetActiveLease should now report not held
	_, isHeldAfter, err := h.manager.GetActiveLease(accountScope, workspaceID, resShort.Deployment.ID)
	if err != nil {
		t.Fatalf("GetActiveLease error: %v", err)
	}
	if isHeldAfter {
		t.Errorf("expected lease to NOT be held after expiration")
	}

	// ReapExpired should release the expired lease and trigger ReleaseBehaviorRestart
	initialStopCalls := atomic.LoadInt32(&h.mockProv.stopCalls)
	initialStartCalls := atomic.LoadInt32(&h.mockProv.startCalls)

	reaped, err := h.manager.ReapExpired(ctx, accountScope, workspaceID)
	if err != nil {
		t.Fatalf("ReapExpired failed: %v", err)
	}
	if len(reaped) != 1 {
		t.Errorf("expected 1 reaped lease, got %d: %v", len(reaped), reaped)
	}

	// Verify provider restart was executed by release behavior
	if atomic.LoadInt32(&h.mockProv.stopCalls) <= initialStopCalls {
		t.Errorf("expected provider.Stop to be called on release restart")
	}
	if atomic.LoadInt32(&h.mockProv.startCalls) <= initialStartCalls {
		t.Errorf("expected provider.Start to be called on release restart")
	}
}
