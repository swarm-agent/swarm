package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// mockSSHRunner intercepts ssh command executions and simulates remote Docker daemon behavior.
type mockSSHRunner struct {
	mu          sync.Mutex
	calls       []mockCall
	handlers    map[string]func(remoteSubcmd string, remoteArgs []string) ([]byte, error)
	ioHandlers  map[string]func(stdin io.Reader, stdout, stderr io.Writer, remoteSubcmd string, remoteArgs []string) error
	containers  map[string]*mockContainerState
	portSeq     int
	failSSH     bool
	failDocker  bool
	customError error
}

func newMockSSHRunner() *mockSSHRunner {
	m := &mockSSHRunner{
		handlers:   make(map[string]func(remoteSubcmd string, remoteArgs []string) ([]byte, error)),
		ioHandlers: make(map[string]func(stdin io.Reader, stdout, stderr io.Writer, remoteSubcmd string, remoteArgs []string) error),
		containers: make(map[string]*mockContainerState),
		portSeq:    49150,
	}

	// Default remote Docker handlers
	m.handlers["info"] = func(subcmd string, args []string) ([]byte, error) {
		return []byte("24.0.7\n"), nil
	}
	m.handlers["version"] = func(subcmd string, args []string) ([]byte, error) {
		return []byte("25.0.3\n"), nil
	}
	m.handlers["run"] = func(subcmd string, args []string) ([]byte, error) {
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
	m.handlers["inspect"] = func(subcmd string, args []string) ([]byte, error) {
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
	m.handlers["start"] = func(subcmd string, args []string) ([]byte, error) {
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
	m.handlers["stop"] = func(subcmd string, args []string) ([]byte, error) {
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
	m.handlers["rm"] = func(subcmd string, args []string) ([]byte, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		target := args[len(args)-1]
		delete(m.containers, target)
		return []byte(target + "\n"), nil
	}
	m.handlers["exec"] = func(subcmd string, args []string) ([]byte, error) {
		argStr := strings.Join(args, " ")
		if strings.Contains(argStr, "swarm-cleanup") {
			return []byte("SWARM_CLEANUP:TERMINATED\n"), nil
		}
		return []byte("exec ok\n"), nil
	}

	return m
}

func (m *mockSSHRunner) parseRemoteCommand(args []string) (dest string, remoteSubcmd string, remoteArgs []string) {
	// Skip SSH flags (-o ..., -p ..., -i ..., -n)
	i := 0
	for i < len(args) {
		arg := args[i]
		if arg == "-o" || arg == "-p" || arg == "-i" {
			i += 2
			continue
		}
		if arg == "-n" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			i++
			continue
		}
		// First non-flag is destination
		dest = arg
		i++
		break
	}

	if i < len(args) {
		remoteArgs = args[i:]
		// Unquote remoteArgs if quoted
		for j, ra := range remoteArgs {
			if strings.HasPrefix(ra, "'") && strings.HasSuffix(ra, "'") && len(ra) >= 2 {
				remoteArgs[j] = strings.ReplaceAll(ra[1:len(ra)-1], "'\\''", "'")
			}
		}
		// find remote docker subcommand
		for k, ra := range remoteArgs {
			if ra == "docker" && k+1 < len(remoteArgs) {
				remoteSubcmd = remoteArgs[k+1]
				break
			}
		}
	}
	return dest, remoteSubcmd, remoteArgs
}

func (m *mockSSHRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	m.mu.Lock()
	m.calls = append(m.calls, mockCall{Name: name, Args: args})
	if m.failSSH {
		m.mu.Unlock()
		return []byte("ssh: connect to host example.com port 22: Connection refused\n"), &exec.ExitError{}
	}
	if m.failDocker {
		m.mu.Unlock()
		return []byte("bash: docker: command not found\n"), &exec.ExitError{}
	}
	if m.customError != nil {
		err := m.customError
		m.mu.Unlock()
		return []byte(err.Error()), err
	}

	_, remoteSubcmd, remoteArgs := m.parseRemoteCommand(args)
	handler, ok := m.handlers[remoteSubcmd]
	m.mu.Unlock()

	if ok {
		return handler(remoteSubcmd, remoteArgs)
	}
	return nil, nil
}

func (m *mockSSHRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return m.Run(ctx, name, args...)
}

func (m *mockSSHRunner) RunWithIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	m.mu.Lock()
	m.calls = append(m.calls, mockCall{Name: name, Args: args})
	if m.customError != nil {
		err := m.customError
		m.mu.Unlock()
		if stderr != nil {
			_, _ = stderr.Write([]byte(err.Error()))
		}
		return err
	}

	_, remoteSubcmd, remoteArgs := m.parseRemoteCommand(args)
	ioHandler, hasIO := m.ioHandlers[remoteSubcmd]
	handler, hasRegular := m.handlers[remoteSubcmd]
	m.mu.Unlock()

	if hasIO {
		return ioHandler(stdin, stdout, stderr, remoteSubcmd, remoteArgs)
	}
	if hasRegular {
		out, err := handler(remoteSubcmd, remoteArgs)
		if stdout != nil && len(out) > 0 {
			_, _ = stdout.Write(out)
		}
		return err
	}
	return nil
}

func (m *mockSSHRunner) Calls() []mockCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]mockCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

func testSSHConnection() *environments.Connection {
	return &environments.Connection{
		ID:             "conn-ssh-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Remote Dev Server",
		Kind:           environments.ConnectionKindSSH,
		SSH: &environments.SSHConfig{
			Host: "remote.example.com",
			Port: 2222,
			User: "deploy",
		},
	}
}

func TestSSHDockerProvider_Kind(t *testing.T) {
	p := NewSSHDockerProvider(nil)
	if p.Kind() != environments.ConnectionKindSSH {
		t.Fatalf("expected ConnectionKindSSH, got %v", p.Kind())
	}
}

func TestSSHDockerProvider_ValidateConnection_Success(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)

	conn := testSSHConnection()
	ctx := context.Background()

	err := p.ValidateConnection(ctx, conn)
	if err != nil {
		t.Fatalf("expected validation to pass, got: %v", err)
	}

	calls := runner.Calls()
	if len(calls) == 0 {
		t.Fatal("expected ssh command to be called")
	}

	// Verify SSH command structure
	foundDockerInfo := false
	for _, call := range calls {
		if call.Name == "ssh" {
			joined := strings.Join(call.Args, " ")
			if strings.Contains(joined, "BatchMode=yes") &&
				strings.Contains(joined, "ConnectTimeout=10") &&
				strings.Contains(joined, "-p 2222") &&
				strings.Contains(joined, "deploy@remote.example.com") &&
				strings.Contains(joined, "docker") &&
				strings.Contains(joined, "info") {
				foundDockerInfo = true
				break
			}
		}
	}
	if !foundDockerInfo {
		t.Fatal("expected ssh call with BatchMode, port, user@host, and docker info")
	}
}

