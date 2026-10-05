package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/pkg/environments"
)

// NewLocalPodmanProvider shares the bounded container lifecycle and supervised
// exec transport, but selects an explicit local-only rootless engine contract.
func NewLocalPodmanProvider(runner CommandRunner) *LocalDockerProvider {
	p := NewLocalDockerProvider(runner)
	p.kind = environments.ConnectionKindLocalPodman
	if osRunner, ok := p.runner.(*OSCommandRunner); ok {
		// Copy rather than changing a runner shared with another provider.
		localRunner := *osRunner
		localRunner.commandEnv = localPodmanEnvironment
		p.runner = &localRunner
	}
	return p
}

// ValidateRuntimeConnection is checked before lifecycle reuse or mutation, not
// merely at container creation. Stored capability overrides are not authority.
func ValidateRuntimeConnection(conn *environments.Connection, env *environments.Environment) error {
	if conn == nil || env == nil {
		return errors.New("connection and environment are required")
	}
	if env.Container.RootlessSystemd != nil && conn.Kind != environments.ConnectionKindLocalPodman {
		return errors.New("rootless_systemd requires an explicit local_podman connection; no fallback is supported")
	}
	if conn.Kind == environments.ConnectionKindLocalPodman {
		if err := conn.Validate(); err != nil {
			return err
		}
		if env.Container.RootlessSystemd == nil {
			return errors.New("local_podman requires container.rootless_systemd configuration")
		}
		if err := env.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (p *LocalDockerProvider) podmanCapabilities(ctx context.Context, conn *environments.Connection) (environments.ConnectionCapabilities, error) {
	var empty environments.ConnectionCapabilities
	if conn == nil || conn.Kind != environments.ConnectionKindLocalPodman {
		return empty, errors.New("invalid local Podman connection")
	}
	if err := conn.Validate(); err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	out, err := p.runner.Run(ctx, "podman", append(dockerHostArgs(conn), "info", "--format=json")...)
	if err != nil {
		return empty, fmt.Errorf("local Podman capability probe failed: %s", commandDiagnostic(err.Error()+": "+string(out)))
	}
	var info struct {
		Version struct {
			Version string `json:"Version"`
		} `json:"version"`
		Host struct {
			OS              string `json:"os"`
			Arch            string `json:"arch"`
			ServiceIsRemote bool   `json:"serviceIsRemote"`
			Security        struct {
				Rootless bool `json:"rootless"`
			} `json:"security"`
			CgroupVersion     string   `json:"cgroupVersion"`
			CgroupManager     string   `json:"cgroupManager"`
			CgroupControllers []string `json:"cgroupControllers"`
			OCIRuntime        struct {
				Name string `json:"name"`
			} `json:"ociRuntime"`
			Slirp4netns struct {
				Executable string `json:"executable"`
			} `json:"slirp4netns"`
		} `json:"host"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return empty, errors.New("Podman info did not return valid structured capability data")
	}
	if info.Host.OS != "linux" || !info.Host.Security.Rootless || info.Host.ServiceIsRemote {
		return empty, errors.New("local_podman requires a local rootless Linux engine; rootful/remote fallback is forbidden")
	}
	if info.Host.CgroupVersion != "v2" || info.Host.CgroupManager != "systemd" {
		return empty, errors.New("rootless_systemd requires cgroup v2 and the systemd cgroup manager; host configuration was not changed")
	}
	for _, required := range []string{"pids", "cpu", "memory"} {
		found := false
		for _, controller := range info.Host.CgroupControllers {
			if controller == required {
				found = true
			}
		}
		if !found {
			return empty, fmt.Errorf("rootless_systemd requires delegated %s cgroup controller; host configuration was not changed", required)
		}
	}
	if info.Host.OCIRuntime.Name != "crun" || info.Host.Slirp4netns.Executable == "" || strings.TrimSpace(info.Version.Version) == "" {
		return empty, errors.New("rootless_systemd requires crun, slirp4netns and a reported Podman version")
	}
	// A configured manager name alone does not prove that the user's service
	// manager is reachable. Query metadata only, never its environment/secrets.
	managerVersion, err := p.runner.Run(ctx, "systemctl", "--user", "show", "--property=Version", "--value")
	if err != nil {
		return empty, fmt.Errorf("rootless_systemd user manager unavailable (host configuration unchanged): %s", commandDiagnostic(err.Error()))
	}
	if strings.TrimSpace(string(managerVersion)) == "" {
		return empty, errors.New("rootless_systemd user manager did not report a version")
	}
	return environments.ConnectionCapabilities{SupportsPodman: true, RootlessSystemd: true, SupportsPortForward: true, RemoteOS: info.Host.OS, RemoteArch: info.Host.Arch, EngineVersion: info.Version.Version}, nil
}

// validatePodmanInspect checks observed isolation rather than relabeling public
// ports as loopback or trusting a cached connection capability claim.
func validatePodmanInspect(data []byte) error {
	var records []struct {
		HostConfig struct {
			Privileged    bool   `json:"Privileged"`
			CgroupMode    string `json:"CgroupMode"`
			CgroupManager string `json:"CgroupManager"`
			NetworkMode   string `json:"NetworkMode"`
			PidsLimit     int64  `json:"PidsLimit"`
		} `json:"HostConfig"`
		Config struct {
			SystemdMode bool `json:"SystemdMode"`
		} `json:"Config"`
		Mounts []struct {
			Type string `json:"Type"`
		} `json:"Mounts"`
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIP string `json:"HostIp"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(data, &records); err != nil || len(records) != 1 {
		return errors.New("Podman inspect must return exactly one container")
	}
	r := records[0]
	if r.HostConfig.Privileged || r.HostConfig.CgroupMode != "private" || r.HostConfig.CgroupManager != "systemd" || !r.Config.SystemdMode || (r.HostConfig.NetworkMode != "slirp4netns" && r.HostConfig.NetworkMode != "slirp4netns:allow_host_loopback=false") || r.HostConfig.PidsLimit < 1 || r.HostConfig.PidsLimit > 65536 {
		return errors.New("Podman container does not satisfy required rootless_systemd isolation")
	}
	for _, mount := range r.Mounts {
		if mount.Type == "bind" {
			return errors.New("rootless_systemd container has an unexpected host bind mount")
		}
	}
	for _, ports := range r.NetworkSettings.Ports {
		for _, port := range ports {
			if port.HostIP != "127.0.0.1" {
				return errors.New("Podman container has non-loopback port publication")
			}
		}
	}
	return nil
}

func (p *LocalDockerProvider) validateLocalKind(conn *environments.Connection) error {
	// Preserve the existing nil/default Docker transport contract.
	if p.Kind() == environments.ConnectionKindLocalDocker && conn == nil {
		return nil
	}
	if conn == nil || conn.Kind != p.Kind() {
		return errors.New("connection does not match local provider kind")
	}
	if p.Kind() == environments.ConnectionKindLocalPodman {
		return conn.Validate()
	}
	return nil
}
