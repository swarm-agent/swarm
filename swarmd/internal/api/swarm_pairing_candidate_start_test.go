package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"swarm-refactor/swarmtui/pkg/startupconfig"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"testing"
)

func TestSwarmRemotePairingStartPostsManagedRequestToManager(t *testing.T) {
	manager := newLocalAuthTestServer(t)
	managerPublicKey, _, managerFingerprint, err := swarmruntime.GenerateNodeKeypair()
	if err != nil {
		t.Fatalf("generate manager keypair: %v", err)
	}
	manager.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", PublicKey: managerPublicKey, Fingerprint: managerFingerprint}}}
	setLocalAuthTestStartupConfig(t, manager, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.Child = false
		cfg.NetworkMode = startupconfig.NetworkModeTailscale
		cfg.SwarmName = "Manager A"
		cfg.TailscaleURL = "https://manager-a.example.ts.net"
	})
	remoteManager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "100.64.0.1:54321"
		if r.URL.Path == "/v1/swarm/discovery" {
			writeJSON(w, http.StatusOK, swarmDiscoveryResponse{
				OK:                   true,
				SwarmID:              "manager-swarm-1",
				Name:                 "Manager A",
				Role:                 bootstrapRoleMaster,
				Endpoint:             "https://manager-a.example.ts.net",
				TransportMode:        startupconfig.NetworkModeTailscale,
				RendezvousTransports: []onboardingTransportPayload{{Kind: startupconfig.NetworkModeTailscale, Primary: "https://manager-a.example.ts.net", All: []string{"https://manager-a.example.ts.net"}}},
			})
			return
		}
		manager.Handler().ServeHTTP(w, r)
	}))
	defer remoteManager.Close()

	managedPublicKey, _, managedFingerprint, err := swarmruntime.GenerateNodeKeypair()
	if err != nil {
		t.Fatalf("generate managed keypair: %v", err)
	}
	managed := newLocalAuthTestServer(t)
	managed.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "managed-swarm-1", Name: "Managed B", PublicKey: managedPublicKey, Fingerprint: managedFingerprint}}}
	setLocalAuthTestStartupConfig(t, managed, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.Child = false
		cfg.NetworkMode = startupconfig.NetworkModeTailscale
		cfg.SwarmName = "Managed B"
		cfg.TailscaleURL = "https://managed-b.example.ts.net"
	})

	rec := postRemotePairingJSONWithDesktopSession(t, managed, "/v1/swarm/remote-pairing/start", map[string]any{
		"endpoint": remoteManager.URL,
		"rendezvous_transports": []onboardingTransportPayload{{
			Kind:    startupconfig.NetworkModeTailscale,
			Primary: "https://manager-a.example.ts.net",
			All:     []string{"https://manager-a.example.ts.net"},
		}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("candidate start status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response swarmRemotePairingStartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode candidate start response: %v", err)
	}
	if !response.OK || response.Request.RequestID == "" {
		t.Fatalf("candidate start missing request data: %+v", response)
	}
	if response.Request.ManagedSwarmID != "managed-swarm-1" || response.Request.ManagedName != "Managed B" {
		t.Fatalf("managed identity = %q/%q", response.Request.ManagedSwarmID, response.Request.ManagedName)
	}
	if response.Ceremony.Code == "" || len(response.Ceremony.Code) != 6 {
		t.Fatalf("ceremony code = %q, want generated offer ceremony code", response.Ceremony.Code)
	}
	if len(manager.remotePairingPending) != 1 {
		t.Fatalf("manager pending requests = %d, want 1", len(manager.remotePairingPending))
	}
	if len(managed.remotePairingPending) != 0 {
		t.Fatalf("managed host should not store manager approval pending requests")
	}
	for _, pending := range manager.remotePairingPending {
		if pending.ManagedSwarmID != "managed-swarm-1" || pending.ManagerSwarmID != "manager-swarm-1" {
			t.Fatalf("pending manager/managed = %q/%q", pending.ManagerSwarmID, pending.ManagedSwarmID)
		}
		if pending.ManagedEndpoint != "https://managed-b.example.ts.net" || pending.CeremonyCode != response.Ceremony.Code {
			t.Fatalf("pending missing modal data: endpoint=%q code=%q", pending.ManagedEndpoint, pending.CeremonyCode)
		}
	}
}

func TestRemotePairingOfferExemptOnlyFromTailscaleOrLoopback(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "managed-swarm-1", Name: "Managed B", PublicKey: "public-key", Fingerprint: "fingerprint"}}}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.NetworkMode = startupconfig.NetworkModeTailscale
		cfg.SwarmName = "Managed B"
		cfg.TailscaleURL = "https://managed-b.example.ts.net"
	})

	req := httptest.NewRequest(http.MethodPost, "http://managed-b.example.ts.net/v1/swarm/remote-pairing/offer", strings.NewReader(`{}`))
	req.RemoteAddr = "198.51.100.20:54321"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("non-tailnet unauthenticated offer status = %d, want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "http://managed-b.example.ts.net/v1/swarm/remote-pairing/offer", strings.NewReader(`{}`))
	req.RemoteAddr = "100.64.0.20:54321"
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tailnet unauthenticated offer status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}
