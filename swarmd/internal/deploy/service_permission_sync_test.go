package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func newPermissionSyncTestService(t *testing.T) (*Service, *pebblestore.DeployContainerStore, *permission.Service) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	deploymentStore := pebblestore.NewDeployContainerStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "host-swarm", Name: "Host", Role: "master"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), nil, nil)
	deploySvc := NewService(deploymentStore, nil, nil, swarmStore, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"), permSvc)
	return deploySvc, deploymentStore, permSvc
}

func TestUpdateSettingsPushesHostBypassToManagedChild(t *testing.T) {
	deploySvc, deploymentStore, permSvc := newPermissionSyncTestService(t)
	permSvc.SetBypassPermissions(true)

	managedApplyCount := 0
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/permissions/managed/apply":
			managedApplyCount++
			var state permission.ManagedPolicyState
			if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
				t.Fatalf("decode managed apply: %v", err)
			}
			if !state.BypassPermissions {
				t.Fatalf("managed apply bypass = false, want true")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected child path %s", r.URL.Path)
		}
	}))
	t.Cleanup(child.Close)

	if _, err := deploymentStore.Put(pebblestore.DeployContainerRecord{
		ID:                "managed-child",
		Kind:              "container",
		Name:              "Managed Child",
		Status:            "running",
		AttachStatus:      "attached",
		SyncEnabled:       true,
		SyncModules:       []string{workspaceruntime.ReplicationSyncModuleCredentials},
		SyncOwnerSwarmID:  "host-swarm",
		HostBackendURL:    child.URL,
		ChildBackendURL:   child.URL,
		ChildSwarmID:      "child-swarm",
		BypassPermissions: false,
	}); err != nil {
		t.Fatalf("put deployment: %v", err)
	}

	syncEnabled := true
	if _, err := deploySvc.UpdateSettings(context.Background(), ContainerSettingsUpdateInput{ID: "managed-child", SyncEnabled: &syncEnabled}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if managedApplyCount != 1 {
		t.Fatalf("managed apply count = %d, want 1", managedApplyCount)
	}
}

func TestSyncPermissionBundleMirrorsHostBypassForManagedChild(t *testing.T) {
	deploySvc, deploymentStore, permSvc := newPermissionSyncTestService(t)
	permSvc.SetBypassPermissions(true)

	if _, err := deploymentStore.Put(pebblestore.DeployContainerRecord{
		ID:                "managed-child",
		Kind:              "container",
		Name:              "Managed Child",
		BootstrapSecret:   "secret",
		SyncEnabled:       true,
		BypassPermissions: false,
	}); err != nil {
		t.Fatalf("put deployment: %v", err)
	}

	bundle, err := deploySvc.SyncPermissionBundle(context.Background(), ContainerSyncCredentialRequestInput{DeploymentID: "managed-child", BootstrapSecret: "secret"})
	if err != nil {
		t.Fatalf("SyncPermissionBundle() error = %v", err)
	}
	if !bundle.State.BypassPermissions {
		t.Fatalf("bundle bypass = false, want true")
	}
}