func TestSSHDockerProvider_ValidateConnection_SSHFailure(t *testing.T) {
	runner := newMockSSHRunner()
	runner.failSSH = true
	p := NewSSHDockerProvider(runner)

	conn := testSSHConnection()
	err := p.ValidateConnection(context.Background(), conn)
	if err == nil {
		t.Fatal("expected validation to fail on ssh error")
	}
	if !strings.Contains(err.Error(), "ssh connection") && !strings.Contains(err.Error(), "failed") {
		t.Fatalf("expected informative ssh failure error, got: %v", err)
	}
}

func TestSSHDockerProvider_ValidateConnection_DockerMissing(t *testing.T) {
	runner := newMockSSHRunner()
	runner.failDocker = true
	p := NewSSHDockerProvider(runner)

	conn := testSSHConnection()
	err := p.ValidateConnection(context.Background(), conn)
	if err == nil {
		t.Fatal("expected validation to fail when docker is missing")
	}
	if !strings.Contains(err.Error(), "docker is not installed") && !strings.Contains(err.Error(), "failed") {
		t.Fatalf("expected docker missing or failure error, got: %v", err)
	}
}

func TestSSHDockerProvider_ValidateConnection_Invalid(t *testing.T) {
	p := NewSSHDockerProvider(newMockSSHRunner())
	ctx := context.Background()

	if err := p.ValidateConnection(ctx, nil); err == nil {
		t.Fatal("expected error for nil connection")
	}

	wrongKind := testSSHConnection()
	wrongKind.Kind = environments.ConnectionKindLocalDocker
	if err := p.ValidateConnection(ctx, wrongKind); err == nil {
		t.Fatal("expected error for wrong connection kind")
	}

	noSSH := testSSHConnection()
	noSSH.SSH = nil
	if err := p.ValidateConnection(ctx, noSSH); err == nil {
		t.Fatal("expected error for connection without ssh config")
	}
}

func TestSSHDockerProvider_Capabilities(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)

	conn := testSSHConnection()
	caps, err := p.Capabilities(context.Background(), conn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !caps.SupportsDocker {
		t.Fatal("expected SupportsDocker to be true")
	}
	if !caps.SupportsSSH {
		t.Fatal("expected SupportsSSH to be true")
	}
	if caps.SupportsDirectMount {
		t.Fatal("expected SupportsDirectMount to be false on remote SSH host")
	}
	if !caps.SupportsPortForward {
		t.Fatal("expected SupportsPortForward to be true")
	}
	if caps.EngineVersion != "25.0.3" {
		t.Fatalf("expected engine version 25.0.3, got %q", caps.EngineVersion)
	}
}

func TestSSHDockerProvider_Deploy_RemoteExistingPath(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)

	conn := testSSHConnection()
	env := &environments.Environment{
		ID:             "env-node",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Node Testing Env",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "node:20",
			EnvVars: map[string]string{
				"NODE_ENV": "test",
			},
			ExposedPorts: []environments.PortMapping{
				{ContainerPort: 3000, HostPort: 13000, Protocol: "tcp"},
			},
			WorkingDir: "/app",
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindRemoteExistingPath,
				RemoteExistingPath: &environments.RemoteExistingPathConfig{
					RemotePath:    "/srv/repos/my-app",
					ContainerPath: "/app",
					ReadOnly:      false,
				},
			},
			ContainerWorkingDir: "/app",
		},
		DeploymentPolicy: environments.DeploymentPolicy{
			Reuse:           true,
			MaxInstances:    1,
			ReleaseBehavior: environments.ReleaseBehaviorNone,
		},
	}

	dep := &environments.Deployment{
		ID:             "dep-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		EnvironmentID:  "env-node",
		ConnectionID:   "conn-ssh-1",
		Name:           "Deployment 1",
		Status:         environments.DeploymentStatusPending,
	}

	ctx := context.Background()
	res, err := p.Deploy(ctx, DeployRequest{
		Connection:  conn,
		Environment: env,
		Deployment:  dep,
	})
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	if res.Status != environments.DeploymentStatusRunning {
		t.Fatalf("expected status running, got: %s", res.Status)
	}
	if res.Runtime.RemoteWorkspacePath != "/app" {
		t.Fatalf("expected remote workspace path /app, got: %s", res.Runtime.RemoteWorkspacePath)
	}

	// Check calls to verify `docker run` received remote_existing_path mount
	calls := runner.Calls()
	var runArgs []string
	for _, call := range calls {
		joined := strings.Join(call.Args, " ")
		if strings.Contains(joined, "run") && strings.Contains(joined, "node:20") {
			runArgs = call.Args
			break
		}
	}
	if len(runArgs) == 0 {
		t.Fatal("expected docker run command in calls")
	}

	joinedRun := strings.Join(runArgs, " ")
	if !strings.Contains(joinedRun, "-v /srv/repos/my-app:/app") {
		t.Fatalf("expected -v /srv/repos/my-app:/app in run args, got: %s", joinedRun)
	}
	if !strings.Contains(joinedRun, "-w /app") {
		t.Fatalf("expected -w /app in run args, got: %s", joinedRun)
	}
	if !strings.Contains(joinedRun, "-p 13000:3000/tcp") {
		t.Fatalf("expected -p 13000:3000/tcp in run args, got: %s", joinedRun)
	}
}

