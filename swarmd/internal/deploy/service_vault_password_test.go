package deploy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/identity"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	modelruntime "swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestPendingSyncVaultPasswordExpires(t *testing.T) {
	svc := &Service{}
	svc.rememberPendingSyncVaultPassword("deploy-1", "vault-password", time.Now().Add(-time.Second).UnixMilli())
	if got := svc.resolvePendingSyncVaultPassword("deploy-1"); got != "" {
		t.Fatalf("resolvePendingSyncVaultPassword() = %q, want empty for expired entry", got)
	}
}

func TestPendingSyncVaultPasswordRetainedUntilExpiry(t *testing.T) {
	svc := &Service{}
	svc.rememberPendingSyncVaultPassword("deploy-1", "vault-password", time.Now().Add(time.Minute).UnixMilli())
	if got := svc.resolvePendingSyncVaultPassword("deploy-1"); got != "vault-password" {
		t.Fatalf("resolvePendingSyncVaultPassword() = %q, want vault-password", got)
	}
	if got := svc.resolvePendingSyncVaultPassword("deploy-1"); got != "vault-password" {
		t.Fatalf("resolvePendingSyncVaultPassword() second read = %q, want vault-password", got)
	}
	svc.clearPendingSyncVaultPassword("deploy-1")
	if got := svc.resolvePendingSyncVaultPassword("deploy-1"); got != "" {
		t.Fatalf("resolvePendingSyncVaultPassword() after clear = %q, want empty", got)
	}
}

func TestCreateResultCanBePersistedRejectsEmptyContainer(t *testing.T) {
	if createResultCanBePersisted(localcontainers.Container{}) {
		t.Fatalf("createResultCanBePersisted(empty) = true, want false")
	}
}

func TestCreateResultCanBePersistedAcceptsRecordedContainer(t *testing.T) {
	container := localcontainers.Container{
		Name:          "child-swarm",
		ContainerName: "child-swarm",
	}
	if !createResultCanBePersisted(container) {
		t.Fatalf("createResultCanBePersisted(container) = false, want true")
	}
}

func TestCreateResultDisplayNameFallsBackToInputName(t *testing.T) {
	got := createResultDisplayName(ContainerCreateInput{Name: "child-swarm"}, localcontainers.Container{})
	if got != "child-swarm" {
		t.Fatalf("createResultDisplayName() = %q, want child-swarm", got)
	}
}

func newModelDefaultsSyncTestService(t *testing.T) (*Service, *pebblestore.Store) {
	t.Helper()
	store, err := pebblestore.Open(t.TempDir() + "/swarm.pebble")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), events, nil)
	return &Service{model: modelSvc, swarmStore: pebblestore.NewSwarmStore(store), workspace: workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))}, store
}

func TestApplyManagedModelDefaultsBundleAllowsUnsetProviderAndModel(t *testing.T) {
	svc, store := newModelDefaultsSyncTestService(t)
	modelStore := pebblestore.NewModelStore(store)
	if _, err := modelStore.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("seed global preference: %v", err)
	}

	if err := svc.ApplyManagedModelDefaultsBundle(context.Background(), ContainerSyncModelDefaultsBundle{Preference: pebblestore.ModelPreference{Thinking: pebblestore.DefaultThinkingLevel}}); err != nil {
		t.Fatalf("ApplyManagedModelDefaultsBundle() error = %v", err)
	}
	_, ok, err := modelStore.GetGlobalPreference()
	if err != nil {
		t.Fatalf("GetGlobalPreference() error = %v", err)
	}
	if ok {
		t.Fatalf("ApplyManagedModelDefaultsBundle() left a global default after applying an unset provider/model")
	}
}

func TestApplyManagedModelDefaultsBundleRejectsPartialPreference(t *testing.T) {
	svc, _ := newModelDefaultsSyncTestService(t)

	err := svc.ApplyManagedModelDefaultsBundle(context.Background(), ContainerSyncModelDefaultsBundle{Preference: pebblestore.ModelPreference{Provider: "codex", Thinking: "high"}})
	if err == nil {
		t.Fatalf("ApplyManagedModelDefaultsBundle() expected partial preference error")
	}
	if !strings.Contains(err.Error(), "missing provider, model, or thinking") {
		t.Fatalf("ApplyManagedModelDefaultsBundle() error = %v", err)
	}
}

