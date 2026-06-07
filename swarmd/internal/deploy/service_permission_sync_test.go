package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	agentruntime "swarm/packages/swarmd/internal/agent"
	authruntime "swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
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
	if _, err := deploySvc.swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{SwarmID: "child-swarm", Name: "Child", Relationship: swarmruntime.RelationshipChild, OutgoingPeerAuthToken: "host-to-child-token"}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}

	managedApplyCount := 0
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(peerAuthSwarmIDHeader); got != "host-swarm" {
			t.Fatalf("peer auth swarm id = %q, want host-swarm", got)
		}
		if got := r.Header.Get(peerAuthTokenHeader); got != "host-to-child-token" {
			t.Fatalf("peer auth token = %q, want host-to-child-token", got)
		}
		switch r.URL.Path {
		case "/v1/deploy/container/pairing/account-bind":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path_id": PathContainerPairingAccountBind})
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
		UserID:            testPrincipal().UserID,
		AccountScopeID:    testPrincipal().AccountScopeID,
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

func TestPushManagedSyncToLocalChildrenFailsBeforeHTTPWhenPeerAuthMissing(t *testing.T) {
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
	deploySvc := NewService(deploymentStore, nil, nil, swarmStore, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"), permission.NewService(pebblestore.NewPermissionStore(store), nil, nil))

	childHit := false
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		childHit = true
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	t.Cleanup(child.Close)

	if _, err := deploymentStore.Put(pebblestore.DeployContainerRecord{
		ID:               "managed-child",
		Kind:             "container",
		Name:             "Managed Child",
		Status:           "running",
		AttachStatus:     "attached",
		SyncEnabled:      true,
		SyncModules:      []string{workspaceruntime.ReplicationSyncModulePermissions},
		SyncOwnerSwarmID: "host-swarm",
		ChildBackendURL:  child.URL,
		ChildSwarmID:     "child-swarm",
		UserID:           testPrincipal().UserID,
		AccountScopeID:   testPrincipal().AccountScopeID,
	}); err != nil {
		t.Fatalf("put deployment: %v", err)
	}

	err = deploySvc.PushManagedSyncToLocalChildren(testPrincipalContext(), "test")
	if err == nil {
		t.Fatalf("PushManagedSyncToLocalChildren() succeeded without peer auth")
	}
	if !strings.Contains(err.Error(), "peer auth trusted peer") {
		t.Fatalf("PushManagedSyncToLocalChildren() error = %v", err)
	}
	if childHit {
		t.Fatalf("child server was hit despite missing peer auth")
	}
}

func TestSyncPermissionBundleMirrorsHostBypassForManagedChild(t *testing.T) {
	deploySvc, deploymentStore, permSvc := newPermissionSyncTestService(t)
	permSvc.SetBypassPermissions(true)

	childHit := false
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		childHit = true
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	t.Cleanup(child.Close)

	if _, err := deploymentStore.Put(pebblestore.DeployContainerRecord{
		ID:                "managed-child",
		Kind:              "container",
		Name:              "Managed Child",
		BootstrapSecret:   "secret",
		SyncEnabled:       true,
		SyncModules:       []string{workspaceruntime.ReplicationSyncModulePermissions},
		SyncOwnerSwarmID:  "host-swarm",
		ChildBackendURL:   child.URL,
		ChildSwarmID:      "child-swarm",
		UserID:            testPrincipal().UserID,
		AccountScopeID:    testPrincipal().AccountScopeID,
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
	if childHit {
		t.Fatalf("SyncPermissionBundle should export locally and must not push to child")
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
	if _, _, _, err := agentSvc.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "probe", Mode: agentruntime.ModeSubagent, Prompt: "sync me", ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}, Enabled: pebblestore.BoolPtr(true)}); err != nil {
		t.Fatalf("upsert agent: %v", err)
	}
	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), events)
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "openrouter", AccountScopeID: testPrincipal().AccountScopeID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-sync", Active: true}); err != nil {
		t.Fatalf("upsert credential: %v", err)
	}
	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), nil, nil)
	if _, err := swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{SwarmID: "child-swarm", Name: "Child", Relationship: swarmruntime.RelationshipChild, OutgoingPeerAuthToken: "host-to-child-token"}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}
	deploySvc := NewService(deploymentStore, nil, nil, swarmStore, authSvc, agentSvc, nil, filepath.Join(t.TempDir(), "swarm.conf"), nil, permSvc)

	agentApplyCount := 0
	credentialApplyCount := 0
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(peerAuthSwarmIDHeader); got != "host-swarm" {
			t.Fatalf("peer auth swarm id = %q, want host-swarm", got)
		}
		if got := r.Header.Get(peerAuthTokenHeader); got != "host-to-child-token" {
			t.Fatalf("peer auth token = %q, want host-to-child-token", got)
		}
		switch r.URL.Path {
		case "/v1/deploy/container/pairing/account-bind":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path_id": PathContainerPairingAccountBind})
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
			if bundle.UserID != testPrincipal().UserID || bundle.AccountScopeID != testPrincipal().AccountScopeID {
				t.Fatalf("credential bundle identity = %q/%q", bundle.UserID, bundle.AccountScopeID)
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
		UserID:           testPrincipal().UserID,
		AccountScopeID:   testPrincipal().AccountScopeID,
	}); err != nil {
		t.Fatalf("put deployment: %v", err)
	}

	if err := deploySvc.PushManagedSyncToLocalChildren(testPrincipalContext(), "test"); err != nil {
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

func TestSyncManagedCredentialsOnceIgnoresStaleManagedConfigWhenDBUnpaired(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	swarmStore := pebblestore.NewSwarmStore(store)
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.Child = true
	cfg.SwarmRole = startupconfig.SwarmRoleManaged
	cfg.ParentSwarmID = "stale-manager-swarm"
	cfg.PairingState = startupconfig.PairingStatePaired
	cfg.ManagedHostSync = startupconfig.ManagedHostSyncConfig{
		Mode:              "managed",
		Modules:           []string{workspaceruntime.ReplicationSyncModuleCredentials},
		OwnerSwarmID:      "stale-manager-swarm",
		HostAPIBaseURL:    "http://127.0.0.1:1",
		SyncCredentialURL: "http://127.0.0.1:1/v1/deploy/container/sync/credentials",
	}
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, authruntime.NewService(pebblestore.NewAuthStore(store), nil), nil, nil, startupPath)

	if err := deploySvc.SyncManagedCredentialsOnce(context.Background()); err != nil {
		t.Fatalf("SyncManagedCredentialsOnce() error = %v", err)
	}
	if _, ok, err := swarmStore.GetLocalPairing(); err != nil || ok {
		t.Fatalf("stale config created or mutated pairing ok=%v err=%v", ok, err)
	}
	status, err := deploySvc.ManagedHostSyncStatus()
	if err != nil {
		t.Fatalf("ManagedHostSyncStatus() error = %v", err)
	}
	if status.OwnerSwarmID != "" || status.Current || status.SnapshotHash != "" {
		t.Fatalf("status used stale config as managed authority: %+v", status)
	}
}