func TestSSHDockerProvider_Deploy_RejectsLocalMount(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)

	conn := testSSHConnection()
	env := &environments.Environment{
		ID:             "env-localmount",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Invalid LocalMount Env",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "alpine:latest",
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindLocalMount,
				LocalMount: &environments.LocalMountConfig{
					HostPath:      "/workspaces/localrepo",
					ContainerPath: "/workspace",
				},
			},
		},
	}

	dep := &environments.Deployment{
		ID:             "dep-localmount",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		EnvironmentID:  "env-localmount",
		ConnectionID:   "conn-ssh-1",
		Name:           "Invalid LocalMount Deployment",
		Status:         environments.DeploymentStatusPending,
	}

	_, err := p.Deploy(context.Background(), DeployRequest{
		Connection:  conn,
		Environment: env,
		Deployment:  dep,
	})
	if err == nil {
		t.Fatal("expected deploy to fail when local_mount strategy is used with SSH connection")
	}
	if !strings.Contains(err.Error(), "local_mount workspace provisioning strategy cannot be used with remote SSH host") {
		t.Fatalf("expected explicit rejection of local_mount, got: %v", err)
	}
}

func TestSSHDockerProvider_Deploy_ReuseExisting(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)

	conn := testSSHConnection()
	env := &environments.Environment{
		ID:             "env-reuse",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Reusable Env",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "redis:7",
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindRegistryImage,
				RegistryImage: &environments.RegistryImageConfig{
					Image: "redis:7",
				},
			},
		},
		DeploymentPolicy: environments.DeploymentPolicy{
			Reuse:           true,
			MaxInstances:    1,
			ReleaseBehavior: environments.ReleaseBehaviorNone,
		},
	}

	dep := &environments.Deployment{
		ID:             "dep-reuse",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		EnvironmentID:  "env-reuse",
		ConnectionID:   "conn-ssh-1",
		Name:           "Reusable Deployment",
		Status:         environments.DeploymentStatusPending,
	}

	// First deploy
	res1, err := p.Deploy(context.Background(), DeployRequest{
		Connection:  conn,
		Environment: env,
		Deployment:  dep,
	})
	if err != nil {
		t.Fatalf("first deploy failed: %v", err)
	}

	// Count `docker run` calls so far
	runCount1 := 0
	for _, c := range runner.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), " run ") {
			runCount1++
		}
	}

	// Second deploy with reuse enabled
	res2, err := p.Deploy(context.Background(), DeployRequest{
		Connection:  conn,
		Environment: env,
		Deployment:  dep,
	})
	if err != nil {
		t.Fatalf("second deploy failed: %v", err)
	}

	runCount2 := 0
	for _, c := range runner.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), " run ") {
			runCount2++
		}
	}

	if runCount2 != runCount1 {
		t.Fatalf("expected container to be reused without calling docker run again, count1=%d, count2=%d", runCount1, runCount2)
	}
	if res2.Runtime.ContainerID != res1.Runtime.ContainerID {
		t.Fatalf("expected same container ID on reuse, got %s vs %s", res1.Runtime.ContainerID, res2.Runtime.ContainerID)
	}
}

func TestSSHDockerProvider_Deploy_SetupCommands(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)

	conn := testSSHConnection()
	env := &environments.Environment{
		ID:             "env-setup",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Setup Command Env",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "python:3.11",
			SetupCommands: []string{
				"pip install pytest",
			},
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindRegistryImage,
				RegistryImage: &environments.RegistryImageConfig{
					Image: "python:3.11",
				},
			},
		},
	}

	dep := &environments.Deployment{
		ID:             "dep-setup",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		EnvironmentID:  "env-setup",
		ConnectionID:   "conn-ssh-1",
		Name:           "Setup Deployment",
		Status:         environments.DeploymentStatusPending,
	}

	_, err := p.Deploy(context.Background(), DeployRequest{
		Connection:  conn,
		Environment: env,
		Deployment:  dep,
	})
	if err != nil {
		t.Fatalf("deploy with setup commands failed: %v", err)
	}

	foundSetupExec := false
	for _, c := range runner.Calls() {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, "exec") && strings.Contains(joined, "pip install pytest") {
			foundSetupExec = true
			break
		}
	}
	if !foundSetupExec {
		t.Fatal("expected setup command execution over SSH")
	}
}

