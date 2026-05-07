package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestSwarmRemotePairingOfferGeneratesManagedSwarmOffer(t *testing.T) {
	server := newLocalAuthTestServer(t)
	publicKey, _, fingerprint, err := swarmruntime.GenerateNodeKeypair()
	if err != nil {
		t.Fatalf("generate test node keypair: %v", err)
	}
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "managed-swarm-1", Name: "State Managed", Role: "standalone", PublicKey: publicKey, Fingerprint: fingerprint}}}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.Child = false
		cfg.NetworkMode = startupconfig.NetworkModeTailscale
		cfg.SwarmName = "Managed Host B"
		cfg.TailscaleURL = "https://managed-b.example.ts.net"
		cfg.Port = 7777
		cfg.AdvertisePort = 7777
	})

	response := createRemotePairingOfferWithDesktopSession(t, server, `{"ttl_seconds":120}`)
	if !response.OK {
		t.Fatalf("ok = false: %+v", response)
	}
	if response.PathID != swarmRemotePairingOfferPathID {
		t.Fatalf("path_id = %q, want %q", response.PathID, swarmRemotePairingOfferPathID)
	}
	offer := response.Offer
	if offer.Version != swarmRemotePairingOfferVersion || offer.Type != "managed_swarm_offer" {
		t.Fatalf("offer version/type = %q/%q", offer.Version, offer.Type)
	}
	if len(offer.Token) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(offer.Token))
	}
	if !offer.SingleUse {
		t.Fatalf("offer single_use = false")
	}
	if offer.SwarmID != "managed-swarm-1" || offer.SwarmName != "Managed Host B" {
		t.Fatalf("offer identity = %q/%q", offer.SwarmID, offer.SwarmName)
	}
	if offer.PublicKey != publicKey || offer.Fingerprint != fingerprint {
		t.Fatalf("offer key material mismatch public=%q fingerprint=%q", offer.PublicKey, offer.Fingerprint)
	}
	if offer.Endpoint != "https://managed-b.example.ts.net" {
		t.Fatalf("endpoint = %q", offer.Endpoint)
	}
	if offer.APIPort != 7777 || offer.TransportMode != startupconfig.NetworkModeTailscale {
		t.Fatalf("transport api_port=%d mode=%q", offer.APIPort, offer.TransportMode)
	}
	if len(offer.EndpointCandidates) == 0 {
		t.Fatalf("endpoint candidates empty")
	}
	if len(offer.RendezvousTransports) == 0 {
		t.Fatalf("rendezvous transports empty")
	}
	if offer.ExpiresAt-offer.CreatedAt != 120 {
		t.Fatalf("ttl = %d, want 120", offer.ExpiresAt-offer.CreatedAt)
	}
	if offer.Ceremony.Code == "" || len(offer.Ceremony.Code) != 6 {
		t.Fatalf("ceremony code = %q, want short code", offer.Ceremony.Code)
	}
	if !offer.Ceremony.VerificationOnly {
		t.Fatalf("ceremony verification_only = false")
	}
	if strings.Contains(strings.ToLower(offer.Ceremony.Description), "unlock") && !strings.Contains(strings.ToLower(offer.Ceremony.Description), "does not unlock") {
		t.Fatalf("ceremony description should be verification-only, got %q", offer.Ceremony.Description)
	}
	if deriveSwarmRemotePairingOfferCeremonyCode(offer) != offer.Ceremony.Code {
		t.Fatalf("ceremony code is not derived from offer transcript")
	}
}

func TestSwarmRemotePairingOfferCeremonyCodeChangesWithTranscript(t *testing.T) {
	now := time.Unix(1700000000, 0)
	cfg := startupconfig.Default(filepath.Join(t.TempDir(), "swarm.conf"))
	cfg.SwarmMode = true
	cfg.NetworkMode = startupconfig.NetworkModeTailscale
	cfg.SwarmName = "Managed Host B"
	cfg.TailscaleURL = "https://managed-b.example.ts.net"
	state := swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "managed-swarm-1", Name: "Managed Host B", PublicKey: "public-key", Fingerprint: "fingerprint"}}

	offer, err := buildSwarmRemotePairingOffer(cfg, onboardingResponse{}, state, 2*time.Minute, now)
	if err != nil {
		t.Fatalf("build offer: %v", err)
	}
	changed := offer
	changed.Token = strings.Repeat("a", 64)
	if deriveSwarmRemotePairingOfferCeremonyCode(changed) == offer.Ceremony.Code {
		t.Fatalf("ceremony code did not change after token changed")
	}
	changed = offer
	changed.Fingerprint = "other-fingerprint"
	if deriveSwarmRemotePairingOfferCeremonyCode(changed) == offer.Ceremony.Code {
		t.Fatalf("ceremony code did not change after identity changed")
	}
}

func TestSwarmRemotePairingOfferRejectsInvalidTTL(t *testing.T) {
	server := newLocalAuthTestServer(t)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.NetworkMode = startupconfig.NetworkModeTailscale
		cfg.TailscaleURL = "https://managed-b.example.ts.net"
	})

	rec := createRemotePairingOfferRecorderWithDesktopSession(t, server, `{"ttl_seconds":30}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short ttl status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "token") || strings.Contains(rec.Body.String(), "tskey-") {
		t.Fatalf("invalid ttl response leaked sensitive material: %s", rec.Body.String())
	}
}

func TestSwarmRemotePairingOfferRequiresReachableEndpoint(t *testing.T) {
	cfg := startupconfig.Default(filepath.Join(t.TempDir(), "swarm.conf"))
	cfg.SwarmMode = true
	cfg.NetworkMode = startupconfig.NetworkModeTailscale
	state := swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "managed-swarm-1", Name: "Managed Host B", PublicKey: "public-key", Fingerprint: "fingerprint"}}

	_, err := buildSwarmRemotePairingOffer(cfg, onboardingResponse{}, state, 2*time.Minute, time.Unix(1700000000, 0))
	if err == nil {
		t.Fatalf("build offer succeeded without reachable endpoint")
	}
	if strings.Contains(err.Error(), "public-key") || strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("missing endpoint error leaked identity material: %v", err)
	}
}

func createRemotePairingOfferWithDesktopSession(t *testing.T, server *Server, body string) swarmRemotePairingOfferResponse {
	t.Helper()
	rec := createRemotePairingOfferRecorderWithDesktopSession(t, server, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("remote pairing offer status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response swarmRemotePairingOfferResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode remote pairing offer: %v", err)
	}
	return response
}

func createRemotePairingOfferRecorderWithDesktopSession(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	token, expiresAt, err := server.desktopLocalSessions.Ensure(time.Now())
	if err != nil {
		t.Fatalf("ensure desktop local session: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5555/v1/swarm/remote-pairing/offer", strings.NewReader(body))
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
