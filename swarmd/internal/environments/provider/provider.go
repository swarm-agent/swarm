package provider

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// DeploymentProvider manages the lifecycle and runtime access of environments on a target connection.
type DeploymentProvider interface {
	// Kind returns the ConnectionKind that this provider handles (e.g. ConnectionKindLocalDocker, ConnectionKindSSH).
	Kind() environments.ConnectionKind

	// ValidateConnection checks that the connection configuration is valid and reachable.
	ValidateConnection(ctx context.Context, conn *environments.Connection) error

	// Capabilities returns the connection capabilities supported by this provider for the given connection.
	Capabilities(ctx context.Context, conn *environments.Connection) (environments.ConnectionCapabilities, error)

	// Deploy provisions and starts a deployment for the given environment on the connection host.
	Deploy(ctx context.Context, req DeployRequest) (*DeployResult, error)

	// Inspect retrieves current runtime status and health for a deployment.
	Inspect(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) (*InspectResult, error)

	// Start starts a stopped deployment container.
	Start(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) error

	// Stop stops a running deployment container.
	Stop(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) error

	// Destroy stops and cleans up the container and associated resources.
	Destroy(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) error

	// ResolveAccess returns consumer-relevant access metadata for an active deployment.
	ResolveAccess(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) (*DeploymentAccess, error)

	// Exec executes a command inside the running deployment container.
	Exec(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment, req ExecRequest) (*ExecResult, error)
}

// DeployRequest contains parameters for instantiating an environment deployment.
type DeployRequest struct {
	Connection    *environments.Connection
	Environment   *environments.Environment
	Deployment    *environments.Deployment
	WorkspacePath string            // Host workspace path (used by local_mount if HostPath is empty)
	EnvOverrides  map[string]string // Optional environment variable overrides
}

// DeployResult contains the outcome of a Deploy operation.
type DeployResult struct {
	Runtime      environments.RuntimeMetadata  `json:"runtime"`
	Health       environments.HealthStatus     `json:"health"`
	Status       environments.DeploymentStatus `json:"status"`
	ErrorMessage string                        `json:"error_message,omitempty"`
}

// InspectResult contains runtime state and health retrieved from inspecting the container.
type InspectResult struct {
	Status       environments.DeploymentStatus `json:"status"`
	Health       environments.HealthStatus     `json:"health"`
	Runtime      environments.RuntimeMetadata  `json:"runtime"`
	ErrorMessage string                        `json:"error_message,omitempty"`
}

// DeploymentAccess exposes consumer-relevant access metadata for AI tasks, test runners, or workers.
type DeploymentAccess struct {
	PrimaryEndpoint     string                      `json:"primary_endpoint,omitempty"` // e.g. "http://127.0.0.1:18080"
	Endpoints           map[string]string           `json:"endpoints,omitempty"`        // port or name mapped endpoints, e.g. "http": "http://127.0.0.1:18080", "8080": "http://127.0.0.1:18080"
	ExecSupported       bool                        `json:"exec_supported"`
	RemoteWorkspacePath string                      `json:"remote_workspace_path,omitempty"` // working dir inside container (e.g. "/workspace")
	MappedPorts         []environments.AssignedPort `json:"mapped_ports,omitempty"`
	ContainerID         string                      `json:"container_id,omitempty"`
	RuntimeIP           string                      `json:"runtime_ip,omitempty"`
}

// ExecRequest specifies a command to execute inside a running container.
type ExecRequest struct {
	Command    []string          `json:"command"`
	WorkingDir string            `json:"working_dir,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Stdin      io.Reader         `json:"-"`
	Timeout    time.Duration     `json:"timeout,omitempty"`
}

// ExecResult contains the output and exit code of a command execution.
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// Success returns true if the command exited with code 0.
func (r *ExecResult) Success() bool {
	return r != nil && r.ExitCode == 0
}

// CombinedOutput returns combined stdout and stderr.
func (r *ExecResult) CombinedOutput() string {
	if r == nil {
		return ""
	}
	if r.Stdout != "" && r.Stderr != "" {
		return r.Stdout + "\n" + r.Stderr
	}
	if r.Stdout != "" {
		return r.Stdout
	}
	return r.Stderr
}

// Registry maintains registered DeploymentProvider instances by ConnectionKind.
type Registry struct {
	mu        sync.RWMutex
	providers map[environments.ConnectionKind]DeploymentProvider
}

// NewRegistry creates a new empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[environments.ConnectionKind]DeploymentProvider),
	}
}

// Register registers a provider for its Kind().
func (r *Registry) Register(p DeploymentProvider) {
	if p == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Kind()] = p
}

// Get retrieves a provider by connection kind.
func (r *Registry) Get(kind environments.ConnectionKind) (DeploymentProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[kind]
	return p, ok
}

// Providers returns all registered providers.
func (r *Registry) Providers() []DeploymentProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]DeploymentProvider, 0, len(r.providers))
	for _, p := range r.providers {
		list = append(list, p)
	}
	return list
}

// CommandRunner abstracts command execution for testability and portability.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	RunCombined(ctx context.Context, name string, args ...string) ([]byte, error)
	RunWithIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
}

// OSCommandRunner executes commands via os/exec.
type OSCommandRunner struct{}

func (r *OSCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Output()
}

func (r *OSCommandRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

func (r *OSCommandRunner) RunWithIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