func TestSSHDockerProvider_Deploy_HealthCheckHTTP(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)

	// Mock httpGet to simulate healthy endpoint
	var probedURL string
	p.httpGet = func(ctx context.Context, url string) (int, error) {
		probedURL = url
		return 200, nil
	}

	conn := testSSHConnection()
	env := &environments.Environment{
		ID:             "env-health",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Health Check Env",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "nginx:alpine",
			ExposedPorts: []environments.PortMapping{
				{ContainerPort: 8080, HostPort: 8080, Protocol: "tcp"},
			},
		},
		HealthCheck: &environments.HealthCheck{
			HTTPPath: "/healthz",
			HTTPPort: 8080,
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindRegistryImage,
				RegistryImage: &environments.RegistryImageConfig{
					Image: "nginx:alpine",
				},
			},
		},
	}

	dep := &environments.Deployment{
		ID:             "dep-health",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		EnvironmentID:  "env-health",
		ConnectionID:   "conn-ssh-1",
		Name:           "Health Deployment",
		Status:         environments.DeploymentStatusPending,
	}

	res, err := p.Deploy(context.Background(), DeployRequest{
		Connection:  conn,
		Environment: env,
		Deployment:  dep,
	})
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	if res.Health != environments.HealthStatusHealthy {
		t.Fatalf("expected health status healthy, got: %s", res.Health)
	}
	if probedURL != "http://remote.example.com:49151/healthz" {
		t.Fatalf("expected probe URL on remote.example.com, got: %s", probedURL)
	}
}

func TestSSHDockerProvider_Inspect(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)

	cState := &mockContainerState{
		ID:         "c12345678901",
		Name:       "swarm-env-1-dep-1",
		Running:    true,
		HostPort:   "32100",
		Health:     "healthy",
		ExitCode:   0,
		WorkingDir: "/srv/app",
	}
	runner.containers["swarm-env-1-dep-1"] = cState
	runner.containers["c12345678901"] = cState

	conn := testSSHConnection()
	dep := &environments.Deployment{
		ID:             "dep-1",
		EnvironmentID:  "env-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		ConnectionID:   "conn-ssh-1",
		Runtime: environments.RuntimeMetadata{
			ContainerID:        "c12345678901",
			ProviderResourceID: "swarm-env-1-dep-1",
		},
	}

	ins, err := p.Inspect(context.Background(), conn, dep)
	if err != nil {
		t.Fatalf("inspect failed: %v", err)
	}

	if ins.Status != environments.DeploymentStatusRunning {
		t.Fatalf("expected status running, got: %s", ins.Status)
	}
	if ins.Health != environments.HealthStatusHealthy {
		t.Fatalf("expected health healthy, got: %s", ins.Health)
	}
	if ins.Runtime.Endpoint != "http://remote.example.com:32100" {
		t.Fatalf("expected endpoint http://remote.example.com:32100, got: %s", ins.Runtime.Endpoint)
	}
}

func TestSSHDockerProvider_StartStopDestroy(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)

	runner.containers["swarm-env-1-dep-1"] = &mockContainerState{
		ID:       "c123",
		Name:     "swarm-env-1-dep-1",
		Running:  true,
		HostPort: "32100",
	}

	conn := testSSHConnection()
	dep := &environments.Deployment{
		ID:            "dep-1",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "swarm-env-1-dep-1",
		},
	}

	ctx := context.Background()

	// Stop
	if err := p.Stop(ctx, conn, dep); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if runner.containers["swarm-env-1-dep-1"].Running {
		t.Fatal("expected container to be stopped")
	}

	// Start
	if err := p.Start(ctx, conn, dep); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if !runner.containers["swarm-env-1-dep-1"].Running {
		t.Fatal("expected container to be running")
	}

	// Destroy
	if err := p.Destroy(ctx, conn, dep); err != nil {
		t.Fatalf("destroy failed: %v", err)
	}
	if _, exists := runner.containers["swarm-env-1-dep-1"]; exists {
		t.Fatal("expected container to be deleted")
	}
}

func TestSSHDockerProvider_ResolveAccess(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)

	cState := &mockContainerState{
		ID:         "c12345678901",
		Name:       "swarm-env-1-dep-1",
		Running:    true,
		HostPort:   "32100",
		Health:     "healthy",
		ExitCode:   0,
		WorkingDir: "/workspace",
	}
	runner.containers["swarm-env-1-dep-1"] = cState
	runner.containers["c12345678901"] = cState

	conn := testSSHConnection()
	dep := &environments.Deployment{
		ID:            "dep-1",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ContainerID:        "c12345678901",
			ProviderResourceID: "swarm-env-1-dep-1",
		},
	}

	access, err := p.ResolveAccess(context.Background(), conn, dep)
	if err != nil {
		t.Fatalf("resolve access failed: %v", err)
	}

	if !access.ExecSupported {
		t.Fatal("expected ExecSupported to be true")
	}
	if access.PrimaryEndpoint != "http://remote.example.com:32100" {
		t.Fatalf("expected primary endpoint http://remote.example.com:32100, got: %s", access.PrimaryEndpoint)
	}
	if access.Endpoints["8080"] != "http://remote.example.com:32100" {
		t.Fatalf("expected endpoint for port 8080, got: %v", access.Endpoints)
	}
	if access.RemoteWorkspacePath != "/workspace" {
		t.Fatalf("expected remote workspace path /workspace, got: %s", access.RemoteWorkspacePath)
	}
}

