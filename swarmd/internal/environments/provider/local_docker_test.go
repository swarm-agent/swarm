package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

type mockCall struct {
	Name string
	Args []string
}

type mockContainerState struct {
	ID         string
	Name       string
	Running    bool
	HostPort   string
	Health     string
	ExitCode   int
	Image      string
	WorkingDir string
}

type mockRunner struct {
	mu         sync.Mutex
	calls      []mockCall
	handlers   map[string]func(args []string) ([]byte, error)
	ioHandlers map[string]func(stdin io.Reader, stdout, stderr io.Writer, args []string) error
	containers map[string]*mockContainerState
	portSeq    int
}

func newMockRunner() *mockRunner {
	m := &mockRunner{
		handlers:   make(map[string]func(args []string) ([]byte, error)),
		ioHandlers: make(map[string]func(stdin io.Reader, stdout, stderr io.Writer, args []string) error),
		containers: make(map[string]*mockContainerState),
		portSeq:    49150,
	}

	// Default handlers simulating Docker daemon
	m.handlers["info"] = func(args []string) ([]byte, error) {
		return []byte("24.0.5\n"), nil
	}
	m.handlers["version"] = func(args []string) ([]byte, error) {
		return []byte("25.0.3\n"), nil
	}
	m.handlers["run"] = func(args []string) ([]byte, error) {
		m.mu.Lock()
		defer m.mu.Unlock()

		var name string
		for i, a := range args {
			if a == "--name" && i+1 < len(args) {
				name = args[i+1]
				break
			}
		}
		if name == "" {
			name = fmt.Sprintf("container-%d", len(m.containers)+1)
		}

		m.portSeq++
		hostPort := fmt.Sprintf("%d", m.portSeq)

		cid := fmt.Sprintf("%012x", m.portSeq)
		cState := &mockContainerState{
			ID:         cid,
			Name:       name,
			Running:    true,
			HostPort:   hostPort,
			Health:     "healthy",
			ExitCode:   0,
			WorkingDir: "/workspace",
		}
		m.containers[name] = cState
		m.containers[cid] = cState
		return []byte(cid + "\n"), nil
	}

	m.handlers["inspect"] = func(args []string) ([]byte, error) {
		m.mu.Lock()
		defer m.mu.Unlock()

		target := args[len(args)-1]
		cState, ok := m.containers[target]
		if !ok {
			return nil, fmt.Errorf("Error: No such object: %s", target)
		}

		status := "stopped"
		if cState.Running {
			status = "running"
		}
		return makeInspectJSON(cState.ID, status, cState.ExitCode, cState.HostPort, cState.Health), nil
	}

	m.handlers["start"] = func(args []string) ([]byte, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		target := args[len(args)-1]
		cState, ok := m.containers[target]
		if !ok {
			return nil, fmt.Errorf("Error: No such object: %s", target)
		}
		cState.Running = true
		return []byte(target + "\n"), nil
	}

	m.handlers["stop"] = func(args []string) ([]byte, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		target := args[len(args)-1]
		cState, ok := m.containers[target]
		if !ok {
			return nil, fmt.Errorf("Error: No such object: %s", target)
		}
		cState.Running = false
		return []byte(target + "\n"), nil
	}

	m.handlers["rm"] = func(args []string) ([]byte, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		target := args[len(args)-1]
		delete(m.containers, target)
		return []byte(target + "\n"), nil
	}
	m.handlers["exec"] = func(args []string) ([]byte, error) {
		argStr := strings.Join(args, " ")
		if strings.Contains(argStr, "swarm-cleanup") {
			return []byte("SWARM_CLEANUP:TERMINATED\n"), nil
		}
		return []byte("exec ok\n"), nil
	}

	return m
}

func (m *mockRunner) getSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-H" && i+1 < len(args) {
			i++
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			continue
		}
		return args[i]
	}
	return ""
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	m.mu.Lock()
	m.calls = append(m.calls, mockCall{Name: name, Args: args})
	subcmd := m.getSubcommand(args)
	handler, ok := m.handlers[subcmd]
	m.mu.Unlock()

	if ok {
		return handler(args)
	}
	return nil, nil
}

func (m *mockRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return m.Run(ctx, name, args...)
}

func (m *mockRunner) RunWithIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	m.mu.Lock()
	m.calls = append(m.calls, mockCall{Name: name, Args: args})
	subcmd := m.getSubcommand(args)
	ioHandler, hasIO := m.ioHandlers[subcmd]
	handler, hasRegular := m.handlers[subcmd]
	m.mu.Unlock()

	if hasIO {
		return ioHandler(stdin, stdout, stderr, args)
	}
	if hasRegular {
		out, err := handler(args)
		if stdout != nil && len(out) > 0 {
			_, _ = stdout.Write(out)
		}
		return err
	}
	return nil
}

func (m *mockRunner) Calls() []mockCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]mockCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

// Sample Docker inspect JSON for testing
func makeInspectJSON(id, status string, exitCode int, hostPort string, healthStatus string) []byte {
	ins := []dockerInspectJSON{
		{
			ID: id,
			State: struct {
				Status     string `json:"Status"`
				Running    bool   `json:"Running"`
				Paused     bool   `json:"Paused"`
				Restarting bool   `json:"Restarting"`
				Dead       bool   `json:"Dead"`
				ExitCode   int    `json:"ExitCode"`
				Error      string `json:"Error"`
				Health     *struct {
					Status        string `json:"Status"`
					FailingStreak int    `json:"FailingStreak"`
				} `json:"Health,omitempty"`
			}{
				Status:   status,
				Running:  status == "running",
				ExitCode: exitCode,
			},
			NetworkSettings: struct {
				IPAddress string `json:"IPAddress"`
				Ports     map[string][]struct {
					HostIP   string `json:"HostIp"`
					HostPort string `json:"HostPort"`
				} `json:"Ports"`
			}{
				IPAddress: "172.17.0.2",
				Ports: map[string][]struct {
					HostIP   string `json:"HostIp"`
					HostPort string `json:"HostPort"`
				}{
					"8080/tcp": {
						{
							HostIP:   "127.0.0.1",
							HostPort: hostPort,
						},
					},
				},
			},
			Config: struct {
				Image      string `json:"Image"`
				WorkingDir string `json:"WorkingDir"`
			}{
				Image:      "ubuntu:22.04",
				WorkingDir: "/workspace",
			},
		},
	}

	if healthStatus != "" {
		ins[0].State.Health = &struct {
			Status        string `json:"Status"`
			FailingStreak int    `json:"FailingStreak"`
		}{
			Status: healthStatus,
		}
	}

	b, _ := json.Marshal(ins)
	return b
}

