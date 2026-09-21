package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

var _ DeploymentProvider = (*LocalDockerProvider)(nil)
var _ OperationCanceler = (*LocalDockerProvider)(nil)

// LocalDockerProvider manages environment deployments on a local Docker daemon.
type LocalDockerProvider struct {
	runner  CommandRunner
	httpGet func(ctx context.Context, url string) (int, error)
}

// NewLocalDockerProvider creates a new LocalDockerProvider using the supplied CommandRunner.
// If runner is nil, OSCommandRunner is used.
func NewLocalDockerProvider(runner CommandRunner) *LocalDockerProvider {
	if runner == nil {
		runner = &OSCommandRunner{}
	}
	return &LocalDockerProvider{
		runner:  runner,
		httpGet: defaultHTTPGet,
	}
}

// Kind returns ConnectionKindLocalDocker.
func (p *LocalDockerProvider) Kind() environments.ConnectionKind {
	return environments.ConnectionKindLocalDocker
}

// ValidateConnection checks that the local Docker daemon is reachable and responding.
func (p *LocalDockerProvider) ValidateConnection(ctx context.Context, conn *environments.Connection) error {
	if conn == nil {
		return errors.New("connection cannot be nil")
	}
	if err := conn.Validate(); err != nil {
		return fmt.Errorf("invalid connection: %w", err)
	}
	if conn.Kind != environments.ConnectionKindLocalDocker {
		return fmt.Errorf("unsupported connection kind for local docker provider: %q", conn.Kind)
	}

	args := append(dockerHostArgs(conn), "info", "--format", "{{.ServerVersion}}")
	out, err := p.runner.Run(ctx, "docker", args...)
	if err != nil {
		return fmt.Errorf("docker connection validation failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Capabilities returns the supported capabilities for local Docker.
func (p *LocalDockerProvider) Capabilities(ctx context.Context, conn *environments.Connection) (environments.ConnectionCapabilities, error) {
	caps := environments.ConnectionCapabilities{
		SupportsDocker:      true,
		SupportsSSH:         false,
		SupportsDirectMount: true,
		SupportsPortForward: true,
		RemoteOS:            "linux",
	}

	if conn != nil {
		args := append(dockerHostArgs(conn), "version", "--format", "{{.Server.Version}}")
		out, err := p.runner.Run(ctx, "docker", args...)
		if err == nil {
			caps.EngineVersion = strings.TrimSpace(string(out))
		}
	}
	return caps, nil
}

// Deploy provisions and starts a Docker container based on the Environment definition and local_mount strategy.
func (p *LocalDockerProvider) Deploy(ctx context.Context, req DeployRequest) (*DeployResult, error) {
	if req.Connection == nil {
		return nil, errors.New("connection cannot be nil")
	}
	if req.Environment == nil {
		return nil, errors.New("environment cannot be nil")
	}
	if req.Deployment == nil {
		return nil, errors.New("deployment cannot be nil")
	}
	if req.Connection.Kind != environments.ConnectionKindLocalDocker {
		return nil, fmt.Errorf("unsupported connection kind: %q", req.Connection.Kind)
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

	// Check if a container with this name already exists
	inspectArgs := append(dockerHostArgs(req.Connection), "inspect", cName)
	existingOut, inspectErr := p.runner.Run(deployCtx, "docker", inspectArgs...)
	if inspectErr == nil {
		ins, parseErr := parseDockerInspect(existingOut)
		if parseErr == nil && ins.State.Running && req.Environment.DeploymentPolicy.Reuse {
			// Container is already running and reusable
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
		rmArgs := append(dockerHostArgs(req.Connection), "rm", "-f", "-v", cName)
		_, _ = p.runner.Run(deployCtx, "docker", rmArgs...)
	}

	// Build `docker run -d` arguments with -i to keep STDIN open so interactive shell entrypoints do not exit
	runArgs := append(dockerHostArgs(req.Connection), "run", "-d", "-i", "--name", cName)

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

	// Exposed port mappings (default loopback 127.0.0.1 for local security)
	for _, port := range req.Environment.Container.ExposedPorts {
		proto := "tcp"
		if port.Protocol != "" {
			proto = port.Protocol
		}
		if port.HostPort > 0 {
			runArgs = append(runArgs, "-p", fmt.Sprintf("127.0.0.1:%d:%d/%s", port.HostPort, port.ContainerPort, proto))
		} else {
			runArgs = append(runArgs, "-p", fmt.Sprintf("127.0.0.1::%d/%s", port.ContainerPort, proto))
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

	// Workspace provisioning (local_mount)
	var remoteWorkspacePath string
	if req.Environment.Provisioning.Strategy.Kind == environments.SourceStrategyKindLocalMount &&
		req.Environment.Provisioning.Strategy.LocalMount != nil {
		mount := req.Environment.Provisioning.Strategy.LocalMount
		hostPath := mount.HostPath
		if hostPath == "" {
			hostPath = req.WorkspacePath
		}
		if hostPath == "" {
			return nil, errors.New("local_mount host_path is empty and no workspace path was provided")
		}
		if !filepath.IsAbs(hostPath) {
			return nil, fmt.Errorf("local_mount host_path must be an absolute path: %q", hostPath)
		}
		spec := fmt.Sprintf("%s:%s", hostPath, mount.ContainerPath)
		if mount.ReadOnly {
			spec += ":ro"
		}
		runArgs = append(runArgs, "-v", spec)
		remoteWorkspacePath = mount.ContainerPath
	}

	// Additional mounts
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

	// Run container
	runOut, runErr := p.runner.Run(deployCtx, "docker", runArgs...)
	if runErr != nil {
		return nil, fmt.Errorf("failed to deploy docker container %s: %w (output: %s)", cName, runErr, strings.TrimSpace(string(runOut)))
	}

	containerID := strings.TrimSpace(string(runOut))

	// Execute setup commands if defined
	if len(req.Environment.Container.SetupCommands) > 0 {
		for i, cmdStr := range req.Environment.Container.SetupCommands {
			execArgs := append(dockerHostArgs(req.Connection), "exec", cName, "sh", "-c", cmdStr)
			setupOut, setupErr := p.runner.RunCombined(deployCtx, "docker", execArgs...)
			if setupErr != nil {
				// Destroy failed container
				rmArgs := append(dockerHostArgs(req.Connection), "rm", "-f", "-v", cName)
				_, _ = p.runner.Run(context.Background(), "docker", rmArgs...)
				return nil, fmt.Errorf("setup command [%d] %q failed: %w (output: %s)", i, cmdStr, setupErr, sanitizeOutput(string(setupOut)))
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
		return nil, fmt.Errorf("failed to inspect newly deployed container: %w", err)
	}
	if insRes.Status == environments.DeploymentStatusStopped || insRes.Status == environments.DeploymentStatusFailed {
		rmArgs := append(dockerHostArgs(req.Connection), "rm", "-f", "-v", cName)
		_, _ = p.runner.Run(context.Background(), "docker", rmArgs...)
		return nil, fmt.Errorf("deployed container %s is not running (status: %s)", cName, insRes.Status)
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
				probeURL := fmt.Sprintf("http://127.0.0.1:%d%s", mappedHostPort, req.Environment.HealthCheck.HTTPPath)
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

// Inspect retrieves live container status, mapped ports, and health.
func (p *LocalDockerProvider) Inspect(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) (*InspectResult, error) {
	if deployment == nil {
		return nil, errors.New("deployment cannot be nil")
	}
	target := resolveContainerTarget(deployment)
	if target == "" {
		return nil, errors.New("cannot inspect deployment without container target or ID")
	}

	args := append(dockerHostArgs(conn), "inspect", target)
	out, err := p.runner.Run(ctx, "docker", args...)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container %s: %w", target, err)
	}

	ins, err := parseDockerInspect(out)
	if err != nil {
		return nil, fmt.Errorf("failed to parse inspect output for container %s: %w", target, err)
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
			hostIP := b.HostIP
			if hostIP == "" || hostIP == "0.0.0.0" {
				hostIP = "127.0.0.1"
			}
			endpointURL := ""
			if proto == "tcp" {
				endpointURL = fmt.Sprintf("http://%s:%d", hostIP, hPort)
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

// Start starts a stopped deployment container.
func (p *LocalDockerProvider) Start(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) error {
	if deployment == nil {
		return errors.New("deployment cannot be nil")
	}
	target := resolveContainerTarget(deployment)
	if target == "" {
		return errors.New("cannot start deployment without container target or ID")
	}

	args := append(dockerHostArgs(conn), "start", target)
	out, err := p.runner.Run(ctx, "docker", args...)
	if err != nil {
		return fmt.Errorf("failed to start container %s: %w (output: %s)", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Stop stops a running deployment container.
func (p *LocalDockerProvider) Stop(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) error {
	if deployment == nil {
		return errors.New("deployment cannot be nil")
	}
	target := resolveContainerTarget(deployment)
	if target == "" {
		return errors.New("cannot stop deployment without container target or ID")
	}

	args := append(dockerHostArgs(conn), "stop", "-t", "10", target)
	out, err := p.runner.Run(ctx, "docker", args...)
	if err != nil {
		return fmt.Errorf("failed to stop container %s: %w (output: %s)", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Destroy stops and cleans up the container and associated resources.
func (p *LocalDockerProvider) Destroy(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) error {
	if deployment == nil {
		return errors.New("deployment cannot be nil")
	}
	target := resolveContainerTarget(deployment)
	if target == "" {
		return errors.New("cannot destroy deployment without container target or ID")
	}

	args := append(dockerHostArgs(conn), "rm", "-f", "-v", target)
	out, err := p.runner.Run(ctx, "docker", args...)
	if err != nil {
		return fmt.Errorf("failed to destroy container %s: %w (output: %s)", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ResolveAccess returns consumer-relevant access metadata for an active deployment.
func (p *LocalDockerProvider) ResolveAccess(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) (*DeploymentAccess, error) {
	if deployment == nil {
		return nil, errors.New("deployment cannot be nil")
	}
	target := resolveContainerTarget(deployment)
	if target == "" {
		return nil, errors.New("cannot resolve access without container target or ID")
	}

	// Inspect container for fresh runtime ports and state
	insRes, err := p.Inspect(ctx, conn, deployment)
	if err != nil {
		// Fallback to cached runtime if present
		if deployment.Runtime.ContainerID != "" || deployment.Runtime.ProviderResourceID != "" {
			return p.accessFromRuntime(deployment.Runtime), nil
		}
		return nil, fmt.Errorf("failed to resolve access for deployment %s: %w", deployment.ID, err)
	}

	rt := insRes.Runtime
	if deployment.Runtime.RemoteWorkspacePath != "" && rt.RemoteWorkspacePath == "" {
		rt.RemoteWorkspacePath = deployment.Runtime.RemoteWorkspacePath
	}
	return p.accessFromRuntime(rt), nil
}

func (p *LocalDockerProvider) accessFromRuntime(rt environments.RuntimeMetadata) *DeploymentAccess {
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

type localDockerTransport struct {
	runner CommandRunner
	conn   *environments.Connection
}

func (t *localDockerTransport) RunExec(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, execArgs ...string) error {
	args := append(dockerHostArgs(t.conn), "exec")
	args = append(args, execArgs...)
	return t.runner.RunWithIO(ctx, stdin, stdout, stderr, "docker", args...)
}

func (t *localDockerTransport) RunExecCombined(ctx context.Context, execArgs ...string) ([]byte, error) {
	args := append(dockerHostArgs(t.conn), "exec")
	args = append(args, execArgs...)
	return t.runner.RunCombined(ctx, "docker", args...)
}

// Exec executes a command inside the running deployment container under supervised process control.
func (p *LocalDockerProvider) Exec(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment, req ExecRequest) (*ExecResult, error) {
	target, err := validateExecParams(deployment, req)
	if err != nil {
		return nil, err
	}
	transport := &localDockerTransport{runner: p.runner, conn: conn}
	return executeSupervised(ctx, transport, target, req)
}

// CancelExec cancels a running or orphaned execution operation inside the target container.
func (p *LocalDockerProvider) CancelExec(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment, req CancelExecRequest) (*CancelExecResult, error) {
	target, err := validateCancelParams(deployment, req)
	if err != nil {
		return nil, err
	}
	transport := &localDockerTransport{runner: p.runner, conn: conn}
	return cancelSupervised(ctx, transport, target, req)
}

// Internal helpers

func dockerHostArgs(conn *environments.Connection) []string {
	if conn == nil || conn.LocalDocker == nil {
		return nil
	}
	if conn.LocalDocker.Host != "" {
		return []string{"-H", conn.LocalDocker.Host}
	}
	if conn.LocalDocker.SocketPath != "" {
		socket := conn.LocalDocker.SocketPath
		if !strings.HasPrefix(socket, "unix://") {
			socket = "unix://" + socket
		}
		return []string{"-H", socket}
	}
	return nil
}

func resolveEnvValue(val string, overrides map[string]string) string {
	return os.Expand(val, func(key string) string {
		if overrides != nil {
			if v, ok := overrides[key]; ok {
				return v
			}
		}
		return os.Getenv(key)
	})
}

type dockerInspectJSON struct {
	ID    string `json:"Id"`
	State struct {
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
	} `json:"State"`
	NetworkSettings struct {
		IPAddress string `json:"IPAddress"`
		Ports     map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
	Config struct {
		Image      string `json:"Image"`
		WorkingDir string `json:"WorkingDir"`
	} `json:"Config"`
}

func parseDockerInspect(data []byte) (*dockerInspectJSON, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("empty docker inspect output")
	}
	var list []dockerInspectJSON
	if err := json.Unmarshal(data, &list); err == nil {
		if len(list) == 0 {
			return nil, errors.New("empty docker inspect record list")
		}
		return &list[0], nil
	}
	var single dockerInspectJSON
	if err := json.Unmarshal(data, &single); err != nil {
		return nil, fmt.Errorf("failed to parse docker inspect output: %w", err)
	}
	return &single, nil
}

func defaultHTTPGet(ctx context.Context, targetURL string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