func TestApplyManagedCredentialBundleUsesPersistedPairingAccountWhenRequestHasNoPrincipal(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), events)
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "fireworks", AccountScopeID: testPrincipal().AccountScopeID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-managed-sync", Active: true}); err != nil {
		t.Fatalf("upsert host credential: %v", err)
	}
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, authSvc, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"))
	if _, err := swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{PairingState: "paired", ParentSwarmID: "host-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID}); err != nil {
		t.Fatalf("put pairing: %v", err)
	}

	bundle, _, err := authSvc.ExportCredentialsForAccount(testPrincipal().AccountScopeID, "bundle-password", "")
	if err != nil {
		t.Fatalf("export credentials: %v", err)
	}
	if err := deploySvc.ApplyManagedCredentialBundle(context.Background(), ContainerSyncCredentialBundle{OwnerSwarmID: "host-swarm", BundlePassword: "bundle-password", Bundle: bundle}); err != nil {
		t.Fatalf("ApplyManagedCredentialBundle() error = %v", err)
	}
	list, err := authSvc.ListCredentialsForAccount(testPrincipal().AccountScopeID, "fireworks", "", 10)
	if err != nil {
		t.Fatalf("list credentials: %v", err)
	}
	if list.Total == 0 || len(list.Records) == 0 || !list.Records[0].Active {
		t.Fatalf("imported credentials = %#v, want active fireworks credential", list)
	}
}

func TestApplyManagedCredentialBundleUsesPersistedPairingAccountWhenContextHasBootstrapPrincipal(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), events)
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "fireworks", AccountScopeID: testPrincipal().AccountScopeID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-managed-sync", Active: true}); err != nil {
		t.Fatalf("upsert host credential: %v", err)
	}
	bundle, _, err := authSvc.ExportCredentialsForAccount(testPrincipal().AccountScopeID, "bundle-password", "")
	if err != nil {
		t.Fatalf("export credentials: %v", err)
	}
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, authSvc, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"))
	if _, err := swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{PairingState: "paired", ParentSwarmID: "host-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID}); err != nil {
		t.Fatalf("put pairing: %v", err)
	}
	bootstrapCtx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "bootstrap-user", AccountScopeID: "bootstrap-account", AccountScopeSource: identity.AccountScopeSourceServerState})

	err = deploySvc.ApplyManagedCredentialBundle(bootstrapCtx, ContainerSyncCredentialBundle{OwnerSwarmID: "host-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, BundlePassword: "bundle-password", Bundle: bundle})
	if err != nil {
		t.Fatalf("ApplyManagedCredentialBundle() error = %v", err)
	}
	pairing, ok, err := swarmStore.GetLocalPairing()
	if err != nil || !ok {
		t.Fatalf("get pairing ok=%v err=%v", ok, err)
	}
	if pairing.AccountScopeID != testPrincipal().AccountScopeID || pairing.UserID != testPrincipal().UserID {
		t.Fatalf("pairing account changed: %#v", pairing)
	}
}

func TestPostLocalAttachFinalizeAddsContextPrincipalToPayload(t *testing.T) {
	var got ContainerAttachFinalizeInput
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode finalize payload: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer child.Close()

	deploySvc := &Service{client: child.Client()}
	if err := deploySvc.postLocalAttachFinalize(testPrincipalContext(), child.URL, "", ContainerAttachFinalizeInput{DeploymentID: "deployment-1"}); err != nil {
		t.Fatalf("postLocalAttachFinalize() error = %v", err)
	}
	if got.UserID != testPrincipal().UserID || got.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("finalize principal = %q/%q, want %q/%q", got.UserID, got.AccountScopeID, testPrincipal().UserID, testPrincipal().AccountScopeID)
	}
}