func TestLocalDockerProvider_Kind(t *testing.T) {
	p := NewLocalDockerProvider(nil)
	if p.Kind() != environments.ConnectionKindLocalDocker {
		t.Fatalf("expected ConnectionKindLocalDocker, got %v", p.Kind())
	}
}

func TestLocalDockerProvider_ValidateConnection(t *testing.T) {
	runner := newMockRunner()
	p := NewLocalDockerProvider(runner)

	conn := &environments.Connection{
		ID:             "conn-local",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Local Docker",
		Kind:           environments.ConnectionKindLocalDocker,
		LocalDocker: &environments.LocalDockerConfig{
			SocketPath: "/var/run/docker.sock",
		},
	}

	ctx := context.Background()
	if err := p.ValidateConnection(ctx, conn); err != nil {
		t.Fatalf("expected validation to pass, got: %v", err)
	}

	// Verify -H unix:///var/run/docker.sock was passed
	calls := runner.Calls()
	if len(calls) == 0 {
		t.Fatal("expected docker info to be called")
	}
	lastCall := calls[len(calls)-1]
	foundHost := false
	for i, arg := range lastCall.Args {
		if arg == "-H" && i+1 < len(lastCall.Args) && lastCall.Args[i+1] == "unix:///var/run/docker.sock" {
			foundHost = true
			break
		}
	}
	if !foundHost {
		t.Fatalf("expected -H unix:///var/run/docker.sock in args, got: %v", lastCall.Args)
	}

	// Test error cases
	if err := p.ValidateConnection(ctx, nil); err == nil {
		t.Fatal("expected error for nil connection")
	}

	sshConn := &environments.Connection{
		ID:             "conn-ssh",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "SSH Server",
		Kind:           environments.ConnectionKindSSH,
		SSH:            &environments.SSHConfig{Host: "example.com", User: "root"},
	}
	if err := p.ValidateConnection(ctx, sshConn); err == nil {
		t.Fatal("expected error for non-local connection kind")
	}

	// Daemon error
	runner.handlers["info"] = func(args []string) ([]byte, error) {
		return []byte("cannot connect to docker daemon"), errors.New("exit status 1")
	}
	if err := p.ValidateConnection(ctx, conn); err == nil {
		t.Fatal("expected error when docker info fails")
	}
}

func TestLocalDockerProvider_Capabilities(t *testing.T) {
	runner := newMockRunner()
	p := NewLocalDockerProvider(runner)

	conn := &environments.Connection{
		ID:             "conn-local",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Local Docker",
		Kind:           environments.ConnectionKindLocalDocker,
	}

	caps, err := p.Capabilities(context.Background(), conn)
	if err != nil {
		t.Fatalf("Capabilities failed: %v", err)
	}
	if !caps.SupportsDocker || !caps.SupportsDirectMount || !caps.SupportsPortForward {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}
	if caps.EngineVersion != "25.0.3" {
		t.Fatalf("expected EngineVersion 25.0.3, got %q", caps.EngineVersion)
	}
}

func TestLocalDockerProvider_Deploy_Basic(t *testing.T) {
	runner := newMockRunner()
	p := NewLocalDockerProvider(runner)

	absHostPath, _ := filepath.Abs("/test/host/workspace")
	req := DeployRequest{
		Connection: &environments.Connection{
			ID:             "conn-1",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			Name:           "Local Docker",
			Kind:           environments.ConnectionKindLocalDocker,
		},
		Environment: &environments.Environment{
			ID:             "env-node",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			Name:           "Node.js Dev",
			Mode:           environments.EnvironmentModeDeployable,
			Role:           environments.EnvironmentRoleDevelopment,
			Container: environments.ContainerDefinition{
				Image:   "node:20",
				Command: []string{"node", "server.js"},
				ExposedPorts: []environments.PortMapping{
					{ContainerPort: 8080, Protocol: "tcp"},
				},
				EnvVars: map[string]string{
					"NODE_ENV": "development",
				},
				WorkingDir: "/app",
			},
			Provisioning: environments.WorkspaceProvisioning{
				Strategy: environments.SourceStrategy{
					Kind: environments.SourceStrategyKindLocalMount,
					LocalMount: &environments.LocalMountConfig{
						HostPath:      absHostPath,
						ContainerPath: "/workspace",
					},
				},
			},
			DeploymentPolicy: environments.DeploymentPolicy{
				Reuse:           true,
				MaxInstances:    2,
				ReleaseBehavior: environments.ReleaseBehaviorRestart,
			},
		},
		Deployment: &environments.Deployment{
			ID:             "dep-101",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			EnvironmentID:  "env-node",
			ConnectionID:   "conn-1",
			Name:           "Node Instance 1",
			Status:         environments.DeploymentStatusPending,
			Health:         environments.HealthStatusUnknown,
		},
		WorkspacePath: absHostPath,
	}

	ctx := context.Background()
	result, err := p.Deploy(ctx, req)
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	if result.Status != environments.DeploymentStatusRunning {
		t.Fatalf("expected status running, got %v", result.Status)
	}
	if result.Health != environments.HealthStatusHealthy {
		t.Fatalf("expected health healthy, got %v", result.Health)
	}
	if result.Runtime.ContainerID == "" {
		t.Fatal("expected non-empty ContainerID")
	}
	if result.Runtime.RemoteWorkspacePath != "/workspace" {
		t.Fatalf("expected RemoteWorkspacePath /workspace, got %q", result.Runtime.RemoteWorkspacePath)
	}
	if len(result.Runtime.AssignedPorts) != 1 {
		t.Fatalf("expected 1 assigned port, got %d", len(result.Runtime.AssignedPorts))
	}
	if result.Runtime.AssignedPorts[0].HostPort <= 0 {
		t.Fatalf("expected valid host port, got %d", result.Runtime.AssignedPorts[0].HostPort)
	}
	if !strings.HasPrefix(result.Runtime.Endpoint, "http://127.0.0.1:") {
		t.Fatalf("expected loopback endpoint, got %q", result.Runtime.Endpoint)
	}

	// Verify docker run arguments
	var runArgs []string
	for _, call := range runner.Calls() {
		if len(call.Args) > 0 && call.Args[0] == "run" {
			runArgs = call.Args
			break
		}
	}
	if len(runArgs) == 0 {
		t.Fatal("expected docker run command to be executed")
	}

	argStr := strings.Join(runArgs, " ")
	if !strings.Contains(argStr, "--name swarm-env-node-dep-101") {
		t.Errorf("missing container name flag: %s", argStr)
	}
	if !strings.Contains(argStr, "--label swarm.deployment_id=dep-101") {
		t.Errorf("missing deployment label: %s", argStr)
	}
	if !strings.Contains(argStr, "-p 127.0.0.1::8080/tcp") {
		t.Errorf("missing loopback port mapping flag: %s", argStr)
	}
	if !strings.Contains(argStr, fmt.Sprintf("-v %s:/workspace", absHostPath)) {
		t.Errorf("missing local_mount volume flag: %s", argStr)
	}
	if !strings.Contains(argStr, "-e NODE_ENV=development") {
		t.Errorf("missing env var flag: %s", argStr)
	}
	if !strings.Contains(argStr, "node:20 node server.js") {
		t.Errorf("missing image and command: %s", argStr)
	}
}

