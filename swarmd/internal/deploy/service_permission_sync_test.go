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
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "openrouter", AccountScopeID: testPrincipal().AccountScopeID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-sync", Active: true}); err != nil {
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
		UserID:           "user-1",
		AccountScopeID:   "account-1",
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
	if _, _, _, err := agentSvc.Upsert(agentruntime.UpsertInput{Name: "managed-probe", Mode: agentruntime.ModeSubagent, Prompt: "sync me", Enabled: pebblestore.BoolPtr(true)}); err != nil {
		t.Fatalf("upsert agent: %v", err)
	}
	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), events)
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{Provider: "openrouter", AccountScopeID: testPrincipal().AccountScopeID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-test-managed-sync", Active: true}); err != nil {
		t.Fatalf("upsert credential: %v", err)
	}
	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), nil, nil)
	permSvc.SetBypassPermissions(true)
	if _, err := permSvc.UpsertRule(permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "read"}); err != nil {
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