func TestFinalizeAttachFromHostUsesInputPrincipalForHostDrivenSync(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), events)
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "fireworks", AccountScopeID: testPrincipal().AccountScopeID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-managed-sync", Active: true}); err != nil {
		t.Fatalf("upsert host credential: %v", err)
	}
	bundle, _, err := authSvc.ExportCredentialsForAccount(testPrincipal().AccountScopeID, "bundle-password", "")
	if err != nil {
		t.Fatalf("export credentials: %v", err)
	}

	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.Child = true
	cfg.SwarmName = "child"
	cfg.BypassPermissions = true
	cfg.DeployContainer.Enabled = true
	cfg.DeployContainer.HostDriven = true
	cfg.DeployContainer.SyncEnabled = true
	cfg.DeployContainer.SyncModules = []string{workspaceruntime.ReplicationSyncModuleCredentials}
	cfg.DeployContainer.DeploymentID = "deployment-1"
	cfg.DeployContainer.BootstrapSecret = "bootstrap-secret"
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}

	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "child-swarm", Name: "Child", Role: "child"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	swarmSvc := swarmruntime.NewService(swarmStore, events, nil)
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, swarmSvc, swarmStore, authSvc, nil, nil, startupPath)
	err = deploySvc.FinalizeAttachFromHost(context.Background(), ContainerAttachFinalizeInput{
		DeploymentID:             "deployment-1",
		BootstrapSecret:          "bootstrap-secret",
		UserID:                   testPrincipal().UserID,
		AccountScopeID:           testPrincipal().AccountScopeID,
		HostSwarmID:              "host-swarm",
		HostToChildPeerAuthToken: "host-to-child-token",
		ChildToHostPeerAuthToken: "child-to-host-token",
		SyncOwnerSwarmID:         "host-swarm",
		SyncBundlePassword:       "bundle-password",
		SyncBundle:               bundle,
	})
	if err != nil {
		t.Fatalf("FinalizeAttachFromHost() error = %v", err)
	}
	pairing, ok, err := swarmStore.GetLocalPairing()
	if err != nil || !ok {
		t.Fatalf("get pairing ok=%v err=%v", ok, err)
	}
	if pairing.UserID != testPrincipal().UserID || pairing.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("pairing principal = %q/%q, want %q/%q", pairing.UserID, pairing.AccountScopeID, testPrincipal().UserID, testPrincipal().AccountScopeID)
	}
	list, err := authSvc.ListCredentialsForAccount(testPrincipal().AccountScopeID, "fireworks", "", 10)
	if err != nil {
		t.Fatalf("list credentials: %v", err)
	}
	if list.Total == 0 || len(list.Records) == 0 || !list.Records[0].Active {
		t.Fatalf("imported credentials = %#v, want active fireworks credential", list)
	}
}

