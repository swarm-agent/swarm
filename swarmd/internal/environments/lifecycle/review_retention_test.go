package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: daemon cleanup must run immediately on every manager start, retry
// failed provider stops, and never renew the durable deadline. Real Pebble plus
// a deterministic provider isolates scheduling and persistence without containers.
func TestReviewCleanupAutomaticRestart(t *testing.T) {
	h := setupSupervisedHarness(t)
	conn := createTestConnection(t, h.connections, "account", "workspace", "connection", "Local")
	dep, err := h.deployments.Save(environments.Deployment{ID: "review", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "environment", ConnectionID: conn.ID, Name: "Review", Status: environments.DeploymentStatusReady, Build: &environments.ImageBuildResult{}, CreatedAt: time.Now().Add(-25 * time.Hour).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	h.mockProv.stopErr = errors.New("injected stop failure")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	reported := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.manager.RunReviewCleanup(ctx, func(err error) {
			select {
			case reported <- err:
			default:
			}
		})
	}()
	select {
	case err := <-reported:
		if err == nil {
			t.Fatal("failure hidden")
		}
	case <-ctx.Done():
		t.Fatal("automatic cleanup did not run")
	}
	cancel()
	<-done
	stored, _, err := h.deployments.Get("account", "workspace", dep.ID)
	if err != nil || stored.Status != environments.DeploymentStatusFailed || stored.ReviewDeadline != dep.ReviewDeadline {
		t.Fatalf("lost retry state: %+v %v", stored, err)
	}
	h.mockProv.stopErr = nil
	// Recreate manager/store wrappers: recovery must not depend on an old timer.
	mgr := NewDeploymentManager(h.connections, h.environments, pebblestore.NewDeploymentStore(h.store), h.workspaces, h.manager.registry)
	defer mgr.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	var once sync.Once
	h.store.SetEnvironmentPublisher(func(pebblestore.V3RealtimeOutboxRecord) { once.Do(cancel2) })
	mgr.RunReviewCleanup(ctx2, func(err error) { t.Errorf("retry failed: %v", err) })
	stored, _, err = h.deployments.Get("account", "workspace", dep.ID)
	if err != nil || stored.Status != environments.DeploymentStatusStopped || stored.ReviewDeadline != dep.ReviewDeadline || h.mockProv.stopCalls != 2 {
		t.Fatalf("restart did not retry exact hold: %+v %v", stored, err)
	}
	if err := mgr.StartDeployment(context.Background(), "account", "workspace", dep.ID); err == nil || h.mockProv.startCalls != 0 {
		t.Fatal("expired hold restarted")
	}
}

// Purpose: guardDeploymentIdle is the last destructive provider boundary. Even
// legacy leases outliving a review hold and unresolved operations must fence
// stop/destroy. Injecting legacy durable receipts proves upgrade safety.
func TestReviewCleanupLiveConsumerAndOperationGuard(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := createTestConnection(t, h.connections, "account", "workspace", "connection", "Local")
	dep, err := h.deployments.Save(environments.Deployment{ID: "review", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "environment", ConnectionID: conn.ID, Name: "Review", Status: environments.DeploymentStatusReady, Build: &environments.ImageBuildResult{}, CreatedAt: time.Now().Add(-25 * time.Hour).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	lease := environments.DeploymentLease{ID: "legacy", AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: dep.ID, EnvironmentID: dep.EnvironmentID, ConsumerType: environments.ConsumerTypeSession, ConsumerID: "owner", Active: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if err := h.store.PutJSON(pebblestore.KeyDeploymentLeaseForAccount("account", "workspace", lease.ID), lease); err != nil {
		t.Fatal(err)
	}
	if err := h.store.PutBytes(pebblestore.KeyDeploymentActiveLeaseForAccount("account", "workspace", dep.ID), []byte(lease.ID)); err != nil {
		t.Fatal(err)
	}
	if stopped, err := h.manager.CleanupReviewDeployments(ctx, "account", "workspace"); err != nil || len(stopped) != 0 {
		t.Fatalf("live lease: %v %v", stopped, err)
	}
	if err := h.manager.DestroyDeployment(ctx, DestroyDeploymentRequest{AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: dep.ID}); !errors.Is(err, ErrDeploymentLeaseHeld) {
		t.Fatalf("destroy bypass: %v", err)
	}
	if _, err := h.deployments.ReleaseLease("account", "workspace", lease.ID, "done"); err != nil {
		t.Fatal(err)
	}
	op, _, err := h.opStore.AdmitOperation(environments.EnvironmentOperation{OperationID: "active", AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: dep.ID, EnvironmentID: dep.EnvironmentID, Action: environments.OperationActionExec, Attribution: environments.OperationAttribution{SessionID: "owner"}, IdempotencyKey: "active", RequestHash: "hash", Deadline: time.Now().Add(time.Minute).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.manager.StopDeployment(ctx, "account", "workspace", dep.ID); !errors.Is(err, environments.ErrDeploymentOperationConflict) {
		t.Fatalf("operation bypass: %v (%s)", err, op.OperationID)
	}
	if h.mockProv.stopCalls != 0 || h.mockProv.destroyCalls != 0 {
		t.Fatal("protected consumer lost provider")
	}
}
