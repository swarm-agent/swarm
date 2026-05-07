package swarm

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestTrustManagedPeerPersistsManagerSideAuthAndRelationship(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	svc := NewService(pebblestore.NewSwarmStore(store), nil, nil)
	peer, err := svc.TrustManagedPeer(TrustManagedPeerInput{
		ManagedSwarmID:        "managed-swarm-1",
		ManagedName:           "Managed B",
		ManagedRole:           RelationshipManaged,
		ManagedPublicKey:      "managed-public-key",
		ManagedFingerprint:    "managed-fingerprint",
		TransportMode:         "tailscale",
		RendezvousTransports:  []TransportSummary{{Kind: "tailscale", Primary: "https://managed-b.example.ts.net", All: []string{"https://managed-b.example.ts.net"}}},
		OutgoingPeerAuthToken: "managed-to-manager-token",
		IncomingPeerAuthToken: "manager-to-managed-token",
	})
	if err != nil {
		t.Fatalf("trust managed peer: %v", err)
	}
	if peer.Relationship != RelationshipManaged || peer.ParentSwarmID != "" {
		t.Fatalf("peer relationship = %+v", peer)
	}

	stored, ok, err := pebblestore.NewSwarmStore(store).GetTrustedPeer("managed-swarm-1")
	if err != nil || !ok {
		t.Fatalf("get trusted peer ok=%t err=%v", ok, err)
	}
	if stored.OutgoingPeerAuthToken != "managed-to-manager-token" {
		t.Fatalf("outgoing peer token = %q", stored.OutgoingPeerAuthToken)
	}
	if stored.IncomingPeerAuthHash != HashPeerAuthToken("manager-to-managed-token") {
		t.Fatalf("incoming peer auth hash = %q", stored.IncomingPeerAuthHash)
	}
	if stored.Relationship != RelationshipManaged || stored.Role != RelationshipManaged {
		t.Fatalf("stored relationship/role = %q/%q", stored.Relationship, stored.Role)
	}
}

func TestApproveManagedPairingPersistsManagedSideAuth(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	swarmStore := pebblestore.NewSwarmStore(store)
	svc := NewService(swarmStore, nil, nil)
	pairing, err := svc.ApproveManagedPairing(ApproveManagedPairingInput{
		ManagerSwarmID:        "manager-swarm-1",
		ManagerName:           "Manager A",
		ManagerPublicKey:      "manager-public-key",
		ManagerFingerprint:    "manager-fingerprint",
		TransportMode:         "tailscale",
		RendezvousTransports:  []TransportSummary{{Kind: "tailscale", Primary: "https://manager-a.example.ts.net", All: []string{"https://manager-a.example.ts.net"}}},
		OutgoingPeerAuthToken: "manager-to-managed-token",
		IncomingPeerAuthToken: "managed-to-manager-token",
	})
	if err != nil {
		t.Fatalf("approve managed pairing: %v", err)
	}
	if pairing.PairingState == "" || pairing.ParentSwarmID != "manager-swarm-1" {
		t.Fatalf("pairing = %+v", pairing)
	}

	stored, ok, err := swarmStore.GetTrustedPeer("manager-swarm-1")
	if err != nil || !ok {
		t.Fatalf("get manager trusted peer ok=%t err=%v", ok, err)
	}
	if stored.Relationship != RelationshipManager {
		t.Fatalf("manager relationship = %q", stored.Relationship)
	}
	if stored.OutgoingPeerAuthToken != "manager-to-managed-token" {
		t.Fatalf("outgoing peer token = %q", stored.OutgoingPeerAuthToken)
	}
	if stored.IncomingPeerAuthHash != HashPeerAuthToken("managed-to-manager-token") {
		t.Fatalf("incoming peer auth hash = %q", stored.IncomingPeerAuthHash)
	}
}
