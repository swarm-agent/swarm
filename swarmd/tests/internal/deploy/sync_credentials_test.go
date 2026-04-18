package deploy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	agentruntime "swarm/packages/swarmd/internal/agent"
	authruntime "swarm/packages/swarmd/internal/auth"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func openSharedStore(t *testing.T, name string) *pebblestore.Store {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func writeStartupConfig(t *testing.T, cfg startupconfig.FileConfig) string {
	t.Helper()
	if err := os.WriteFile(cfg.Path, []byte(startupconfig.Format(cfg)), 0o600); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	return cfg.Path
}

func TestSyncAgentBundleReturnsRequestedLocalState(t *testing.T) {
	store := openSharedStore(t, "agent-sync.pebble")
	agentStore := pebblestore.NewAgentStore(store)
	if err := agentStore.PutProfile(pebblestore.AgentProfile{Name: "helper", Mode: "subagent", Prompt: "help", Enabled: true}); err != nil {
		t.Fatalf("put profile: %v", err)
	}
	if err := agentStore.PutCustomTool(pebblestore.AgentCustomToolDefinition{Name: "git_status_short", Kind: pebblestore.AgentCustomToolKindFixedBash, Command: "git status --short"}); err != nil {
		t.Fatalf("put custom tool: %v", err)
	}
	if err := agentStore.SetActivePrimary("swarm"); err != nil {
		t.Fatalf("set active primary: %v", err)
	}
	if err := agentStore.SetActiveSubagent("explorer", "helper"); err != nil {
		t.Fatalf("set active subagent: %v", err)
	}
	deployStore := pebblestore.NewDeployContainerStore(store)
	if _, err := deployStore.Put(pebblestore.DeployContainerRecord{
		ID:               "deploy-agent-sync",
		Name:             "deploy-agent-sync",
		SyncEnabled:      true,
		SyncMode:         "managed",
		SyncModules:      []string{"credentials", "agents", "custom_tools"},
		SyncOwnerSwarmID: "swarm_host",
		BootstrapSecret:  "bootstrap-secret",
	}); err != nil {
		t.Fatalf("put deployment record: %v", err)
	}
	agentSvc := agentruntime.NewService(agentStore, nil)
	bundle, err := deployruntime.NewService(deployStore, nil, nil, nil, nil, agentSvc, nil, "").SyncAgentBundle(context.Background(), deployruntime.ContainerSyncCredentialRequestInput{
		DeploymentID:    "deploy-agent-sync",
		BootstrapSecret: "bootstrap-secret",
	})
	if err != nil {
		t.Fatalf("SyncAgentBundle() error = %v", err)
	}
	if bundle.SnapshotHash == "" {
		t.Fatalf("snapshot hash was empty")
	}
	if len(bundle.State.Profiles) != 1 {
		t.Fatalf("profiles = %d, want 1", len(bundle.State.Profiles))
	}
	if len(bundle.State.CustomTools) != 1 {
		t.Fatalf("custom tools = %d, want 1", len(bundle.State.CustomTools))
	}
	if bundle.State.ActiveSubagent["explorer"] != "helper" {
		t.Fatalf("active subagent explorer = %q, want helper", bundle.State.ActiveSubagent["explorer"])
	}
}

func TestSyncManagedCredentialsOncePullsUpdatedSnapshot(t *testing.T) {
	const (
		ownerSwarmID       = "swarm_host"
		deploymentID       = "deploy-sync-child"
		bootstrapSecret    = "bootstrap-secret"
		bundlePassword     = "bundle-password"
		childSwarmID       = "swarm_child"
		outgoingPeerToken  = "peer-token"
		syncCredentialPath = "/v1/deploy/container/sync/credentials"
	)

	hostStore := openSharedStore(t, "host-auth.pebble")
	hostAuthStore := pebblestore.NewAuthStore(hostStore)
	if _, err := hostAuthStore.UpsertCredential(pebblestore.AuthCredentialInput{
		ID:        "fw-primary",
		Provider:  "fireworks",
		Type:      pebblestore.AuthTypeAPI,
		Label:     "primary",
		APIKey:    "fw-primary-key",
		SetActive: true,
	}); err != nil {
		t.Fatalf("upsert primary host credential: %v", err)
	}

	hostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != syncCredentialPath {
			http.NotFound(w, r)
			return
		}
		var req deployruntime.ContainerSyncCredentialRequestInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.DeploymentID != deploymentID {
			http.Error(w, "unexpected deployment id", http.StatusBadRequest)
			return
		}
		if req.BootstrapSecret != bootstrapSecret {
			http.Error(w, "unexpected bootstrap secret", http.StatusBadRequest)
			return
		}
		payload, exported, err := hostAuthStore.ExportCredentials(bundlePassword, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"path_id": deployruntime.PathContainerSyncCredentials,
			"bundle": deployruntime.ContainerSyncCredentialBundle{
				OwnerSwarmID:   ownerSwarmID,
				BundlePassword: bundlePassword,
				Bundle:         payload,
				Exported:       exported,
			},
		})
	}))
	defer hostServer.Close()

	childStore := openSharedStore(t, "child-store.pebble")
	childAuthStore := pebblestore.NewAuthStore(childStore)
	childAuthSvc := authruntime.NewService(childAuthStore, nil)
	childAgentStore := pebblestore.NewAgentStore(childStore)
	childAgentSvc := agentruntime.NewService(childAgentStore, nil)
	childSwarmStore := pebblestore.NewSwarmStore(childStore)
	if _, err := childSwarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: childSwarmID, Name: "Child"}); err != nil {
		t.Fatalf("put child local node: %v", err)
	}
	if _, err := childSwarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{
		PairingState:      startupconfig.PairingStatePaired,
		ParentSwarmID:     ownerSwarmID,
		LastUpdatedByRole: "child",
	}); err != nil {
		t.Fatalf("put child local pairing: %v", err)
	}
	if _, err := childSwarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{
		SwarmID:               ownerSwarmID,
		Name:                  "Primary",
		Role:                  "master",
		Relationship:          "parent",
		OutgoingPeerAuthToken: outgoingPeerToken,
	}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}

	startupPath := filepath.Join(t.TempDir(), "swarm-child.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.Mode = startupconfig.ModeBox
	cfg.Child = true
	cfg.ParentSwarmID = ownerSwarmID
	cfg.PairingState = startupconfig.PairingStatePaired
	cfg.DeployContainer = startupconfig.DeployContainerBootstrap{
		Enabled:           true,
		SyncEnabled:       true,
		SyncMode:          "managed",
		SyncModules:       []string{"credentials"},
		SyncOwnerSwarmID:  ownerSwarmID,
		SyncCredentialURL: hostServer.URL + syncCredentialPath,
		DeploymentID:      deploymentID,
		HostAPIBaseURL:    hostServer.URL,
		BootstrapSecret:   bootstrapSecret,
	}
	writeStartupConfig(t, cfg)

	childDeploySvc := deployruntime.NewService(nil, nil, nil, childSwarmStore, childAuthSvc, childAgentSvc, nil, startupPath)

	if err := childDeploySvc.SyncManagedCredentialsOnce(context.Background()); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	pairing, ok, err := childSwarmStore.GetLocalPairing()
	if err != nil {
		t.Fatalf("get child pairing after initial sync: %v", err)
	}
	if !ok {
		t.Fatalf("expected child pairing after initial sync")
	}
	if pairing.ManagedAuthSnapshotHash == "" {
		t.Fatalf("managed auth snapshot hash was empty after initial sync")
	}
	if pairing.ManagedAuthLastError != "" {
		t.Fatalf("managed auth last error = %q, want empty", pairing.ManagedAuthLastError)
	}
	childRecords, err := childAuthStore.ListCredentials("fireworks", 10)
	if err != nil {
		t.Fatalf("list child credentials after initial sync: %v", err)
	}
	if len(childRecords) != 1 || childRecords[0].ID != "fw-primary" {
		t.Fatalf("child credentials after initial sync = %#v, want fw-primary only", childRecords)
	}
	childVault, err := childAuthStore.VaultStatus()
	if err != nil {
		t.Fatalf("child vault status after initial sync: %v", err)
	}
	if childVault.Enabled {
		t.Fatalf("child vault enabled after initial sync = true, want false for stage-1 plain sync")
	}

	initialHash := pairing.ManagedAuthSnapshotHash

	if _, err := hostAuthStore.UpsertCredential(pebblestore.AuthCredentialInput{
		ID:        "fw-secondary",
		Provider:  "fireworks",
		Type:      pebblestore.AuthTypeAPI,
		Label:     "secondary",
		APIKey:    "fw-secondary-key",
		SetActive: false,
	}); err != nil {
		t.Fatalf("upsert secondary host credential: %v", err)
	}
	if _, err := hostAuthStore.SetActiveCredential("fireworks", "fw-secondary"); err != nil {
		t.Fatalf("set active host credential to secondary: %v", err)
	}

	if err := childDeploySvc.SyncManagedCredentialsOnce(context.Background()); err != nil {
		t.Fatalf("updated sync: %v", err)
	}
	pairing, ok, err = childSwarmStore.GetLocalPairing()
	if err != nil {
		t.Fatalf("get child pairing after updated sync: %v", err)
	}
	if !ok {
		t.Fatalf("expected child pairing after updated sync")
	}
	if pairing.ManagedAuthSnapshotHash == "" || pairing.ManagedAuthSnapshotHash == initialHash {
		t.Fatalf("managed auth snapshot hash after updated sync = %q, want new non-empty hash", pairing.ManagedAuthSnapshotHash)
	}
	childRecords, err = childAuthStore.ListCredentials("fireworks", 10)
	if err != nil {
		t.Fatalf("list child credentials after updated sync: %v", err)
	}
	if len(childRecords) != 2 {
		t.Fatalf("child credential count after updated sync = %d, want 2", len(childRecords))
	}
	active, ok, err := childAuthStore.GetActiveCredential("fireworks")
	if err != nil {
		t.Fatalf("get child active credential after updated sync: %v", err)
	}
	if !ok || active.ID != "fw-secondary" {
		t.Fatalf("child active credential after updated sync = %#v, want fw-secondary", active)
	}
}

