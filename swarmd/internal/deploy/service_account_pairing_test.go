package deploy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/identity"
	modelruntime "swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func newAccountPairingTestService(t *testing.T) (*Service, *pebblestore.Store, *pebblestore.SwarmStore) {
	t.Helper()
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
	identitySvc := identity.NewService(pebblestore.NewIdentityStore(store))
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), events, nil)
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, swarmStore, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"), modelSvc, identitySvc)
	return deploySvc, store, swarmStore
}

func TestFinalizeChildAttachRequiresAccountBoundIdentity(t *testing.T) {
	deploySvc, _, _ := newAccountPairingTestService(t)

	err := deploySvc.finalizeChildAttach(context.Background(), startupconfig.FileConfig{DeployContainer: startupconfig.DeployContainerBootstrap{Enabled: true, HostDriven: true}}, swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "child-swarm"}}, ContainerAttachState{HostSwarmID: "host-swarm"}, ContainerAttachFinalizeInput{})
	if err == nil {
		t.Fatalf("finalizeChildAttach() succeeded without identity")
	}
	if !strings.Contains(err.Error(), "local pairing user id and account scope id are required") {
		t.Fatalf("finalizeChildAttach() error = %v", err)
	}
}

func TestFinalizeChildAttachPersistsAccountBoundPairingAndMaterializesIdentity(t *testing.T) {
	deploySvc, store, swarmStore := newAccountPairingTestService(t)

	principal := testPrincipal()
	err := deploySvc.finalizeChildAttach(identity.ContextWithPrincipal(context.Background(), principal), startupconfig.FileConfig{BypassPermissions: true, DeployContainer: startupconfig.DeployContainerBootstrap{Enabled: true, HostDriven: true, SyncModules: []string{workspaceruntime.ReplicationSyncModuleModelDefaults}}}, swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "child-swarm"}}, ContainerAttachState{HostSwarmID: "host-swarm"}, ContainerAttachFinalizeInput{UserID: principal.UserID, AccountScopeID: principal.AccountScopeID})
	if err != nil {
		t.Fatalf("finalizeChildAttach() error = %v", err)
	}
	pairing, ok, err := swarmStore.GetLocalPairing()
	if err != nil || !ok {
		t.Fatalf("get pairing ok=%v err=%v", ok, err)
	}
	if pairing.PairingState != startupconfig.PairingStatePaired || pairing.ParentSwarmID != "host-swarm" || pairing.UserID != principal.UserID || pairing.AccountScopeID != principal.AccountScopeID {
		t.Fatalf("pairing = %#v", pairing)
	}
	identityStore := pebblestore.NewIdentityStore(store)
	if _, ok, err := identityStore.GetUser(principal.UserID); err != nil || !ok {
		t.Fatalf("linked user ok=%v err=%v", ok, err)
	}
	if _, ok, err := identityStore.GetAccountScope(principal.AccountScopeID); err != nil || !ok {
		t.Fatalf("linked account ok=%v err=%v", ok, err)
	}
}

func TestBindLocalPairingAccountRepairsIdentitylessPairingIdempotently(t *testing.T) {
	deploySvc, store, swarmStore := newAccountPairingTestService(t)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "child-swarm", Name: "Child", Role: "child"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{PairingState: startupconfig.PairingStateUnpaired}); err != nil {
		t.Fatalf("put identityless pairing: %v", err)
	}
	input := ContainerPairingAccountBindInput{DeploymentID: "deploy-1", HostSwarmID: "host-swarm", ChildSwarmID: "child-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID}
	if err := deploySvc.BindLocalPairingAccount(context.Background(), input); err != nil {
		t.Fatalf("BindLocalPairingAccount() error = %v", err)
	}
	if err := deploySvc.BindLocalPairingAccount(context.Background(), input); err != nil {
		t.Fatalf("BindLocalPairingAccount() repeat error = %v", err)
	}
	pairing, ok, err := swarmStore.GetLocalPairing()
	if err != nil || !ok {
		t.Fatalf("get pairing ok=%v err=%v", ok, err)
	}
	if pairing.UserID != testPrincipal().UserID || pairing.AccountScopeID != testPrincipal().AccountScopeID || pairing.ParentSwarmID != "host-swarm" {
		t.Fatalf("pairing = %#v", pairing)
	}
	if _, ok, err := pebblestore.NewIdentityStore(store).GetAccountUser(testPrincipal().AccountScopeID, testPrincipal().UserID); err != nil || !ok {
		t.Fatalf("linked account user ok=%v err=%v", ok, err)
	}
}

func TestBindLocalPairingAccountRejectsMismatchAndPreservesPairing(t *testing.T) {
	deploySvc, _, swarmStore := newAccountPairingTestService(t)
	original := pebblestore.SwarmLocalPairingRecord{PairingState: startupconfig.PairingStatePaired, ParentSwarmID: "host-swarm", UserID: "user-a", AccountScopeID: "account-a"}
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "child-swarm", Name: "Child", Role: "child"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := swarmStore.PutLocalPairing(original); err != nil {
		t.Fatalf("put pairing: %v", err)
	}
	err := deploySvc.BindLocalPairingAccount(context.Background(), ContainerPairingAccountBindInput{HostSwarmID: "host-swarm", ChildSwarmID: "child-swarm", UserID: "user-b", AccountScopeID: "account-a"})
	if err == nil {
		t.Fatalf("BindLocalPairingAccount() succeeded with mismatched user")
	}
	pairing, ok, getErr := swarmStore.GetLocalPairing()
	if getErr != nil || !ok {
		t.Fatalf("get pairing ok=%v err=%v", ok, getErr)
	}
	if pairing.UserID != original.UserID || pairing.AccountScopeID != original.AccountScopeID || pairing.ParentSwarmID != original.ParentSwarmID {
		t.Fatalf("pairing mutated after mismatch: %#v", pairing)
	}
}
