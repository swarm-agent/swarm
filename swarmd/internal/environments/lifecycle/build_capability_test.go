package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
)

// Purpose: validateAdmission/executeBuild must reject local rootless_systemd
// definitions on SSH before persistence or provider effects. Real stores and an
// injected builder prove there is no silent remote/local runtime substitution.
func TestSSHManagedBuildCapabilityBeforeEffects(t *testing.T) {
	h, env, _, _ := buildLifecycleFixture(t)
	conn, err := h.connections.Save(environments.Connection{ID: "ssh", Name: "SSH", AccountScopeID: "account", WorkspaceID: "workspace", Kind: environments.ConnectionKindSSH, SSH: &environments.SSHConfig{Host: "example.invalid", User: "builder"}})
	if err != nil {
		t.Fatal(err)
	}
	p := &managedBuildProvider{mockProvider: newMockProvider(environments.ConnectionKindSSH)}
	h.manager.registry.Register(p)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, action := range []string{environments.OperationActionBuild, environments.OperationActionEnsure, environments.OperationActionDeploy} {
		req := SubmitOperationRequest{AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: env.ID, ConnectionID: conn.ID, Action: action, Attribution: environments.OperationAttribution{Actor: "test"}}
		if out, err := h.manager.Submit(ctx, req); err == nil || out != nil {
			t.Fatalf("%s admitted unsupported SSH capability: %v", action, err)
		}
	}
	if out, err := h.manager.executeBuild(ctx, "op_unsupported", SubmitOperationRequest{AccountScopeID: "account"}, &env, &conn); err == nil || out != nil {
		t.Fatalf("unsupported build executed: %+v %v", out, err)
	}
	if p.calls != 0 || p.cleanupCalls != 0 {
		t.Fatalf("capability rejection touched provider: builds=%d cleanup=%d", p.calls, p.cleanupCalls)
	}
	if _, found, err := h.opStore.Get("account", "workspace", "op_unsupported"); err != nil || found {
		t.Fatalf("capability rejection persisted operation: found=%t err=%v", found, err)
	}
	deps, err := h.deployments.ListByEnvironment("account", "workspace", env.ID, 10)
	if err != nil || len(deps) != 0 {
		t.Fatal("capability rejection allocated deployment")
	}
	if err := provider.ValidateRuntimeConnection(&conn, &env); err == nil {
		t.Fatalf("runtime selection bypassed build guard: %v", err)
	}
}

// Purpose: SSH build recovery must not replay remote work or manufacture a
// termination receipt. DeploymentManager.cleanupTargetProcess is the narrowest
// boundary between durable operation recovery and the SSH cleanup capability.
func TestSSHManagedBuildRecoveryUnconfirmed(t *testing.T) {
	h, _, _, _ := buildLifecycleFixture(t)
	conn, err := h.connections.Save(environments.Connection{ID: "ssh", Name: "Remote", AccountScopeID: "account", WorkspaceID: "workspace", Kind: environments.ConnectionKindSSH, SSH: &environments.SSHConfig{Host: "example.invalid", User: "tester"}})
	if err != nil {
		t.Fatal(err)
	}
	h.manager.registry.Register(provider.NewSSHDockerProvider(nil))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	confirmed, err := h.manager.cleanupTargetProcess(ctx, environments.EnvironmentOperation{OperationID: "op_interrupted", AccountScopeID: "account", WorkspaceID: "workspace", Action: environments.OperationActionBuild, BuildConnectionID: conn.ID}, nil, nil)
	if confirmed || !errors.Is(err, provider.ErrOperationNotConfirmed) {
		t.Fatal("remote termination fabricated", err)
	}
}