func TestSSHDockerProvider_Exec(t *testing.T) {
	runner := newMockSSHRunner()
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, subcmd string, args []string) error {
		if stdout != nil {
			_, _ = stdout.Write([]byte("command succeeded\n"))
		}
		return nil
	}

	p := NewSSHDockerProvider(runner)
	conn := testSSHConnection()
	dep := &environments.Deployment{
		ID:            "dep-1",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "swarm-env-1-dep-1",
		},
	}

	res, err := p.Exec(context.Background(), conn, dep, ExecRequest{
		Command:    []string{"echo", "hello"},
		WorkingDir: "/workspace",
		Env: map[string]string{
			"TEST_VAR": "123",
		},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("exec failed: %v", err)
	}

	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "command succeeded") {
		t.Fatalf("unexpected stdout: %s", res.Stdout)
	}

	// Verify command construction
	calls := runner.Calls()
	if len(calls) == 0 {
		t.Fatal("expected calls recorded")
	}
	var execCall *mockCall
	for i := range calls {
		joined := strings.Join(calls[i].Args, " ")
		if strings.Contains(joined, "swarm-supervisor") {
			execCall = &calls[i]
			break
		}
	}
	if execCall == nil {
		t.Fatal("expected supervised exec call recorded")
	}
	joined := strings.Join(execCall.Args, " ")
	if !strings.Contains(joined, "docker") || !strings.Contains(joined, "exec") {
		t.Fatalf("expected docker exec in ssh args, got: %s", joined)
	}
	if !strings.Contains(joined, "-w /workspace") {
		t.Fatalf("expected -w /workspace in ssh args, got: %s", joined)
	}
	if !strings.Contains(joined, "-e TEST_VAR=123") {
		t.Fatalf("expected -e TEST_VAR=123 in ssh args, got: %s", joined)
	}
}

func TestSSHDockerProvider_Exec_NonZeroExit(t *testing.T) {
	runner := newMockSSHRunner()
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, subcmd string, args []string) error {
		if stderr != nil {
			_, _ = stderr.Write([]byte("command failed with error\n"))
		}
		// Return an ExitError simulation
		return &exec.ExitError{}
	}

	p := NewSSHDockerProvider(runner)
	conn := testSSHConnection()
	dep := &environments.Deployment{
		ID:            "dep-1",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "swarm-env-1-dep-1",
		},
	}

	res, err := p.Exec(context.Background(), conn, dep, ExecRequest{
		Command: []string{"false"},
	})
	if err != nil {
		t.Fatalf("expected nil error on command non-zero exit, got: %v", err)
	}
	if res.ExitCode == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(res.Stderr, "command failed with error") {
		t.Fatalf("unexpected stderr: %s", res.Stderr)
	}
}

func TestSSHDockerProvider_Exec_Stdin(t *testing.T) {
	runner := newMockSSHRunner()
	var capturedStdin string
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, subcmd string, args []string) error {
		if stdin != nil {
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(stdin)
			capturedStdin = buf.String()
		}
		if stdout != nil {
			_, _ = stdout.Write([]byte("piped: " + capturedStdin))
		}
		return nil
	}

	p := NewSSHDockerProvider(runner)
	conn := testSSHConnection()
	dep := &environments.Deployment{
		ID:            "dep-1",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "swarm-env-1-dep-1",
		},
	}

	res, err := p.Exec(context.Background(), conn, dep, ExecRequest{
		Command: []string{"cat"},
		Stdin:   strings.NewReader("input data stream"),
	})
	if err != nil {
		t.Fatalf("exec with stdin failed: %v", err)
	}
	if !strings.Contains(res.Stdout, "piped: input data stream") {
		t.Fatalf("expected piped output, got: %s", res.Stdout)
	}

	// Verify -i flag was passed to docker exec
	calls := runner.Calls()
	var execCall *mockCall
	for i := range calls {
		joined := strings.Join(calls[i].Args, " ")
		if strings.Contains(joined, "swarm-supervisor") {
			execCall = &calls[i]
			break
		}
	}
	if execCall == nil {
		t.Fatal("expected supervised exec call recorded")
	}
	joined := strings.Join(execCall.Args, " ")
	if !strings.Contains(joined, "-i") {
		t.Fatalf("expected -i flag for stdin streaming, got: %s", joined)
	}
}

func TestSSHDockerProvider_QuoteShellArg(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "''"},
		{"simple", "simple"},
		{"file.txt", "file.txt"},
		{"path/to/file", "path/to/file"},
		{"key=value", "key=value"},
		{"127.0.0.1:8080", "127.0.0.1:8080"},
		{"hello world", "'hello world'"},
		{"don't", "'don'\\''t'"},
		{"echo $FOO", "'echo $FOO'"},
		{"cmd; rm -rf /", "'cmd; rm -rf /'"},
	}

	for _, tc := range tests {
		got := quoteShellArg(tc.input)
		if got != tc.expected {
			t.Errorf("quoteShellArg(%q) = %q, expected %q", tc.input, got, tc.expected)
		}
	}
}