func TestSyncCredentialBundleUnlocksVaultedHostWhenPasswordProvided(t *testing.T) {
	store := openSharedStore(t, "host-store.pebble")
	authStore := pebblestore.NewAuthStore(store)
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{
		ID:        "fw-primary",
		Provider:  "fireworks",
		Type:      pebblestore.AuthTypeAPI,
		Label:     "primary",
		APIKey:    "fw-primary-key",
		SetActive: true,
	}); err != nil {
		t.Fatalf("upsert host credential: %v", err)
	}
	if _, err := authStore.EnableVault("vault-password"); err != nil {
		t.Fatalf("enable vault: %v", err)
	}
	if _, err := authStore.LockVault(); err != nil {
		t.Fatalf("lock vault: %v", err)
	}

	deployStore := pebblestore.NewDeployContainerStore(store)
	if _, err := deployStore.Put(pebblestore.DeployContainerRecord{
		ID:                 "deploy-sync-child",
		Name:               "deploy-sync-child",
		SyncEnabled:        true,
		SyncMode:           "managed",
		SyncModules:        []string{"credentials"},
		SyncOwnerSwarmID:   "swarm_host",
		SyncBundlePassword: "bundle-password",
		BootstrapSecret:    "bootstrap-secret",
	}); err != nil {
		t.Fatalf("put deployment record: %v", err)
	}

	deploySvc := deployruntime.NewService(deployStore, nil, nil, nil, authruntime.NewService(authStore, nil), nil, nil, "")
	bundle, err := deploySvc.SyncCredentialBundle(context.Background(), deployruntime.ContainerSyncCredentialRequestInput{
		DeploymentID:    "deploy-sync-child",
		BootstrapSecret: "bootstrap-secret",
		VaultPassword:   "vault-password",
	})
	if err != nil {
		t.Fatalf("SyncCredentialBundle() error = %v, want nil", err)
	}
	if len(bundle.Bundle) == 0 {
		t.Fatalf("sync bundle payload was empty")
	}
}

