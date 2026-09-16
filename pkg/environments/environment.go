package environments

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// EnvironmentMode defines how an environment is used.
type EnvironmentMode string

const (
	// EnvironmentModeAttached represents an environment attached for interactive development or live session execution.
	EnvironmentModeAttached EnvironmentMode = "attached"

	// EnvironmentModeDeployable represents an environment provisioned and deployed on-demand for jobs, tests, or tasks.
	EnvironmentModeDeployable EnvironmentMode = "deployable"
)

// EnvironmentRole categorizes the functional role of the environment.
type EnvironmentRole string

const (
	EnvironmentRoleDevelopment EnvironmentRole = "development"
	EnvironmentRoleTesting     EnvironmentRole = "testing"
	EnvironmentRoleBuild       EnvironmentRole = "build"
	EnvironmentRoleCustom      EnvironmentRole = "custom"
)

// ReleaseBehavior specifies what happens to a deployment when its lease or consumer releases it.
type ReleaseBehavior string

const (
	// ReleaseBehaviorNone keeps the running instance as-is for immediate reuse without restarting.
	ReleaseBehaviorNone ReleaseBehavior = "none"

	// ReleaseBehaviorRestart restarts the container when released to provide a clean process state.
	ReleaseBehaviorRestart ReleaseBehavior = "restart"

	// ReleaseBehaviorRecreate stops and removes the container, creating a fresh instance on next deployment.
	ReleaseBehaviorRecreate ReleaseBehavior = "recreate"
)

// PortMapping defines a port exposed by the container.
type PortMapping struct {
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port,omitempty"` // optional requested host port; 0 means dynamic allocation
	Protocol      string `json:"protocol,omitempty"`  // "tcp" (default) or "udp"
}

// ContainerDefinition specifies the container configuration.
// Strictly contains NO runtime state (container_id, live IPs, runtime ports).
type ContainerDefinition struct {
	Image        string            `json:"image"`
	Command      []string          `json:"command,omitempty"`
	Args         []string          `json:"args,omitempty"`
	EnvVars      map[string]string `json:"env_vars,omitempty"`
	ExposedPorts []PortMapping     `json:"exposed_ports,omitempty"`
	Privileged   bool              `json:"privileged,omitempty"`
	User         string            `json:"user,omitempty"`
	WorkingDir    string            `json:"working_dir,omitempty"`
	SetupCommands []string          `json:"setup_commands,omitempty"`
}

// HealthCheck specifies how to determine if the container is healthy and ready.
type HealthCheck struct {
	Test               []string `json:"test,omitempty"` // e.g. ["CMD", "curl", "-f", "http://localhost:8080/health"]
	HTTPPath           string   `json:"http_path,omitempty"`
	HTTPPort           int      `json:"http_port,omitempty"`
	IntervalSeconds    int      `json:"interval_seconds,omitempty"`
	TimeoutSeconds     int      `json:"timeout_seconds,omitempty"`
	Retries            int      `json:"retries,omitempty"`
	StartPeriodSeconds int      `json:"start_period_seconds,omitempty"`
}

// ResourceRequirements specifies resource limits and requirements for the container.
type ResourceRequirements struct {
	CPULimit      string `json:"cpu_limit,omitempty"`    // e.g. "2.0"
	MemoryLimit   string `json:"memory_limit,omitempty"` // e.g. "4Gi"
	GPURequired   bool   `json:"gpu_required,omitempty"`
	GPUCount      int    `json:"gpu_count,omitempty"`
}

// DeploymentPolicy defines reuse, capacity, and release rules.
type DeploymentPolicy struct {
	// Reuse determines if existing deployed instances can be reused when released or idle.
	Reuse bool `json:"reuse"`

	// MaxInstances limits the maximum concurrent instances of this environment. Default is 1.
	MaxInstances int `json:"max_instances"`

	// ReleaseBehavior defines what happens when a lease is released: none, restart, recreate.
	ReleaseBehavior ReleaseBehavior `json:"release_behavior"`

	// IdleTimeoutSeconds is the number of seconds of inactivity before an idle instance may be stopped (0 = disabled).
	IdleTimeoutSeconds int `json:"idle_timeout_seconds,omitempty"`
}

// Environment is the static, reusable definition of an execution environment.
// Invariant: strictly contains NO ephemeral runtime state (container IDs, runtime IPs,
// provider resource IDs, assigned ports, live status). Runtime instance state belongs in Deployment.
type Environment struct {
	ID                    string                `json:"id"`
	AccountScopeID        string                `json:"account_scope_id"`
	WorkspaceID           string                `json:"workspace_id"`
	Name                  string                `json:"name"`
	Description           string                `json:"description,omitempty"`
	Mode                  EnvironmentMode       `json:"mode"`
	Role                  EnvironmentRole       `json:"role"`
	PreferredConnectionID string                `json:"preferred_connection_id,omitempty"`
	Container             ContainerDefinition   `json:"container"`
	Provisioning          WorkspaceProvisioning `json:"provisioning"`
	DeploymentPolicy      DeploymentPolicy      `json:"deployment_policy"`
	HealthCheck           *HealthCheck          `json:"health_check,omitempty"`
	Resources             *ResourceRequirements `json:"resources,omitempty"`
	Labels                map[string]string     `json:"labels,omitempty"`
	CreatedAt             int64                 `json:"created_at"`
	UpdatedAt             int64                 `json:"updated_at"`
}

