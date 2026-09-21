package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

var _ DeploymentProvider = (*SSHDockerProvider)(nil)
var _ OperationCanceler = (*SSHDockerProvider)(nil)

// SSHDockerProvider manages environment deployments on a remote Docker daemon via SSH.
// Strictly adheres to security architecture: NO private keys, passwords, or secrets are stored
// in Pebble or provider state; authentication relies exclusively on the system SSH agent and config
// using non-interactive batch mode.
type SSHDockerProvider struct {
	runner  CommandRunner
	httpGet func(ctx context.Context, url string) (int, error)
}

// NewSSHDockerProvider creates a new SSHDockerProvider using the supplied CommandRunner.
// If runner is nil, OSCommandRunner is used.
func NewSSHDockerProvider(runner CommandRunner) *SSHDockerProvider {
	if runner == nil {
		runner = &OSCommandRunner{}
	}
	return &SSHDockerProvider{
		runner:  runner,
		httpGet: defaultHTTPGet,
	}
}

// Kind returns ConnectionKindSSH.
func (p *SSHDockerProvider) Kind() environments.ConnectionKind {
	return environments.ConnectionKindSSH
}

// ValidateConnection checks that the remote host is reachable via SSH and that Docker is installed and running.
func (p *SSHDockerProvider) ValidateConnection(ctx context.Context, conn *environments.Connection) error {
	if conn == nil {
		return errors.New("connection cannot be nil")
	}
	if err := conn.Validate(); err != nil {
		return fmt.Errorf("invalid connection: %w", err)
	}
	if conn.Kind != environments.ConnectionKindSSH {
		return fmt.Errorf("unsupported connection kind for ssh docker provider: %q", conn.Kind)
	}
	if conn.SSH == nil {
		return errors.New("ssh configuration is required")
	}

	_, dest, err := p.buildSSHBaseArgs(conn)
	if err != nil {
		return err
	}

	out, err := p.runSSH(ctx, conn, "docker", "info", "--format", "{{.ServerVersion}}")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 255:
				return fmt.Errorf("ssh connection to %s failed (network, auth, or reachability error): %w (output: %s)", dest, err, strings.TrimSpace(string(out)))
			case 127:
				return fmt.Errorf("ssh connection to %s succeeded, but docker is not installed or not in PATH (exit code 127): %w (output: %s)", dest, err, strings.TrimSpace(string(out)))
			default:
				return fmt.Errorf("remote docker daemon validation on %s failed: %w (output: %s)", dest, err, strings.TrimSpace(string(out)))
			}
		}
		return fmt.Errorf("ssh docker connection validation failed on %s: %w (output: %s)", dest, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Capabilities returns the supported capabilities for remote SSH Docker.
func (p *SSHDockerProvider) Capabilities(ctx context.Context, conn *environments.Connection) (environments.ConnectionCapabilities, error) {
	caps := environments.ConnectionCapabilities{
		SupportsDocker:      true,
		SupportsSSH:         true,
		SupportsDirectMount: false, // Remote SSH hosts do not share a local filesystem for direct local bind-mounts
		SupportsPortForward: true,
		RemoteOS:            "linux",
	}

	if conn != nil && conn.SSH != nil {
		out, err := p.runSSH(ctx, conn, "docker", "version", "--format", "{{.Server.Version}}")
		if err == nil {
			caps.EngineVersion = strings.TrimSpace(string(out))
		}
	}
	return caps, nil
}

// Deploy provisions and starts a Docker container on the remote SSH host.
func (p *SSHDockerProvider) Deploy(ctx context.Context, req DeployRequest) (*DeployResult, error) {
	if req.Connection == nil {
		return nil, errors.New("connection cannot be nil")
	}
	if req.Environment == nil {
		return nil, errors.New("environment cannot be nil")
	}
	if req.Deployment == nil {
		return nil, errors.New("deployment cannot be nil")
	}
	if req.Connection.Kind != environments.ConnectionKindSSH {
		return nil, fmt.Errorf("unsupported connection kind: %q", req.Connection.Kind)
	}
	if req.Connection.SSH == nil {
		return nil, errors.New("ssh configuration is required")
	}
	if err := req.Environment.Validate(); err != nil {
		return nil, fmt.Errorf("invalid environment definition: %w", err)
	}
	if err := req.Deployment.Validate(); err != nil {
		return nil, fmt.Errorf("invalid deployment record: %w", err)
	}

	deployCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		deployCtx, cancel = context.WithTimeout(ctx, DefaultOperationTimeout)
		defer cancel()
	}

	cName := containerName(req.Environment.ID, req.Deployment.ID)

	// Check if a container with this name already exists on the remote host
	inspectOut, inspectErr := p.runSSH(deployCtx, req.Connection, "docker", "inspect", cName)
	if inspectErr == nil {
		ins, parseErr := parseDockerInspect(inspectOut)
		if parseErr == nil && ins.State.Running && req.Environment.DeploymentPolicy.Reuse {
			// Container is already running and reusable on remote host
			insRes, err := p.Inspect(deployCtx, req.Connection, &environments.Deployment{
				ID:            req.Deployment.ID,
				EnvironmentID: req.Environment.ID,
				Runtime: environments.RuntimeMetadata{
					ContainerID:        ins.ID,
					ProviderResourceID: cName,
				},
			})
			if err == nil {
				return &DeployResult{
					Runtime: insRes.Runtime,
					Health:  insRes.Health,
					Status:  insRes.Status,
				}, nil
			}
		}
		// Existing container is stopped, dead, or not reusable - remove it before recreation
		rmArgs := []string{"docker", "rm", "-f", "-v", cName}
		_, _ = p.runSSH(deployCtx, req.Connection, rmArgs...)
	}

	// Build `docker run -d` arguments for the remote host
	runArgs := []string{"docker", "run", "-d", "--name", cName}

	// Scoping and metadata labels
	runArgs = append(runArgs,
		"--label", "swarm.deployment_id="+req.Deployment.ID,
		"--label", "swarm.environment_id="+req.Environment.ID,
		"--label", "swarm.workspace_id="+req.Deployment.WorkspaceID,
		"--label", "swarm.account_scope_id="+req.Deployment.AccountScopeID,
	)
	for k, v := range req.Environment.Labels {
		runArgs = append(runArgs, "--label", fmt.Sprintf("%s=%s", k, v))
	}

	// Resource limits
	if req.Environment.Resources != nil {
		if req.Environment.Resources.CPULimit != "" {
			runArgs = append(runArgs, "--cpus", req.Environment.Resources.CPULimit)
		}
		if req.Environment.Resources.MemoryLimit != "" {
			runArgs = append(runArgs, "--memory", req.Environment.Resources.MemoryLimit)
		}
		if req.Environment.Resources.GPURequired {
			if req.Environment.Resources.GPUCount > 0 {
				runArgs = append(runArgs, "--gpus", fmt.Sprintf("count=%d", req.Environment.Resources.GPUCount))
			} else {
				runArgs = append(runArgs, "--gpus", "all")
			}
		}
	}

	// Environment variables
	for k, v := range req.Environment.Container.EnvVars {
		resolved := resolveEnvValue(v, req.EnvOverrides)
		runArgs = append(runArgs, "-e", fmt.Sprintf("%s=%s", k, resolved))
	}
	for k, v := range req.EnvOverrides {
		if _, exists := req.Environment.Container.EnvVars[k]; !exists {
			runArgs = append(runArgs, "-e", fmt.Sprintf("%s=%s", k, v))
		}
	}

	// Exposed port mappings on remote host
	for _, port := range req.Environment.Container.ExposedPorts {
		proto := "tcp"
		if port.Protocol != "" {
			proto = port.Protocol
		}
		if port.HostPort > 0 {
			runArgs = append(runArgs, "-p", fmt.Sprintf("%d:%d/%s", port.HostPort, port.ContainerPort, proto))
		} else {
			runArgs = append(runArgs, "-p", fmt.Sprintf("%d/%s", port.ContainerPort, proto))
		}
	}

	// Working directory & User & Privileged
	workingDir := req.Environment.Provisioning.ContainerWorkingDir
	if workingDir == "" {
		workingDir = req.Environment.Container.WorkingDir
	}
	if workingDir != "" {
		runArgs = append(runArgs, "-w", workingDir)
	}
	if req.Environment.Container.User != "" {
		runArgs = append(runArgs, "-u", req.Environment.Container.User)
	}
	if req.Environment.Container.Privileged {
		runArgs = append(runArgs, "--privileged")
	}

	// Workspace provisioning strategy translation
	var remoteWorkspacePath string
	switch req.Environment.Provisioning.Strategy.Kind {
	case environments.SourceStrategyKindRemoteExistingPath:
		if req.Environment.Provisioning.Strategy.RemoteExistingPath == nil {
			return nil, errors.New("remote_existing_path configuration is required for remote_existing_path strategy")
		}
		rep := req.Environment.Provisioning.Strategy.RemoteExistingPath
		if rep.RemotePath == "" {
			return nil, errors.New("remote_existing_path remote_path cannot be empty")
		}
		if !strings.HasPrefix(rep.RemotePath, "/") {
			return nil, fmt.Errorf("remote_existing_path remote_path must be an absolute path: %q", rep.RemotePath)
		}
		if rep.ContainerPath == "" {
			return nil, errors.New("remote_existing_path container_path cannot be empty")
		}
		if !strings.HasPrefix(rep.ContainerPath, "/") {
			return nil, fmt.Errorf("remote_existing_path container_path must be an absolute path: %q", rep.ContainerPath)
		}
		spec := fmt.Sprintf("%s:%s", rep.RemotePath, rep.ContainerPath)
		if rep.ReadOnly {
			spec += ":ro"
		}
		runArgs = append(runArgs, "-v", spec)
		remoteWorkspacePath = rep.ContainerPath

	case environments.SourceStrategyKindLocalMount:
		return nil, errors.New("local_mount workspace provisioning strategy cannot be used with remote SSH host: local filesystem is not shared with remote host; use remote_existing_path or sync instead")

	case environments.SourceStrategyKindRegistryImage:
		remoteWorkspacePath = workingDir

	case environments.SourceStrategyKindSync:
		if req.Environment.Provisioning.Strategy.Sync != nil && req.Environment.Provisioning.Strategy.Sync.RemotePath != "" {
			remoteWorkspacePath = req.Environment.Provisioning.Strategy.Sync.RemotePath
		} else {
			remoteWorkspacePath = workingDir
		}

	case environments.SourceStrategyKindGitCheckout:
		remoteWorkspacePath = workingDir

	case "":
		remoteWorkspacePath = workingDir

	default:
		return nil, fmt.Errorf("unsupported workspace provisioning strategy %q for ssh docker provider", req.Environment.Provisioning.Strategy.Kind)
	}

	// Additional remote mounts
	for _, m := range req.Environment.Provisioning.Mounts {
		spec := fmt.Sprintf("%s:%s", m.HostPath, m.ContainerPath)
		if m.ReadOnly {
			spec += ":ro"
		}
		runArgs = append(runArgs, "-v", spec)
	}

	if remoteWorkspacePath == "" {
		remoteWorkspacePath = workingDir
	}

	// Health check flags
	if req.Environment.HealthCheck != nil && len(req.Environment.HealthCheck.Test) > 0 {
		runArgs = append(runArgs, "--health-cmd", strings.Join(req.Environment.HealthCheck.Test, " "))
		if req.Environment.HealthCheck.IntervalSeconds > 0 {
			runArgs = append(runArgs, "--health-interval", fmt.Sprintf("%ds", req.Environment.HealthCheck.IntervalSeconds))
		}
		if req.Environment.HealthCheck.TimeoutSeconds > 0 {
			runArgs = append(runArgs, "--health-timeout", fmt.Sprintf("%ds", req.Environment.HealthCheck.TimeoutSeconds))
		}
		if req.Environment.HealthCheck.Retries > 0 {
			runArgs = append(runArgs, "--health-retries", fmt.Sprintf("%d", req.Environment.HealthCheck.Retries))
		}
		if req.Environment.HealthCheck.StartPeriodSeconds > 0 {
			runArgs = append(runArgs, "--health-start-period", fmt.Sprintf("%ds", req.Environment.HealthCheck.StartPeriodSeconds))
		}
	}

	// Image and command/args
	runArgs = append(runArgs, req.Environment.Container.Image)
	if len(req.Environment.Container.Command) > 0 {
		runArgs = append(runArgs, req.Environment.Container.Command...)
	}
	if len(req.Environment.Container.Args) > 0 {
		runArgs = append(runArgs, req.Environment.Container.Args...)
	}

	// Run container on remote host over SSH
	runOut, runErr := p.runSSH(deployCtx, req.Connection, runArgs...)
	if runErr != nil {
		return nil, fmt.Errorf("failed to deploy remote docker container %s on %s: %w (output: %s)", cName, sshHostName(req.Connection), runErr, strings.TrimSpace(string(runOut)))
	}

	containerID := strings.TrimSpace(string(runOut))

	// Execute setup commands if defined
	if len(req.Environment.Container.SetupCommands) > 0 {
		for i, cmdStr := range req.Environment.Container.SetupCommands {
			execArgs := []string{"docker", "exec", cName, "sh", "-c", cmdStr}
			setupOut, setupErr := p.runSSHCombined(deployCtx, req.Connection, execArgs...)
			if setupErr != nil {
				// Destroy failed container on remote host
				rmArgs := []string{"docker", "rm", "-f", "-v", cName}
				_, _ = p.runSSH(context.Background(), req.Connection, rmArgs...)
				return nil, fmt.Errorf("setup command [%d] %q failed on remote container %s: %w (output: %s)", i, cmdStr, cName, setupErr, sanitizeOutput(string(setupOut)))
			}
		}
	}

	// Inspect container to obtain dynamic runtime state (ports, IP, health)
	insRes, err := p.Inspect(deployCtx, req.Connection, &environments.Deployment{
		ID:            req.Deployment.ID,
		EnvironmentID: req.Environment.ID,
		Runtime: environments.RuntimeMetadata{
			ContainerID:        containerID,
			ProviderResourceID: cName,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect newly deployed remote container: %w", err)
	}

	runtimeMeta := insRes.Runtime
	runtimeMeta.ProviderResourceID = cName
	if remoteWorkspacePath != "" {
		runtimeMeta.RemoteWorkspacePath = remoteWorkspacePath
	}

	// Determine health status
	healthStatus := insRes.Health
	if req.Environment.HealthCheck != nil {
		if req.Environment.HealthCheck.HTTPPath != "" && req.Environment.HealthCheck.HTTPPort > 0 {
			var mappedHostPort int
			for _, ap := range runtimeMeta.AssignedPorts {
				if ap.ContainerPort == req.Environment.HealthCheck.HTTPPort {
					mappedHostPort = ap.HostPort
					break
				}
			}
			if mappedHostPort > 0 {
				host := req.Connection.SSH.Host
				probeURL := fmt.Sprintf("http://%s%s", net.JoinHostPort(host, strconv.Itoa(mappedHostPort)), req.Environment.HealthCheck.HTTPPath)
				statusCode, probeErr := p.httpGet(deployCtx, probeURL)
				if probeErr == nil && statusCode >= 200 && statusCode < 400 {
					healthStatus = environments.HealthStatusHealthy
				} else {
					healthStatus = environments.HealthStatusUnhealthy
				}
			}
		}
	} else if insRes.Status == environments.DeploymentStatusRunning {
		healthStatus = environments.HealthStatusHealthy
	}

	return &DeployResult{
		Runtime: runtimeMeta,
		Health:  healthStatus,
		Status:  insRes.Status,
	}, nil
}

// Inspect retrieves live container status, mapped ports, and health on the remote host.
func (p *SSHDockerProvider) Inspect(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) (*InspectResult, error) {
	if deployment == nil {
		return nil, errors.New("deployment cannot be nil")
	}
	target := resolveContainerTarget(deployment)
	if target == "" {
		return nil, errors.New("cannot inspect deployment without container target or ID")
	}

	out, err := p.runSSH(ctx, conn, "docker", "inspect", target)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container %s on %s: %w", target, sshHostName(conn), err)
	}

	ins, err := parseDockerInspect(out)
	if err != nil {
		return nil, fmt.Errorf("failed to parse inspect output for container %s on %s: %w", target, sshHostName(conn), err)
	}

	// Status mapping
	var status environments.DeploymentStatus
	if ins.State.Running {
		status = environments.DeploymentStatusRunning
	} else if ins.State.Restarting {
		status = environments.DeploymentStatusStarting
	} else if ins.State.Paused {
		status = environments.DeploymentStatusStopped
	} else if ins.State.Dead {
		status = environments.DeploymentStatusFailed
	} else if ins.State.ExitCode != 0 {
		status = environments.DeploymentStatusFailed
	} else {
		status = environments.DeploymentStatusStopped
	}

	// Health mapping
	health := environments.HealthStatusUnknown
	if ins.State.Health != nil {
		switch strings.ToLower(ins.State.Health.Status) {
		case "healthy":
			health = environments.HealthStatusHealthy
		case "unhealthy":
			health = environments.HealthStatusUnhealthy
		case "starting":
			health = environments.HealthStatusStarting
		default:
			health = environments.HealthStatusUnknown
		}
	} else if ins.State.Running {
		health = environments.HealthStatusHealthy
	}

	// Assigned ports extraction
	var assignedPorts []environments.AssignedPort
	var primaryEndpoint string

	host := "127.0.0.1"
	if conn != nil && conn.SSH != nil && conn.SSH.Host != "" {
		host = conn.SSH.Host
	}

	portKeys := make([]string, 0, len(ins.NetworkSettings.Ports))
	for k := range ins.NetworkSettings.Ports {
		portKeys = append(portKeys, k)
	}
	sort.Strings(portKeys)

	for _, k := range portKeys {
		bindings := ins.NetworkSettings.Ports[k]
		parts := strings.Split(k, "/")
		cPort, _ := strconv.Atoi(parts[0])
		proto := "tcp"
		if len(parts) > 1 {
			proto = parts[1]
		}
		for _, b := range bindings {
			hPort, _ := strconv.Atoi(b.HostPort)
			endpointURL := ""
			if proto == "tcp" {
				endpointURL = fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(hPort)))
			}
			ap := environments.AssignedPort{
				ContainerPort: cPort,
				HostPort:      hPort,
				Protocol:      proto,
				EndpointURL:   endpointURL,
			}
			assignedPorts = append(assignedPorts, ap)
			if primaryEndpoint == "" && endpointURL != "" {
				primaryEndpoint = endpointURL
			}
		}
	}

	cid := ins.ID
	if len(cid) == 64 {
		cid = cid[:12]
	}

	runtimeMeta := environments.RuntimeMetadata{
		ContainerID:         cid,
		ProviderResourceID:  target,
		Endpoint:            primaryEndpoint,
		AssignedPorts:       assignedPorts,
		RemoteWorkspacePath: ins.Config.WorkingDir,
		RuntimeIP:           ins.NetworkSettings.IPAddress,
	}

	return &InspectResult{
		Status:       status,
		Health:       health,
		Runtime:      runtimeMeta,
		ErrorMessage: ins.State.Error,
	}, nil
}

