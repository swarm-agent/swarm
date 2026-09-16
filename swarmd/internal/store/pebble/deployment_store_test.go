package pebblestore

import (
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
)

func TestDeploymentStore_CRUD(t *testing.T) {
	store := openEphemeralStore(t)
	ds := NewDeploymentStore(store)

	dep := environments.Deployment{
		ID:             "dep-test-1",
		AccountScopeID: "acc-test",
		WorkspaceID:    "ws-test",
		EnvironmentID:  "env-test-1",
		ConnectionID:   "conn-local-1",
		Name:           "Test Container Instance 1",
		Status:         environments.DeploymentStatusPending,
		Health:         environments.HealthStatusUnknown,
		Runtime: environments.RuntimeMetadata{
			ContainerID:        "c_123456789abc",
			ProviderResourceID: "res_abc",
			Endpoint:           "http://127.0.0.1:18080",
			AssignedPorts: []environments.AssignedPort{
				{ContainerPort: 8080, HostPort: 18080, Protocol: "tcp", EndpointURL: "http://127.0.0.1:18080"},
			},
			RemoteWorkspacePath: "/workspace",
			RuntimeIP:           "172.17.0.2",
			EngineVersion:       "24.0.7",
		},
	}

	saved, err := ds.Save(dep)
	if err != nil {
		t.Fatalf("save deployment: %v", err)
	}
	if saved.CreatedAt <= 0 || saved.UpdatedAt <= 0 || saved.Lifecycle.CreatedAt <= 0 {
		t.Fatalf("expected positive timestamps: %+v", saved)
	}

	got, found, err := ds.Get("acc-test", "ws-test", "dep-test-1")
	if err != nil || !found {
		t.Fatalf("get deployment: found=%v err=%v", found, err)
	}
	if got.Runtime.ContainerID != "c_123456789abc" || got.Runtime.Endpoint != "http://127.0.0.1:18080" {
		t.Fatalf("runtime metadata mismatch: %+v", got.Runtime)
	}
	if len(got.Runtime.AssignedPorts) != 1 || got.Runtime.AssignedPorts[0].HostPort != 18080 {
		t.Fatalf("assigned ports mismatch: %+v", got.Runtime.AssignedPorts)
	}

	// Update status to Starting then Running
	updated, err := ds.UpdateStatus("acc-test", "ws-test", "dep-test-1", environments.DeploymentStatusStarting, environments.HealthStatusStarting, "")
	if err != nil {
		t.Fatalf("update status starting: %v", err)
	}
	if updated.Lifecycle.StartedAt <= 0 {
		t.Fatal("expected positive started_at timestamp")
	}

	running, err := ds.UpdateStatus("acc-test", "ws-test", "dep-test-1", environments.DeploymentStatusRunning, environments.HealthStatusHealthy, "")
	if err != nil {
		t.Fatalf("update status running: %v", err)
	}
	if running.Lifecycle.ReadyAt <= 0 {
		t.Fatal("expected positive ready_at timestamp")
	}

	// Update runtime
	newRuntime := running.Runtime
	newRuntime.RuntimeIP = "172.17.0.5"
	withNewRuntime, err := ds.UpdateRuntime("acc-test", "ws-test", "dep-test-1", newRuntime)
	if err != nil {
		t.Fatalf("update runtime: %v", err)
	}
	if withNewRuntime.Runtime.RuntimeIP != "172.17.0.5" {
		t.Fatalf("runtime IP mismatch: %s", withNewRuntime.Runtime.RuntimeIP)
	}

	// List
	list, err := ds.List("acc-test", "ws-test", 100)
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	if len(list) != 1 || list[0].ID != "dep-test-1" {
		t.Fatalf("unexpected list: %+v", list)
	}

	// List by environment
	listByEnv, err := ds.ListByEnvironment("acc-test", "ws-test", "env-test-1", 100)
	if err != nil {
		t.Fatalf("list by environment: %v", err)
	}
	if len(listByEnv) != 1 {
		t.Fatalf("expected 1 deployment for env-test-1, got %d", len(listByEnv))
	}
	listByOtherEnv, err := ds.ListByEnvironment("acc-test", "ws-test", "env-other", 100)
	if err != nil {
		t.Fatalf("list by other environment: %v", err)
	}
	if len(listByOtherEnv) != 0 {
		t.Fatalf("expected 0 deployments for env-other, got %d", len(listByOtherEnv))
	}

	// Delete
	deleted, err := ds.Delete("acc-test", "ws-test", "dep-test-1")
	if err != nil || !deleted {
		t.Fatalf("delete deployment: deleted=%v, err=%v", deleted, err)
	}
	_, found, err = ds.Get("acc-test", "ws-test", "dep-test-1")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if found {
		t.Fatal("expected deployment to be deleted")
	}
}

func TestDeploymentStore_Isolation(t *testing.T) {
	store := openEphemeralStore(t)
	ds := NewDeploymentStore(store)

	dep := environments.Deployment{
		ID:             "dep-iso-1",
		AccountScopeID: "acc-1",
		WorkspaceID:    "ws-1",
		EnvironmentID:  "env-1",
		ConnectionID:   "conn-1",
		Name:           "Isolated Dep",
		Status:         environments.DeploymentStatusRunning,
	}

	if _, err := ds.Save(dep); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Cross-account
	_, found, err := ds.Get("acc-2", "ws-1", "dep-iso-1")
	if err != nil {
		t.Fatalf("get cross-account: %v", err)
	}
	if found {
		t.Fatal("cross-account leakage detected")
	}

	// Cross-workspace
	_, found, err = ds.Get("acc-1", "ws-2", "dep-iso-1")
	if err != nil {
		t.Fatalf("get cross-workspace: %v", err)
	}
	if found {
		t.Fatal("cross-workspace leakage detected")
	}

	// Cross-account update status fails
	_, err = ds.UpdateStatus("acc-2", "ws-1", "dep-iso-1", environments.DeploymentStatusStopped, "", "")
	if err == nil {
		t.Fatal("expected error on cross-account update status")
	}
}
