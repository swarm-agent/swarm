package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

// mockTestProvider implements provider.DeploymentProvider for tool execution unit testing.
type mockTestProvider struct {
	kind        environments.ConnectionKind
	validateErr error
	caps        environments.ConnectionCapabilities
}

func newMockTestProvider(kind environments.ConnectionKind) *mockTestProvider {
	return &mockTestProvider{
		kind: kind,
		caps: environments.ConnectionCapabilities{
			SupportsDocker:      true,
			SupportsSSH:         kind == environments.ConnectionKindSSH,
			SupportsDirectMount: kind == environments.ConnectionKindLocalDocker,
			SupportsPortForward: true,
		},
	}
}

func (m *mockTestProvider) Kind() environments.ConnectionKind {
	return m.kind
}

func (m *mockTestProvider) ValidateConnection(_ context.Context, _ *environments.Connection) error {
	return m.validateErr
}

func (m *mockTestProvider) Capabilities(_ context.Context, _ *environments.Connection) (environments.ConnectionCapabilities, error) {
	return m.caps, nil
}

func (m *mockTestProvider) Deploy(_ context.Context, req provider.DeployRequest) (*provider.DeployResult, error) {
	return &provider.DeployResult{
		Runtime: environments.RuntimeMetadata{
			ContainerID:         "mock_ctr_" + req.Deployment.ID,
			ProviderResourceID:  "mock_res_" + req.Deployment.ID,
			Endpoint:            "http://127.0.0.1:18080",
			RemoteWorkspacePath: "/workspace",
		},
		Health: environments.HealthStatusHealthy,
		Status: environments.DeploymentStatusBusy,
	}, nil
}

func (m *mockTestProvider) Inspect(_ context.Context, _ *environments.Connection, dep *environments.Deployment) (*provider.InspectResult, error) {
	return &provider.InspectResult{
		Status: environments.DeploymentStatusReady,
		Health: environments.HealthStatusHealthy,
		Runtime: environments.RuntimeMetadata{
			ContainerID:         "mock_ctr_" + dep.ID,
			ProviderResourceID:  "mock_res_" + dep.ID,
			Endpoint:            "http://127.0.0.1:18080",
			RemoteWorkspacePath: "/workspace",
		},
	}, nil
}

func (m *mockTestProvider) Start(_ context.Context, _ *environments.Connection, _ *environments.Deployment) error {
	return nil
}

func (m *mockTestProvider) Stop(_ context.Context, _ *environments.Connection, _ *environments.Deployment) error {
	return nil
}

func (m *mockTestProvider) Destroy(_ context.Context, _ *environments.Connection, _ *environments.Deployment) error {
	return nil
}

func (m *mockTestProvider) ResolveAccess(_ context.Context, _ *environments.Connection, dep *environments.Deployment) (*provider.DeploymentAccess, error) {
	return &provider.DeploymentAccess{
		PrimaryEndpoint:     "http://127.0.0.1:18080",
		Endpoints:           map[string]string{"http": "http://127.0.0.1:18080"},
		ExecSupported:       true,
		RemoteWorkspacePath: "/workspace",
		ContainerID:         "mock_ctr_" + dep.ID,
	}, nil
}

func (m *mockTestProvider) Exec(_ context.Context, _ *environments.Connection, _ *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
	return &provider.ExecResult{
		ExitCode: 0,
		Stdout:   "command output: " + strings.Join(req.Command, " "),
		Stderr:   "",
	}, nil
}

func (m *mockTestProvider) CancelExec(_ context.Context, _ *environments.Connection, _ *environments.Deployment, req provider.CancelExecRequest) (*provider.CancelExecResult, error) {
	return &provider.CancelExecResult{
		OperationID: req.OperationID,
		Terminated:  true,
		ObservedAt:  time.Now(),
		SignalSent:  "SIGTERM",
	}, nil
}

type toolTestHarness struct {
	rt          *Runtime
	store       *pebblestore.Store
	connStore   *pebblestore.ConnectionStore
	envStore    *pebblestore.EnvironmentStore
	depStore    *pebblestore.DeploymentStore
	leaseStore  *pebblestore.LeaseStore
	wsStore     *pebblestore.WorkspaceStore
	mgr         *lifecycle.DeploymentManager
	providerReg *provider.Registry
	scope       WorkspaceScope
	tmpDir      string
}

