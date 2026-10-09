package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
)

// Purpose: environment definitions are written by agents, and with permission
// bypass on nobody reviews them. LocalDockerProvider.Deploy is the last point
// before `docker run`, so it must refuse host mounts outside the deployment's
// workspace (including symlink escapes), privileged mode, reserved swarm.*
// labels, and must never expand $VAR from the daemon's own environment. Each
// case asserts the refusal and that no container was started. Unit level with
// the mock runner: these are argument-construction decisions and need no
// engine.

func hardeningRequest(t *testing.T, workspace string) DeployRequest {
	t.Helper()
	return DeployRequest{
		Connection: &environments.Connection{
			ID: "conn-1", AccountScopeID: "acct-1", WorkspaceID: "ws-1",
			Name: "Local Docker", Kind: environments.ConnectionKindLocalDocker,
		},
		Environment: &environments.Environment{
			ID: "env-1", AccountScopeID: "acct-1", WorkspaceID: "ws-1", Name: "Dev",
			Mode: environments.EnvironmentModeDeployable, Role: environments.EnvironmentRoleDevelopment,
			Container: environments.ContainerDefinition{Image: "alpine:3.20"},
			Provisioning: environments.WorkspaceProvisioning{
				Strategy: environments.SourceStrategy{
					Kind:       environments.SourceStrategyKindLocalMount,
					LocalMount: &environments.LocalMountConfig{ContainerPath: "/workspace"},
				},
			},
		},
		Deployment: &environments.Deployment{
			ID: "dep-1", AccountScopeID: "acct-1", WorkspaceID: "ws-1", EnvironmentID: "env-1",
			ConnectionID: "conn-1", Name: "Dev", Status: environments.DeploymentStatusPending,
		},
		WorkspacePath: workspace,
	}
}

func dockerRunArgs(runner *mockRunner) []string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		for i, arg := range call.Args {
			if arg == "run" {
				return call.Args[i:]
			}
		}
	}
	return nil
}

func TestLocalDockerDeployRefusesMountsOutsideWorkspace(t *testing.T) {
	workspace := existingWorkspaceDir(t)
	outside := existingWorkspaceDir(t)
	escape := filepath.Join(workspace, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(req *DeployRequest){
		"local_mount host path outside": func(req *DeployRequest) {
			req.Environment.Provisioning.Strategy.LocalMount.HostPath = outside
		},
		"extra mount of docker socket": func(req *DeployRequest) {
			req.Environment.Provisioning.Mounts = []environments.AdditionalMount{{HostPath: "/var/run/docker.sock", ContainerPath: "/var/run/docker.sock"}}
		},
		"extra mount of filesystem root": func(req *DeployRequest) {
			req.Environment.Provisioning.Mounts = []environments.AdditionalMount{{HostPath: "/", ContainerPath: "/host"}}
		},
		"extra mount through symlink escape": func(req *DeployRequest) {
			req.Environment.Provisioning.Mounts = []environments.AdditionalMount{{HostPath: escape, ContainerPath: "/data"}}
		},
		"extra mount with docker volume syntax": func(req *DeployRequest) {
			req.Environment.Provisioning.Mounts = []environments.AdditionalMount{{HostPath: workspace + ":/etc", ContainerPath: "/data"}}
		},
		"no workspace path": func(req *DeployRequest) {
			req.WorkspacePath = ""
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			runner := newMockRunner()
			req := hardeningRequest(t, workspace)
			mutate(&req)
			if _, err := NewLocalDockerProvider(runner).Deploy(context.Background(), req); err == nil {
				t.Fatal("expected deploy to be refused")
			}
			if args := dockerRunArgs(runner); args != nil {
				t.Fatalf("no container may be started, got %v", args)
			}
		})
	}
}