func TestApplyManagedCredentialBundleRejectsBundleAccountMismatch(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), events)
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "fireworks", AccountScopeID: testPrincipal().AccountScopeID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-managed-sync", Active: true}); err != nil {
		t.Fatalf("upsert host credential: %v", err)
	}
	bundle, _, err := authSvc.ExportCredentialsForAccount(testPrincipal().AccountScopeID, "bundle-password", "")
	if err != nil {
		t.Fatalf("export credentials: %v", err)
	}
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, authSvc, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"))
	if _, err := swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{PairingState: "paired", ParentSwarmID: "host-swarm", UserID: "local-user", AccountScopeID: "local-account"}); err != nil {
		t.Fatalf("put pairing: %v", err)
	}

	err = deploySvc.ApplyManagedCredentialBundle(context.Background(), ContainerSyncCredentialBundle{OwnerSwarmID: "host-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, BundlePassword: "bundle-password", Bundle: bundle})
	if err == nil {
		t.Fatalf("ApplyManagedCredentialBundle() succeeded with mismatched pairing account")
	}
}

func TestPushManagedSyncToManagedHostsPushesAgentsAndCredentials(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	swarmNodeStore := pebblestore.NewSwarmNodeStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "manager-swarm", Name: "Manager", Role: "master"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{SwarmID: "managed-swarm", Name: "Managed", Relationship: swarmruntime.RelationshipManaged, OutgoingPeerAuthToken: "manager-to-managed-token"}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if _, _, _, err := agentSvc.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "managed-probe", Mode: agentruntime.ModeSubagent, Prompt: "sync me", ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}, Enabled: pebblestore.BoolPtr(true)}); err != nil {
		t.Fatalf("upsert agent: %v", err)
	}
	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), events)
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "openrouter", AccountScopeID: testPrincipal().AccountScopeID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-managed-sync", Active: true}); err != nil {
		t.Fatalf("upsert credential: %v", err)
	}
	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), nil, nil)
	permSvc.SetBypassPermissions(true)
	if _, err := permSvc.UpsertRuleForAccount(testPrincipal().AccountScopeID, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "read"}); err != nil {
		t.Fatalf("upsert permission rule: %v", err)
	}
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, authSvc, agentSvc, nil, filepath.Join(t.TempDir(), "swarm.conf"), permSvc, swarmNodeStore)

	agentApplyCount := 0
	credentialApplyCount := 0
	permissionApplyCount := 0
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(peerAuthSwarmIDHeader); got != "manager-swarm" {
			t.Fatalf("peer auth swarm id = %q, want manager-swarm", got)
		}
		if got := r.Header.Get(peerAuthTokenHeader); got != "manager-to-managed-token" {
			t.Fatalf("peer auth token = %q, want manager-to-managed-token", got)
		}
		switch r.URL.Path {
		case "/v1/deploy/container/managed/credentials/apply":
			credentialApplyCount++
			var bundle ContainerSyncCredentialBundle
			if err := json.NewDecoder(r.Body).Decode(&bundle); err != nil {
				t.Fatalf("decode credential bundle: %v", err)
			}
			if bundle.OwnerSwarmID != "managed-swarm" || bundle.BundlePassword == "" || len(bundle.Bundle) == 0 || bundle.SnapshotHash == "" {
				t.Fatalf("credential bundle incomplete: %#v", bundle)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path_id": PathContainerManagedCredentialsApply})
		case "/v1/deploy/container/managed/agents/apply":
			agentApplyCount++
			var bundle ContainerSyncAgentBundle
			if err := json.NewDecoder(r.Body).Decode(&bundle); err != nil {
				t.Fatalf("decode agent bundle: %v", err)
			}
			if len(bundle.State.Profiles) != 1 || bundle.State.Profiles[0].Name != "managed-probe" || bundle.SnapshotHash == "" {
				t.Fatalf("agent bundle incomplete: %#v", bundle)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path_id": PathContainerManagedAgentsApply})
		case "/v1/permissions/managed/apply":
			permissionApplyCount++
			var state permission.ManagedPolicyState
			if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
				t.Fatalf("decode permission state: %v", err)
			}
			if !state.BypassPermissions {
				t.Fatalf("permission bypass = false, want true")
			}
			if len(state.Policy.Rules) == 0 {
				t.Fatalf("permission policy rules empty")
			}
			foundReadAllow := false
			for _, rule := range state.Policy.Rules {
				if rule.Kind == permission.PolicyRuleKindTool && rule.Decision == permission.PolicyDecisionAllow && rule.Tool == "read" {
					foundReadAllow = true
				}
			}
			if !foundReadAllow {
				t.Fatalf("permission policy did not include read allow rule: %#v", state.Policy.Rules)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path_id": PathContainerManagedPermissionsApply})
		default:
			t.Fatalf("unexpected managed path %s", r.URL.Path)
		}
	}))
	t.Cleanup(managed.Close)
	if _, err := swarmNodeStore.Put(pebblestore.SwarmNodeRecord{SwarmID: "managed-swarm", Name: "Managed", Role: swarmruntime.RelationshipManaged, BackendURL: managed.URL, Status: "online"}); err != nil {
		t.Fatalf("put swarm node: %v", err)
	}

	if err := deploySvc.PushManagedSyncToManagedHosts(testPrincipalContext(), "test"); err != nil {
		t.Fatalf("PushManagedSyncToManagedHosts() error = %v", err)
	}
	if credentialApplyCount != 1 {
		t.Fatalf("credential apply count = %d, want 1", credentialApplyCount)
	}
	if agentApplyCount != 1 {
		t.Fatalf("agent apply count = %d, want 1", agentApplyCount)
	}
	if permissionApplyCount != 1 {
		t.Fatalf("permission apply count = %d, want 1", permissionApplyCount)
	}
}