func TestSSHDockerProvider_Deploy_ValidationErrors(t *testing.T) {
	p := NewSSHDockerProvider(newMockSSHRunner())
	ctx := context.Background()
	conn := testSSHConnection()
	env := &environments.Environment{
		ID:             "env-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Valid Env",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "ubuntu:22.04",
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindRegistryImage,
				RegistryImage: &environments.RegistryImageConfig{
					Image: "ubuntu:22.04",
				},
			},
		},
	}
	dep := &environments.Deployment{
		ID:             "dep-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		EnvironmentID:  "env-1",
		ConnectionID:   "conn-ssh-1",
		Name:           "Deployment",
		Status:         environments.DeploymentStatusPending,
	}

	// nil connection
	if _, err := p.Deploy(ctx, DeployRequest{Environment: env, Deployment: dep}); err == nil {
		t.Fatal("expected error for nil connection")
	}

	// nil environment
	if _, err := p.Deploy(ctx, DeployRequest{Connection: conn, Deployment: dep}); err == nil {
		t.Fatal("expected error for nil environment")
	}

	// nil deployment
	if _, err := p.Deploy(ctx, DeployRequest{Connection: conn, Environment: env}); err == nil {
		t.Fatal("expected error for nil deployment")
	}

	// wrong connection kind
	localConn := testSSHConnection()
	localConn.Kind = environments.ConnectionKindLocalDocker
	if _, err := p.Deploy(ctx, DeployRequest{Connection: localConn, Environment: env, Deployment: dep}); err == nil {
		t.Fatal("expected error for non-ssh connection")
	}

	// invalid environment (empty image)
	invalidEnv := env.Clone()
	invalidEnv.Container.Image = ""
	if _, err := p.Deploy(ctx, DeployRequest{Connection: conn, Environment: invalidEnv, Deployment: dep}); err == nil {
		t.Fatal("expected error for invalid environment")
	}

	// invalid deployment (empty ID)
	invalidDep := dep.Clone()
	invalidDep.ID = ""
	if _, err := p.Deploy(ctx, DeployRequest{Connection: conn, Environment: env, Deployment: invalidDep}); err == nil {
		t.Fatal("expected error for invalid deployment")
	}
}

func TestSSHDockerProvider_Deploy_RemoteExistingPath_ValidationErrors(t *testing.T) {
	p := NewSSHDockerProvider(newMockSSHRunner())
	ctx := context.Background()
	conn := testSSHConnection()

	baseEnv := func(rep *environments.RemoteExistingPathConfig) *environments.Environment {
		return &environments.Environment{
			ID:             "env-rep",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			Name:           "REP Env",
			Mode:           environments.EnvironmentModeDeployable,
			Role:           environments.EnvironmentRoleTesting,
			Container: environments.ContainerDefinition{
				Image: "ubuntu:22.04",
			},
			Provisioning: environments.WorkspaceProvisioning{
				Strategy: environments.SourceStrategy{
					Kind:               environments.SourceStrategyKindRemoteExistingPath,
					RemoteExistingPath: rep,
				},
			},
		}
	}

	dep := &environments.Deployment{
		ID:             "dep-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		EnvironmentID:  "env-rep",
		ConnectionID:   "conn-ssh-1",
		Name:           "Deployment",
		Status:         environments.DeploymentStatusPending,
	}

	// nil config
	if _, err := p.Deploy(ctx, DeployRequest{Connection: conn, Environment: baseEnv(nil), Deployment: dep}); err == nil {
		t.Fatal("expected error for nil remote_existing_path config")
	}

	// empty remote path
	if _, err := p.Deploy(ctx, DeployRequest{
		Connection:  conn,
		Environment: baseEnv(&environments.RemoteExistingPathConfig{RemotePath: "", ContainerPath: "/workspace"}),
		Deployment:  dep,
	}); err == nil {
		t.Fatal("expected error for empty remote path")
	}

	// relative remote path
	if _, err := p.Deploy(ctx, DeployRequest{
		Connection:  conn,
		Environment: baseEnv(&environments.RemoteExistingPathConfig{RemotePath: "relative/path", ContainerPath: "/workspace"}),
		Deployment:  dep,
	}); err == nil {
		t.Fatal("expected error for relative remote path")
	}

	// empty container path
	if _, err := p.Deploy(ctx, DeployRequest{
		Connection:  conn,
		Environment: baseEnv(&environments.RemoteExistingPathConfig{RemotePath: "/srv/repo", ContainerPath: ""}),
		Deployment:  dep,
	}); err == nil {
		t.Fatal("expected error for empty container path")
	}

	// relative container path
	if _, err := p.Deploy(ctx, DeployRequest{
		Connection:  conn,
		Environment: baseEnv(&environments.RemoteExistingPathConfig{RemotePath: "/srv/repo", ContainerPath: "relative"}),
		Deployment:  dep,
	}); err == nil {
		t.Fatal("expected error for relative container path")
	}
}

func TestSSHDockerProvider_Inspect_Errors(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)
	conn := testSSHConnection()
	ctx := context.Background()

	// nil deployment
	if _, err := p.Inspect(ctx, conn, nil); err == nil {
		t.Fatal("expected error for nil deployment")
	}

	// empty target
	if _, err := p.Inspect(ctx, conn, &environments.Deployment{}); err == nil {
		t.Fatal("expected error for empty target")
	}

	// inspect command error on remote
	dep := &environments.Deployment{
		ID:            "dep-missing",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "nonexistent-container",
		},
	}
	if _, err := p.Inspect(ctx, conn, dep); err == nil {
		t.Fatal("expected error inspecting nonexistent container")
	}

	// inspect returning malformed JSON
	runner.containers["malformed"] = &mockContainerState{ID: "malformed"}
	runner.handlers["inspect"] = func(subcmd string, args []string) ([]byte, error) {
		return []byte("{not-valid-json"), nil
	}
	depMalformed := &environments.Deployment{
		ID:            "dep-m",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "malformed",
		},
	}
	if _, err := p.Inspect(ctx, conn, depMalformed); err == nil {
		t.Fatal("expected error parsing malformed inspect JSON")
	}
}

