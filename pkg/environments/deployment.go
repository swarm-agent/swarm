package environments

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// DeploymentStatus represents the current lifecycle state of an active or historical deployment.
type DeploymentStatus string

const (
	DeploymentStatusPending      DeploymentStatus = "pending"
	DeploymentStatusProvisioning DeploymentStatus = "provisioning"
	DeploymentStatusStarting     DeploymentStatus = "starting"
	DeploymentStatusRunning      DeploymentStatus = "running"
	DeploymentStatusReady        DeploymentStatus = "ready"
	DeploymentStatusBusy         DeploymentStatus = "busy"
	DeploymentStatusStopping     DeploymentStatus = "stopping"
	DeploymentStatusStopped      DeploymentStatus = "stopped"
	DeploymentStatusFailed       DeploymentStatus = "failed"
	DeploymentStatusTerminated   DeploymentStatus = "terminated"
)

// HealthStatus represents the health check status of the container.
type HealthStatus string

const (
	HealthStatusUnknown   HealthStatus = "unknown"
	HealthStatusStarting  HealthStatus = "starting"
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
)

// AssignedPort records a port allocated and forwarded from the environment host to the container.
type AssignedPort struct {
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	Protocol      string `json:"protocol"` // "tcp" or "udp"
	EndpointURL   string `json:"endpoint_url,omitempty"`
}

// RuntimeMetadata stores the dynamic runtime attributes of an instantiated environment deployment.
type RuntimeMetadata struct {
	ContainerID         string         `json:"container_id,omitempty"`
	ProviderResourceID  string         `json:"provider_resource_id,omitempty"`
	Endpoint            string         `json:"endpoint,omitempty"` // primary service endpoint (e.g. http://127.0.0.1:18080)
	AssignedPorts       []AssignedPort `json:"assigned_ports,omitempty"`
	RemoteWorkspacePath string         `json:"remote_workspace_path,omitempty"`
	RuntimeIP           string         `json:"runtime_ip,omitempty"`
	EngineVersion       string         `json:"engine_version,omitempty"`
}

// DeploymentLifecycle captures timestamps for deployment lifecycle events.
type DeploymentLifecycle struct {
	CreatedAt    int64 `json:"created_at"`
	StartedAt    int64 `json:"started_at,omitempty"`
	ReadyAt      int64 `json:"ready_at,omitempty"`
	StoppedAt    int64 `json:"stopped_at,omitempty"`
	TerminatedAt int64 `json:"terminated_at,omitempty"`
	LastActiveAt int64 `json:"last_active_at,omitempty"`
}

// Deployment represents an instantiated, running or historical execution environment.
// Strictly scoped to AccountScopeID and WorkspaceID.
type Deployment struct {
	ID             string              `json:"id"`
	AccountScopeID string              `json:"account_scope_id"`
	WorkspaceID    string              `json:"workspace_id"`
	EnvironmentID  string              `json:"environment_id"`
	ConnectionID   string              `json:"connection_id"`
	Name           string              `json:"name"`
	Status         DeploymentStatus    `json:"status"`
	Health         HealthStatus        `json:"health"`
	ErrorMessage   string              `json:"error_message,omitempty"`
	Runtime        RuntimeMetadata     `json:"runtime"`
	Lifecycle      DeploymentLifecycle `json:"lifecycle"`
	CreatedAt      int64               `json:"created_at"`
	UpdatedAt      int64               `json:"updated_at"`
}