func TestReconcilePermissionSyncPushesManagedHosts(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	swarmNodeStore := pebblestore.NewSwarmNodeStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "manager-swarm", Name: "Manager", Role: "master"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{SwarmID: "managed-swarm", Name: "Managed", Relationship: swarmruntime.RelationshipManaged, OutgoingPeerAuthToken: "manager-to-managed-token"}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}
	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), events)
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "openrouter", AccountScopeID: testPrincipal().AccountScopeID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-managed-sync", Active: true}); err != nil {
		t.Fatalf("upsert credential: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), nil, nil)
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, authSvc, agentSvc, nil, filepath.Join(t.TempDir(), "swarm.conf"), permSvc, swarmNodeStore)

	permissionApplyCount := 0
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/deploy/container/managed/credentials/apply", "/v1/deploy/container/managed/agents/apply":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/v1/permissions/managed/apply":
			permissionApplyCount++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected managed path %s", r.URL.Path)
		}
	}))
	t.Cleanup(managed.Close)
	if _, err := swarmNodeStore.Put(pebblestore.SwarmNodeRecord{SwarmID: "managed-swarm", Name: "Managed", Role: swarmruntime.RelationshipManaged, BackendURL: managed.URL, Status: "online"}); err != nil {
		t.Fatalf("put swarm node: %v", err)
	}

	if err := deploySvc.ReconcilePermissionSync(testPrincipalContext()); err != nil {
		t.Fatalf("ReconcilePermissionSync() error = %v", err)
	}
	if permissionApplyCount != 1 {
		t.Fatalf("permission apply count = %d, want 1", permissionApplyCount)
	}
}

func TestPushManagedSyncToManagedHostsRequiresAck(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	swarmNodeStore := pebblestore.NewSwarmNodeStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "manager-swarm", Name: "Manager", Role: "master"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{SwarmID: "managed-swarm", Name: "Managed", Relationship: swarmruntime.RelationshipManaged, OutgoingPeerAuthToken: "manager-to-managed-token"}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), events)
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "openrouter", AccountScopeID: testPrincipal().AccountScopeID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-managed-sync", Active: true}); err != nil {
		t.Fatalf("upsert credential: %v", err)
	}
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, authSvc, agentSvc, nil, filepath.Join(t.TempDir(), "swarm.conf"), swarmNodeStore)

	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not applied"})
	}))
	t.Cleanup(managed.Close)
	if _, err := swarmNodeStore.Put(pebblestore.SwarmNodeRecord{SwarmID: "managed-swarm", Name: "Managed", Role: swarmruntime.RelationshipManaged, BackendURL: managed.URL, Status: "online"}); err != nil {
		t.Fatalf("put swarm node: %v", err)
	}

	if err := deploySvc.PushManagedSyncToManagedHosts(testPrincipalContext(), "test"); err == nil {
		t.Fatalf("PushManagedSyncToManagedHosts() error = nil, want acknowledgement failure")
	}
}

func TestManagedHostAgentPermissionAndModelSyncAreAccountScoped(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID}); err != nil {
		t.Fatalf("put local pairing: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	enabled := true
	if _, _, _, err := agentSvc.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "account-a-agent", Mode: agentruntime.ModeSubagent, Prompt: "sync A", ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}, Enabled: &enabled}); err != nil {
		t.Fatalf("upsert account agent: %v", err)
	}
	if _, _, _, err := agentSvc.UpsertForAccount("account-b", agentruntime.UpsertInput{Name: "account-b-agent", Mode: agentruntime.ModeSubagent, Prompt: "do not sync", ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}, Enabled: &enabled}); err != nil {
		t.Fatalf("upsert other account agent: %v", err)
	}
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetPreferenceForAccount(testPrincipal().AccountScopeID, testPrincipal().UserID, "provider-a", "model-a", "medium"); err != nil {
		t.Fatalf("set account model preference: %v", err)
	}
	if _, _, err := modelSvc.SetPreferenceForAccount("account-b", "user-b", "provider-b", "model-b", "low"); err != nil {
		t.Fatalf("set other account model preference: %v", err)
	}
	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), nil, nil)
	permSvc.SetBypassPermissions(true)
	if _, err := permSvc.UpsertRuleForAccount(testPrincipal().AccountScopeID, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "read"}); err != nil {
		t.Fatalf("upsert account permission: %v", err)
	}
	if _, err := permSvc.UpsertRuleForAccount("account-b", permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionDeny, Tool: "bash"}); err != nil {
		t.Fatalf("upsert other account permission: %v", err)
	}
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, nil, agentSvc, nil, filepath.Join(t.TempDir(), "swarm.conf"), modelSvc, permSvc)

	agentBundle, err := deploySvc.SyncManagedHostAgentBundle(testPrincipalContext(), ContainerSyncCredentialRequestInput{})
	if err != nil {
		t.Fatalf("sync managed host agent bundle: %v", err)
	}
	if len(agentBundle.State.Profiles) != 1 || agentBundle.State.Profiles[0].Name != "account-a-agent" {
		t.Fatalf("agent bundle profiles = %+v", agentBundle.State.Profiles)
	}
	modelBundle, err := deploySvc.SyncManagedHostModelDefaultsBundle(testPrincipalContext(), ContainerSyncCredentialRequestInput{})
	if err != nil {
		t.Fatalf("sync managed host model bundle: %v", err)
	}
	if modelBundle.Preference.Provider != "provider-a" || modelBundle.Preference.Model != "model-a" || modelBundle.Preference.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("model bundle preference = %+v", modelBundle.Preference)
	}
	permissionBundle, err := deploySvc.SyncManagedHostPermissionBundle(testPrincipalContext(), ContainerSyncCredentialRequestInput{})
	if err != nil {
		t.Fatalf("sync managed host permission bundle: %v", err)
	}
	foundRead := false
	for _, rule := range permissionBundle.State.Policy.Rules {
		if rule.Kind == permission.PolicyRuleKindTool && rule.Tool == "read" {
			foundRead = true
		}
		if rule.Kind == permission.PolicyRuleKindTool && rule.Tool == "bash" {
			t.Fatalf("permission bundle included other account rule: %+v", permissionBundle.State)
		}
	}
	if !foundRead {
		t.Fatalf("permission bundle state = %+v", permissionBundle.State)
	}
}