func TestLocalDockerProvider_Deploy_ResourceLimitsAndEnvOverrides(t *testing.T) {
	runner := newMockRunner()
	p := NewLocalDockerProvider(runner)
	absHostPath, _ := filepath.Abs("/workspace")

	req := DeployRequest{
		Connection: &environments.Connection{
			ID:             "conn-1",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			Name:           "Local Docker",
			Kind:           environments.ConnectionKindLocalDocker,
		},
		Environment: &environments.Environment{
			ID:             "env-res",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			Name:           "Resource Constrained",
			Mode:           environments.EnvironmentModeDeployable,
			Role:           environments.EnvironmentRoleTesting,
			Container: environments.ContainerDefinition{
				Image: "golang:1.24",
				EnvVars: map[string]string{
					"BASE_ENV": "base_${TEST_MODE}",
				},
			},
			Resources: &environments.ResourceRequirements{
				CPULimit:    "2.5",
				MemoryLimit: "4Gi",
				GPURequired: true,
				GPUCount:    1,
			},
			Provisioning: environments.WorkspaceProvisioning{
				Strategy: environments.SourceStrategy{
					Kind: environments.SourceStrategyKindLocalMount,
					LocalMount: &environments.LocalMountConfig{
						HostPath:      absHostPath,
						ContainerPath: "/go/src/app",
						ReadOnly:      true,
					},
				},
			},
		},
		Deployment: &environments.Deployment{
			ID:             "dep-res",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			EnvironmentID:  "env-res",
			ConnectionID:   "conn-1",
			Name:           "Test Dep",
			Status:         environments.DeploymentStatusPending,
			Health:         environments.HealthStatusUnknown,
		},
		WorkspacePath: absHostPath,
		EnvOverrides: map[string]string{
			"TEST_MODE":  "fast",
			"CUSTOM_VAR": "custom_val",
		},
	}

	ctx := context.Background()
	_, err := p.Deploy(ctx, req)
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	var runArgs []string
	for _, call := range runner.Calls() {
		if len(call.Args) > 0 && call.Args[0] == "run" {
			runArgs = call.Args
			break
		}
	}

	argStr := strings.Join(runArgs, " ")
	if !strings.Contains(argStr, "--cpus 2.5") {
		t.Errorf("missing --cpus 2.5 in %s", argStr)
	}
	if !strings.Contains(argStr, "--memory 4Gi") {
		t.Errorf("missing --memory 4Gi in %s", argStr)
	}
	if !strings.Contains(argStr, "--gpus count=1") {
		t.Errorf("missing --gpus count=1 in %s", argStr)
	}
	if !strings.Contains(argStr, "-e BASE_ENV=base_fast") {
		t.Errorf("missing expanded env var in %s", argStr)
	}
	if !strings.Contains(argStr, "-e CUSTOM_VAR=custom_val") {
		t.Errorf("missing custom env override in %s", argStr)
	}
	if !strings.Contains(argStr, fmt.Sprintf("-v %s:/go/src/app:ro", absHostPath)) {
		t.Errorf("missing read-only volume spec in %s", argStr)
	}
}

func TestLocalDockerProvider_Deploy_SetupCommands(t *testing.T) {
	runner := newMockRunner()
	var execCommands []string
	runner.handlers["exec"] = func(args []string) ([]byte, error) {
		execCommands = append(execCommands, strings.Join(args, " "))
		return []byte("setup success\n"), nil
	}

	p := NewLocalDockerProvider(runner)
	absHostPath, _ := filepath.Abs("/workspace")

	req := DeployRequest{
		Connection: &environments.Connection{
			ID:             "conn-1",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			Name:           "Local Docker",
			Kind:           environments.ConnectionKindLocalDocker,
		},
		Environment: &environments.Environment{
			ID:             "env-setup",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			Name:           "Setup Test",
			Mode:           environments.EnvironmentModeDeployable,
			Role:           environments.EnvironmentRoleTesting,
			Container: environments.ContainerDefinition{
				Image: "alpine:latest",
				SetupCommands: []string{
					"mkdir -p /data",
					"echo ready > /data/status",
				},
			},
			Provisioning: environments.WorkspaceProvisioning{
				Strategy: environments.SourceStrategy{
					Kind: environments.SourceStrategyKindLocalMount,
					LocalMount: &environments.LocalMountConfig{
						HostPath:      absHostPath,
						ContainerPath: "/workspace",
					},
				},
			},
		},
		Deployment: &environments.Deployment{
			ID:             "dep-setup",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			EnvironmentID:  "env-setup",
			ConnectionID:   "conn-1",
			Name:           "Setup Dep",
			Status:         environments.DeploymentStatusPending,
			Health:         environments.HealthStatusUnknown,
		},
		WorkspacePath: absHostPath,
	}

	ctx := context.Background()
	_, err := p.Deploy(ctx, req)
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	if len(execCommands) != 2 {
		t.Fatalf("expected 2 setup commands, got %d: %v", len(execCommands), execCommands)
	}
	if !strings.Contains(execCommands[0], "mkdir -p /data") {
		t.Errorf("expected mkdir in setup 1: %s", execCommands[0])
	}
	if !strings.Contains(execCommands[1], "echo ready > /data/status") {
		t.Errorf("expected echo in setup 2: %s", execCommands[1])
	}

	// Test setup command failure triggers cleanup and returns error
	var rmCalled bool
	runner.handlers["rm"] = func(args []string) ([]byte, error) {
		rmCalled = true
		return []byte("removed"), nil
	}
	runner.handlers["exec"] = func(args []string) ([]byte, error) {
		return []byte("permission denied"), errors.New("exit status 1")
	}

	// Reset containers to simulate new deployment attempt
	runner.containers = make(map[string]*mockContainerState)

	_, err = p.Deploy(ctx, req)
	if err == nil {
		t.Fatal("expected error on failed setup command")
	}
	if !rmCalled {
		t.Fatal("expected container to be cleaned up via rm after failed setup")
	}
}

