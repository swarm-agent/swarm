package swarm

import (
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestDetachToStandaloneRemovesManagedGroupMembership(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	swarmStore := pebblestore.NewSwarmStore(store)
	svc := NewService(swarmStore, nil, nil)
	if _, err := swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{SwarmID: "manager-swarm", Relationship: RelationshipManager}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}
	if _, err := swarmStore.PutGroup(pebblestore.SwarmGroupRecord{ID: "group-1", Name: "Manager", HostSwarmID: "manager-swarm"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	if _, err := swarmStore.PutGroupMembership(pebblestore.SwarmGroupMembershipRecord{GroupID: "group-1", SwarmID: "managed-swarm", Name: "Managed", SwarmRole: RelationshipManaged, MembershipRole: GroupMembershipRoleMember}); err != nil {
		t.Fatalf("put membership: %v", err)
	}
	if err := swarmStore.PutCurrentGroupID("group-1"); err != nil {
		t.Fatalf("put current group: %v", err)
	}

	if err := svc.DetachToStandalone("managed-swarm"); err != nil {
		t.Fatalf("detach: %v", err)
	}
	pairing, ok, err := swarmStore.GetLocalPairing()
	if err != nil || !ok {
		t.Fatalf("get pairing ok=%t err=%v", ok, err)
	}
	if pairing.PairingState != startupconfig.PairingStateUnpaired || pairing.ParentSwarmID != "" || pairing.UserID != "" || pairing.AccountScopeID != "" || pairing.ManagedAuthSnapshotHash != "" || pairing.ManagedAuthOwnerSwarmID != "" {
		t.Fatalf("pairing = %+v", pairing)
	}
	if peers, err := swarmStore.ListTrustedPeers(10); err != nil || len(peers) != 0 {
		t.Fatalf("trusted peers len=%d err=%v", len(peers), err)
	}
	if memberships, err := swarmStore.ListGroupMembershipsBySwarm("managed-swarm", 10); err != nil || len(memberships) != 0 {
		t.Fatalf("memberships len=%d err=%v", len(memberships), err)
	}
	if current, ok, err := swarmStore.GetCurrentGroupID(); err != nil || ok || current != "" {
		t.Fatalf("current group = %q ok=%t err=%v", current, ok, err)
	}
}

func TestRemoveManagedPeerCleansManagerSideRecords(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	swarmStore := pebblestore.NewSwarmStore(store)
	svc := NewService(swarmStore, nil, nil)
	if _, err := swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{SwarmID: "managed-swarm", Relationship: RelationshipManaged}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}
	if _, err := swarmStore.PutGroup(pebblestore.SwarmGroupRecord{ID: "group-1", Name: "Manager", HostSwarmID: "manager-swarm"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	if _, err := swarmStore.PutGroupMembership(pebblestore.SwarmGroupMembershipRecord{GroupID: "group-1", SwarmID: "managed-swarm", Name: "Managed", SwarmRole: RelationshipManaged, MembershipRole: GroupMembershipRoleMember}); err != nil {
		t.Fatalf("put membership: %v", err)
	}

	result, err := svc.RemoveManagedPeer(RemoveManagedPeerInput{ManagedSwarmID: "managed-swarm"})
	if err != nil {
		t.Fatalf("remove managed peer: %v", err)
	}
	if !result.RemovedTrustedPeer || result.RemovedGroupMemberships != 1 {
		t.Fatalf("result = %+v", result)
	}
	if _, ok, err := swarmStore.GetTrustedPeer("managed-swarm"); err != nil || ok {
		t.Fatalf("trusted peer ok=%t err=%v", ok, err)
	}
	if memberships, err := swarmStore.ListGroupMembershipsBySwarm("managed-swarm", 10); err != nil || len(memberships) != 0 {
		t.Fatalf("memberships len=%d err=%v", len(memberships), err)
	}
}

func TestDetachToStandaloneClearsPairingIdentityAndManagedAuthMetadata(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	swarmStore := pebblestore.NewSwarmStore(store)
	svc := NewService(swarmStore, nil, nil)
	if _, err := swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{
		PairingState:                   startupconfig.PairingStatePaired,
		ParentSwarmID:                  "manager-swarm",
		UserID:                         "user-a",
		AccountScopeID:                 "account-a",
		WorkspaceBootstrapDeploymentID: "deploy-a",
		WorkspaceBootstrapAt:           123,
		ManagedAuthOwnerSwarmID:        "manager-swarm",
		ManagedAuthSnapshotHash:        "hash-a",
		ManagedAuthAppliedAt:           456,
		ManagedAuthLastAttemptAt:       789,
		ManagedAuthLastError:           "previous error",
		RendezvousTransports:           []pebblestore.SwarmTransportRecord{{Kind: "tailscale", Primary: "https://manager.example"}},
	}); err != nil {
		t.Fatalf("put pairing: %v", err)
	}

	if err := svc.DetachToStandalone("managed-swarm"); err != nil {
		t.Fatalf("detach: %v", err)
	}
	pairing, ok, err := swarmStore.GetLocalPairing()
	if err != nil || !ok {
		t.Fatalf("get pairing ok=%t err=%v", ok, err)
	}
	if pairing.PairingState != startupconfig.PairingStateUnpaired || pairing.ParentSwarmID != "" || pairing.UserID != "" || pairing.AccountScopeID != "" || pairing.WorkspaceBootstrapDeploymentID != "" || pairing.WorkspaceBootstrapAt != 0 || pairing.ManagedAuthOwnerSwarmID != "" || pairing.ManagedAuthSnapshotHash != "" || pairing.ManagedAuthAppliedAt != 0 || pairing.ManagedAuthLastAttemptAt != 0 || pairing.ManagedAuthLastError != "" || len(pairing.RendezvousTransports) != 0 {
		t.Fatalf("pairing metadata not cleared: %+v", pairing)
	}
}