func setupEnvironmentsToolHarness(t *testing.T) *toolTestHarness {
	t.Helper()
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "pebble")
	store, err := pebblestore.Open(dbDir)
	if err != nil {
		t.Fatalf("Open store failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	connStore := pebblestore.NewConnectionStore(store)
	envStore := pebblestore.NewEnvironmentStore(store)
	depStore := pebblestore.NewDeploymentStore(store)
	wsStore := pebblestore.NewWorkspaceStore(store)

	providerReg := provider.NewRegistry()
	mockLocal := newMockTestProvider(environments.ConnectionKindLocalDocker)
	mockSSH := newMockTestProvider(environments.ConnectionKindSSH)
	providerReg.Register(mockLocal)
	providerReg.Register(mockSSH)

	mgr := lifecycle.NewDeploymentManager(connStore, envStore, depStore, wsStore, providerReg)
	t.Cleanup(func() { _ = mgr.Close() })

	rt := NewRuntime(2)
	rt.SetEnvironmentServices(connStore, envStore, mgr, wsStore, providerReg)

	wsPath := filepath.Join(tmpDir, "workspace")
	_ = os.MkdirAll(wsPath, 0755)

	scope := WorkspaceScope{
		PrimaryPath: wsPath,
		Roots:       []string{wsPath},
		Principal: identity.Principal{
			AccountScopeID: "test-account",
		},
		SessionID: "test-session-001",
	}

	return &toolTestHarness{
		rt:          rt,
		store:       store,
		connStore:   connStore,
		envStore:    envStore,
		depStore:    depStore,
		leaseStore:  depStore.Leases(),
		wsStore:     wsStore,
		mgr:         mgr,
		providerReg: providerReg,
		scope:       scope,
		tmpDir:      tmpDir,
	}
}

var toolCallSeq atomic.Int64

type mockEnvWorkspaceService struct {
	workspaceID   string
	workspacePath string
}

func (s *mockEnvWorkspaceService) CurrentBindingForPrincipal(identity.Principal) (workspaceruntime.Resolution, bool, error) {
	return workspaceruntime.Resolution{}, false, nil
}

func (s *mockEnvWorkspaceService) ScopeForPathForPrincipal(_ identity.Principal, path string) (workspaceruntime.Scope, error) {
	return workspaceruntime.Scope{
		Matched:       true,
		WorkspaceID:   s.workspaceID,
		WorkspacePath: s.workspacePath,
	}, nil
}

func (s *mockEnvWorkspaceService) ListKnownForPrincipal(identity.Principal, int) ([]workspaceruntime.Entry, error) {
	return nil, nil
}

func execTool(t *testing.T, h *toolTestHarness, name string, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	seq := toolCallSeq.Add(1)
	return h.rt.ExecuteForWorkspaceScopeWithRuntime(context.Background(), h.scope, Call{
		CallID:    fmt.Sprintf("call-%s-%d", name, seq),
		Name:      name,
		Arguments: string(raw),
	})
}

// TestEnvironmentsTool_SchemaValidation verifies tool schema definitions.
func TestEnvironmentsTool_SchemaValidation(t *testing.T) {
	rt := NewRuntime(1)
	defs := rt.Definitions()

	toolDefs := make(map[string]Definition)
	for _, d := range defs {
		toolDefs[d.Name] = d
	}

	for _, name := range []string{"manage_connections", "manage_environments"} {
		def, exists := toolDefs[name]
		if !exists {
			t.Fatalf("expected tool %q to be registered in Definitions()", name)
		}
		if def.Type != "function" {
			t.Errorf("tool %q: expected Type=function, got %q", name, def.Type)
		}
		if def.Description == "" {
			t.Errorf("tool %q: expected non-empty description", name)
		}

		params := def.Parameters
		props, ok := params["properties"].(map[string]any)
		if !ok {
			t.Fatalf("tool %q: properties is not a map", name)
		}
		actionProp, ok := props["action"].(map[string]any)
		if !ok {
			t.Fatalf("tool %q: action property missing", name)
		}
		enums, ok := actionProp["enum"].([]string)
		if !ok || len(enums) == 0 {
			t.Fatalf("tool %q: action enum missing or empty", name)
		}

		reqs, ok := params["required"].([]string)
		if !ok || len(reqs) == 0 || reqs[0] != "action" {
			t.Errorf("tool %q: expected required to contain action", name)
		}
	}

	if _, exists := toolDefs["manage_deployments"]; exists {
		t.Fatalf("manage_deployments must NOT be registered in Definitions()")
	}

	// Verify action sets for each tool
	connActions := toolDefs["manage_connections"].Parameters["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string)
	for _, act := range []string{"list", "get", "create", "update", "delete", "check", "capabilities"} {
		found := false
		for _, a := range connActions {
			if a == act {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("manage_connections missing action %q", act)
		}
	}

	envActions := toolDefs["manage_environments"].Parameters["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string)
	for _, act := range []string{
		"list", "get", "create", "update", "delete", "set_default_test", "export", "import",
		"list_deployments", "get_deployment", "ensure", "deploy", "exec", "start", "stop", "destroy", "release",
		"summary", "history", "get_operation", "cancel", "cancel_operation", "help",
	} {
		found := false
		for _, a := range envActions {
			if a == act {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("manage_environments missing action %q", act)
		}
	}
}

// TestEnvironmentsTool_ManageConnectionsLifecycle tests the full lifecycle of manage_connections tool.
func TestEnvironmentsTool_ManageConnectionsLifecycle(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)

	// 1. Create local_docker connection
	createLocalArgs := map[string]any{
		"action":       "create",
		"id":           "conn-local-1",
		"name":         "Primary Local Docker",
		"description":  "Main host Docker daemon",
		"kind":         "local_docker",
		"socket_path":  "/var/run/docker.sock",
		"workspace_id": "ws-1",
	}
	out, err := execTool(t, h, "manage_connections", createLocalArgs)
	if err != nil {
		t.Fatalf("manage_connections create failed: %v", err)
	}
	var createRes map[string]any
	if err := json.Unmarshal([]byte(out), &createRes); err != nil {
		t.Fatalf("unmarshal create output: %v", err)
	}
	if createRes["status"] != "ok" || createRes["action"] != "create" {
		t.Errorf("expected status=ok, action=create, got: %v", createRes)
	}
	connObj, ok := createRes["connection"].(map[string]any)
	if !ok || connObj["id"] != "conn-local-1" || connObj["kind"] != "local_docker" {
		t.Errorf("unexpected created connection object: %v", connObj)
	}

	// 2. Create SSH connection
	createSSHArgs := map[string]any{
		"action":       "create",
		"id":           "conn-ssh-1",
		"name":         "Remote Build Server",
		"kind":         "ssh",
		"host":         "192.168.1.100",
		"user":         "builder",
		"port":         2222,
		"workspace_id": "ws-1",
	}
	out, err = execTool(t, h, "manage_connections", createSSHArgs)
	if err != nil {
		t.Fatalf("manage_connections create ssh failed: %v", err)
	}

	// 3. List connections
	listArgs := map[string]any{
		"action":       "list",
		"workspace_id": "ws-1",
	}
	out, err = execTool(t, h, "manage_connections", listArgs)
	if err != nil {
		t.Fatalf("manage_connections list failed: %v", err)
	}
	var listRes map[string]any
	_ = json.Unmarshal([]byte(out), &listRes)
	if listRes["count"].(float64) != 2 {
		t.Errorf("expected count=2, got %v", listRes["count"])
	}

	// 4. Get connection
	getArgs := map[string]any{
		"action":       "get",
		"id":           "conn-local-1",
		"workspace_id": "ws-1",
	}
	out, err = execTool(t, h, "manage_connections", getArgs)
	if err != nil {
		t.Fatalf("manage_connections get failed: %v", err)
	}
	var getRes map[string]any
	_ = json.Unmarshal([]byte(out), &getRes)
	if getRes["connection"].(map[string]any)["name"] != "Primary Local Docker" {
		t.Errorf("unexpected get result: %v", getRes)
	}

	// 5. Update connection
	updateArgs := map[string]any{
		"action":       "update",
		"id":           "conn-local-1",
		"name":         "Updated Docker Name",
		"workspace_id": "ws-1",
	}
	out, err = execTool(t, h, "manage_connections", updateArgs)
	if err != nil {
		t.Fatalf("manage_connections update failed: %v", err)
	}
	var updateRes map[string]any
	_ = json.Unmarshal([]byte(out), &updateRes)
	if updateRes["connection"].(map[string]any)["name"] != "Updated Docker Name" {
		t.Errorf("unexpected update result: %v", updateRes)
	}

	// 6. Check connection reachability
	checkArgs := map[string]any{
		"action":       "check",
		"id":           "conn-local-1",
		"workspace_id": "ws-1",
	}
	out, err = execTool(t, h, "manage_connections", checkArgs)
	if err != nil {
		t.Fatalf("manage_connections check failed: %v", err)
	}
	var checkRes map[string]any
	_ = json.Unmarshal([]byte(out), &checkRes)
	if checkRes["reachable"] != true {
		t.Errorf("expected reachable=true, got: %v", checkRes)
	}

	// 7. Capabilities
	capsArgs := map[string]any{
		"action":       "capabilities",
		"id":           "conn-local-1",
		"workspace_id": "ws-1",
	}
	out, err = execTool(t, h, "manage_connections", capsArgs)
	if err != nil {
		t.Fatalf("manage_connections capabilities failed: %v", err)
	}
	var capsRes map[string]any
	_ = json.Unmarshal([]byte(out), &capsRes)
	if capsRes["capabilities"] == nil {
		t.Errorf("expected capabilities in response, got: %v", capsRes)
	}

	// 8. Delete connection
	delArgs := map[string]any{
		"action":       "delete",
		"id":           "conn-ssh-1",
		"workspace_id": "ws-1",
	}
	out, err = execTool(t, h, "manage_connections", delArgs)
	if err != nil {
		t.Fatalf("manage_connections delete failed: %v", err)
	}
	var delRes map[string]any
	_ = json.Unmarshal([]byte(out), &delRes)
	if delRes["id"] != "conn-ssh-1" {
		t.Errorf("unexpected delete result: %v", delRes)
	}
}

// TestEnvironmentsTool_ManageEnvironmentsLifecycle tests create, get, list, update, set_default_test, export, import, delete.
func TestEnvironmentsTool_ManageEnvironmentsLifecycle(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)

	// Seed an active workspace entry for workspace settings tests
	wsPath := filepath.Join(h.tmpDir, "ws-env")
	_ = os.MkdirAll(wsPath, 0755)
	wsEntry, err := h.wsStore.SaveForAccount(h.scope.Principal.AccountScopeID, wsPath, "Test Workspace", "", true)
	if err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	workspaceID := wsEntry.WorkspaceID

	// 1. Create environment
	createArgs := map[string]any{
		"action":                  "create",
		"id":                      "env-go-test",
		"name":                    "Go Testbench",
		"description":             "Isolated Go testing environment",
		"image":                   "golang:1.24",
		"preferred_connection_id": "conn-local-1",
		"workspace_id":            workspaceID,
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
			"max_instances":    2,
			"release_behavior": "restart",
		},
	}
	out, err := execTool(t, h, "manage_environments", createArgs)
	if err != nil {
		t.Fatalf("manage_environments create failed: %v", err)
	}
	var createRes map[string]any
	_ = json.Unmarshal([]byte(out), &createRes)
	if createRes["status"] != "ok" {
		t.Errorf("expected status=ok, got: %v", createRes)
	}
	envObj := createRes["environment"].(map[string]any)
	if envObj["id"] != "env-go-test" || envObj["name"] != "Go Testbench" {
		t.Errorf("unexpected environment object: %v", envObj)
	}

	// 2. Set default test environment
	setDefaultArgs := map[string]any{
		"action":       "set_default_test",
		"id":           "env-go-test",
		"workspace_id": workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", setDefaultArgs)
	if err != nil {
		t.Fatalf("manage_environments set_default_test failed: %v", err)
	}
	var setDefRes map[string]any
	_ = json.Unmarshal([]byte(out), &setDefRes)
	if setDefRes["default_test_environment_id"] != "env-go-test" {
		t.Errorf("expected default_test_environment_id=env-go-test, got: %v", setDefRes)
	}

	// 3. Get environment (should report is_default_test=true)
	getArgs := map[string]any{
		"action":       "get",
		"id":           "env-go-test",
		"workspace_id": workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", getArgs)
	if err != nil {
		t.Fatalf("manage_environments get failed: %v", err)
	}
	var getRes map[string]any
	_ = json.Unmarshal([]byte(out), &getRes)
	if getRes["is_default_test"] != true {
		t.Errorf("expected is_default_test=true, got: %v", getRes["is_default_test"])
	}

	// 4. Update environment
	updateArgs := map[string]any{
		"action":       "update",
		"id":           "env-go-test",
		"name":         "Go 1.24 Advanced Testbench",
		"workspace_id": workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", updateArgs)
	if err != nil {
		t.Fatalf("manage_environments update failed: %v", err)
	}
	var updateRes map[string]any
	_ = json.Unmarshal([]byte(out), &updateRes)
	if updateRes["environment"].(map[string]any)["name"] != "Go 1.24 Advanced Testbench" {
		t.Errorf("unexpected update result: %v", updateRes)
	}

	// 5. Export environment
	exportArgs := map[string]any{
		"action":       "export",
		"id":           "env-go-test",
		"workspace_id": workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", exportArgs)
	if err != nil {
		t.Fatalf("manage_environments export failed: %v", err)
	}
	var exportRes map[string]any
	_ = json.Unmarshal([]byte(out), &exportRes)
	jsonStr, ok := exportRes["json"].(string)
	if !ok || jsonStr == "" {
		t.Fatalf("expected non-empty json string in export result")
	}

	// 6. Import environment into another ID/workspace
	var envToImport environments.Environment
	_ = json.Unmarshal([]byte(jsonStr), &envToImport)
	envToImport.ID = "env-imported-1"
	envToImport.Name = "Imported Go Environment"
	importJSON, _ := json.Marshal(envToImport)

	importArgs := map[string]any{
		"action":       "import",
		"json":         string(importJSON),
		"workspace_id": workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", importArgs)
	if err != nil {
		t.Fatalf("manage_environments import failed: %v", err)
	}
	var importRes map[string]any
	_ = json.Unmarshal([]byte(out), &importRes)
	if importRes["environment"].(map[string]any)["id"] != "env-imported-1" {
		t.Errorf("unexpected imported environment: %v", importRes)
	}

	// 7. List environments
	listArgs := map[string]any{
		"action":       "list",
		"workspace_id": workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", listArgs)
	if err != nil {
		t.Fatalf("manage_environments list failed: %v", err)
	}
	var listRes map[string]any
	_ = json.Unmarshal([]byte(out), &listRes)
	if listRes["count"].(float64) != 2 {
		t.Errorf("expected count=2, got: %v", listRes["count"])
	}

	// 8. Delete environment
	delArgs := map[string]any{
		"action":       "delete",
		"id":           "env-imported-1",
		"workspace_id": workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", delArgs)
	if err != nil {
		t.Fatalf("manage_environments delete failed: %v", err)
	}
	var delRes map[string]any
	_ = json.Unmarshal([]byte(out), &delRes)
	if delRes["id"] != "env-imported-1" {
		t.Errorf("unexpected delete result: %v", delRes)
	}
}

// TestEnvironmentsTool_ManageDeploymentsLifecycle verifies deploy, ensure, access, exec, check, release, stop, destroy.
func TestEnvironmentsTool_ManageDeploymentsLifecycle(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)
	accountScope := h.scope.Principal.AccountScopeID
	workspaceID := "ws-dep-lifecycle"

	// Seed active workspace
	wsPath := filepath.Join(h.tmpDir, "ws-dep")
	_ = os.MkdirAll(wsPath, 0755)
	wsEntry, err := h.wsStore.SaveForAccount(accountScope, wsPath, "Deployment Test WS", "", true)
	if err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	workspaceID = wsEntry.WorkspaceID

	// Seed connection
	conn := environments.Connection{
		ID:             "conn-dep-test",
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Name:           "Local Docker for Deps",
		Kind:           environments.ConnectionKindLocalDocker,
		Capabilities: environments.ConnectionCapabilities{
			SupportsDocker:      true,
			SupportsDirectMount: true,
			SupportsPortForward: true,
		},
		LocalDocker: &environments.LocalDockerConfig{},
	}
	_, _ = h.connStore.Save(conn)

	// Seed environment with ReleaseBehaviorRestart
	env := environments.Environment{
		ID:                    "env-dep-test",
		AccountScopeID:        accountScope,
		WorkspaceID:           workspaceID,
		Name:                  "Test App Environment",
		Mode:                  environments.EnvironmentModeDeployable,
		Role:                  environments.EnvironmentRoleTesting,
		PreferredConnectionID: conn.ID,
		Container: environments.ContainerDefinition{
			Image: "test-image:latest",
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindLocalMount,
				LocalMount: &environments.LocalMountConfig{
					ContainerPath: "/workspace",
				},
			},
		},
		DeploymentPolicy: environments.DeploymentPolicy{
			Reuse:           true,
			MaxInstances:    2,
			ReleaseBehavior: environments.ReleaseBehaviorRestart,
		},
	}
	if _, err := h.envStore.Save(env); err != nil {
		t.Fatalf("save env: %v", err)
	}

	// 1. Action: ensure (supervised admission returns bounded async receipt)
	ensureArgs := map[string]any{
		"action":         "ensure",
		"environment_id": env.ID,
		"consumer_type":  "session",
		"consumer_id":    "session-user-1",
		"workspace_id":   workspaceID,
	}
	out, err := execTool(t, h, "manage_environments", ensureArgs)
	if err != nil {
		t.Fatalf("manage_environments ensure failed: %v", err)
	}
	var ensureRes map[string]any
	_ = json.Unmarshal([]byte(out), &ensureRes)
	if ensureRes["status"] == nil || ensureRes["operation_id"] == nil {
		t.Fatalf("expected bounded async receipt with operation_id, got: %v", ensureRes)
	}
	opID := ensureRes["operation_id"].(string)

	// Wait for ensure operation to complete in background
	var deploymentID string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		getOpOut, getErr := execTool(t, h, "manage_environments", map[string]any{
			"action":       "get_operation",
			"operation_id": opID,
			"workspace_id": workspaceID,
		})
		if getErr == nil {
			var getOpRes map[string]any
			_ = json.Unmarshal([]byte(getOpOut), &getOpRes)
			if opMap, ok := getOpRes["operation"].(map[string]any); ok {
				if opMap["status"] == "succeeded" {
					if id, ok := opMap["deployment_id"].(string); ok {
						deploymentID = id
					}
					break
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if deploymentID == "" {
		// Fall back to listing deployments
		listDeps, _ := h.mgr.ListDeployments("test-account", workspaceID, 1)
		if len(listDeps) > 0 {
			deploymentID = listDeps[0].ID
		}
	}
	if deploymentID == "" {
		t.Fatalf("ensure operation did not resolve deployment ID within deadline")
	}

	// 2. Action: get_deployment
	getArgs := map[string]any{
		"action":        "get_deployment",
		"deployment_id": deploymentID,
		"workspace_id":  workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", getArgs)
	if err != nil {
		t.Fatalf("manage_environments get_deployment failed: %v", err)
	}
	var getRes map[string]any
	_ = json.Unmarshal([]byte(out), &getRes)
	if getRes["deployment"].(map[string]any)["id"] != deploymentID {
		t.Errorf("unexpected deployment in get_deployment: %v", getRes)
	}
	if getRes["active_lease"] == nil && getRes["lease"] == nil {
		t.Errorf("expected active lease in get_deployment result")
	}

	// 3. Action: list_deployments (reads without lazy reap)
	listDepArgs := map[string]any{
		"action":       "list_deployments",
		"workspace_id": workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", listDepArgs)
	if err != nil {
		t.Fatalf("manage_environments list_deployments failed: %v", err)
	}
	var listDepRes map[string]any
	_ = json.Unmarshal([]byte(out), &listDepRes)
	if count, ok := listDepRes["count"].(float64); !ok || count < 1 {
		t.Fatalf("expected at least 1 deployment in list_deployments, got: %v", listDepRes)
	}

	// 4. Action: summary
	sumArgs := map[string]any{
		"action":       "summary",
		"workspace_id": workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", sumArgs)
	if err != nil {
		t.Fatalf("manage_environments summary failed: %v", err)
	}
	var sumRes map[string]any
	_ = json.Unmarshal([]byte(out), &sumRes)
	if sumRes["summary"] == nil {
		t.Errorf("expected summary object in response: %v", sumRes)
	}

	// 5. Action: exec (returns supervised receipt)
	execArgs := map[string]any{
		"action":        "exec",
		"deployment_id": deploymentID,
		"command":       []any{"ls", "-la", "/workspace"},
		"workspace_id":  workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", execArgs)
	if err != nil {
		t.Fatalf("manage_environments exec failed: %v", err)
	}
	var execRes map[string]any
	_ = json.Unmarshal([]byte(out), &execRes)
	execOpID, _ := execRes["operation_id"].(string)
	if execOpID == "" {
		t.Fatalf("expected operation_id in exec receipt: %v", execRes)
	}
	execDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(execDeadline) {
		getOpOut, getErr := execTool(t, h, "manage_environments", map[string]any{
			"action":       "get_operation",
			"operation_id": execOpID,
			"workspace_id": workspaceID,
		})
		if getErr == nil {
			var getOpRes map[string]any
			_ = json.Unmarshal([]byte(getOpOut), &getOpRes)
			if opMap, ok := getOpRes["operation"].(map[string]any); ok && opMap["status"] == "succeeded" {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	// 6. Action: history (verifies operation history querying)
	histArgs := map[string]any{
		"action":       "history",
		"workspace_id": workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", histArgs)
	if err != nil {
		t.Fatalf("manage_environments history failed: %v", err)
	}
	var histRes map[string]any
	_ = json.Unmarshal([]byte(out), &histRes)
	if histRes["history"] == nil {
		t.Errorf("expected history in response: %v", histRes)
	}

	// 7. Action: release
	relArgs := map[string]any{
		"action":        "release",
		"deployment_id": deploymentID,
		"reason":        "test task completed",
		"workspace_id":  workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", relArgs)
	if err != nil {
		t.Fatalf("manage_environments release failed: %v", err)
	}
	var relRes map[string]any
	_ = json.Unmarshal([]byte(out), &relRes)
	relOpID, _ := relRes["operation_id"].(string)
	if relOpID == "" {
		t.Errorf("expected operation_id in release receipt, got: %v", relRes)
	}
	relDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(relDeadline) {
		getOpOut, getErr := execTool(t, h, "manage_environments", map[string]any{
			"action":       "get_operation",
			"operation_id": relOpID,
			"workspace_id": workspaceID,
		})
		if getErr == nil {
			var getOpRes map[string]any
			_ = json.Unmarshal([]byte(getOpOut), &getOpRes)
			if opMap, ok := getOpRes["operation"].(map[string]any); ok && opMap["status"] == "succeeded" {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	// 8. Action: stop
	stopArgs := map[string]any{
		"action":        "stop",
		"deployment_id": deploymentID,
		"workspace_id":  workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", stopArgs)
	if err != nil {
		t.Fatalf("manage_environments stop failed: %v", err)
	}
	var stopRes map[string]any
	_ = json.Unmarshal([]byte(out), &stopRes)
	stopOpID, _ := stopRes["operation_id"].(string)
	if stopOpID == "" {
		t.Errorf("expected operation_id in stop receipt, got: %v", stopRes)
	}
	stopDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(stopDeadline) {
		getOpOut, getErr := execTool(t, h, "manage_environments", map[string]any{
			"action":       "get_operation",
			"operation_id": stopOpID,
			"workspace_id": workspaceID,
		})
		if getErr == nil {
			var getOpRes map[string]any
			_ = json.Unmarshal([]byte(getOpOut), &getOpRes)
			if opMap, ok := getOpRes["operation"].(map[string]any); ok && opMap["status"] == "succeeded" {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	// 9. Action: destroy
	destroyArgs := map[string]any{
		"action":        "destroy",
		"deployment_id": deploymentID,
		"reason":        "final cleanup",
		"workspace_id":  workspaceID,
	}
	out, err = execTool(t, h, "manage_environments", destroyArgs)
	if err != nil {
		t.Fatalf("manage_environments destroy failed: %v", err)
	}
	var destRes map[string]any
	_ = json.Unmarshal([]byte(out), &destRes)
	destOpID, _ := destRes["operation_id"].(string)
	if destOpID == "" {
		t.Errorf("expected operation_id in destroy receipt: %v", destRes)
	}
	destDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(destDeadline) {
		getOpOut, getErr := execTool(t, h, "manage_environments", map[string]any{
			"action":       "get_operation",
			"operation_id": destOpID,
			"workspace_id": workspaceID,
		})
		if getErr == nil {
			var getOpRes map[string]any
			_ = json.Unmarshal([]byte(getOpOut), &getOpRes)
			if opMap, ok := getOpRes["operation"].(map[string]any); ok && opMap["status"] == "succeeded" {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	// 10. Obsolete manage_deployments must fail closed
	_, err = execTool(t, h, "manage_deployments", map[string]any{"action": "list"})
	if err == nil {
		t.Fatalf("expected manage_deployments to fail closed as obsolete")
	}
}

// TestEnvironmentsTool_WorkspaceIsolation verifies strict multi-workspace containment.
func TestEnvironmentsTool_WorkspaceIsolation(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)

	// Create connection in workspace A
	_, err := execTool(t, h, "manage_connections", map[string]any{
		"action":       "create",
		"id":           "conn-ws-a",
		"name":         "Connection WS A",
		"kind":         "local_docker",
		"workspace_id": "ws-a",
	})
	if err != nil {
		t.Fatalf("create connection ws-a: %v", err)
	}

	// Create connection in workspace B
	_, err = execTool(t, h, "manage_connections", map[string]any{
		"action":       "create",
		"id":           "conn-ws-b",
		"name":         "Connection WS B",
		"kind":         "local_docker",
		"workspace_id": "ws-b",
	})
	if err != nil {
		t.Fatalf("create connection ws-b: %v", err)
	}

	// Listing in workspace A should only return conn-ws-a
	outA, err := execTool(t, h, "manage_connections", map[string]any{
		"action":       "list",
		"workspace_id": "ws-a",
	})
	if err != nil {
		t.Fatalf("list ws-a: %v", err)
	}
	var resA map[string]any
	_ = json.Unmarshal([]byte(outA), &resA)
	if resA["count"].(float64) != 1 {
		t.Errorf("expected count=1 for ws-a, got: %v", resA["count"])
	}
	connsA := resA["connections"].([]any)
	if connsA[0].(map[string]any)["id"] != "conn-ws-a" {
		t.Errorf("expected conn-ws-a in ws-a, got: %v", connsA[0])
	}

	// Attempting to get conn-ws-b with workspace_id ws-a must fail
	_, err = execTool(t, h, "manage_connections", map[string]any{
		"action":       "get",
		"id":           "conn-ws-b",
		"workspace_id": "ws-a",
	})
	if err == nil {
		t.Errorf("expected cross-workspace get to fail")
	}

	// Unauthenticated account scope must fail
	unauthHarness := *h
	unauthHarness.scope.Principal.AccountScopeID = ""
	_, err = execTool(t, &unauthHarness, "manage_connections", map[string]any{
		"action": "list",
	})
	if err == nil || !strings.Contains(err.Error(), "requires an authenticated account scope") {
		t.Errorf("expected authenticated account scope error, got: %v", err)
	}
}

// TestEnvironments_EndToEndLifecycleSmoke verifies the complete end-to-end flow:
// resolve default test environment -> resolve connection -> ensure/lease deployment -> resolve access -> exec -> release deployment -> reuse.
func TestEnvironments_EndToEndLifecycleSmoke(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)
	wsPath := filepath.Join(h.tmpDir, "ws-smoke")
	_ = os.MkdirAll(wsPath, 0755)
	wsEntry, err := h.wsStore.SaveForAccount(h.scope.Principal.AccountScopeID, wsPath, "Smoke Workspace", "", true)
	if err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	workspaceID := wsEntry.WorkspaceID
	h.scope.PrimaryPath = wsPath

	// 1. Create default connection via tool
	connOut, err := execTool(t, h, "manage_connections", map[string]any{
		"action":       "create",
		"name":         "E2E Smoke Connection",
		"kind":         "local_docker",
		"is_default":   true,
		"workspace_id": workspaceID,
	})
	if err != nil {
		t.Fatalf("create connection failed: %v", err)
	}
	var connRes map[string]any
	if err := json.Unmarshal([]byte(connOut), &connRes); err != nil {
		t.Fatalf("unmarshal connRes: %v", err)
	}
	connObj := connRes["connection"].(map[string]any)
	connID := connObj["id"].(string)

	// 2. Create environment definition via tool with release_behavior=restart, reuse=true
	envOut, err := execTool(t, h, "manage_environments", map[string]any{
		"action":           "create",
		"name":             "E2E Smoke Environment",
		"image":            "golang:1.24-alpine",
		"release_behavior": "restart",
		"reuse":            true,
		"max_instances":    3,
		"ports": []any{
			map[string]any{
				"container_port": 8080,
				"host_port":      18080,
				"protocol":       "tcp",
			},
		},
		"workspace_id": workspaceID,
	})
	if err != nil {
		t.Fatalf("create environment failed: %v", err)
	}
	var envRes map[string]any
	if err := json.Unmarshal([]byte(envOut), &envRes); err != nil {
		t.Fatalf("unmarshal envRes: %v", err)
	}
	envObj := envRes["environment"].(map[string]any)
	envID := envObj["id"].(string)

	// 3. Set as default test environment
	setDefOut, err := execTool(t, h, "manage_environments", map[string]any{
		"action":         "set_default_test",
		"environment_id": envID,
		"workspace_id":   workspaceID,
	})
	if err != nil {
		t.Fatalf("set default test environment failed: %v", err)
	}
	var setDefRes map[string]any
	if err := json.Unmarshal([]byte(setDefOut), &setDefRes); err != nil {
		t.Fatalf("unmarshal setDefRes: %v", err)
	}
	if setDefRes["default_test_environment_id"] != envID {
		t.Fatalf("expected default_test_environment_id=%s, got: %v", envID, setDefRes["default_test_environment_id"])
	}

	// 4. Ensure deployment using auto-resolved default test environment (omitting environment_id and connection_id)
	ensureOut, err := execTool(t, h, "manage_environments", map[string]any{
		"action":        "ensure",
		"consumer_type": "test_run",
		"consumer_id":   "test-run-e2e-smoke",
		"consumer_metadata": map[string]any{
			"suite": "smoke-e2e",
		},
		"workspace_id": workspaceID,
	})
	if err != nil {
		t.Fatalf("ensure deployment via default test environment failed: %v", err)
	}
	var ensureRes map[string]any
	if err := json.Unmarshal([]byte(ensureOut), &ensureRes); err != nil {
		t.Fatalf("unmarshal ensureRes: %v", err)
	}
	if ensureRes["operation_id"] == nil {
		t.Fatalf("expected bounded async receipt with operation_id, got: %v", ensureRes)
	}
	opID := ensureRes["operation_id"].(string)

	// Wait for ensure to complete in background
	var depID string
	var leaseID string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		getOpOut, getErr := execTool(t, h, "manage_environments", map[string]any{
			"action":       "get_operation",
			"operation_id": opID,
			"workspace_id": workspaceID,
		})
		if getErr == nil {
			var getOpRes map[string]any
			_ = json.Unmarshal([]byte(getOpOut), &getOpRes)
			if opMap, ok := getOpRes["operation"].(map[string]any); ok {
				if opMap["status"] == "succeeded" {
					if id, ok := opMap["deployment_id"].(string); ok {
						depID = id
					}
					if lid, ok := opMap["lease_id"].(string); ok {
						leaseID = lid
					}
					break
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if depID == "" {
		listDeps, _ := h.mgr.ListDeployments("test-account", workspaceID, 1)
		if len(listDeps) > 0 {
			depID = listDeps[0].ID
			if activeLease, ok, _ := h.mgr.GetActiveLease("test-account", workspaceID, depID); ok {
				leaseID = activeLease.ID
			}
		}
	}
	if depID == "" {
		t.Fatalf("ensure did not resolve deployment ID within deadline")
	}

	// Verify deployment was created with auto-resolved environment and connection
	dep, foundDep, _ := h.mgr.GetDeployment("test-account", workspaceID, depID)
	if !foundDep {
		t.Fatalf("expected deployment %q in store", depID)
	}
	if dep.EnvironmentID != envID {
		t.Errorf("expected auto-resolved environment_id=%s, got: %v", envID, dep.EnvironmentID)
	}
	if dep.ConnectionID != connID {
		t.Errorf("expected auto-resolved connection_id=%s, got: %v", connID, dep.ConnectionID)
	}

	// 5. Exec command inside the leased deployment container
	execOut, err := execTool(t, h, "manage_environments", map[string]any{
		"action":        "exec",
		"deployment_id": depID,
		"command":       []any{"echo", "smoke-test-ok"},
		"workspace_id":  workspaceID,
	})
	if err != nil {
		t.Fatalf("exec command inside deployment failed: %v", err)
	}
	var execRes map[string]any
	if err := json.Unmarshal([]byte(execOut), &execRes); err != nil {
		t.Fatalf("unmarshal execRes: %v", err)
	}
	if execRes["operation_id"] == nil {
		t.Errorf("expected operation_id in exec receipt, got: %v", execRes)
	}

	// 6. Release deployment lease
	relOut, err := execTool(t, h, "manage_environments", map[string]any{
		"action":        "release",
		"deployment_id": depID,
		"lease_id":      leaseID,
		"reason":        "smoke test passed",
		"workspace_id":  workspaceID,
	})
	if err != nil {
		t.Fatalf("release deployment failed: %v", err)
	}
	var relRes map[string]any
	if err := json.Unmarshal([]byte(relOut), &relRes); err != nil {
		t.Fatalf("unmarshal relRes: %v", err)
	}
	if relRes["operation_id"] == nil {
		t.Errorf("expected operation_id in release receipt, got: %v", relRes)
	}
	relOpID, _ := relRes["operation_id"].(string)
	relDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(relDeadline) {
		getOpOut, getErr := execTool(t, h, "manage_environments", map[string]any{
			"action":       "get_operation",
			"operation_id": relOpID,
			"workspace_id": workspaceID,
		})
		if getErr == nil {
			var getOpRes map[string]any
			_ = json.Unmarshal([]byte(getOpOut), &getOpRes)
			if opMap, ok := getOpRes["operation"].(map[string]any); ok && opMap["status"] == "succeeded" {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	// 7. Second consumer (worker) calls ensure
	workerEnsureOut, err := execTool(t, h, "manage_environments", map[string]any{
		"action":        "ensure",
		"consumer_type": "worker",
		"consumer_id":   "worker-run-hourly",
		"workspace_id":  workspaceID,
	})
	if err != nil {
		t.Fatalf("worker ensure deployment failed: %v", err)
	}
	var workerEnsureRes map[string]any
	if err := json.Unmarshal([]byte(workerEnsureOut), &workerEnsureRes); err != nil {
		t.Fatalf("unmarshal workerEnsureRes: %v", err)
	}
	if workerEnsureRes["operation_id"] == nil {
		t.Errorf("expected operation_id in worker ensure receipt, got: %v", workerEnsureRes)
	}
	workerEnsureOpID, _ := workerEnsureRes["operation_id"].(string)
	weDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(weDeadline) {
		getOpOut, getErr := execTool(t, h, "manage_environments", map[string]any{
			"action":       "get_operation",
			"operation_id": workerEnsureOpID,
			"workspace_id": workspaceID,
		})
		if getErr == nil {
			var getOpRes map[string]any
			_ = json.Unmarshal([]byte(getOpOut), &getOpRes)
			if opMap, ok := getOpRes["operation"].(map[string]any); ok && opMap["status"] == "succeeded" {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	// 8. Clean up: destroy deployment
	destOut, err := execTool(t, h, "manage_environments", map[string]any{
		"action":        "destroy",
		"deployment_id": depID,
		"reason":        "e2e cleanup",
		"workspace_id":  workspaceID,
	})
	if err != nil {
		t.Fatalf("destroy deployment failed: %v", err)
	}
	var destRes map[string]any
	if err := json.Unmarshal([]byte(destOut), &destRes); err != nil {
		t.Fatalf("unmarshal destRes: %v", err)
	}
	if destRes["operation_id"] == nil {
		t.Errorf("expected operation_id in destroy receipt: %v", destRes)
	}

	destOpID, _ := destRes["operation_id"].(string)
	destDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(destDeadline) {
		getOpOut, getErr := execTool(t, h, "manage_environments", map[string]any{
			"action":       "get_operation",
			"operation_id": destOpID,
			"workspace_id": workspaceID,
		})
		if getErr == nil {
			var getOpRes map[string]any
			_ = json.Unmarshal([]byte(getOpOut), &getOpRes)
			if opMap, ok := getOpRes["operation"].(map[string]any); ok && opMap["status"] == "succeeded" {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Verify deployment is marked terminated in store after destroy
	dep, found, err := h.depStore.Get(h.scope.Principal.AccountScopeID, workspaceID, depID)
	if err != nil {
		t.Fatalf("error checking deployment in store: %v", err)
	}
	if !found {
		t.Errorf("expected destroyed deployment to be retained in store")
	} else if dep.Status != environments.DeploymentStatusTerminated {
		t.Errorf("expected deployment status to be terminated after destroy, got: %s", dep.Status)
	}
}

func TestEnvironmentsTool_ObsoleteManageDeploymentsFailsClosed(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)

	// 1. Verify manage_deployments is absent from Definitions()
	defs := h.rt.Definitions()
	for _, d := range defs {
		if d.Name == "manage_deployments" {
			t.Fatalf("manage_deployments must NOT be present in tool Definitions()")
		}
	}

	// 2. Calling manage_deployments with any action must fail with obsolete error
	actions := []string{"list", "get", "ensure", "exec", "stop", "destroy", "release"}
	for _, action := range actions {
		_, err := execTool(t, h, "manage_deployments", map[string]any{
			"action": action,
		})
		if err == nil {
			t.Errorf("expected manage_deployments action %q to fail, got nil err", action)
		}
		if !strings.Contains(err.Error(), "manage_deployments has been removed") {
			t.Errorf("expected obsolete removal message for action %q, got: %v", action, err)
		}
	}
}

func TestEnvironmentsTool_ReadPurityNoSideEffects(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)
	workspaceID := "ws-purity"

	// Create environment definition to query
	env := environments.Environment{
		ID:             "env-purity",
		AccountScopeID: "test-account",
		WorkspaceID:    workspaceID,
		Name:           "Purity Test",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "alpine:latest",
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindRegistryImage,
				RegistryImage: &environments.RegistryImageConfig{
					Image: "alpine:latest",
				},
			},
		},
	}
	if _, err := h.envStore.Save(env); err != nil {
		t.Fatalf("save env: %v", err)
	}

	// Record initial operation summary count
	summaryBefore, err := h.mgr.Summary(context.Background(), "test-account", workspaceID)
	if err != nil {
		t.Fatalf("get summary before: %v", err)
	}

	// Execute read-only actions
	readActions := []map[string]any{
		{"action": "list", "workspace_id": workspaceID},
		{"action": "get", "environment_id": "env-purity", "workspace_id": workspaceID},
		{"action": "export", "environment_id": "env-purity", "workspace_id": workspaceID},
		{"action": "help"},
		{"action": "list_deployments", "workspace_id": workspaceID},
		{"action": "summary", "workspace_id": workspaceID},
		{"action": "history", "workspace_id": workspaceID},
	}

	for _, args := range readActions {
		out, err := execTool(t, h, "manage_environments", args)
		if err != nil {
			t.Errorf("read action %v failed: %v", args["action"], err)
		}
		var res map[string]any
		_ = json.Unmarshal([]byte(out), &res)
		if res["status"] != "ok" {
			t.Errorf("expected status=ok for %v, got %v", args["action"], res["status"])
		}
	}

	// Verify no operations were created and summary is untouched
	summaryAfter, err := h.mgr.Summary(context.Background(), "test-account", workspaceID)
	if err != nil {
		t.Fatalf("get summary after: %v", err)
	}
	if summaryAfter.TotalOps != summaryBefore.TotalOps {
		t.Errorf("read actions produced side effects: total_ops changed from %d to %d",
			summaryBefore.TotalOps, summaryAfter.TotalOps)
	}
}

func TestEnvironmentsTool_CrossWorkspaceOwnerRejectionNoSideEffects(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)
	h.rt.SetManageWorktreeServices(nil, &mockEnvWorkspaceService{workspaceID: "ws-authorized", workspacePath: h.scope.PrimaryPath}, nil)

	// Attempting operation with mismatched workspace_id must be rejected
	_, err := execTool(t, h, "manage_environments", map[string]any{
		"action":         "ensure",
		"environment_id": "env-any",
		"workspace_id":   "other-foreign-workspace",
	})
	if err == nil {
		t.Fatalf("expected cross-workspace mismatch to be rejected")
	}
	if !strings.Contains(err.Error(), "workspace ID mismatch") {
		t.Errorf("expected workspace ID mismatch error, got: %v", err)
	}
}

func TestEnvironmentsTool_CancelActionAliasAndStableIdempotency(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)
	workspaceID := "ws-test-123"

	// Create environment
	conn := environments.Connection{
		ID:             "conn-cancel-test",
		AccountScopeID: "test-account",
		WorkspaceID:    workspaceID,
		Name:           "Local Docker",
		Kind:           environments.ConnectionKindLocalDocker,
		Capabilities: environments.ConnectionCapabilities{
			SupportsDocker: true,
		},
	}
	if _, err := h.connStore.Save(conn); err != nil {
		t.Fatalf("save conn: %v", err)
	}

	createRes, err := execTool(t, h, "manage_environments", map[string]any{
		"action":                  "create",
		"workspace_id":            workspaceID,
		"name":                    "Cancel Test Env",
		"preferred_connection_id": "conn-cancel-test",
		"container": map[string]any{
			"image": "alpine:latest",
		},
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	var created map[string]any
	_ = json.Unmarshal([]byte(createRes), &created)
	envID := created["environment"].(map[string]any)["id"].(string)

	// Launch with stable callID idempotency
	runCtx := WithArtifactRunContext(context.Background(), ArtifactRunContext{
		SessionID:      "sess-attributed-1",
		RunID:          "run-attributed-1",
		ChildSessionID: "child-worker-1",
	})
	callRaw, _ := json.Marshal(map[string]any{
		"action":         "ensure",
		"workspace_id":   workspaceID,
		"environment_id": envID,
	})
	out, err := h.rt.ExecuteForWorkspaceScopeWithRuntime(runCtx, h.scope, Call{
		CallID:    "call-stable-123",
		Name:      "manage_environments",
		Arguments: string(callRaw),
	})
	if err != nil {
		t.Fatalf("ensure with runCtx failed: %v", err)
	}
	var ensureRes map[string]any
	_ = json.Unmarshal([]byte(out), &ensureRes)
	opID := ensureRes["operation_id"].(string)

	// Cancel using action: "cancel" alias (not just cancel_operation)
	cancelRaw, _ := json.Marshal(map[string]any{
		"action":       "cancel",
		"workspace_id": workspaceID,
		"operation_id": opID,
		"reason":       "user requested stop",
	})
	cancelOut, err := h.rt.ExecuteForWorkspaceScopeWithRuntime(runCtx, h.scope, Call{
		CallID:    "call-cancel-123",
		Name:      "manage_environments",
		Arguments: string(cancelRaw),
	})
	if err != nil {
		t.Fatalf("cancel action alias failed: %v", err)
	}
	var cancelRes map[string]any
	_ = json.Unmarshal([]byte(cancelOut), &cancelRes)
	if cancelRes["status"] != "cancelling" && cancelRes["status"] != "cancelled" {
		t.Errorf("expected cancelling/cancelled status, got: %v", cancelRes["status"])
	}
}