func TestLocalDockerProvider_Deploy_HealthCheck(t *testing.T) {
	runner := newMockRunner()
	p := NewLocalDockerProvider(runner)
	var probedURL string
	p.httpGet = func(ctx context.Context, url string) (int, error) {
		probedURL = url
		return 200, nil
	}

	absHostPath, _ := filepath.Abs("/workspace")
	req := DeployRequest{
		Connection: &environments.Connection{
			ID:             "conn-1",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			Name:           "Local Docker",
			Kind:           environments.ConnectionKindLocalDocker,
		},
		Environment: &environments.Environment{
			ID:             "env-hc",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			Name:           "HealthCheck Test",
			Mode:           environments.EnvironmentModeDeployable,
			Role:           environments.EnvironmentRoleTesting,
			Container: environments.ContainerDefinition{
				Image: "nginx:alpine",
				ExposedPorts: []environments.PortMapping{
					{ContainerPort: 8080, Protocol: "tcp"},
				},
			},
			HealthCheck: &environments.HealthCheck{
				Test:               []string{"CMD", "curl", "-f", "http://localhost:8080/healthz"},
				IntervalSeconds:    5,
				TimeoutSeconds:     2,
				Retries:            3,
				StartPeriodSeconds: 1,
				HTTPPath:           "/healthz",
				HTTPPort:           8080,
			},
			Provisioning: environments.WorkspaceProvisioning{
				Strategy: environments.SourceStrategy{
					Kind: environments.SourceStrategyKindLocalMount,
					LocalMount: &environments.LocalMountConfig{
						HostPath:      absHostPath,
						ContainerPath: "/workspace",
					},
				},
			},
		},
		Deployment: &environments.Deployment{
			ID:             "dep-hc",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			EnvironmentID:  "env-hc",
			ConnectionID:   "conn-1",
			Name:           "HC Dep",
			Status:         environments.DeploymentStatusPending,
			Health:         environments.HealthStatusUnknown,
		},
		WorkspacePath: absHostPath,
	}

	ctx := context.Background()
	res, err := p.Deploy(ctx, req)
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	if res.Health != environments.HealthStatusHealthy {
		t.Fatalf("expected HealthStatusHealthy, got %v", res.Health)
	}
	if !strings.HasPrefix(probedURL, "http://127.0.0.1:") || !strings.HasSuffix(probedURL, "/healthz") {
		t.Fatalf("expected probed URL like http://127.0.0.1:<port>/healthz, got %q", probedURL)
	}

	// Verify health check flags on docker run
	var runArgs []string
	for _, call := range runner.Calls() {
		if len(call.Args) > 0 && call.Args[0] == "run" {
			runArgs = call.Args
			break
		}
	}
	argStr := strings.Join(runArgs, " ")
	if !strings.Contains(argStr, "--health-cmd CMD curl -f http://localhost:8080/healthz") {
		t.Errorf("missing health-cmd in %s", argStr)
	}
	if !strings.Contains(argStr, "--health-interval 5s") {
		t.Errorf("missing health-interval in %s", argStr)
	}
	if !strings.Contains(argStr, "--health-timeout 2s") {
		t.Errorf("missing health-timeout in %s", argStr)
	}
	if !strings.Contains(argStr, "--health-retries 3") {
		t.Errorf("missing health-retries in %s", argStr)
	}
}

func TestLocalDockerProvider_Deploy_MultipleDeploymentsSameEnvironment(t *testing.T) {
	runner := newMockRunner()
	p := NewLocalDockerProvider(runner)
	absHostPath, _ := filepath.Abs("/workspace")

	sharedEnv := &environments.Environment{
		ID:             "env-shared",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Shared Test Environment",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "python:3.11-slim",
			ExposedPorts: []environments.PortMapping{
				{ContainerPort: 8000, Protocol: "tcp"},
			},
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindLocalMount,
				LocalMount: &environments.LocalMountConfig{
					HostPath:      absHostPath,
					ContainerPath: "/app",
				},
			},
		},
		DeploymentPolicy: environments.DeploymentPolicy{
			Reuse:        true,
			MaxInstances: 5,
		},
	}

	dep1 := &environments.Deployment{
		ID:             "dep-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		EnvironmentID:  sharedEnv.ID,
		ConnectionID:   "conn-1",
		Name:           "Instance 1",
		Status:         environments.DeploymentStatusPending,
		Health:         environments.HealthStatusUnknown,
	}

	dep2 := &environments.Deployment{
		ID:             "dep-2",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		EnvironmentID:  sharedEnv.ID,
		ConnectionID:   "conn-1",
		Name:           "Instance 2",
		Status:         environments.DeploymentStatusPending,
		Health:         environments.HealthStatusUnknown,
	}

	conn := &environments.Connection{
		ID:             "conn-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Local Docker",
		Kind:           environments.ConnectionKindLocalDocker,
	}

	ctx := context.Background()

	// Deploy first instance
	res1, err := p.Deploy(ctx, DeployRequest{
		Connection:    conn,
		Environment:   sharedEnv,
		Deployment:    dep1,
		WorkspacePath: absHostPath,
	})
	if err != nil {
		t.Fatalf("Deploy 1 failed: %v", err)
	}

	// Deploy second instance from identical sharedEnv definition
	res2, err := p.Deploy(ctx, DeployRequest{
		Connection:    conn,
		Environment:   sharedEnv,
		Deployment:    dep2,
		WorkspacePath: absHostPath,
	})
	if err != nil {
		t.Fatalf("Deploy 2 failed: %v", err)
	}

	// Invariant check: Environment definition was not mutated
	if sharedEnv.ID != "env-shared" || sharedEnv.Container.Image != "python:3.11-slim" {
		t.Fatal("Environment definition was mutated")
	}

	// Deployments have distinct container IDs and mapped host ports
	if res1.Runtime.ContainerID == res2.Runtime.ContainerID {
		t.Fatalf("deployments should have distinct container IDs: %q == %q", res1.Runtime.ContainerID, res2.Runtime.ContainerID)
	}
	if res1.Runtime.Endpoint == res2.Runtime.Endpoint {
		t.Fatalf("deployments should have distinct endpoints: %q == %q", res1.Runtime.Endpoint, res2.Runtime.Endpoint)
	}
}

