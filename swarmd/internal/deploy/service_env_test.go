package deploy

import (
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func envValue(values []string, key string) string {
	for _, value := range values {
		currentKey, currentValue, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}
		if currentKey == key {
			return currentValue
		}
	}
	return ""
}

func TestBuildChildContainerEnvKeepsPublishedPortsReachableWithLocalTransport(t *testing.T) {
	env := buildChildContainerEnv(containerBootstrapEnvInput{
		ChildName:                "sync-debug-child",
		DeploymentID:             "sync-debug-child",
		BootstrapSecret:          "bootstrap-secret",
		HostAPIBaseURL:           "http://127.0.0.1:8781",
		HostDesktopURL:           "http://127.0.0.1:9781",
		LocalTransportSocketPath: childLocalTransportSocketPath,
		ChildAdvertiseHost:       "127.0.0.1",
		ChildAdvertisePort:       8782,
		SyncEnabled:              true,
		SyncMode:                 "managed",
		SyncModules:              []string{"credentials", "agents", "custom_tools"},
		SyncOwnerSwarmID:         "swarm_host",
		SyncCredentialURL:        "http://127.0.0.1:8781/v1/deploy/container/sync/credentials",
		SyncAgentURL:             "http://127.0.0.1:8781/v1/deploy/container/sync/agents",
		BypassPermissions:        true,
	})

	if got := envValue(env, "SWARMD_LISTEN"); got != "0.0.0.0:7781" {
		t.Fatalf("SWARMD_LISTEN = %q, want %q", got, "0.0.0.0:7781")
	}

	encoded := envValue(env, "SWARM_CHILD_STARTUP_CONFIG")
	if encoded == "" {
		t.Fatalf("SWARM_CHILD_STARTUP_CONFIG was empty")
	}

	t.Setenv("SWARM_CHILD_STARTUP_CONFIG", encoded)
	cfg, err := startupconfig.Load(filepath.Join(t.TempDir(), "swarm-child.conf"))
	if err != nil {
		t.Fatalf("load child startup config: %v", err)
	}
	if got := cfg.DeployContainer.LocalTransportSocketPath; got != childLocalTransportSocketPath {
		t.Fatalf("child local transport socket path = %q, want %q", got, childLocalTransportSocketPath)
	}
	if got := cfg.DeployContainer.SyncAgentURL; got != "http://127.0.0.1:8781/v1/deploy/container/sync/agents" {
		t.Fatalf("child sync agent url = %q, want %q", got, "http://127.0.0.1:8781/v1/deploy/container/sync/agents")
	}
	if len(cfg.DeployContainer.SyncModules) != 3 {
		t.Fatalf("child sync modules = %#v, want 3 entries", cfg.DeployContainer.SyncModules)
	}
	if got := cfg.AdvertisePort; got != 8782 {
		t.Fatalf("child advertise port = %d, want %d", got, 8782)
	}
	if !cfg.BypassPermissions {
		t.Fatalf("child bypass permissions = %t, want true", cfg.BypassPermissions)
	}
}

func TestResolveLocalContainerBootstrapTargetsLoopbackBindPrefersLocalTransport(t *testing.T) {
	cfg := startupconfig.Default(filepath.Join(t.TempDir(), "swarm.conf"))
	cfg.Host = "127.0.0.2"
	cfg.Port = 7781
	cfg.DesktopPort = 5555
	cfg.AdvertiseHost = "204.168.240.114"
	state := swarmruntime.LocalState{
		Node: swarmruntime.LocalNodeState{
			Transports: []swarmruntime.TransportSummary{
				{
					Kind:    startupconfig.NetworkModeTailscale,
					Primary: "https://dev-hel1.tail617a4d.ts.net",
					All:     []string{"https://dev-hel1.tail617a4d.ts.net", "100.101.195.59"},
				},
			},
		},
	}

	host, apiBaseURL, desktopURL, hostDriven, err := resolveLocalContainerBootstrapTargets(cfg, state, "docker", childLocalTransportSocketPath)
	if err != nil {
		t.Fatalf("resolveLocalContainerBootstrapTargets() error = %v", err)
	}
	if host != startupconfig.DefaultHost {
		t.Fatalf("host = %q, want %q", host, startupconfig.DefaultHost)
	}
	if apiBaseURL != "http://127.0.0.1:7781" {
		t.Fatalf("apiBaseURL = %q, want %q", apiBaseURL, "http://127.0.0.1:7781")
	}
	if desktopURL != "http://127.0.0.1:5555" {
		t.Fatalf("desktopURL = %q, want %q", desktopURL, "http://127.0.0.1:5555")
	}
	if !hostDriven {
		t.Fatalf("hostDriven = %t, want true", hostDriven)
	}
}

func TestResolveLocalContainerBootstrapTargetsLoopbackBindFallsBackToHostDrivenWithoutSocket(t *testing.T) {
	cfg := startupconfig.Default(filepath.Join(t.TempDir(), "swarm.conf"))
	cfg.Host = "127.0.0.2"
	cfg.Port = 7781
	cfg.DesktopPort = 5555
	cfg.AdvertiseHost = "204.168.240.114"
	state := swarmruntime.LocalState{
		Node: swarmruntime.LocalNodeState{
			Transports: []swarmruntime.TransportSummary{
				{
					Kind:    startupconfig.NetworkModeTailscale,
					Primary: "https://dev-hel1.tail617a4d.ts.net",
					All:     []string{"https://dev-hel1.tail617a4d.ts.net", "100.101.195.59"},
				},
			},
		},
	}

	host, apiBaseURL, desktopURL, hostDriven, err := resolveLocalContainerBootstrapTargets(cfg, state, "docker", "")
	if err != nil {
		t.Fatalf("resolveLocalContainerBootstrapTargets() error = %v", err)
	}
	if host != startupconfig.DefaultHost {
		t.Fatalf("host = %q, want %q", host, startupconfig.DefaultHost)
	}
	if apiBaseURL != "http://127.0.0.1:7781" {
		t.Fatalf("apiBaseURL = %q, want %q", apiBaseURL, "http://127.0.0.1:7781")
	}
	if desktopURL != "http://127.0.0.1:5555" {
		t.Fatalf("desktopURL = %q, want %q", desktopURL, "http://127.0.0.1:5555")
	}
	if !hostDriven {
		t.Fatalf("hostDriven = %t, want true", hostDriven)
	}
}