// Start starts a stopped deployment container on the remote SSH host.
func (p *SSHDockerProvider) Start(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) error {
	if deployment == nil {
		return errors.New("deployment cannot be nil")
	}
	target := resolveContainerTarget(deployment)
	if target == "" {
		return errors.New("cannot start deployment without container target or ID")
	}

	out, err := p.runSSH(ctx, conn, "docker", "start", target)
	if err != nil {
		return fmt.Errorf("failed to start container %s on %s: %w (output: %s)", target, sshHostName(conn), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Stop stops a running deployment container on the remote SSH host.
func (p *SSHDockerProvider) Stop(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) error {
	if deployment == nil {
		return errors.New("deployment cannot be nil")
	}
	target := resolveContainerTarget(deployment)
	if target == "" {
		return errors.New("cannot stop deployment without container target or ID")
	}

	out, err := p.runSSH(ctx, conn, "docker", "stop", "-t", "10", target)
	if err != nil {
		return fmt.Errorf("failed to stop container %s on %s: %w (output: %s)", target, sshHostName(conn), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Destroy stops and cleans up the container and associated resources on the remote SSH host.
func (p *SSHDockerProvider) Destroy(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) error {
	if deployment == nil {
		return errors.New("deployment cannot be nil")
	}
	target := resolveContainerTarget(deployment)
	if target == "" {
		return errors.New("cannot destroy deployment without container target or ID")
	}

	out, err := p.runSSH(ctx, conn, "docker", "rm", "-f", "-v", target)
	if err != nil {
		return fmt.Errorf("failed to destroy container %s on %s: %w (output: %s)", target, sshHostName(conn), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ResolveAccess returns consumer-relevant access metadata for an active remote deployment.
func (p *SSHDockerProvider) ResolveAccess(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) (*DeploymentAccess, error) {
	if deployment == nil {
		return nil, errors.New("deployment cannot be nil")
	}
	target := resolveContainerTarget(deployment)
	if target == "" {
		return nil, errors.New("cannot resolve access without container target or ID")
	}

	// Inspect container for fresh runtime ports and state on remote host
	insRes, err := p.Inspect(ctx, conn, deployment)
	if err != nil {
		// Fallback to cached runtime if present
		if deployment.Runtime.ContainerID != "" || deployment.Runtime.ProviderResourceID != "" {
			return p.accessFromRuntime(deployment.Runtime, conn), nil
		}
		return nil, fmt.Errorf("failed to resolve access for deployment %s: %w", deployment.ID, err)
	}

	rt := insRes.Runtime
	if deployment.Runtime.RemoteWorkspacePath != "" && rt.RemoteWorkspacePath == "" {
		rt.RemoteWorkspacePath = deployment.Runtime.RemoteWorkspacePath
	}
	return p.accessFromRuntime(rt, conn), nil
}

func (p *SSHDockerProvider) accessFromRuntime(rt environments.RuntimeMetadata, conn *environments.Connection) *DeploymentAccess {
	endpoints := make(map[string]string)
	for _, ap := range rt.AssignedPorts {
		if ap.EndpointURL != "" {
			endpoints[strconv.Itoa(ap.ContainerPort)] = ap.EndpointURL
		}
	}
	if rt.Endpoint != "" {
		endpoints["primary"] = rt.Endpoint
	}

	return &DeploymentAccess{
		PrimaryEndpoint:     rt.Endpoint,
		Endpoints:           endpoints,
		ExecSupported:       true,
		RemoteWorkspacePath: rt.RemoteWorkspacePath,
		MappedPorts:         rt.AssignedPorts,
		ContainerID:         rt.ContainerID,
		RuntimeIP:           rt.RuntimeIP,
	}
}

type sshDockerTransport struct {
	provider *SSHDockerProvider
	conn     *environments.Connection
}

func (t *sshDockerTransport) RunExec(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, execArgs ...string) error {
	remoteArgs := append([]string{"docker", "exec"}, execArgs...)
	return t.provider.runSSHWithIO(ctx, t.conn, stdin, stdout, stderr, remoteArgs...)
}

func (t *sshDockerTransport) RunExecCombined(ctx context.Context, execArgs ...string) ([]byte, error) {
	remoteArgs := append([]string{"docker", "exec"}, execArgs...)
	return t.provider.runSSHCombined(ctx, t.conn, remoteArgs...)
}

// Exec executes a command inside the running remote container over SSH with supervisor process group management.
func (p *SSHDockerProvider) Exec(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment, req ExecRequest) (*ExecResult, error) {
	target, err := validateExecParams(deployment, req)
	if err != nil {
		return nil, err
	}
	transport := &sshDockerTransport{provider: p, conn: conn}
	return executeSupervised(ctx, transport, target, req)
}

// CancelExec cancels a running or orphaned execution operation inside the remote target container.
func (p *SSHDockerProvider) CancelExec(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment, req CancelExecRequest) (*CancelExecResult, error) {
	target, err := validateCancelParams(deployment, req)
	if err != nil {
		return nil, err
	}
	transport := &sshDockerTransport{provider: p, conn: conn}
	return cancelSupervised(ctx, transport, target, req)
}

// Internal SSH command construction helpers

func (p *SSHDockerProvider) buildSSHBaseArgs(conn *environments.Connection) ([]string, string, error) {
	if conn == nil {
		return nil, "", errors.New("connection cannot be nil")
	}
	if conn.SSH == nil {
		return nil, "", errors.New("ssh configuration is required for ssh connection")
	}
	host := strings.TrimSpace(conn.SSH.Host)
	if host == "" {
		return nil, "", errors.New("ssh host cannot be empty")
	}
	user := strings.TrimSpace(conn.SSH.User)

	dest := host
	if user != "" {
		dest = fmt.Sprintf("%s@%s", user, host)
	}

	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
	}

	if conn.SSH.Port > 0 && conn.SSH.Port != 22 {
		args = append(args, "-p", strconv.Itoa(conn.SSH.Port))
	}
	if conn.SSH.IdentityFile != "" {
		args = append(args, "-i", conn.SSH.IdentityFile)
	}
	if conn.SSH.KnownHostsFile != "" {
		args = append(args, "-o", fmt.Sprintf("UserKnownHostsFile=%s", conn.SSH.KnownHostsFile))
	}

	return args, dest, nil
}

func (p *SSHDockerProvider) buildSSHCommand(conn *environments.Connection, remoteArgs ...string) ([]string, string, error) {
	baseArgs, dest, err := p.buildSSHBaseArgs(conn)
	if err != nil {
		return nil, "", err
	}

	args := append(baseArgs, dest)
	for _, arg := range remoteArgs {
		args = append(args, quoteShellArg(arg))
	}
	return args, dest, nil
}

func (p *SSHDockerProvider) runSSH(ctx context.Context, conn *environments.Connection, remoteArgs ...string) ([]byte, error) {
	args, _, err := p.buildSSHCommand(conn, remoteArgs...)
	if err != nil {
		return nil, err
	}
	return p.runner.Run(ctx, "ssh", args...)
}

func (p *SSHDockerProvider) runSSHCombined(ctx context.Context, conn *environments.Connection, remoteArgs ...string) ([]byte, error) {
	args, _, err := p.buildSSHCommand(conn, remoteArgs...)
	if err != nil {
		return nil, err
	}
	return p.runner.RunCombined(ctx, "ssh", args...)
}

func (p *SSHDockerProvider) runSSHWithIO(ctx context.Context, conn *environments.Connection, stdin io.Reader, stdout, stderr io.Writer, remoteArgs ...string) error {
	baseArgs, dest, err := p.buildSSHBaseArgs(conn)
	if err != nil {
		return err
	}
	args := baseArgs
	if stdin == nil {
		args = append(args, "-n")
	}
	args = append(args, dest)
	for _, arg := range remoteArgs {
		args = append(args, quoteShellArg(arg))
	}
	return p.runner.RunWithIO(ctx, stdin, stdout, stderr, "ssh", args...)
}

func sshHostName(conn *environments.Connection) string {
	if conn != nil && conn.SSH != nil && conn.SSH.Host != "" {
		return conn.SSH.Host
	}
	return "remote host"
}

// quoteShellArg quotes a string for safe evaluation by a remote POSIX shell.
// Safe characters are returned unquoted; any string containing spaces or shell metacharacters
// is single-quoted with embedded single quotes escaped.
func quoteShellArg(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '.' || r == '/' || r == '-' || r == '=' || r == ':' || r == '@' || r == ',') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