func TestLocalDockerProvider_Inspect_Statuses(t *testing.T) {
	runner := newMockRunner()
	p := NewLocalDockerProvider(runner)

	conn := &environments.Connection{
		ID:   "conn-1",
		Kind: environments.ConnectionKindLocalDocker,
	}

	tests := []struct {
		name           string
		dockerStatus   string
		exitCode       int
		healthStatus   string
		expectedStatus environments.DeploymentStatus
		expectedHealth environments.HealthStatus
	}{
		{
			name:           "running and healthy",
			dockerStatus:   "running",
			exitCode:       0,
			healthStatus:   "healthy",
			expectedStatus: environments.DeploymentStatusRunning,
			expectedHealth: environments.HealthStatusHealthy,
		},
		{
			name:           "running but unhealthy",
			dockerStatus:   "running",
			exitCode:       0,
			healthStatus:   "unhealthy",
			expectedStatus: environments.DeploymentStatusRunning,
			expectedHealth: environments.HealthStatusUnhealthy,
		},
		{
			name:           "stopped cleanly",
			dockerStatus:   "exited",
			exitCode:       0,
			healthStatus:   "",
			expectedStatus: environments.DeploymentStatusStopped,
			expectedHealth: environments.HealthStatusUnknown,
		},
		{
			name:           "exited with error",
			dockerStatus:   "exited",
			exitCode:       137,
			healthStatus:   "",
			expectedStatus: environments.DeploymentStatusFailed,
			expectedHealth: environments.HealthStatusUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner.containers["cid-test"] = &mockContainerState{
				ID:       "cid-test",
				Name:     "test-container",
				Running:  tc.dockerStatus == "running",
				ExitCode: tc.exitCode,
				HostPort: "49170",
				Health:   tc.healthStatus,
			}

			res, err := p.Inspect(context.Background(), conn, &environments.Deployment{
				ID: "dep-test",
				Runtime: environments.RuntimeMetadata{
					ContainerID: "cid-test",
				},
			})
			if err != nil {
				t.Fatalf("Inspect failed: %v", err)
			}
			if res.Status != tc.expectedStatus {
				t.Errorf("expected status %v, got %v", tc.expectedStatus, res.Status)
			}
			if res.Health != tc.expectedHealth {
				t.Errorf("expected health %v, got %v", tc.expectedHealth, res.Health)
			}
		})
	}
}

func TestLocalDockerProvider_Start_Stop_Destroy(t *testing.T) {
	runner := newMockRunner()
	runner.containers["cid-ssd"] = &mockContainerState{
		ID:       "cid-ssd",
		Name:     "container-ssd",
		Running:  false,
		HostPort: "49175",
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-ssd",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-ssd",
		},
	}

	ctx := context.Background()

	// Start
	if err := p.Start(ctx, conn, dep); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if !runner.containers["cid-ssd"].Running {
		t.Fatal("expected container to be running after Start")
	}

	// Stop
	if err := p.Stop(ctx, conn, dep); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if runner.containers["cid-ssd"].Running {
		t.Fatal("expected container to be stopped after Stop")
	}

	// Destroy
	if err := p.Destroy(ctx, conn, dep); err != nil {
		t.Fatalf("Destroy failed: %v", err)
	}
	if _, exists := runner.containers["cid-ssd"]; exists {
		t.Fatal("expected container to be deleted from containers map after Destroy")
	}
}

func TestLocalDockerProvider_ResolveAccess(t *testing.T) {
	runner := newMockRunner()
	runner.containers["cid-access"] = &mockContainerState{
		ID:         "cid-access",
		Name:       "access-container",
		Running:    true,
		HostPort:   "49180",
		Health:     "healthy",
		WorkingDir: "/workspace",
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-access",
		Runtime: environments.RuntimeMetadata{
			ContainerID:         "cid-access",
			RemoteWorkspacePath: "/workspace",
		},
	}

	access, err := p.ResolveAccess(context.Background(), conn, dep)
	if err != nil {
		t.Fatalf("ResolveAccess failed: %v", err)
	}

	if !access.ExecSupported {
		t.Fatal("expected ExecSupported to be true")
	}
	if access.PrimaryEndpoint != "http://127.0.0.1:49180" {
		t.Fatalf("expected primary endpoint http://127.0.0.1:49180, got %q", access.PrimaryEndpoint)
	}
	if access.Endpoints["8080"] != "http://127.0.0.1:49180" {
		t.Fatalf("expected endpoint for 8080, got %v", access.Endpoints)
	}
	if access.RemoteWorkspacePath != "/workspace" {
		t.Fatalf("expected RemoteWorkspacePath /workspace, got %q", access.RemoteWorkspacePath)
	}
	if len(access.MappedPorts) != 1 || access.MappedPorts[0].HostPort != 49180 {
		t.Fatalf("unexpected mapped ports: %+v", access.MappedPorts)
	}
	if access.ContainerID != "cid-access" {
		t.Fatalf("expected ContainerID cid-access, got %q", access.ContainerID)
	}
}

func TestLocalDockerProvider_Exec(t *testing.T) {
	runner := newMockRunner()
	var executedArgs []string
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
		executedArgs = args
		_, _ = stdout.Write([]byte("total 4\n-rw-r--r-- 1 root root 12 Sep 16 12:00 file.txt\n"))
		return nil
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-exec",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-exec",
		},
	}

	req := ExecRequest{
		Command:    []string{"ls", "-la"},
		WorkingDir: "/workspace",
		Env: map[string]string{
			"FOO": "BAR",
		},
		Timeout: 5 * time.Second,
	}

	res, err := p.Exec(context.Background(), conn, dep, req)
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}

	if !res.Success() {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "file.txt") {
		t.Fatalf("expected file.txt in stdout, got %q", res.Stdout)
	}
	if res.Stderr != "" {
		t.Fatalf("expected empty stderr, got %q", res.Stderr)
	}

	argStr := strings.Join(executedArgs, " ")
	if !strings.Contains(argStr, "exec -w /workspace -e FOO=BAR cid-exec") {
		t.Fatalf("unexpected executed args: %s", argStr)
	}
	if !strings.Contains(argStr, "swarm-supervisor") || !strings.Contains(argStr, "ls -la") {
		t.Fatalf("missing supervisor or command in executed args: %s", argStr)
	}
	if res.OperationID == "" {
		t.Fatal("expected non-empty OperationID")
	}
	if res.LastObservedAt.IsZero() {
		t.Fatal("expected non-zero LastObservedAt")
	}

	// Test empty command returns error
	_, err = p.Exec(context.Background(), conn, dep, ExecRequest{Command: nil})
	if err == nil {
		t.Fatal("expected error for empty command")
	}

	// Test non-zero exit code
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
		_, _ = stderr.Write([]byte("cat: nonexistent: No such file or directory\n"))
		return exec.Command("false").Run()
	}

	failRes, err := p.Exec(context.Background(), conn, dep, ExecRequest{Command: []string{"cat", "nonexistent"}})
	if err != nil {
		t.Fatalf("Exec returned unexpected Go error: %v", err)
	}
	if failRes.ExitCode == 0 {
		t.Fatal("expected non-zero exit code for failed command")
	}
	if !strings.Contains(failRes.Stderr, "No such file or directory") {
		t.Fatalf("expected stderr output, got %q", failRes.Stderr)
	}
	if failRes.Success() {
		t.Fatal("failRes.Success() should be false")
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()
	local := NewLocalDockerProvider(nil)
	reg.Register(local)

	p, ok := reg.Get(environments.ConnectionKindLocalDocker)
	if !ok || p == nil {
		t.Fatal("expected to find local_docker provider in registry")
	}

	_, ok = reg.Get(environments.ConnectionKindSSH)
	if ok {
		t.Fatal("did not expect to find ssh provider in registry before registration")
	}

	providers := reg.Providers()
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
}

