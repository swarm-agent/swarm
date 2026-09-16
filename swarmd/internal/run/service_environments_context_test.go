package run

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
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

func TestResolveDefaultTestbench_PreferredConnectionPrecedence(t *testing.T) {
	accountScopeID := "acc-1"
	workspaceID := "ws-1"

	connPreferred := environments.Connection{
		ID:             "conn-pref",
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Name:           "Preferred Docker",
		Kind:           environments.ConnectionKindLocalDocker,
	}
	connDefault := environments.Connection{
		ID:             "conn-default",
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Name:           "Default Docker",
		Kind:           environments.ConnectionKindLocalDocker,
	}

	testEnv := environments.Environment{
		ID:                    "env-test",
		AccountScopeID:        accountScopeID,
		WorkspaceID:           workspaceID,
		Name:                  "Testbench Container",
		Role:                  environments.EnvironmentRoleTesting,
		PreferredConnectionID: "conn-pref",
		Container: environments.ContainerDefinition{
			Image: "golang:1.26",
		},
	}

	connStore := &mockConnectionStore{
		connections: map[string]environments.Connection{
			"conn-pref":    connPreferred,
			"conn-default": connDefault,
		},
	}
	envStore := &mockEnvironmentStore{
		envs: []environments.Environment{testEnv},
	}
	wsStore := &mockWorkspaceSettingsStore{
		settings: map[string]environments.WorkspaceSettings{
			workspaceID: {
				WorkspaceID:              workspaceID,
				AccountScopeID:           accountScopeID,
				DefaultTestEnvironmentID: "env-test",
				DefaultConnectionID:      "conn-default",
			},
		},
	}
	depSvc := &mockDeploymentService{
		resolveFn: func(ctx context.Context, acc, ws string, explicitConnID string, env *environments.Environment) (*environments.Connection, error) {
			// Tier 2 should resolve to conn-pref
			if env.PreferredConnectionID == "conn-pref" {
				return &connPreferred, nil
			}
			return &connDefault, nil
		},
	}

	svc := &Service{}
	svc.SetEnvironmentServices(connStore, envStore, depSvc, wsStore)

	ctx := context.Background()
	result, err := svc.ResolveDefaultTestbench(ctx, accountScopeID, workspaceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.DefaultTestEnvironmentID != "env-test" {
		t.Errorf("expected DefaultTestEnvironmentID=env-test, got %q", result.DefaultTestEnvironmentID)
	}
	if result.DefaultTestEnvironment == nil || result.DefaultTestEnvironment.ID != "env-test" {
		t.Fatalf("expected DefaultTestEnvironment populated")
	}
	if result.ResolvedConnection == nil || result.ResolvedConnection.ID != "conn-pref" {
		t.Errorf("expected ResolvedConnection=conn-pref, got %v", result.ResolvedConnection)
	}
	if result.ConnectionTier != 2 {
		t.Errorf("expected ConnectionTier=2 (preferred), got %d", result.ConnectionTier)
	}
	if !strings.Contains(result.ConnectionResolutionNote, "preferred") {
		t.Errorf("expected note to mention preferred, got %q", result.ConnectionResolutionNote)
	}

	prompt := FormatWorkspaceEnvironmentPromptBlock(result)
	if !strings.Contains(prompt, "default_test_environment: env-test") {
		t.Errorf("expected prompt to contain default_test_environment: env-test, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "conn-pref") {
		t.Errorf("expected prompt to contain conn-pref, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "environment preferred connection") {
		t.Errorf("expected prompt to mention environment preferred connection, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "manage_deployments") || !strings.Contains(prompt, "ensure") {
		t.Errorf("expected prompt to contain manage_deployments ensure guidance, got:\n%s", prompt)
	}
}

func TestResolveDefaultTestbench_WorkspaceDefaultConnectionFallback(t *testing.T) {
	accountScopeID := "acc-1"
	workspaceID := "ws-1"

	connDefault := environments.Connection{
		ID:             "conn-default",
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Name:           "Host Docker Socket",
		Kind:           environments.ConnectionKindLocalDocker,
	}

	// Environment has NO preferred connection ID
	testEnv := environments.Environment{
		ID:             "env-node",
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Name:           "Node Testbench",
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "node:22",
		},
	}

	connStore := &mockConnectionStore{
		connections: map[string]environments.Connection{
			"conn-default": connDefault,
		},
	}
	envStore := &mockEnvironmentStore{
		envs: []environments.Environment{testEnv},
	}
	wsStore := &mockWorkspaceSettingsStore{
		settings: map[string]environments.WorkspaceSettings{
			workspaceID: {
				WorkspaceID:              workspaceID,
				AccountScopeID:           accountScopeID,
				DefaultTestEnvironmentID: "env-node",
				DefaultConnectionID:      "conn-default",
			},
		},
	}
	depSvc := &mockDeploymentService{
		resolveFn: func(ctx context.Context, acc, ws string, explicitConnID string, env *environments.Environment) (*environments.Connection, error) {
			// Tier 3 workspace default connection
			if env.PreferredConnectionID == "" {
				return &connDefault, nil
			}
			return nil, lifecycle.ErrNoConnectionResolved
		},
	}

	svc := &Service{}
	svc.SetEnvironmentServices(connStore, envStore, depSvc, wsStore)

	ctx := context.Background()
	result, err := svc.ResolveDefaultTestbench(ctx, accountScopeID, workspaceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ResolvedConnection == nil || result.ResolvedConnection.ID != "conn-default" {
		t.Fatalf("expected ResolvedConnection=conn-default, got %v", result.ResolvedConnection)
	}
	if result.ConnectionTier != 3 {
		t.Errorf("expected ConnectionTier=3 (workspace default), got %d", result.ConnectionTier)
	}
	if !strings.Contains(result.ConnectionResolutionNote, "workspace default") {
		t.Errorf("expected note to mention workspace default, got %q", result.ConnectionResolutionNote)
	}

	prompt := FormatWorkspaceEnvironmentPromptBlock(result)
	if !strings.Contains(prompt, "workspace default connection") {
		t.Errorf("expected prompt to mention workspace default connection, got:\n%s", prompt)
	}
}

func TestResolveDefaultTestbench_FallbackUnconfigured(t *testing.T) {
	accountScopeID := "acc-1"
	workspaceID := "ws-1"

	connStore := &mockConnectionStore{}
	envStore := &mockEnvironmentStore{}
	wsStore := &mockWorkspaceSettingsStore{
		settings: map[string]environments.WorkspaceSettings{
			workspaceID: {
				WorkspaceID:    workspaceID,
				AccountScopeID: accountScopeID,
				// DefaultTestEnvironmentID is empty
			},
		},
	}

	svc := &Service{}
	svc.SetEnvironmentServices(connStore, envStore, &mockDeploymentService{}, wsStore)

	ctx := context.Background()
	result, err := svc.ResolveDefaultTestbench(ctx, accountScopeID, workspaceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.DefaultTestEnvironment != nil {
		t.Errorf("expected DefaultTestEnvironment to be nil")
	}
	if !strings.Contains(result.FallbackReason, "no default test environment configured") {
		t.Errorf("expected fallback reason for unconfigured default, got %q", result.FallbackReason)
	}

	prompt := FormatWorkspaceEnvironmentPromptBlock(result)
	if !strings.Contains(prompt, "default_test_environment: none (no default test environment configured)") {
		t.Errorf("expected fallback prompt line, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "available_environments: none") {
		t.Errorf("expected available_environments: none, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "active_deployments: none") {
		t.Errorf("expected active_deployments: none, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "test_guidance:") {
		t.Errorf("expected test_guidance in prompt, got:\n%s", prompt)
	}
}

func TestResolveDefaultTestbench_FallbackDeletedEnvironment(t *testing.T) {
	accountScopeID := "acc-1"
	workspaceID := "ws-1"

	connStore := &mockConnectionStore{}
	envStore := &mockEnvironmentStore{envs: nil} // env-deleted is not in store
	wsStore := &mockWorkspaceSettingsStore{
		settings: map[string]environments.WorkspaceSettings{
			workspaceID: {
				WorkspaceID:              workspaceID,
				AccountScopeID:           accountScopeID,
				DefaultTestEnvironmentID: "env-deleted",
			},
		},
	}

	svc := &Service{}
	svc.SetEnvironmentServices(connStore, envStore, &mockDeploymentService{}, wsStore)

	ctx := context.Background()
	result, err := svc.ResolveDefaultTestbench(ctx, accountScopeID, workspaceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.DefaultTestEnvironment != nil {
		t.Errorf("expected DefaultTestEnvironment to be nil")
	}
	if !strings.Contains(result.FallbackReason, "not found or deleted") {
		t.Errorf("expected fallback reason to mention not found or deleted, got %q", result.FallbackReason)
	}

	prompt := FormatWorkspaceEnvironmentPromptBlock(result)
	if !strings.Contains(prompt, "not found or deleted") {
		t.Errorf("expected prompt to report environment not found or deleted, got:\n%s", prompt)
	}
}

func TestResolveDefaultTestbench_ActiveDeploymentsAndLeases(t *testing.T) {
	accountScopeID := "acc-1"
	workspaceID := "ws-1"

	env := environments.Environment{
		ID:             "env-eval",
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Name:           "Eval Environment",
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "python:3.12",
		},
	}

	depRunning := environments.Deployment{
		ID:             "dep-1",
		EnvironmentID:  "env-eval",
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Status:         environments.DeploymentStatusRunning,
	}
	depReady := environments.Deployment{
		ID:             "dep-2",
		EnvironmentID:  "env-eval",
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Status:         environments.DeploymentStatusReady,
	}
	depStopped := environments.Deployment{
		ID:             "dep-3",
		EnvironmentID:  "env-eval",
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Status:         environments.DeploymentStatusStopped,
	}

	depSvc := &mockDeploymentService{
		deps: []environments.Deployment{depRunning, depReady, depStopped},
		leases: map[string]environments.DeploymentLease{
			"dep-1": {
				ID:           "lease-active-1",
				DeploymentID: "dep-1",
				Active:       true,
				ConsumerType: environments.ConsumerTypeSession,
				ConsumerID:   "sess-xyz",
			},
		},
	}

	svc := &Service{}
	svc.SetEnvironmentServices(
		&mockConnectionStore{},
		&mockEnvironmentStore{envs: []environments.Environment{env}},
		depSvc,
		&mockWorkspaceSettingsStore{
			settings: map[string]environments.WorkspaceSettings{
				workspaceID: {
					WorkspaceID:              workspaceID,
					AccountScopeID:           accountScopeID,
					DefaultTestEnvironmentID: "env-eval",
				},
			},
		},
	)

	result, err := svc.ResolveDefaultTestbench(context.Background(), accountScopeID, workspaceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only running or ready deployments should be listed, stopped should be omitted
	if len(result.ActiveDeployments) != 2 {
		t.Fatalf("expected 2 active deployments, got %d", len(result.ActiveDeployments))
	}
	if result.ActiveDeployments[0].DeploymentID != "dep-1" || result.ActiveDeployments[0].LeaseID != "lease-active-1" {
		t.Errorf("unexpected active deployment 0: %+v", result.ActiveDeployments[0])
	}
	if result.ActiveDeployments[1].DeploymentID != "dep-2" || result.ActiveDeployments[1].LeaseID != "" {
		t.Errorf("unexpected active deployment 1: %+v", result.ActiveDeployments[1])
	}

	prompt := FormatWorkspaceEnvironmentPromptBlock(result)
	if !strings.Contains(prompt, "dep-1") || !strings.Contains(prompt, "lease-active-1") {
		t.Errorf("expected prompt to list dep-1 with lease-active-1, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "unleased") {
		t.Errorf("expected prompt to show unleased for dep-2, got:\n%s", prompt)
	}
}

func TestFormatWorkspaceEnvironmentPromptBlock_TokenBudgetCompliance(t *testing.T) {
	accountScopeID := "acc-large"
	workspaceID := "ws-large"

	// Create 50 environments and 50 deployments to test bounding
	var manyEnvs []environments.Environment
	for i := 1; i <= 50; i++ {
		manyEnvs = append(manyEnvs, environments.Environment{
			ID:             fmt.Sprintf("env-%03d", i),
			AccountScopeID: accountScopeID,
			WorkspaceID:    workspaceID,
			Name:           fmt.Sprintf("Very Long Descriptive Environment Name Number %d for Benchmarking", i),
			Role:           environments.EnvironmentRoleTesting,
			Container: environments.ContainerDefinition{
				Image: fmt.Sprintf("docker.example.com/org/repo-image-tag-number-%d:latest", i),
			},
		})
	}

	var manyDeps []ActiveDeploymentSummary
	for i := 1; i <= 50; i++ {
		manyDeps = append(manyDeps, ActiveDeploymentSummary{
			DeploymentID:  fmt.Sprintf("dep-extremely-long-identifier-%03d", i),
			EnvironmentID: fmt.Sprintf("env-%03d", i),
			Status:        "running",
			LeaseID:       fmt.Sprintf("lease-long-hash-uuid-random-key-%03d", i),
			ConsumerType:  "session",
			ConsumerID:    fmt.Sprintf("session-uuid-consumer-identifier-%03d", i),
		})
	}

	conn := &environments.Connection{
		ID:             "conn-1",
		Name:           "Production Cluster Docker",
		Kind:           environments.ConnectionKindSSH,
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
	}

	envCtx := &WorkspaceEnvironmentContext{
		WorkspaceID:              workspaceID,
		AccountScopeID:           accountScopeID,
		DefaultTestEnvironmentID: "env-001",
		DefaultTestEnvironment:   &manyEnvs[0],
		ResolvedConnection:       conn,
		ConnectionTier:           2,
		ConnectionResolutionNote: "environment preferred connection",
		AvailableEnvironments:    manyEnvs,
		ActiveDeployments:        manyDeps,
	}

	prompt := FormatWorkspaceEnvironmentPromptBlock(envCtx)
	if len(prompt) > MaxWorkspaceEnvironmentPromptBytes {
		t.Fatalf("prompt block length %d exceeds MaxWorkspaceEnvironmentPromptBytes %d:\n%s",
			len(prompt), MaxWorkspaceEnvironmentPromptBytes, prompt)
	}

	if !strings.Contains(prompt, "... and 45 more") {
		t.Errorf("expected available environments truncation (... and 45 more), got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "... and 47 more") {
		t.Errorf("expected active deployments truncation (... and 47 more), got:\n%s", prompt)
	}
}

func TestComposeRuntimeInstructions_IncludesEnvironmentBlock(t *testing.T) {
	accountScopeID := "acc-test"
	workspaceID := "ws-test"

	testEnv := environments.Environment{
		ID:             "env-go",
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Name:           "Go Test Runner",
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "golang:1.26",
		},
	}

	svc := &Service{}
	svc.SetEnvironmentServices(
		&mockConnectionStore{},
		&mockEnvironmentStore{envs: []environments.Environment{testEnv}},
		&mockDeploymentService{},
		&mockWorkspaceSettingsStore{
			settings: map[string]environments.WorkspaceSettings{
				workspaceID: {
					WorkspaceID:              workspaceID,
					AccountScopeID:           accountScopeID,
					DefaultTestEnvironmentID: "env-go",
				},
			},
		},
	)

	scope := tool.WorkspaceScope{
		PrimaryPath: workspaceID,
		Roots:       []string{workspaceID},
		Principal: identity.Principal{
			Type:           identity.PrincipalTypeUser,
			UserID:         "user-1",
			AccountScopeID: accountScopeID,
		},
	}

	instructions := svc.ComposeRuntimeInstructions(
		scope,
		"plan",
		false,
		pebblestore.AgentProfile{Name: "swarm", Mode: agentruntime.ModePrimary},
		"",
	)

	if !strings.Contains(instructions, "Workspace test environments:") {
		t.Errorf("expected instructions to include 'Workspace test environments:', got:\n%s", instructions)
	}
	if !strings.Contains(instructions, "default_test_environment: env-go") {
		t.Errorf("expected instructions to contain default_test_environment: env-go, got:\n%s", instructions)
	}
	if !strings.Contains(instructions, "manage_deployments action=\"ensure\"") {
		t.Errorf("expected instructions to contain guidance for manage_deployments ensure, got:\n%s", instructions)
	}
}

func TestMasterHarness_IncludesEnvironmentOrchestrationGuidance(t *testing.T) {
	prompt := masterHarnessPromptWithScope(tool.WorkspaceScope{
		PrimaryPath: "/test/workspace",
		Roots:       []string{"/test/workspace"},
	})

	if !strings.Contains(prompt, "Reusable environments, testbenches, and deployments:") {
		t.Errorf("expected master harness prompt to contain Reusable environments rule, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "manage_deployments action=\"ensure\"") {
		t.Errorf("expected master harness prompt to direct testing to manage_deployments ensure, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "manage_deployments (ensure test environment deployment & lease):") {
		t.Errorf("expected harness tool usage examples to include manage_deployments example, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "manage_environments (list available environments):") {
		t.Errorf("expected harness tool usage examples to include manage_environments example, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "manage_connections (list available connections):") {
		t.Errorf("expected harness tool usage examples to include manage_connections example, got:\n%s", prompt)
	}
}

type testCtxMockProvider struct {
	kind environments.ConnectionKind
}

func (m *testCtxMockProvider) Kind() environments.ConnectionKind { return m.kind }
func (m *testCtxMockProvider) ValidateConnection(ctx context.Context, conn *environments.Connection) error {
	return nil
}
func (m *testCtxMockProvider) Capabilities(ctx context.Context, conn *environments.Connection) (environments.ConnectionCapabilities, error) {
	return environments.ConnectionCapabilities{SupportsDocker: true}, nil
}
func (m *testCtxMockProvider) Deploy(ctx context.Context, req provider.DeployRequest) (*provider.DeployResult, error) {
	return &provider.DeployResult{
		Runtime: environments.RuntimeMetadata{
			ContainerID:        "c1234567890abcdef",
			ProviderResourceID: "container-mock-123",
			Endpoint:           "http://127.0.0.1:8080",
		},
		Status: environments.DeploymentStatusRunning,
		Health: environments.HealthStatusHealthy,
	}, nil
}
func (m *testCtxMockProvider) Inspect(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) (*provider.InspectResult, error) {
	return &provider.InspectResult{
		Status:  dep.Status,
		Health:  dep.Health,
		Runtime: dep.Runtime,
	}, nil
}
func (m *testCtxMockProvider) Start(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	return nil
}
func (m *testCtxMockProvider) Stop(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	return nil
}
func (m *testCtxMockProvider) Destroy(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) error {
	return nil
}
func (m *testCtxMockProvider) ResolveAccess(ctx context.Context, conn *environments.Connection, dep *environments.Deployment) (*provider.DeploymentAccess, error) {
	return &provider.DeploymentAccess{
		PrimaryEndpoint: "http://127.0.0.1:8080",
		ExecSupported:   true,
	}, nil
}
func (m *testCtxMockProvider) Exec(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
	return &provider.ExecResult{ExitCode: 0, Stdout: "ok"}, nil
}

func TestResolveDefaultTestbench_RealPebbleIntegration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "env_prompt_test.pebble")
	store, err := pebblestore.Open(dbPath)
	if err != nil {
		t.Fatalf("open pebble store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cs := pebblestore.NewConnectionStore(store)
	es := pebblestore.NewEnvironmentStore(store)
	ds := pebblestore.NewDeploymentStore(store)
	ws := pebblestore.NewWorkspaceStore(store)

	mockProv := &testCtxMockProvider{kind: environments.ConnectionKindLocalDocker}
	registry := provider.NewRegistry()
	registry.Register(mockProv)

	dm := lifecycle.NewDeploymentManager(cs, es, ds, ws, registry)

	accountScopeID := "acc-pebble"
	workspaceID := "ws-pebble"

	// Create workspace entry
	wsEntry, err := ws.AddForAccount(accountScopeID, "/tmp/ws-pebble", "Pebble Test Workspace")
	if err != nil {
		t.Fatalf("add workspace entry: %v", err)
	}
	workspaceID = wsEntry.WorkspaceID

	// Create connection
	conn := environments.Connection{
		ID:             "conn-docker-local",
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Name:           "Local Docker Daemon",
		Kind:           environments.ConnectionKindLocalDocker,
		Capabilities: environments.ConnectionCapabilities{
			SupportsDocker:      true,
			SupportsDirectMount: true,
		},
	}
	if _, err := cs.Save(conn); err != nil {
		t.Fatalf("save connection: %v", err)
	}

	// Create environment
	env := environments.Environment{
		ID:                    "env-integration",
		AccountScopeID:        accountScopeID,
		WorkspaceID:           workspaceID,
		Name:                  "Integration Testbench",
		Mode:                  environments.EnvironmentModeDeployable,
		Role:                  environments.EnvironmentRoleTesting,
		PreferredConnectionID: "conn-docker-local",
		Container: environments.ContainerDefinition{
			Image: "golang:1.26-alpine",
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindLocalMount,
				LocalMount: &environments.LocalMountConfig{
					HostPath:      "/tmp/ws-pebble",
					ContainerPath: "/workspace",
				},
			},
		},
		DeploymentPolicy: environments.DeploymentPolicy{
			MaxInstances:    2,
			Reuse:           true,
			ReleaseBehavior: environments.ReleaseBehaviorNone,
		},
	}
	if _, err := es.Save(env); err != nil {
		t.Fatalf("save environment: %v", err)
	}

	// Update workspace settings to set default test env and default conn
	defEnv := "env-integration"
	defConn := "conn-docker-local"
	if _, err := ws.UpdateWorkspaceSettings(accountScopeID, workspaceID, &defEnv, &defConn); err != nil {
		t.Fatalf("update workspace settings: %v", err)
	}

	// Ensure a deployment so there's an active leased deployment
	ctx := context.Background()
	ensureRes, err := dm.EnsureDeployment(ctx, lifecycle.EnsureDeploymentRequest{
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		EnvironmentID:  "env-integration",
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "session-test-456",
		DeploymentName: "dep-active-1",
	})
	if err != nil {
		t.Fatalf("ensure deployment: %v", err)
	}
	if ensureRes == nil || ensureRes.Deployment.ID == "" || ensureRes.Lease.ID == "" {
		t.Fatalf("expected deployment and lease created")
	}

	svc := &Service{}
	svc.SetEnvironmentServices(cs, es, dm, ws)

	envCtx, err := svc.ResolveDefaultTestbench(ctx, accountScopeID, workspaceID)
	if err != nil {
		t.Fatalf("resolve default testbench: %v", err)
	}

	if envCtx.DefaultTestEnvironmentID != "env-integration" {
		t.Errorf("expected default env id env-integration, got %s", envCtx.DefaultTestEnvironmentID)
	}
	if envCtx.DefaultTestEnvironment == nil || envCtx.DefaultTestEnvironment.Name != "Integration Testbench" {
		t.Errorf("expected default env Integration Testbench, got %+v", envCtx.DefaultTestEnvironment)
	}
	if envCtx.ResolvedConnection == nil || envCtx.ResolvedConnection.ID != "conn-docker-local" {
		t.Errorf("expected resolved connection conn-docker-local, got %+v", envCtx.ResolvedConnection)
	}
	if envCtx.ConnectionTier != 2 {
		t.Errorf("expected connection tier 2 (preferred), got %d", envCtx.ConnectionTier)
	}
	if len(envCtx.AvailableEnvironments) != 1 {
		t.Errorf("expected 1 available env, got %d", len(envCtx.AvailableEnvironments))
	}
	if len(envCtx.ActiveDeployments) != 1 {
		t.Errorf("expected 1 active deployment, got %d", len(envCtx.ActiveDeployments))
	}
	if envCtx.ActiveDeployments[0].LeaseID != ensureRes.Lease.ID {
		t.Errorf("expected active deployment lease %s, got %s", ensureRes.Lease.ID, envCtx.ActiveDeployments[0].LeaseID)
	}

	promptBlock := FormatWorkspaceEnvironmentPromptBlock(envCtx)
	if !strings.Contains(promptBlock, "default_test_environment: env-integration") {
		t.Errorf("expected prompt to contain default_test_environment, got:\n%s", promptBlock)
	}
	if !strings.Contains(promptBlock, "Integration Testbench") {
		t.Errorf("expected prompt to contain env name, got:\n%s", promptBlock)
	}
	if !strings.Contains(promptBlock, "conn-docker-local") {
		t.Errorf("expected prompt to contain connection ID, got:\n%s", promptBlock)
	}
	if !strings.Contains(promptBlock, "environment preferred connection") {
		t.Errorf("expected prompt to mention environment preferred connection, got:\n%s", promptBlock)
	}
	if !strings.Contains(promptBlock, ensureRes.Lease.ID) {
		t.Errorf("expected prompt to contain lease ID %s, got:\n%s", ensureRes.Lease.ID, promptBlock)
	}
	if !strings.Contains(promptBlock, "session:session-test-456") {
		t.Errorf("expected prompt to contain consumer ID, got:\n%s", promptBlock)
	}
	if !strings.Contains(promptBlock, "test_guidance:") {
		t.Errorf("expected prompt to contain test_guidance, got:\n%s", promptBlock)
	}
}

func TestResolveDefaultTestbench_MissingIDs(t *testing.T) {
	svc := &Service{}
	res, err := svc.ResolveDefaultTestbench(context.Background(), "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.FallbackReason, "missing") {
		t.Errorf("expected fallback reason for missing IDs, got %q", res.FallbackReason)
	}
}

func TestResolveDefaultTestbench_UnconfiguredConnection(t *testing.T) {
	accountScopeID := "acc-unconf"
	workspaceID := "ws-unconf"

	env := environments.Environment{
		ID:             "env-no-conn",
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
		Name:           "No Conn Env",
		Role:           environments.EnvironmentRoleTesting,
		// Neither PreferredConnectionID nor workspace default_connection_id
	}

	svc := &Service{}
	svc.SetEnvironmentServices(
		&mockConnectionStore{},
		&mockEnvironmentStore{envs: []environments.Environment{env}},
		&mockDeploymentService{
			resolveFn: func(ctx context.Context, acc, ws, explicit string, e *environments.Environment) (*environments.Connection, error) {
				return nil, lifecycle.ErrNoConnectionResolved
			},
		},
		&mockWorkspaceSettingsStore{
			settings: map[string]environments.WorkspaceSettings{
				workspaceID: {
					WorkspaceID:              workspaceID,
					AccountScopeID:           accountScopeID,
					DefaultTestEnvironmentID: "env-no-conn",
				},
			},
		},
	)

	res, err := svc.ResolveDefaultTestbench(context.Background(), accountScopeID, workspaceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ResolvedConnection != nil {
		t.Errorf("expected nil ResolvedConnection, got %+v", res.ResolvedConnection)
	}
	if res.ConnectionTier != 0 {
		t.Errorf("expected ConnectionTier 0, got %d", res.ConnectionTier)
	}

	prompt := FormatWorkspaceEnvironmentPromptBlock(res)
	if !strings.Contains(prompt, "no connection configured") {
		t.Errorf("expected prompt to mention no connection configured, got:\n%s", prompt)
	}
}

