package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
)

// Purpose: validateTaskLease is shared by prepared admission and supervised exec;
// missing durable authorization must fail closed, while exclusive legacy leases
// remain unaffected. The service unit layer isolates callback enforcement.
func TestTaskLeaseValidatorFailsClosed(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease := environments.DeploymentLease{Shared: true}
	if h.manager.validateTaskLease(ctx, lease) == nil { t.Fatal("unbound shared lease accepted") }
	lease.TaskBinding = &environments.TaskLeaseBinding{AttachmentID: "attachment"}
	if h.manager.validateTaskLease(ctx, lease) == nil { t.Fatal("missing resolver accepted") }
	calls := 0
	h.manager.SetTaskLeaseValidator(func(context.Context, environments.DeploymentLease) error { calls++; return errors.New("revoked") })
	if h.manager.validateTaskLease(ctx, lease) == nil || calls != 1 { t.Fatal("revocation ignored") }
	if h.manager.validateTaskLease(ctx, environments.DeploymentLease{}) != nil { t.Fatal("ordinary lease changed") }
}

// Purpose: explicit finite review cleanup must stop expired managed resources,
// never a live consumer. CleanupReviewDeployments and direct Exec are tested with
// a real store/fake provider to assert provider effects, not status labels alone.
func TestTaskReviewRetentionAndDirectExec(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := createTestConnection(t, h.connections, "account", "workspace", "connection", "Local")
	dep, err := h.deployments.Save(environments.Deployment{ID: "deployment", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "environment", ConnectionID: conn.ID, Name: "Review", Status: environments.DeploymentStatusReady, Build: &environments.ImageBuildResult{}, CreatedAt: time.Now().Add(-25*time.Hour).UnixMilli()})
	if err != nil { t.Fatal(err) }
	lease, err := h.deployments.AcquireLease(environments.DeploymentLease{AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: dep.ID, EnvironmentID: dep.EnvironmentID, ConsumerType: environments.ConsumerTypeSession, ConsumerID: "owner", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	if err != nil { t.Fatal(err) }
	stopped, err := h.manager.CleanupReviewDeployments(ctx, "account", "workspace")
	if err != nil || len(stopped) != 0 || h.mockProv.stopCalls != 0 { t.Fatalf("live consumer stopped: %v %v", stopped, err) }
	if _, err := h.deployments.ReleaseLease("account", "workspace", lease.ID, "done"); err != nil { t.Fatal(err) }
	stopped, err = h.manager.CleanupReviewDeployments(ctx, "account", "workspace")
	if err != nil || len(stopped) != 1 || h.mockProv.stopCalls != 1 { t.Fatalf("expired review not stopped: %v %v", stopped, err) }
	if _, err := h.manager.Exec(ctx, "account", "workspace", dep.ID, provider.ExecRequest{Command: []string{"true"}}); err == nil || h.mockProv.execCalls != 0 { t.Fatal("stopped deployment executed") }
}
