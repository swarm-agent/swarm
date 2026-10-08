package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
)

type rootlessProbeProvider struct {
	*mockProvider
	probes int
}

func (p *rootlessProbeProvider) Capabilities(context.Context, *environments.Connection) (environments.ConnectionCapabilities, error) {
	p.probes++
	return environments.ConnectionCapabilities{}, errors.New("delegated pids controller unavailable")
}

// Purpose: Ensure/Deploy admission in DeploymentManager must not trust persisted
// capability overrides or fall back to Docker. Real temporary stores prove the
// rejection leaves no deployment/lease state and reaches no provider mutation.
func TestRootlessRuntimeAdmissionNoMutation(t *testing.T) {
	h := setupTestHarness(t)
	conn, err := h.connections.Save(environments.Connection{ID: "podman", Name: "Podman", AccountScopeID: "account", WorkspaceID: "workspace", Kind: environments.ConnectionKindLocalPodman, Capabilities: environments.ConnectionCapabilities{SupportsDocker: true, SupportsPodman: true, RootlessSystemd: true}})
	if err != nil {
		t.Fatal(err)
	}
	env := createTestEnvironment(t, h.environments, "account", "workspace", "rootless", conn.ID, true, 1, environments.ReleaseBehaviorNone)
	env.Container.RootlessSystemd = &environments.RootlessSystemd{CgroupNamespace: "private", Network: "slirp4netns", PidsLimit: 1024}
	env.Provisioning = environments.WorkspaceProvisioning{Strategy: environments.SourceStrategy{Kind: environments.SourceStrategyKindRegistryImage, RegistryImage: &environments.RegistryImageConfig{Image: env.Container.Image, PullPolicy: "never"}}}
	if _, err = h.environments.Save(env); err != nil {
		t.Fatal(err)
	}
	probe := &rootlessProbeProvider{mockProvider: newMockProvider(environments.ConnectionKindLocalPodman)}
	registry := provider.NewRegistry()
	registry.Register(probe)
	manager := NewDeploymentManager(h.connections, h.environments, h.deployments, h.workspaces, registry)
	_, err = manager.EnsureDeployment(context.Background(), EnsureDeploymentRequest{AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: env.ID, ConsumerID: "consumer", ConsumerType: environments.ConsumerTypeTestRun})
	if err == nil || !strings.Contains(err.Error(), "pids") {
		t.Fatalf("unverified ensure: %v", err)
	}
	_, err = manager.DeployDeployment(context.Background(), DeployDeploymentRequest{AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: env.ID})
	if err == nil || !strings.Contains(err.Error(), "pids") {
		t.Fatalf("unverified deploy: %v", err)
	}
	deps, err := h.deployments.ListByEnvironment("account", "workspace", env.ID, 10)
	if err != nil || len(deps) != 0 || probe.probes != 2 || probe.deployCalls != 0 || probe.startCalls != 0 {
		t.Fatalf("admission changed runtime: deps=%v probes=%d err=%v", deps, probe.probes, err)
	}
	// Wrong-account lookup must fail before any additional capability probe.
	_, err = manager.EnsureDeployment(context.Background(), EnsureDeploymentRequest{AccountScopeID: "foreign", WorkspaceID: "workspace", EnvironmentID: env.ID, ConsumerID: "consumer", ConsumerType: environments.ConsumerTypeTestRun})
	if err == nil || probe.probes != 2 {
		t.Fatal("foreign account reached provider")
	}
}
