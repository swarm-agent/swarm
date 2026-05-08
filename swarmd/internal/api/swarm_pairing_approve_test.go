package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestSwarmRemotePairingApproveManagerApprovesAndReturnsFinalizeMaterial(t *testing.T) {
	manager := newLocalAuthTestServer(t)
	managerPublicKey, _, managerFingerprint, err := swarmruntime.GenerateNodeKeypair()
	if err != nil {
		t.Fatalf("generate manager keypair: %v", err)
	}
	managedPublicKey, _, managedFingerprint, err := swarmruntime.GenerateNodeKeypair()
	if err != nil {
		t.Fatalf("generate managed keypair: %v", err)
	}
	manager.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", PublicKey: managerPublicKey, Fingerprint: managerFingerprint}}}
	setLocalAuthTestStartupConfig(t, manager, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.NetworkMode = startupconfig.NetworkModeTailscale
		cfg.SwarmName = "Manager A"
		cfg.TailscaleURL = "https://manager-a.example.ts.net"
	})

	requestID := "pair-approve-1"
	offer := mustManagedPairingOfferForTest(t, managedPublicKey, managedFingerprint)
	manager.remotePairingPending[requestID] = swarmRemotePairingPendingRequest{
		ID:                          requestID,
		InviteToken:                 offer.Token,
		ManagerSwarmID:              "manager-swarm-1",
		ManagerName:                 "Manager A",
		ManagerPublicKey:            managerPublicKey,
		ManagerFingerprint:          managerFingerprint,
		ManagerEndpoint:             "https://manager-a.example.ts.net",
		ManagedSwarmID:              "managed-swarm-1",
		ManagedName:                 "Managed B",
		ManagedPublicKey:            managedPublicKey,
		ManagedFingerprint:          managedFingerprint,
		ManagedEndpoint:             "https://managed-b.example.ts.net",
		CeremonyCode:                offer.Ceremony.Code,
		TransportMode:               startupconfig.NetworkModeTailscale,
		ManagerRendezvousTransports: []onboardingTransportPayload{{Kind: startupconfig.NetworkModeTailscale, Primary: "https://manager-a.example.ts.net", All: []string{"https://manager-a.example.ts.net"}}},
		ManagedRendezvousTransports: offer.RendezvousTransports,
		ManagerToManagedPeerToken:   offer.Token,
		ManagedToManagerPeerToken:   "managed-to-manager-token",
		CreatedAt:                   time.Now(),
	}

	rec := postRemotePairingJSONWithDesktopSession(t, manager, "/v1/swarm/remote-pairing/approve", map[string]any{
		"request_id":    requestID,
		"approve":       true,
		"ceremony_code": offer.Ceremony.Code,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response swarmRemotePairingApprovalResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode approve response: %v", err)
	}
	if !response.OK || response.Status != startupconfig.PairingStatePaired {
		t.Fatalf("approve ok/status = %v/%q", response.OK, response.Status)
	}
	if response.Invite.Token != offer.Token || response.Enrollment.ID == "" {
		t.Fatalf("approve response missing invite/enrollment: %+v", response)
	}
	if response.Pairing.PairingState != startupconfig.PairingStatePaired || response.Pairing.ParentSwarmID != "manager-swarm-1" {
		t.Fatalf("managed pairing material missing: %+v", response.Pairing)
	}
	if response.Enrollment.Status != swarmruntime.EnrollmentStatusApproved {
		t.Fatalf("enrollment status = %q, want approved", response.Enrollment.Status)
	}
	if _, ok := manager.remotePairingPending[requestID]; ok {
		t.Fatalf("approved pending request was not cleared")
	}
}