func TestDeployContainerRuntimeSyncAppliesAgentsAndModelDefaultsToPairedAccount(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "child-swarm", Name: "Child", Role: "child"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{SwarmID: "host-swarm", Name: "Host", Relationship: swarmruntime.RelationshipParent, OutgoingPeerAuthToken: "child-to-host-token"}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}
	pairing := pebblestore.SwarmLocalPairingRecord{
		PairingState:   startupconfig.PairingStatePaired,
		ParentSwarmID:  "host-swarm",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
	}
	if _, err := swarmStore.PutLocalPairing(pairing); err != nil {
		t.Fatalf("put pairing: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), events, nil)
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, nil, agentSvc, nil, filepath.Join(t.TempDir(), "swarm.conf"), modelSvc)

	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/deploy/container/sync/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "bundle": ContainerSyncAgentBundle{State: agentruntime.State{Profiles: []pebblestore.AgentProfile{{Name: "synced-agent", Mode: agentruntime.ModeSubagent, Prompt: "account scoped", Enabled: true}}}, Modules: []string{workspaceruntime.ReplicationSyncModuleAgents}}})
		case "/v1/deploy/container/sync/model-defaults":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "bundle": ContainerSyncModelDefaultsBundle{Preference: pebblestore.ModelPreference{Provider: "provider-linked", Model: "model-linked", Thinking: "medium", UserID: testPrincipal().UserID}, Modules: []string{workspaceruntime.ReplicationSyncModuleModelDefaults}}})
		default:
			t.Fatalf("unexpected sync path %s", r.URL.Path)
		}
	}))
	t.Cleanup(child.Close)

	cfg := startupconfig.Default(filepath.Join(t.TempDir(), "swarm.conf"))
	cfg.DeployContainer.SyncModules = []string{workspaceruntime.ReplicationSyncModuleAgents, workspaceruntime.ReplicationSyncModuleModelDefaults}
	cfg.DeployContainer.HostAPIBaseURL = child.URL
	if err := deploySvc.syncDeployContainerFromHost(context.Background(), cfg, pairing); err != nil {
		t.Fatalf("syncDeployContainerFromHost() error = %v", err)
	}

	linkedState, err := agentSvc.ListStateForAccount(testPrincipal().AccountScopeID, 10)
	if err != nil {
		t.Fatalf("list linked agent state: %v", err)
	}
	foundSyncedAgent := false
	for _, profile := range linkedState.Profiles {
		if profile.Name == "synced-agent" {
			foundSyncedAgent = true
		}
	}
	if !foundSyncedAgent {
		t.Fatalf("linked account agent state = %+v", linkedState.Profiles)
	}
	globalState, err := agentSvc.ListState(10)
	if err != nil {
		t.Fatalf("list global agent state: %v", err)
	}
	for _, profile := range globalState.Profiles {
		if profile.Name == "synced-agent" {
			t.Fatalf("synced agent was written globally: %+v", globalState.Profiles)
		}
	}
	pref, err := modelSvc.GetPreferenceForAccount(testPrincipal().AccountScopeID)
	if err != nil {
		t.Fatalf("get linked model preference: %v", err)
	}
	if pref.Provider != "provider-linked" || pref.Model != "model-linked" || pref.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("linked model preference = %+v", pref)
	}
	globalPref, err := modelSvc.GetGlobalPreference()
	if err != nil {
		t.Fatalf("get global model preference: %v", err)
	}
	if globalPref.Provider == "provider-linked" {
		t.Fatalf("model preference was written globally: %+v", globalPref)
	}
}

