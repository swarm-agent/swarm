package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	authruntime "swarm/packages/swarmd/internal/auth"
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
		SyncModules:       []string{workspaceruntime.ReplicationSyncModulePermissions},
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

func TestPushManagedSyncToLocalChildrenPushesAgentsAndCredentials(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	deploymentStore := pebblestore.NewDeployContainerStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "host-swarm", Name: "Host", Role: "master"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if _, _, _, err := agentSvc.Upsert(agentruntime.UpsertInput{Name: "probe", Mode: agentruntime.ModeSubagent, Prompt: "sync me", Enabled: pebblestore.BoolPtr(true)}); err != nil {
		t.Fatalf("upsert agent: %v", err)
	}
	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), events)
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "openrouter", Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-sync", Active: true}); err != nil {
		t.Fatalf("upsert credential: %v", err)
	}
	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), nil, nil)
	deploySvc := NewService(deploymentStore, nil, nil, swarmStore, authSvc, agentSvc, nil, filepath.Join(t.TempDir(), "swarm.conf"), nil, permSvc)

	agentApplyCount := 0
	credentialApplyCount := 0
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/deploy/container/managed/agents/apply":
			agentApplyCount++
			var bundle ContainerSyncAgentBundle
			if err := json.NewDecoder(r.Body).Decode(&bundle); err != nil {
				t.Fatalf("decode agent bundle: %v", err)
			}
			if len(bundle.State.Profiles) != 1 || bundle.State.Profiles[0].Name != "probe" {
				t.Fatalf("agent bundle profiles = %#v", bundle.State.Profiles)
			}
			if bundle.SnapshotHash == "" {
				t.Fatalf("agent snapshot hash is empty")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path_id": PathContainerManagedAgentsApply})
		case "/v1/deploy/container/managed/credentials/apply":
			credentialApplyCount++
			var bundle ContainerSyncCredentialBundle
			if err := json.NewDecoder(r.Body).Decode(&bundle); err != nil {
				t.Fatalf("decode credential bundle: %v", err)
			}
			if bundle.OwnerSwarmID != "host-swarm" || bundle.BundlePassword == "" || len(bundle.Bundle) == 0 || bundle.SnapshotHash == "" {
				t.Fatalf("credential bundle incomplete: %#v", bundle)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path_id": PathContainerManagedCredentialsApply})
		case "/v1/permissions/managed/apply":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected child path %s", r.URL.Path)
		}
	}))
	t.Cleanup(child.Close)

	if _, err := deploymentStore.Put(pebblestore.DeployContainerRecord{
		ID:               "managed-child",
		Kind:             "container",
		Name:             "Managed Child",
		Status:           "running",
		AttachStatus:     "attached",
		SyncEnabled:      true,
		SyncModules:      []string{workspaceruntime.ReplicationSyncModuleCredentials, workspaceruntime.ReplicationSyncModuleAgents},
		SyncOwnerSwarmID: "host-swarm",
		ChildBackendURL:  child.URL,
		ChildSwarmID:     "child-swarm",
	}); err != nil {
		t.Fatalf("put deployment: %v", err)
	}

	if err := deploySvc.PushManagedSyncToLocalChildren(context.Background(), "test"); err != nil {
		t.Fatalf("PushManagedSyncToLocalChildren() error = %v", err)
	}
	if agentApplyCount != 1 {
		t.Fatalf("agent apply count = %d, want 1", agentApplyCount)
	}
	if credentialApplyCount != 1 {
		t.Fatalf("credential apply count = %d, want 1", credentialApplyCount)
	}
	record, ok, err := deploymentStore.Get("managed-child")
	if err != nil || !ok {
		t.Fatalf("get deployment ok=%v err=%v", ok, err)
	}
	if record.SyncCredentialSnapshotHash == "" || record.SyncBundleExportedAt == 0 {
		t.Fatalf("credential sync metadata not recorded: %#v", record)
	}
	if record.SyncLastAppliedAt == 0 || record.SyncLastError != "" {
		t.Fatalf("sync status not acknowledged: applied_at=%d error=%q", record.SyncLastAppliedAt, record.SyncLastError)
	}
}