func TestSSHDockerProvider_StartStopDestroy_Errors(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)
	conn := testSSHConnection()
	ctx := context.Background()

	// nil deployment
	if err := p.Start(ctx, conn, nil); err == nil {
		t.Fatal("expected error on Start(nil)")
	}
	if err := p.Stop(ctx, conn, nil); err == nil {
		t.Fatal("expected error on Stop(nil)")
	}
	if err := p.Destroy(ctx, conn, nil); err == nil {
		t.Fatal("expected error on Destroy(nil)")
	}

	// empty target
	emptyDep := &environments.Deployment{}
	if err := p.Start(ctx, conn, emptyDep); err == nil {
		t.Fatal("expected error on Start with empty target")
	}
	if err := p.Stop(ctx, conn, emptyDep); err == nil {
		t.Fatal("expected error on Stop with empty target")
	}
	if err := p.Destroy(ctx, conn, emptyDep); err == nil {
		t.Fatal("expected error on Destroy with empty target")
	}

	// command failure on remote
	failDep := &environments.Deployment{
		ID:            "dep-fail",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "missing-c",
		},
	}
	if err := p.Start(ctx, conn, failDep); err == nil {
		t.Fatal("expected error on Start nonexistent container")
	}
	if err := p.Stop(ctx, conn, failDep); err == nil {
		t.Fatal("expected error on Stop nonexistent container")
	}
}

func TestSSHDockerProvider_Exec_Errors(t *testing.T) {
	p := NewSSHDockerProvider(newMockSSHRunner())
	conn := testSSHConnection()
	ctx := context.Background()

	// nil deployment
	if _, err := p.Exec(ctx, conn, nil, ExecRequest{Command: []string{"echo"}}); err == nil {
		t.Fatal("expected error on Exec(nil)")
	}

	// empty command
	dep := &environments.Deployment{
		ID:            "dep-1",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "c-1",
		},
	}
	if _, err := p.Exec(ctx, conn, dep, ExecRequest{Command: nil}); err == nil {
		t.Fatal("expected error on Exec with empty command")
	}

	// empty target
	if _, err := p.Exec(ctx, conn, &environments.Deployment{}, ExecRequest{Command: []string{"echo"}}); err == nil {
		t.Fatal("expected error on Exec with empty target")
	}
}

func TestSSHDockerProvider_BuildSSHCommand_Options(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)

	conn := &environments.Connection{
		ID:             "conn-custom",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Custom SSH",
		Kind:           environments.ConnectionKindSSH,
		SSH: &environments.SSHConfig{
			Host:           "bastion.cloud.org",
			Port:           22022,
			User:           "admin",
			IdentityFile:   "/workspaces/.ssh/id_ed25519_custom",
			KnownHostsFile: "/workspaces/.ssh/known_hosts.custom",
		},
	}

	args, dest, err := p.buildSSHCommand(conn, "docker", "ps")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if dest != "admin@bastion.cloud.org" {
		t.Fatalf("expected dest admin@bastion.cloud.org, got %s", dest)
	}

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-p 22022") {
		t.Fatalf("expected -p 22022 in args, got: %s", joined)
	}
	if !strings.Contains(joined, "-i /workspaces/.ssh/id_ed25519_custom") {
		t.Fatalf("expected -i in args, got: %s", joined)
	}
	if !strings.Contains(joined, "-o UserKnownHostsFile=/workspaces/.ssh/known_hosts.custom") {
		t.Fatalf("expected UserKnownHostsFile in args, got: %s", joined)
	}
	if !strings.Contains(joined, "-o BatchMode=yes") {
		t.Fatalf("expected BatchMode=yes in args, got: %s", joined)
	}
}

func TestSSHDockerProvider_Exec_TimeoutAndCleanup(t *testing.T) {
	runner := newMockSSHRunner()
	var cleanupCalled bool
	var cleanupOpID string

	runner.handlers["exec"] = func(subcmd string, args []string) ([]byte, error) {
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
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, subcmd string, args []string) error {
		cancelExec()
		return execCtx.Err()
	}

	p := NewSSHDockerProvider(runner)
	conn := testSSHConnection()
	dep := &environments.Deployment{
		ID:            "dep-ssh-timeout",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "swarm-env-1-dep-ssh-timeout",
		},
	}

	req := ExecRequest{
		OperationID: "op-ssh-timeout",
		Command:     []string{"sleep", "60"},
		Timeout:     5 * time.Second,
	}

	_, err := p.Exec(execCtx, conn, dep, req)
	if err == nil {
		t.Fatal("expected error on timed out/cancelled exec over SSH")
	}
	if !errors.Is(err, ErrOperationTimedOut) {
		t.Fatalf("expected ErrOperationTimedOut, got: %v", err)
	}
	if !cleanupCalled {
		t.Fatal("expected remote cleanup command to be invoked over SSH on cancel/timeout")
	}
	if cleanupOpID != "op-ssh-timeout" {
		t.Fatalf("expected cleanup for op-ssh-timeout, got: %q", cleanupOpID)
	}
}