func TestApplyManagedHostInitialSyncBundleAppliesSyncedStateToLinkedAccountOnly(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), events, nil)
	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), nil, nil)
	identitySvc := identity.NewService(pebblestore.NewIdentityStore(store))
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, nil, agentSvc, nil, filepath.Join(t.TempDir(), "swarm.conf"), modelSvc, permSvc, identitySvc)

	bundle := ManagedHostInitialSyncBundle{
		UserID:              testPrincipal().UserID,
		AccountScopeID:      testPrincipal().AccountScopeID,
		SyncModules:         []string{workspaceruntime.ReplicationSyncModuleAgents, workspaceruntime.ReplicationSyncModuleModelDefaults, workspaceruntime.ReplicationSyncModulePermissions},
		AgentBundle:         ContainerSyncAgentBundle{State: agentruntime.State{Profiles: []pebblestore.AgentProfile{{Name: "synced-agent", Mode: agentruntime.ModeSubagent, Prompt: "linked only", Enabled: true}}}, Modules: []string{workspaceruntime.ReplicationSyncModuleAgents}},
		ModelDefaultsBundle: ContainerSyncModelDefaultsBundle{Preference: pebblestore.ModelPreference{Provider: "provider-linked", Model: "model-linked", Thinking: "medium", UserID: testPrincipal().UserID}, Modules: []string{workspaceruntime.ReplicationSyncModuleModelDefaults}},
		PermissionBundle:    ContainerSyncPermissionBundle{State: permission.ManagedPolicyState{Policy: permission.Policy{Rules: []permission.PolicyRule{{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "read"}}}}},
	}
	if _, err := deploySvc.ApplyManagedHostInitialSyncBundle(context.Background(), "manager-swarm", bundle); err != nil {
		t.Fatalf("ApplyManagedHostInitialSyncBundle() error = %v", err)
	}
	linkedState, err := agentSvc.ListStateForAccount(testPrincipal().AccountScopeID, 10)
	if err != nil {
		t.Fatalf("list linked agent state: %v", err)
	}
	foundSyncedAgent := false
	for _, profile := range linkedState.Profiles {
		if profile.Name == "synced-agent" {
			foundSyncedAgent = true
		}
	}
	if !foundSyncedAgent {
		t.Fatalf("linked agent state = %+v", linkedState.Profiles)
	}
	otherState, err := agentSvc.ListStateForAccount("other-account", 10)
	if err != nil {
		t.Fatalf("list other agent state: %v", err)
	}
	if len(otherState.Profiles) != 0 {
		t.Fatalf("other account agent state = %+v", otherState.Profiles)
	}
	pref, err := modelSvc.GetPreferenceForAccount(testPrincipal().AccountScopeID)
	if err != nil {
		t.Fatalf("get linked model preference: %v", err)
	}
	if pref.Provider != "provider-linked" || pref.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("linked model preference = %+v", pref)
	}
	otherPref, err := modelSvc.GetPreferenceForAccount("other-account")
	if err != nil {
		t.Fatalf("get other model preference: %v", err)
	}
	if otherPref.Provider == "provider-linked" {
		t.Fatalf("other model preference was overwritten: %+v", otherPref)
	}
	policy, err := permSvc.CurrentPolicyForAccount(testPrincipal().AccountScopeID)
	if err != nil {
		t.Fatalf("get linked policy: %v", err)
	}
	if len(policy.Rules) != 1 || policy.Rules[0].Tool != "read" {
		t.Fatalf("linked policy = %+v", policy)
	}
	otherPolicy, err := permSvc.CurrentPolicyForAccount("other-account")
	if err != nil {
		t.Fatalf("get other policy: %v", err)
	}
	for _, rule := range otherPolicy.Rules {
		if rule.Kind == permission.PolicyRuleKindTool && rule.Tool == "read" {
			t.Fatalf("other policy included linked account rule: %+v", otherPolicy)
		}
	}
}

func TestSyncManagedHostCredentialBundleIncludesUserAndAccount(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "manager-swarm", Name: "Manager", Role: "master"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), events)
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "fireworks", AccountScopeID: testPrincipal().AccountScopeID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-managed-sync", Active: true}); err != nil {
		t.Fatalf("upsert host credential: %v", err)
	}
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), events, nil)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, authSvc, agentSvc, nil, filepath.Join(t.TempDir(), "swarm.conf"), modelSvc)

	bundle, err := deploySvc.ManagedHostInitialSyncBundle(testPrincipalContext(), "https://manager.example", "managed-swarm")
	if err != nil {
		t.Fatalf("ManagedHostInitialSyncBundle() error = %v", err)
	}
	if bundle.UserID != testPrincipal().UserID || bundle.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("initial sync identity = %q/%q", bundle.UserID, bundle.AccountScopeID)
	}
	if bundle.CredentialBundle.UserID != testPrincipal().UserID || bundle.CredentialBundle.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("credential bundle identity = %q/%q", bundle.CredentialBundle.UserID, bundle.CredentialBundle.AccountScopeID)
	}
}