func TestLocalDockerProvider_Deploy_ValidationErrors(t *testing.T) {
	runner := newMockRunner()
	p := NewLocalDockerProvider(runner)
	absHostPath, _ := filepath.Abs("/workspace")

	conn := &environments.Connection{
		ID:             "conn-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Local Docker",
		Kind:           environments.ConnectionKindLocalDocker,
	}

	env := &environments.Environment{
		ID:             "env-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Test Env",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "alpine:latest",
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindLocalMount,
				LocalMount: &environments.LocalMountConfig{
					HostPath:      absHostPath,
					ContainerPath: "/workspace",
				},
			},
		},
	}

	dep := &environments.Deployment{
		ID:             "dep-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		EnvironmentID:  "env-1",
		ConnectionID:   "conn-1",
		Name:           "Test Dep",
		Status:         environments.DeploymentStatusPending,
		Health:         environments.HealthStatusUnknown,
	}

	ctx := context.Background()

	// Nil connection
	if _, err := p.Deploy(ctx, DeployRequest{Environment: env, Deployment: dep}); err == nil {
		t.Error("expected error for nil connection")
	}

	// Nil environment
	if _, err := p.Deploy(ctx, DeployRequest{Connection: conn, Deployment: dep}); err == nil {
		t.Error("expected error for nil environment")
	}

	// Nil deployment
	if _, err := p.Deploy(ctx, DeployRequest{Connection: conn, Environment: env}); err == nil {
		t.Error("expected error for nil deployment")
	}

	// Unsupported connection kind
	sshConn := &environments.Connection{
		ID:             "conn-ssh",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "SSH Conn",
		Kind:           environments.ConnectionKindSSH,
		SSH:            &environments.SSHConfig{Host: "remote.host", User: "root"},
	}
	if _, err := p.Deploy(ctx, DeployRequest{Connection: sshConn, Environment: env, Deployment: dep}); err == nil {
		t.Error("expected error for SSH connection on local docker provider")
	}

	// Empty image
	invalidEnv := env.Clone()
	invalidEnv.Container.Image = ""
	if _, err := p.Deploy(ctx, DeployRequest{Connection: conn, Environment: invalidEnv, Deployment: dep}); err == nil {
		t.Error("expected error for empty container image")
	}

	// Local mount empty host path and empty workspace path
	noHostPathEnv := env.Clone()
	noHostPathEnv.Provisioning.Strategy.LocalMount.HostPath = ""
	if _, err := p.Deploy(ctx, DeployRequest{Connection: conn, Environment: noHostPathEnv, Deployment: dep, WorkspacePath: ""}); err == nil {
		t.Error("expected error when local_mount host path and workspace path are both empty")
	}
}

func TestLocalDockerProvider_StartStopDestroy_Errors(t *testing.T) {
	runner := newMockRunner()
	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	ctx := context.Background()

	// Nil deployment
	if err := p.Start(ctx, conn, nil); err == nil {
		t.Error("expected error for nil deployment in Start")
	}
	if err := p.Stop(ctx, conn, nil); err == nil {
		t.Error("expected error for nil deployment in Stop")
	}
	if err := p.Destroy(ctx, conn, nil); err == nil {
		t.Error("expected error for nil deployment in Destroy")
	}

	// Nonexistent container error from runner
	runner.handlers["start"] = func(args []string) ([]byte, error) {
		return []byte("Error: No such container"), errors.New("exit status 1")
	}
	dep := &environments.Deployment{ID: "dep-missing"}
	if err := p.Start(ctx, conn, dep); err == nil {
		t.Error("expected error when container does not exist in Start")
	}
}

func TestLocalDockerProvider_ResolveAccess_Fallback(t *testing.T) {
	runner := newMockRunner()
	// Inspect fails because container is not in mockRunner
	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}

	dep := &environments.Deployment{
		ID: "dep-cached",
		Runtime: environments.RuntimeMetadata{
			ContainerID:         "cid-cached",
			Endpoint:            "http://127.0.0.1:49200",
			RemoteWorkspacePath: "/workspace",
			AssignedPorts: []environments.AssignedPort{
				{ContainerPort: 8080, HostPort: 49200, Protocol: "tcp", EndpointURL: "http://127.0.0.1:49200"},
			},
		},
	}

	access, err := p.ResolveAccess(context.Background(), conn, dep)
	if err != nil {
		t.Fatalf("expected fallback to cached runtime, got error: %v", err)
	}
	if access.PrimaryEndpoint != "http://127.0.0.1:49200" {
		t.Fatalf("expected primary endpoint http://127.0.0.1:49200, got %q", access.PrimaryEndpoint)
	}
	if !access.ExecSupported {
		t.Error("expected ExecSupported true")
	}

	// Nil deployment
	if _, err := p.ResolveAccess(context.Background(), conn, nil); err == nil {
		t.Error("expected error for nil deployment")
	}
}