func TestApplyManagedModelDefaultsBundlePersistsCompletePreference(t *testing.T) {
	svc, store := newModelDefaultsSyncTestService(t)

	if err := svc.ApplyManagedModelDefaultsBundle(context.Background(), ContainerSyncModelDefaultsBundle{Preference: pebblestore.ModelPreference{Provider: " codex ", Model: " gpt-5-codex ", Thinking: " high "}}); err != nil {
		t.Fatalf("ApplyManagedModelDefaultsBundle() error = %v", err)
	}
	pref, ok, err := pebblestore.NewModelStore(store).GetGlobalPreference()
	if err != nil {
		t.Fatalf("GetGlobalPreference() error = %v", err)
	}
	if !ok {
		t.Fatalf("ApplyManagedModelDefaultsBundle() did not persist a complete preference")
	}
	if pref.Provider != "codex" || pref.Model != "gpt-5-codex" || pref.Thinking != "high" {
		t.Fatalf("preference = %+v, want codex/gpt-5-codex/high", pref)
	}
}

func TestFinalizeChildAttachAppliesModelDefaultsDuringInitialLocalAttach(t *testing.T) {
	svc, store := newModelDefaultsSyncTestService(t)
	modelStore := pebblestore.NewModelStore(store)
	if _, err := svc.swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "child-swarm", Name: "Child", Role: "child"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := svc.swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{SwarmID: "host-swarm", Name: "Host", Relationship: swarmruntime.RelationshipParent, OutgoingPeerAuthToken: "child-to-host-token"}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}

	var sawSyncModelDefaults bool
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/deploy/container/sync/model-defaults" {
			http.NotFound(w, r)
			return
		}
		sawSyncModelDefaults = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"bundle":{"preference":{"provider":"codex","model":"gpt-5-codex","thinking":"high"},"modules":["model_defaults"]}}`))
	}))
	defer host.Close()

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user_test", AccountScopeID: "acct_test"}
	err := svc.finalizeChildAttach(identity.ContextWithPrincipal(context.Background(), principal), startupconfig.FileConfig{
		BypassPermissions: true,
		DeployContainer: startupconfig.DeployContainerBootstrap{
			Enabled:         true,
			HostDriven:      true,
			SyncEnabled:     true,
			SyncModules:     []string{workspaceruntime.ReplicationSyncModuleModelDefaults},
			DeploymentID:    "localtest",
			BootstrapSecret: "bootstrap-secret",
			HostAPIBaseURL:  host.URL,
		},
	}, swarmruntime.LocalState{
		Node: swarmruntime.LocalNodeState{SwarmID: "child-swarm", Name: "localtest", Role: "child"},
	}, ContainerAttachState{
		DeploymentID:   "localtest",
		HostSwarmID:    "host-swarm",
		HostBackendURL: host.URL,
	}, ContainerAttachFinalizeInput{
		DeploymentID:    "localtest",
		BootstrapSecret: "bootstrap-secret",
		HostSwarmID:     "host-swarm",
		SyncModules:     []string{workspaceruntime.ReplicationSyncModuleModelDefaults},
	})
	if err != nil {
		t.Fatalf("finalizeChildAttach() error = %v", err)
	}
	if !sawSyncModelDefaults {
		t.Fatalf("finalizeChildAttach() did not request model defaults during initial attach")
	}
	pref, ok, err := modelStore.GetPreferenceForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("GetPreferenceForAccount() error = %v", err)
	}
	if !ok || pref.Provider != "codex" || pref.Model != "gpt-5-codex" || pref.Thinking != "high" || pref.AccountScopeID != principal.AccountScopeID {
		t.Fatalf("preference after finalize = %+v ok=%v", pref, ok)
	}
}