// Validate checks that the Deployment satisfies domain constraints and scoping rules.
func (d *Deployment) Validate() error {
	if d == nil {
		return errors.New("deployment is nil")
	}

	d.ID = strings.TrimSpace(d.ID)
	if d.ID == "" {
		return errors.New("deployment id cannot be empty")
	}
	if len(d.ID) > maxIDBytes || !utf8.ValidString(d.ID) {
		return fmt.Errorf("deployment id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	d.AccountScopeID = strings.TrimSpace(d.AccountScopeID)
	if d.AccountScopeID == "" {
		return errors.New("account_scope_id cannot be empty")
	}
	if len(d.AccountScopeID) > maxIDBytes || !utf8.ValidString(d.AccountScopeID) {
		return fmt.Errorf("account_scope_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	d.WorkspaceID = strings.TrimSpace(d.WorkspaceID)
	if d.WorkspaceID == "" {
		return errors.New("workspace_id cannot be empty")
	}
	if len(d.WorkspaceID) > maxIDBytes || !utf8.ValidString(d.WorkspaceID) {
		return fmt.Errorf("workspace_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	d.EnvironmentID = strings.TrimSpace(d.EnvironmentID)
	if d.EnvironmentID == "" {
		return errors.New("environment_id cannot be empty")
	}

	d.ConnectionID = strings.TrimSpace(d.ConnectionID)
	if d.ConnectionID == "" {
		return errors.New("connection_id cannot be empty")
	}

	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		return errors.New("deployment name cannot be empty")
	}
	if len(d.Name) > maxNameBytes || !utf8.ValidString(d.Name) {
		return fmt.Errorf("deployment name exceeds %d bytes or is invalid UTF-8", maxNameBytes)
	}

	switch d.Status {
	case DeploymentStatusPending,
		DeploymentStatusProvisioning,
		DeploymentStatusStarting,
		DeploymentStatusRunning,
		DeploymentStatusReady,
		DeploymentStatusBusy,
		DeploymentStatusStopping,
		DeploymentStatusStopped,
		DeploymentStatusFailed,
		DeploymentStatusTerminated:
		// valid
	default:
		return fmt.Errorf("unsupported deployment status: %q", d.Status)
	}

	switch d.Health {
	case "", HealthStatusUnknown, HealthStatusStarting, HealthStatusHealthy, HealthStatusUnhealthy:
		// valid
	default:
		return fmt.Errorf("unsupported health status: %q", d.Health)
	}

	for i, p := range d.Runtime.AssignedPorts {
		if p.ContainerPort <= 0 || p.ContainerPort > 65535 {
			return fmt.Errorf("assigned_ports[%d] container_port must be between 1 and 65535", i)
		}
		if p.HostPort <= 0 || p.HostPort > 65535 {
			return fmt.Errorf("assigned_ports[%d] host_port must be between 1 and 65535", i)
		}
		if p.Protocol != "" && p.Protocol != "tcp" && p.Protocol != "udp" {
			return fmt.Errorf("assigned_ports[%d] protocol must be 'tcp' or 'udp'", i)
		}
	}

	return nil
}

// IsActive returns true if the deployment is alive or in transition.
func (d *Deployment) IsActive() bool {
	if d == nil {
		return false
	}
	return d.Status == DeploymentStatusPending ||
		d.Status == DeploymentStatusProvisioning ||
		d.Status == DeploymentStatusStarting ||
		d.Status == DeploymentStatusRunning ||
		d.Status == DeploymentStatusReady ||
		d.Status == DeploymentStatusBusy
}

// IsRunning returns true if the deployment is fully running.
func (d *Deployment) IsRunning() bool {
	if d == nil {
		return false
	}
	return d.Status == DeploymentStatusRunning ||
		d.Status == DeploymentStatusReady ||
		d.Status == DeploymentStatusBusy
}

// IsUsable returns true if the deployment is running and not failing health checks.
func (d *Deployment) IsUsable() bool {
	if d == nil {
		return false
	}
	return (d.Status == DeploymentStatusRunning ||
		d.Status == DeploymentStatusReady ||
		d.Status == DeploymentStatusBusy) && d.Health != HealthStatusUnhealthy
}

// IsTerminal returns true if the deployment has concluded its lifecycle.
func (d *Deployment) IsTerminal() bool {
	if d == nil {
		return true
	}
	return d.Status == DeploymentStatusStopped ||
		d.Status == DeploymentStatusFailed ||
		d.Status == DeploymentStatusTerminated
}

// GetPort returns the AssignedPort corresponding to the container port.
func (d *Deployment) GetPort(containerPort int) (AssignedPort, bool) {
	if d == nil {
		return AssignedPort{}, false
	}
	for _, p := range d.Runtime.AssignedPorts {
		if p.ContainerPort == containerPort {
			return p, true
		}
	}
	return AssignedPort{}, false
}

// Clone returns a deep copy of Deployment.
func (d *Deployment) Clone() *Deployment {
	if d == nil {
		return nil
	}
	cp := *d
	if d.Runtime.AssignedPorts != nil {
		cp.Runtime.AssignedPorts = append([]AssignedPort(nil), d.Runtime.AssignedPorts...)
	}
	return &cp
}