func TestExecResult_Helpers(t *testing.T) {
	successRes := &ExecResult{
		ExitCode: 0,
		Stdout:   "hello",
		Stderr:   "",
	}
	if !successRes.Success() {
		t.Error("expected Success() true for exit code 0")
	}
	if successRes.CombinedOutput() != "hello" {
		t.Errorf("expected combined output 'hello', got %q", successRes.CombinedOutput())
	}

	failRes := &ExecResult{
		ExitCode: 1,
		Stdout:   "partial output",
		Stderr:   "fatal error",
	}
	if failRes.Success() {
		t.Error("expected Success() false for exit code 1")
	}
	if failRes.CombinedOutput() != "partial output\nfatal error" {
		t.Errorf("expected combined output with both, got %q", failRes.CombinedOutput())
	}

	var nilRes *ExecResult
	if nilRes.Success() {
		t.Error("expected Success() false for nil ExecResult")
	}
	if nilRes.CombinedOutput() != "" {
		t.Error("expected empty string for nil ExecResult CombinedOutput")
	}
}

func TestLocalDockerProvider_Exec_WithStdin(t *testing.T) {
	runner := newMockRunner()
	var capturedStdin string
	var hasStdinFlag bool

	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
		for _, a := range args {
			if a == "-i" {
				hasStdinFlag = true
				break
			}
		}
		if stdin != nil {
			buf := new(strings.Builder)
			_, _ = io.Copy(buf, stdin)
			capturedStdin = buf.String()
		}
		_, _ = stdout.Write([]byte("stdin received: " + capturedStdin))
		return nil
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-stdin",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-stdin",
		},
	}

	req := ExecRequest{
		Command: []string{"cat"},
		Stdin:   strings.NewReader("sample payload input\n"),
	}

	res, err := p.Exec(context.Background(), conn, dep, req)
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}

	if !hasStdinFlag {
		t.Error("expected -i flag when Stdin is provided")
	}
	if capturedStdin != "sample payload input\n" {
		t.Errorf("expected captured stdin, got %q", capturedStdin)
	}
	if !strings.Contains(res.Stdout, "sample payload input") {
		t.Errorf("expected echo of stdin in stdout, got %q", res.Stdout)
	}
}

func TestLocalDockerProvider_Exec_TimeoutAndCleanup(t *testing.T) {
	runner := newMockRunner()
	var cleanupCalled bool
	var cleanupOpID string

	runner.handlers["exec"] = func(args []string) ([]byte, error) {
		argStr := strings.Join(args, " ")
		if strings.Contains(argStr, "swarm-cleanup") {
			cleanupCalled = true
			for i, a := range args {
				if a == "swarm-cleanup" && i+1 < len(args) {
					cleanupOpID = args[i+1]
				}
			}
			return []byte("SWARM_CLEANUP:TERMINATED\n"), nil
		}
		return []byte("exec ok\n"), nil
	}

	execCtx, cancelExec := context.WithCancel(context.Background())
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
		cancelExec()
		return execCtx.Err()
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-timeout",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-timeout",
		},
	}

	req := ExecRequest{
		OperationID: "op-timeout-test",
		Command:     []string{"sleep", "60"},
		Timeout:     5 * time.Second,
	}

	_, err := p.Exec(execCtx, conn, dep, req)
	if err == nil {
		t.Fatal("expected error on timed out/cancelled exec")
	}
	if !errors.Is(err, ErrOperationCancelled) {
		t.Fatalf("expected ErrOperationCancelled, got: %v", err)
	}
	if !cleanupCalled {
		t.Fatal("expected cleanup command to be invoked on cancel/timeout")
	}
	if cleanupOpID != "op-timeout-test" {
		t.Fatalf("expected cleanup for op-timeout-test, got: %q", cleanupOpID)
	}
}

func TestLocalDockerProvider_Exec_DeadlineExceeded(t *testing.T) {
	runner := newMockRunner()
	var cleanupCalled bool
	var cleanupOpID string

	runner.handlers["exec"] = func(args []string) ([]byte, error) {
		argStr := strings.Join(args, " ")
		if strings.Contains(argStr, "swarm-cleanup") {
			cleanupCalled = true
			for i, a := range args {
				if a == "swarm-cleanup" && i+1 < len(args) {
					cleanupOpID = args[i+1]
				}
			}
			return []byte("SWARM_CLEANUP:TERMINATED\n"), nil
		}
		return []byte("exec ok\n"), nil
	}

	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
		time.Sleep(30 * time.Millisecond)
		return context.DeadlineExceeded
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-deadline",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-deadline",
		},
	}
	req := ExecRequest{
		OperationID: "op-deadline-test",
		Command:     []string{"sleep", "60"},
		Timeout:     10 * time.Millisecond,
	}

	_, err := p.Exec(context.Background(), conn, dep, req)
	if err == nil {
		t.Fatal("expected error on timed out exec")
	}
	if !errors.Is(err, ErrOperationTimedOut) {
		t.Fatalf("expected ErrOperationTimedOut, got: %v", err)
	}
	if !cleanupCalled {
		t.Fatal("expected cleanup command to be invoked on timeout")
	}
	if cleanupOpID != "op-deadline-test" {
		t.Fatalf("expected cleanup for op-deadline-test, got: %q", cleanupOpID)
	}
}

func TestLocalDockerProvider_Exec_ProbePrerequisitesFailed(t *testing.T) {
	runner := newMockRunner()
	runner.handlers["exec"] = func(args []string) ([]byte, error) {
		argStr := strings.Join(args, " ")
		if strings.Contains(argStr, probeScript) {
			return []byte("mkdir: /run/swarm/operations: Read-only file system\n"), errors.New("exit status 1")
		}
		return []byte("exec ok\n"), nil
	}

	var ioCalled bool
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
		ioCalled = true
		return nil
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-fail-probe",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-fail-probe",
		},
	}

	req := ExecRequest{
		Command: []string{"echo", "hi"},
	}

	_, err := p.Exec(context.Background(), conn, dep, req)
	if err == nil {
		t.Fatal("expected error when supervisor primitives unavailable")
	}
	if !errors.Is(err, ErrSupervisorUnavailable) {
		t.Fatalf("expected ErrSupervisorUnavailable, got: %v", err)
	}
	if ioCalled {
		t.Fatal("command execution must not proceed when probe fails (fail closed)")
	}
}

func TestLocalDockerProvider_Exec_InvalidOperationID(t *testing.T) {
	runner := newMockRunner()
	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-1",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-1",
		},
	}

	invalidIDs := []string{
		"../../etc/passwd",
		"op; rm -rf /",
		"op`id`",
		"op$(id)",
		"op\nkill 1",
		"op space",
		strings.Repeat("a", 129),
		".",
		"..",
	}

	for _, badID := range invalidIDs {
		_, err := p.Exec(context.Background(), conn, dep, ExecRequest{
			OperationID: badID,
			Command:     []string{"echo", "hi"},
		})
		if err == nil {
			t.Errorf("expected error for invalid operation ID %q", badID)
		}
		if !errors.Is(err, ErrInvalidOperationID) {
			t.Errorf("expected ErrInvalidOperationID for %q, got: %v", badID, err)
		}
	}
}

