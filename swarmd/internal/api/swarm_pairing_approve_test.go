package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestSwarmRemotePairingPendingListsManagerApprovalRequests(t *testing.T) {
	manager := newLocalAuthTestServer(t)
	manager.remotePairingPending["pair-list-1"] = swarmRemotePairingPendingRequest{
		ID:                 "pair-list-1",
		ManagerSwarmID:     "manager-swarm-1",
		ManagerName:        "Manager A",
		ManagerEndpoint:    "https://manager-a.example.ts.net",
		ManagedSwarmID:     "managed-swarm-1",
		ManagedName:        "Managed B",
		ManagedFingerprint: "managed-fingerprint",
		ManagedEndpoint:    "https://managed-b.example.ts.net",
		CeremonyCode:       "ABC123",
		TransportMode:      startupconfig.NetworkModeTailscale,
		CreatedAt:          time.Unix(123, 0),
	}

	token, expiresAt, err := manager.desktopLocalSessions.Ensure(time.Now())
	if err != nil {
		t.Fatalf("ensure desktop local session: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v1/swarm/remote-pairing/pending", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Origin", "http://127.0.0.1:5555")
	req.Header.Set("Referer", "http://127.0.0.1:5555/app")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(buildDesktopLocalSessionCookie(token, expiresAt, false))
	manager.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pending status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response swarmRemotePairingPendingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode pending response: %v", err)
	}
	if !response.OK || response.Count != 1 || len(response.Items) != 1 {
		t.Fatalf("pending response = %+v", response)
	}
	item := response.Items[0]
	if item.RequestID != "pair-list-1" || item.ManagedName != "Managed B" || item.CeremonyCode != "ABC123" {
		t.Fatalf("pending item = %+v", item)
	}
}

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
