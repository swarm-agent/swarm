package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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

// Purpose: finite review cleanup must stop expired managed resources and reject
// reacquisition. CleanupReviewDeployments and direct Exec are tested with
// a real store/fake provider to assert provider effects, not status labels alone.
func TestTaskReviewRetentionAndDirectExec(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := createTestConnection(t, h.connections, "account", "workspace", "connection", "Local")
	dep, err := h.deployments.Save(environments.Deployment{ID: "deployment", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "environment", ConnectionID: conn.ID, Name: "Review", Status: environments.DeploymentStatusReady, Build: &environments.ImageBuildResult{}, CreatedAt: time.Now().Add(-25*time.Hour).UnixMilli()})
	if err != nil { t.Fatal(err) }
	if _, err := h.deployments.AcquireLease(environments.DeploymentLease{AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: dep.ID, EnvironmentID: dep.EnvironmentID, ConsumerType: environments.ConsumerTypeSession, ConsumerID: "owner", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}); err == nil { t.Fatal("expired hold reacquired") }
	stopped, err := h.manager.CleanupReviewDeployments(ctx, "account", "workspace")
	if err != nil || len(stopped) != 1 || h.mockProv.stopCalls != 1 { t.Fatalf("expired review not stopped: %v %v", stopped, err) }
	if _, err := h.manager.Exec(ctx, "account", "workspace", dep.ID, provider.ExecRequest{Command: []string{"true"}}); err == nil || h.mockProv.execCalls != 0 { t.Fatal("stopped deployment executed") }
}

// Purpose: executeAction must revalidate task/source at the final provider boundary,
// even after initial admission. The validator may call manager read APIs without
// deadlock; a source change must produce zero Exec effects and retain the receipt.
func TestTaskLeaseProviderBoundarySourceChange(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := createTestConnection(t, h.connections, "account", "workspace", "connection", "Local")
	dep, err := h.deployments.Save(environments.Deployment{ID: "deployment", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "environment", ConnectionID: conn.ID, Name: "Review", Status: environments.DeploymentStatusReady, Health: environments.HealthStatusHealthy, Runtime: environments.RuntimeMetadata{ContainerID: "original"}})
	if err != nil { t.Fatal(err) }
	lease, err := h.deployments.AcquireLease(environments.DeploymentLease{AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: dep.ID, EnvironmentID: dep.EnvironmentID, ConsumerType: environments.ConsumerTypeSession, ConsumerID: "owner", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	if err != nil { t.Fatal(err) }
	// Inject a durable bound receipt to isolate execution-time authorization.
	lease.TaskBinding = &environments.TaskLeaseBinding{AttachmentID: "attachment"}
	if err := h.store.PutJSON(pebblestore.KeyDeploymentLeaseForAccount("account", "workspace", lease.ID), lease); err != nil { t.Fatal(err) }
	calls := 0
	h.manager.SetTaskLeaseValidator(func(ctx context.Context, l environments.DeploymentLease) error {
		calls++
		if _, found, err := h.manager.GetDeployment(l.AccountScopeID, l.WorkspaceID, l.DeploymentID); err != nil || !found { return errors.New("missing source") }
		if calls == 2 { return errors.New("source generation revoked") }
		return nil
	})
	var mu sync.Mutex
	last := time.Time{}
	_, err = h.manager.executeAction(ctx, &environments.EnvironmentOperation{OperationID: "exec"}, &mu, &last, SubmitOperationRequest{Action: environments.OperationActionExec, AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: dep.ID, LeaseID: lease.ID, Attribution: environments.OperationAttribution{SessionID: "owner"}, Command: []string{"true"}}, nil, &conn, &dep)
	if err == nil || calls != 2 || h.mockProv.execCalls != 0 { t.Fatalf("boundary not enforced: calls=%d err=%v", calls, err) }
	stored, _, err := h.manager.GetLease("account", "workspace", lease.ID)
	if err != nil || !stored.Active { t.Fatal("rejection consumed receipt") }
}