func TestApplyManagedHostInitialSyncBundleMaterializesLinkedIdentityIdempotently(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	identityStore := pebblestore.NewIdentityStore(store)
	identitySvc := identity.NewService(identityStore)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), events, nil)
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, nil, agentSvc, nil, filepath.Join(t.TempDir(), "swarm.conf"), modelSvc, identitySvc)

	bundle := ManagedHostInitialSyncBundle{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SyncModules: []string{workspaceruntime.ReplicationSyncModuleAgents}}
	if _, err := deploySvc.ApplyManagedHostInitialSyncBundle(context.Background(), "manager-swarm", bundle); err != nil {
		t.Fatalf("ApplyManagedHostInitialSyncBundle() error = %v", err)
	}
	if _, err := deploySvc.ApplyManagedHostInitialSyncBundle(context.Background(), "manager-swarm", bundle); err != nil {
		t.Fatalf("ApplyManagedHostInitialSyncBundle() refinalize error = %v", err)
	}
	user, ok, err := identityStore.GetUser(testPrincipal().UserID)
	if err != nil || !ok {
		t.Fatalf("get linked user ok=%v err=%v", ok, err)
	}
	if user.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("linked user account scope = %q", user.AccountScopeID)
	}
	account, ok, err := identityStore.GetAccountScope(testPrincipal().AccountScopeID)
	if err != nil || !ok {
		t.Fatalf("get linked account ok=%v err=%v", ok, err)
	}
	if account.CreatedByUserID != testPrincipal().UserID || account.UserID != testPrincipal().UserID {
		t.Fatalf("linked account owner = %q/%q", account.CreatedByUserID, account.UserID)
	}
	if _, ok, err := identityStore.GetAccountUser(testPrincipal().AccountScopeID, testPrincipal().UserID); err != nil || !ok {
		t.Fatalf("get linked account user ok=%v err=%v", ok, err)
	}
	selection, ok, err := identityStore.GetCurrentSelection()
	if err != nil || !ok {
		t.Fatalf("get current selection ok=%v err=%v", ok, err)
	}
	if selection.UserID != testPrincipal().UserID {
		t.Fatalf("current selection user = %q", selection.UserID)
	}
	agentState, err := agentSvc.ListStateForAccount(testPrincipal().AccountScopeID, 0)
	if err != nil {
		t.Fatalf("list linked account agents: %v", err)
	}
	if len(agentState.Profiles) == 0 {
		t.Fatalf("linked account agent defaults were not materialized")
	}
	pairing, ok, err := swarmStore.GetLocalPairing()
	if err != nil || !ok {
		t.Fatalf("get pairing ok=%v err=%v", ok, err)
	}
	if pairing.UserID != testPrincipal().UserID || pairing.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("pairing identity = %q/%q", pairing.UserID, pairing.AccountScopeID)
	}
}

func TestApplyManagedHostInitialSyncBundleRequiresIdentityEnvelope(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	swarmStore := pebblestore.NewSwarmStore(store)
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"))

	_, err = deploySvc.ApplyManagedHostInitialSyncBundle(context.Background(), "manager-swarm", ManagedHostInitialSyncBundle{AccountScopeID: testPrincipal().AccountScopeID})
	if err == nil {
		t.Fatalf("ApplyManagedHostInitialSyncBundle() succeeded without user id")
	}
	if _, ok, getErr := swarmStore.GetLocalPairing(); getErr != nil || ok {
		t.Fatalf("pairing changed after rejected bundle ok=%v err=%v", ok, getErr)
	}
}

func TestApplyManagedHostInitialSyncBundleRejectsIdentityMismatchAndPreservesPairing(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	swarmStore := pebblestore.NewSwarmStore(store)
	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), events)
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "fireworks", AccountScopeID: testPrincipal().AccountScopeID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-managed-sync", Active: true}); err != nil {
		t.Fatalf("upsert host credential: %v", err)
	}
	payload, _, err := authSvc.ExportCredentialsForAccount(testPrincipal().AccountScopeID, "bundle-password", "")
	if err != nil {
		t.Fatalf("export credentials: %v", err)
	}
	original := pebblestore.SwarmLocalPairingRecord{PairingState: "paired", ParentSwarmID: "manager-swarm", UserID: "local-user", AccountScopeID: "local-account"}
	if _, err := swarmStore.PutLocalPairing(original); err != nil {
		t.Fatalf("put pairing: %v", err)
	}
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, swarmStore, authSvc, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"))

	_, err = deploySvc.ApplyManagedHostInitialSyncBundle(context.Background(), "manager-swarm", ManagedHostInitialSyncBundle{
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		CredentialBundle: ContainerSyncCredentialBundle{
			OwnerSwarmID:   "manager-swarm",
			UserID:         testPrincipal().UserID,
			AccountScopeID: testPrincipal().AccountScopeID,
			BundlePassword: "bundle-password",
			Bundle:         payload,
		},
	})
	if err == nil {
		t.Fatalf("ApplyManagedHostInitialSyncBundle() succeeded with mismatched persisted pairing identity")
	}
	pairing, ok, err := swarmStore.GetLocalPairing()
	if err != nil || !ok {
		t.Fatalf("get pairing ok=%v err=%v", ok, err)
	}
	if pairing.UserID != original.UserID || pairing.AccountScopeID != original.AccountScopeID || pairing.ManagedAuthSnapshotHash != "" {
		t.Fatalf("pairing mutated after rejected bundle: %#v", pairing)
	}
}
