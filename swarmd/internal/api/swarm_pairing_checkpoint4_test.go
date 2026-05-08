package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestSwarmRemotePairingRequestCreatesManagerPendingApproval(t *testing.T) {
	server := newLocalAuthTestServer(t)
	managerPublicKey, _, managerFingerprint, err := swarmruntime.GenerateNodeKeypair()
	if err != nil {
		t.Fatalf("generate manager keypair: %v", err)
	}
	managedPublicKey, _, managedFingerprint, err := swarmruntime.GenerateNodeKeypair()
	if err != nil {
		t.Fatalf("generate managed keypair: %v", err)
	}
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", PublicKey: managerPublicKey, Fingerprint: managerFingerprint}}}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.Child = false
		cfg.NetworkMode = startupconfig.NetworkModeTailscale
		cfg.SwarmName = "Manager A"
		cfg.TailscaleURL = "https://manager-a.example.ts.net"
	})
	offer := mustManagedPairingOfferForTest(t, managedPublicKey, managedFingerprint)

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/remote-pairing/request", map[string]any{
		"invite_token":          offer.Token,
		"manager_swarm_id":      "manager-swarm-1",
		"manager_name":          "Manager A",
		"manager_endpoint":      "https://manager-a.example.ts.net",
		"offer":                 offer,
		"ceremony_code":         offer.Ceremony.Code,
		"transport_mode":        startupconfig.NetworkModeTailscale,
		"peer_auth_token":       "managed-to-manager-token",
		"rendezvous_transports": offer.RendezvousTransports,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("request status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response swarmRemotePairingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode request response: %v", err)
	}
	if !response.OK || response.Status != startupconfig.PairingStatePendingApproval {
		t.Fatalf("response ok/status = %v/%q", response.OK, response.Status)
	}
	if response.RequestID == "" {
		t.Fatalf("request id is empty")
	}
	if response.ManagedSwarmID != "managed-swarm-1" || response.ManagedName != "Managed B" {
		t.Fatalf("managed identity = %q/%q", response.ManagedSwarmID, response.ManagedName)
	}
	if response.ManagedPublicKey != managedPublicKey || response.ManagedFingerprint != managedFingerprint {
		t.Fatalf("managed key/fingerprint not reflected in response")
	}
	if response.CeremonyCode != offer.Ceremony.Code {
		t.Fatalf("ceremony code = %q, want %q", response.CeremonyCode, offer.Ceremony.Code)
	}
	pending, ok := server.remotePairingPending[response.RequestID]
	if !ok {
		t.Fatalf("pending request %q was not stored", response.RequestID)
	}
	if pending.ManagerSwarmID != "manager-swarm-1" || pending.ManagedSwarmID != "managed-swarm-1" {
		t.Fatalf("pending manager/managed = %q/%q", pending.ManagerSwarmID, pending.ManagedSwarmID)
	}
	if pending.ManagedEndpoint != "https://managed-b.example.ts.net" || len(pending.ManagedRendezvousTransports) == 0 {
		t.Fatalf("pending missing managed reachability: endpoint=%q transports=%d", pending.ManagedEndpoint, len(pending.ManagedRendezvousTransports))
	}
	if pending.ManagerToManagedPeerToken != offer.Token || pending.ManagedToManagerPeerToken != "managed-to-manager-token" {
		t.Fatalf("pending peer tokens not retained as expected")
	}
}

func TestSwarmRemotePairingRequestRejectsCeremonyCodeMismatch(t *testing.T) {
	server := newLocalAuthTestServer(t)
	managerPublicKey, _, managerFingerprint, err := swarmruntime.GenerateNodeKeypair()
	if err != nil {
		t.Fatalf("generate manager keypair: %v", err)
	}
	managedPublicKey, _, managedFingerprint, err := swarmruntime.GenerateNodeKeypair()
	if err != nil {
		t.Fatalf("generate managed keypair: %v", err)
	}
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", PublicKey: managerPublicKey, Fingerprint: managerFingerprint}}}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.NetworkMode = startupconfig.NetworkModeTailscale
		cfg.SwarmName = "Manager A"
		cfg.TailscaleURL = "https://manager-a.example.ts.net"
	})
	offer := mustManagedPairingOfferForTest(t, managedPublicKey, managedFingerprint)

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/remote-pairing/request", map[string]any{
		"invite_token":     offer.Token,
		"manager_swarm_id": "manager-swarm-1",
		"manager_endpoint": "https://manager-a.example.ts.net",
		"offer":            offer,
		"ceremony_code":    "BADBAD",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatch status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ceremony code") {
		t.Fatalf("mismatch error did not mention ceremony code: %s", rec.Body.String())
	}
	if len(server.remotePairingPending) != 0 {
		t.Fatalf("mismatch stored pending request")
	}
}

func TestSwarmRemotePairingApproveRejectsCeremonyCodeMismatch(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.remotePairingPending["pair-1"] = swarmRemotePairingPendingRequest{
		ID:             "pair-1",
		ManagerSwarmID: "manager-swarm-1",
		ManagedSwarmID: "managed-swarm-1",
		ManagedName:    "Managed B",
		CeremonyCode:   "ABC123",
	}

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/remote-pairing/approve", map[string]any{
		"request_id":    "pair-1",
		"approve":       true,
		"ceremony_code": "BADBAD",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("approve mismatch status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if _, ok := server.remotePairingPending["pair-1"]; !ok {
		t.Fatalf("mismatch should leave pending request for retry")
	}
}

func mustManagedPairingOfferForTest(t *testing.T, publicKey, fingerprint string) swarmRemotePairingOfferPayload {
	t.Helper()
	now := time.Now()
	offer, err := buildSwarmRemotePairingOffer(startupconfig.FileConfig{
		SwarmMode:     true,
		NetworkMode:   startupconfig.NetworkModeTailscale,
		SwarmName:     "Managed B",
		TailscaleURL:  "https://managed-b.example.ts.net",
		Port:          7777,
		AdvertisePort: 7777,
	}, onboardingResponse{}, swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "managed-swarm-1", Name: "Managed B", PublicKey: publicKey, Fingerprint: fingerprint}}, 2*time.Minute, now)
	if err != nil {
		t.Fatalf("build offer: %v", err)
	}
	return offer
}

func postRemotePairingJSONWithDesktopSession(t *testing.T, server *Server, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	token, expiresAt, err := server.desktopLocalSessions.Ensure(time.Now())
	if err != nil {
		t.Fatalf("ensure desktop local session: %v", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5555"+path, strings.NewReader(string(raw)))
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Origin", "http://127.0.0.1:5555")
	req.Header.Set("Referer", "http://127.0.0.1:5555/app")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(buildDesktopLocalSessionCookie(token, expiresAt, false))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}