func TestSSHDockerProvider_Exec_ProbePrerequisitesFailed(t *testing.T) {
	runner := newMockSSHRunner()
	runner.handlers["exec"] = func(subcmd string, args []string) ([]byte, error) {
		argStr := strings.Join(args, " ")
		if strings.Contains(argStr, probeScript) {
			return []byte("mkdir: /run/swarm/operations: Permission denied\n"), errors.New("exit status 1")
		}
		return []byte("exec ok\n"), nil
	}

	var ioCalled bool
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, subcmd string, args []string) error {
		ioCalled = true
		return nil
	}

	p := NewSSHDockerProvider(runner)
	conn := testSSHConnection()
	dep := &environments.Deployment{
		ID:            "dep-ssh-probe-fail",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "swarm-env-1-dep-ssh-probe-fail",
		},
	}

	_, err := p.Exec(context.Background(), conn, dep, ExecRequest{
		Command: []string{"echo", "hi"},
	})
	if err == nil {
		t.Fatal("expected error when remote supervisor primitives unavailable")
	}
	if !errors.Is(err, ErrSupervisorUnavailable) {
		t.Fatalf("expected ErrSupervisorUnavailable, got: %v", err)
	}
	if ioCalled {
		t.Fatal("remote execution must not proceed when supervisor probe fails (fail closed)")
	}
}

func TestSSHDockerProvider_Exec_SafeArgQuoting(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)
	conn := testSSHConnection()
	dep := &environments.Deployment{
		ID:            "dep-quote",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "swarm-env-1-dep-quote",
		},
	}

	req := ExecRequest{
		Command: []string{"sh", "-c", "echo 'hello world'; cat \"filename with spaces.txt\""},
	}

	res, err := p.Exec(context.Background(), conn, dep, req)
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}

	calls := runner.Calls()
	var execCall *mockCall
	for i := range calls {
		joined := strings.Join(calls[i].Args, " ")
		if strings.Contains(joined, "swarm-supervisor") {
			execCall = &calls[i]
			break
		}
	}
	if execCall == nil {
		t.Fatal("expected supervised exec call recorded")
	}

	// Verify that the command string with quotes and spaces was properly escaped for remote SSH execution
	joined := strings.Join(execCall.Args, " ")
	if !strings.Contains(joined, "echo") || !strings.Contains(joined, "filename with spaces.txt") {
		t.Fatalf("expected complex command in ssh args, got: %s", joined)
	}
}

func TestSSHDockerProvider_CancelExec_Success(t *testing.T) {
	runner := newMockSSHRunner()
	var cleanupArgs []string
	runner.handlers["exec"] = func(subcmd string, args []string) ([]byte, error) {
		cleanupArgs = args
		return []byte("SWARM_CLEANUP:TERMINATED\n"), nil
	}

	p := NewSSHDockerProvider(runner)
	conn := testSSHConnection()
	dep := &environments.Deployment{
		ID:            "dep-ssh-cancel",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "swarm-env-1-dep-ssh-cancel",
		},
	}

	req := CancelExecRequest{
		OperationID: "op-ssh-cancel-99",
		GracePeriod: 4 * time.Second,
	}

	res, err := p.CancelExec(context.Background(), conn, dep, req)
	if err != nil {
		t.Fatalf("CancelExec failed: %v", err)
	}
	if !res.Terminated {
		t.Fatal("expected Terminated true")
	}
	if res.OperationID != "op-ssh-cancel-99" {
		t.Fatalf("expected OperationID op-ssh-cancel-99, got: %s", res.OperationID)
	}
	if res.SignalSent != "SIGTERM/SIGKILL" {
		t.Fatalf("expected SignalSent SIGTERM/SIGKILL, got: %s", res.SignalSent)
	}
	joined := strings.Join(cleanupArgs, " ")
	if !strings.Contains(joined, "swarm-cleanup") || !strings.Contains(joined, "op-ssh-cancel-99") {
		t.Fatalf("unexpected remote cleanup invocation args: %s", joined)
	}
}

func TestSSHDockerProvider_CancelExec_CleanupFailure(t *testing.T) {
	runner := newMockSSHRunner()
	runner.handlers["exec"] = func(subcmd string, args []string) ([]byte, error) {
		return []byte("SWARM_CLEANUP:CLEANUP_FAILED\n"), errors.New("exit status 1")
	}

	p := NewSSHDockerProvider(runner)
	conn := testSSHConnection()
	dep := &environments.Deployment{
		ID:            "dep-ssh-fail",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "swarm-env-1-dep-ssh-fail",
		},
	}

	res, err := p.CancelExec(context.Background(), conn, dep, CancelExecRequest{
		OperationID: "op-ssh-fail",
	})
	if err == nil {
		t.Fatal("expected error on remote cleanup failure")
	}
	if !errors.Is(err, ErrOperationCleanupFailed) {
		t.Fatalf("expected ErrOperationCleanupFailed, got: %v", err)
	}
	if res.Terminated {
		t.Fatal("expected Terminated false when remote cleanup failed")
	}
}

func TestSSHDockerProvider_Exec_RedactedEnvInError(t *testing.T) {
	runner := newMockSSHRunner()
	runner.ioHandlers["exec"] = func(stdin io.Reader, stdout, stderr io.Writer, subcmd string, args []string) error {
		return errors.New("ssh connection dropped")
	}

	p := NewSSHDockerProvider(runner)
	conn := testSSHConnection()
	dep := &environments.Deployment{
		ID:            "dep-ssh-secret",
		EnvironmentID: "env-1",
		Runtime: environments.RuntimeMetadata{
			ProviderResourceID: "swarm-env-1-dep-ssh-secret",
		},
	}

	secretToken := "secret-ssh-token-xyz987"
	req := ExecRequest{
		Command: []string{"run-task"},
		Env: map[string]string{
			"SSH_SECRET": secretToken,
		},
	}

	_, err := p.Exec(context.Background(), conn, dep, req)
	if err == nil {
		t.Fatal("expected error on remote exec failure")
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Fatalf("secret value leaked in error message: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "SSH_SECRET=[REDACTED]") {
		t.Fatalf("expected redacted key in error message, got: %s", err.Error())
	}
}