// Validate checks that the Environment definition adheres to domain invariants and constraints.
func (e *Environment) Validate() error {
	if e == nil {
		return errors.New("environment is nil")
	}

	e.ID = strings.TrimSpace(e.ID)
	if e.ID == "" {
		return errors.New("environment id cannot be empty")
	}
	if len(e.ID) > maxIDBytes || !utf8.ValidString(e.ID) {
		return fmt.Errorf("environment id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	e.AccountScopeID = strings.TrimSpace(e.AccountScopeID)
	if e.AccountScopeID == "" {
		return errors.New("account_scope_id cannot be empty")
	}
	if len(e.AccountScopeID) > maxIDBytes || !utf8.ValidString(e.AccountScopeID) {
		return fmt.Errorf("account_scope_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	e.WorkspaceID = strings.TrimSpace(e.WorkspaceID)
	if e.WorkspaceID == "" {
		return errors.New("workspace_id cannot be empty")
	}
	if len(e.WorkspaceID) > maxIDBytes || !utf8.ValidString(e.WorkspaceID) {
		return fmt.Errorf("workspace_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" {
		return errors.New("environment name cannot be empty")
	}
	if len(e.Name) > maxNameBytes || !utf8.ValidString(e.Name) {
		return fmt.Errorf("environment name exceeds %d bytes or is invalid UTF-8", maxNameBytes)
	}

	if len(e.Description) > maxDescriptionBytes || !utf8.ValidString(e.Description) {
		return fmt.Errorf("environment description exceeds %d bytes or is invalid UTF-8", maxDescriptionBytes)
	}

	switch e.Mode {
	case EnvironmentModeAttached, EnvironmentModeDeployable:
		// valid
	default:
		return fmt.Errorf("unsupported environment mode: %q", e.Mode)
	}

	switch e.Role {
	case EnvironmentRoleDevelopment, EnvironmentRoleTesting, EnvironmentRoleBuild, EnvironmentRoleCustom:
		// valid
	default:
		return fmt.Errorf("unsupported environment role: %q", e.Role)
	}

	e.Container.Image = strings.TrimSpace(e.Container.Image)
	if e.Container.Image == "" {
		return errors.New("container image cannot be empty")
	}

	for i, port := range e.Container.ExposedPorts {
		if port.ContainerPort <= 0 || port.ContainerPort > 65535 {
			return fmt.Errorf("exposed_ports[%d] container_port must be between 1 and 65535", i)
		}
		if port.HostPort < 0 || port.HostPort > 65535 {
			return fmt.Errorf("exposed_ports[%d] host_port must be between 0 and 65535", i)
		}
		if port.Protocol != "" && port.Protocol != "tcp" && port.Protocol != "udp" {
			return fmt.Errorf("exposed_ports[%d] protocol must be 'tcp' or 'udp'", i)
		}
	}

	if err := e.Provisioning.Validate(); err != nil {
		return fmt.Errorf("invalid environment provisioning: %w", err)
	}

	if e.DeploymentPolicy.MaxInstances <= 0 {
		e.DeploymentPolicy.MaxInstances = 1
	}

	switch e.DeploymentPolicy.ReleaseBehavior {
	case "", ReleaseBehaviorNone:
		e.DeploymentPolicy.ReleaseBehavior = ReleaseBehaviorNone
	case ReleaseBehaviorRestart, ReleaseBehaviorRecreate:
		// valid
	default:
		return fmt.Errorf("unsupported release_behavior: %q", e.DeploymentPolicy.ReleaseBehavior)
	}

	if e.HealthCheck != nil {
		if e.HealthCheck.HTTPPort < 0 || e.HealthCheck.HTTPPort > 65535 {
			return errors.New("health_check http_port must be between 0 and 65535")
		}
	}

	return nil
}

// Clone returns a deep copy of Environment.
func (e *Environment) Clone() *Environment {
	if e == nil {
		return nil
	}
	cp := *e
	if e.Container.Command != nil {
		cp.Container.Command = append([]string(nil), e.Container.Command...)
	}
	if e.Container.Args != nil {
		cp.Container.Args = append([]string(nil), e.Container.Args...)
	}
	if e.Container.SetupCommands != nil {
		cp.Container.SetupCommands = append([]string(nil), e.Container.SetupCommands...)
	}
	if e.Container.EnvVars != nil {
		cp.Container.EnvVars = make(map[string]string, len(e.Container.EnvVars))
		for k, v := range e.Container.EnvVars {
			cp.Container.EnvVars[k] = v
		}
	}
	if e.Container.ExposedPorts != nil {
		cp.Container.ExposedPorts = append([]PortMapping(nil), e.Container.ExposedPorts...)
	}
	cp.Provisioning = e.Provisioning.Clone()
	if e.HealthCheck != nil {
		hc := *e.HealthCheck
		if e.HealthCheck.Test != nil {
			hc.Test = append([]string(nil), e.HealthCheck.Test...)
		}
		cp.HealthCheck = &hc
	}
	if e.Resources != nil {
		res := *e.Resources
		cp.Resources = &res
	}
	if e.Labels != nil {
		cp.Labels = make(map[string]string, len(e.Labels))
		for k, v := range e.Labels {
			cp.Labels[k] = v
		}
	}
	return &cp
}