func TestLocalDockerDeployAdmitsMountInsideWorkspace(t *testing.T) {
	workspace := existingWorkspaceDir(t)
	sub := filepath.Join(workspace, "data")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := newMockRunner()
	req := hardeningRequest(t, workspace)
	req.Environment.Provisioning.Mounts = []environments.AdditionalMount{{HostPath: sub, ContainerPath: "/data", ReadOnly: true}}
	if _, err := NewLocalDockerProvider(runner).Deploy(context.Background(), req); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	joined := strings.Join(dockerRunArgs(runner), " ")
	if !strings.Contains(joined, "-v "+workspace+":/workspace") || !strings.Contains(joined, "-v "+sub+":/data:ro") {
		t.Fatalf("expected confined mounts, got %s", joined)
	}
}

func TestLocalDockerDeployRefusesPrivilegedAndReservedLabels(t *testing.T) {
	workspace := existingWorkspaceDir(t)
	cases := map[string]func(req *DeployRequest){
		"privileged": func(req *DeployRequest) { req.Environment.Container.Privileged = true },
		"reserved label": func(req *DeployRequest) {
			req.Environment.Labels = map[string]string{"swarm.account_scope_id": "other"}
		},
		"dash image": func(req *DeployRequest) { req.Environment.Container.Image = "--privileged" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			runner := newMockRunner()
			req := hardeningRequest(t, workspace)
			mutate(&req)
			if _, err := NewLocalDockerProvider(runner).Deploy(context.Background(), req); err == nil {
				t.Fatal("expected deploy to be refused")
			}
			if args := dockerRunArgs(runner); args != nil {
				t.Fatalf("no container may be started, got %v", args)
			}
		})
	}
}

func TestLocalDockerDeployNeverExpandsDaemonEnvironment(t *testing.T) {
	t.Setenv("SWARM_TEST_DAEMON_SECRET", "daemon-value-must-not-leak")
	workspace := existingWorkspaceDir(t)
	runner := newMockRunner()
	req := hardeningRequest(t, workspace)
	req.Environment.Container.EnvVars = map[string]string{
		"LEAK":    "$SWARM_TEST_DAEMON_SECRET",
		"FROMOVR": "${PORT}",
	}
	req.EnvOverrides = map[string]string{"PORT": "8080"}
	if _, err := NewLocalDockerProvider(runner).Deploy(context.Background(), req); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	joined := strings.Join(dockerRunArgs(runner), " ")
	if strings.Contains(joined, "daemon-value-must-not-leak") {
		t.Fatalf("daemon environment leaked into the container: %s", joined)
	}
	if !strings.Contains(joined, "LEAK=${SWARM_TEST_DAEMON_SECRET}") || !strings.Contains(joined, "FROMOVR=8080") {
		t.Fatalf("expected literal unknown reference and override expansion, got %s", joined)
	}
}

func TestSSHDeployRefusesMountsOutsideRemotePath(t *testing.T) {
	runner := newMockSSHRunner()
	env := &environments.Environment{
		ID: "env-1", AccountScopeID: "acct-1", WorkspaceID: "ws-1", Name: "Remote",
		Mode: environments.EnvironmentModeDeployable, Role: environments.EnvironmentRoleDevelopment,
		Container: environments.ContainerDefinition{Image: "alpine:3.20"},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind:               environments.SourceStrategyKindRemoteExistingPath,
				RemoteExistingPath: &environments.RemoteExistingPathConfig{RemotePath: "/srv/app", ContainerPath: "/app"},
			},
			Mounts: []environments.AdditionalMount{{HostPath: "/srv/app/../../var/run/docker.sock", ContainerPath: "/sock"}},
		},
	}
	dep := &environments.Deployment{ID: "dep-1", AccountScopeID: "acct-1", WorkspaceID: "ws-1", EnvironmentID: "env-1", ConnectionID: "conn-ssh-1", Name: "Remote", Status: environments.DeploymentStatusPending}
	_, err := NewSSHDockerProvider(runner).Deploy(context.Background(), DeployRequest{Connection: testSSHConnection(), Environment: env, Deployment: dep})
	if err == nil || !strings.Contains(err.Error(), "outside remote_existing_path") {
		t.Fatalf("expected remote mount refusal, got %v", err)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call.Args, " "), "docker run") {
			t.Fatalf("no container may be started: %v", call.Args)
		}
	}
}