func TestLocalDockerProvider_Exec_OutputCap(t *testing.T) {
	runner := newMockRunner()
	largeOutput := strings.Repeat("A", 10000)
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
		_, _ = stdout.Write([]byte(largeOutput))
		return nil
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-cap",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-cap",
		},
	}

	req := ExecRequest{
		Command:   []string{"cat", "large"},
		MaxOutput: 1024,
	}

	res, err := p.Exec(context.Background(), conn, dep, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Stdout) != 1024 {
		t.Fatalf("expected stdout bounded to 1024 bytes, got %d", len(res.Stdout))
	}
	if !res.Truncated {
		t.Fatal("expected Truncated to be true")
	}
}

func TestLocalDockerProvider_Exec_ProgressCallback(t *testing.T) {
	runner := newMockRunner()
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
		_, _ = stdout.Write([]byte("progress 1\n"))
		_, _ = stdout.Write([]byte("progress 2\n"))
		return nil
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-prog",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-prog",
		},
	}

	var events []ExecProgress
	req := ExecRequest{
		OperationID: "op-progress-test",
		Command:     []string{"my-task"},
		OnProgress: func(pr ExecProgress) {
			events = append(events, pr)
		},
	}

	res, err := p.Exec(context.Background(), conn, dep, req)
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 progress callbacks, got %d", len(events))
	}
	for i, ev := range events {
		if ev.OperationID != "op-progress-test" {
			t.Errorf("event %d has wrong OperationID: %q", i, ev.OperationID)
		}
		if ev.Stream != "stdout" {
			t.Errorf("event %d has wrong stream: %q", i, ev.Stream)
		}
		if ev.Timestamp.IsZero() {
			t.Errorf("event %d has zero timestamp", i)
		}
	}
	if res.LastObservedAt.IsZero() {
		t.Fatal("expected non-zero LastObservedAt")
	}
}

func TestLocalDockerProvider_CancelExec_Success(t *testing.T) {
	runner := newMockRunner()
	var cleanupArgs []string
	runner.handlers["exec"] = func(args []string) ([]byte, error) {
		cleanupArgs = args
		return []byte("SWARM_CLEANUP:TERMINATED\n"), nil
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-cancel",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-cancel",
		},
	}

	req := CancelExecRequest{
		OperationID: "op-cancel-123",
		GracePeriod: 3 * time.Second,
	}

	res, err := p.CancelExec(context.Background(), conn, dep, req)
	if err != nil {
		t.Fatalf("CancelExec failed: %v", err)
	}
	if !res.Terminated {
		t.Fatal("expected Terminated true")
	}
	if res.OperationID != "op-cancel-123" {
		t.Fatalf("expected OperationID op-cancel-123, got: %s", res.OperationID)
	}
	if res.SignalSent != "SIGTERM/SIGKILL" {
		t.Fatalf("expected SignalSent SIGTERM/SIGKILL, got: %s", res.SignalSent)
	}
	joined := strings.Join(cleanupArgs, " ")
	if !strings.Contains(joined, "swarm-cleanup op-cancel-123 3") {
		t.Fatalf("unexpected cleanup invocation args: %s", joined)
	}
}

func TestLocalDockerProvider_CancelExec_PostRestart(t *testing.T) {
	runner := newMockRunner()
	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-restarted",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-restarted",
		},
	}

	res, err := p.CancelExec(context.Background(), conn, dep, CancelExecRequest{
		OperationID: "op-prior-daemon-run",
	})
	if err != nil {
		t.Fatalf("CancelExec post-restart failed: %v", err)
	}
	if !res.Terminated {
		t.Fatal("expected Terminated true for post-restart cancellation")
	}
}

func TestLocalDockerProvider_CancelExec_PIDReuse(t *testing.T) {
	runner := newMockRunner()
	runner.handlers["exec"] = func(args []string) ([]byte, error) {
		return []byte("SWARM_CLEANUP:PID_REUSE_DETECTED\n"), nil
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-reuse",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-reuse",
		},
	}

	res, err := p.CancelExec(context.Background(), conn, dep, CancelExecRequest{
		OperationID: "op-reused",
	})
	if err != nil {
		t.Fatalf("CancelExec with PID reuse returned unexpected error: %v", err)
	}
	if !res.Terminated {
		t.Fatal("expected Terminated true when PID reuse detected (process already exited)")
	}
	if !strings.Contains(res.ErrorMessage, "reused") {
		t.Fatalf("expected ErrorMessage mentioning PID reuse, got: %q", res.ErrorMessage)
	}
}

func TestLocalDockerProvider_CancelExec_CleanupFailure(t *testing.T) {
	runner := newMockRunner()
	runner.handlers["exec"] = func(args []string) ([]byte, error) {
		return []byte("SWARM_CLEANUP:CLEANUP_FAILED\n"), errors.New("exit status 1")
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-fail-clean",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-fail-clean",
		},
	}

	res, err := p.CancelExec(context.Background(), conn, dep, CancelExecRequest{
		OperationID: "op-failed-clean",
	})
	if err == nil {
		t.Fatal("expected error on cleanup failure")
	}
	if !errors.Is(err, ErrOperationCleanupFailed) {
		t.Fatalf("expected ErrOperationCleanupFailed, got: %v", err)
	}
	if res.Terminated {
		t.Fatal("expected Terminated false when cleanup failed")
	}
}

func TestLocalDockerProvider_Exec_RedactedEnvInError(t *testing.T) {
	runner := newMockRunner()
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
		return errors.New("daemon connection lost")
	}

	p := NewLocalDockerProvider(runner)
	conn := &environments.Connection{ID: "conn-1", Kind: environments.ConnectionKindLocalDocker}
	dep := &environments.Deployment{
		ID: "dep-secret",
		Runtime: environments.RuntimeMetadata{
			ContainerID: "cid-secret",
		},
	}

	secretVal := "super-sensitive-token-12345"
	req := ExecRequest{
		Command: []string{"run-task"},
		Env: map[string]string{
			"SECRET_KEY": secretVal,
		},
	}

	_, err := p.Exec(context.Background(), conn, dep, req)
	if err == nil {
		t.Fatal("expected error on exec failure")
	}
	if strings.Contains(err.Error(), secretVal) {
		t.Fatalf("secret value leaked in error message: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "SECRET_KEY=[REDACTED]") {
		t.Fatalf("expected redacted key in error message, got: %s", err.Error())
	}
}