func TestSyncCredentialBundleRequiresUnlockForLockedVaultedHost(t *testing.T) {
	store := openSharedStore(t, "host-store.pebble")
	authStore := pebblestore.NewAuthStore(store)
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{
		ID:        "fw-primary",
		Provider:  "fireworks",
		Type:      pebblestore.AuthTypeAPI,
		Label:     "primary",
		APIKey:    "fw-primary-key",
		SetActive: true,
	}); err != nil {
		t.Fatalf("upsert host credential: %v", err)
	}
	if _, err := authStore.EnableVault("vault-password"); err != nil {
		t.Fatalf("enable vault: %v", err)
	}
	if _, err := authStore.LockVault(); err != nil {
		t.Fatalf("lock vault: %v", err)
	}

	deployStore := pebblestore.NewDeployContainerStore(store)
	if _, err := deployStore.Put(pebblestore.DeployContainerRecord{
		ID:                 "deploy-sync-child",
		Name:               "deploy-sync-child",
		SyncEnabled:        true,
		SyncMode:           "managed",
		SyncModules:        []string{"credentials"},
		SyncOwnerSwarmID:   "swarm_host",
		SyncBundlePassword: "bundle-password",
		BootstrapSecret:    "bootstrap-secret",
	}); err != nil {
		t.Fatalf("put deployment record: %v", err)
	}

	deploySvc := deployruntime.NewService(deployStore, nil, nil, nil, authruntime.NewService(authStore, nil), nil, nil, "")
	_, err := deploySvc.SyncCredentialBundle(context.Background(), deployruntime.ContainerSyncCredentialRequestInput{
		DeploymentID:    "deploy-sync-child",
		BootstrapSecret: "bootstrap-secret",
	})
	if err == nil {
		t.Fatalf("SyncCredentialBundle() error = nil, want unlock failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unlock") && !strings.Contains(strings.ToLower(err.Error()), "locked") {
		t.Fatalf("SyncCredentialBundle() error = %q, want unlock failure", err.Error())
	}
}
