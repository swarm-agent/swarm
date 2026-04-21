package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestOnboardingRedactsSensitiveMetadataWithoutAuth(t *testing.T) {
	server := newLocalAuthTestServer(t)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmName = "Redacted Swarm"
		cfg.Host = "0.0.0.0"
		cfg.Port = 7777
		cfg.DesktopPort = 5555
		cfg.AdvertiseHost = "192.168.1.55"
		cfg.AdvertisePort = 7777
		cfg.TailscaleURL = "https://example.tailnet.ts.net"
		cfg.PeerTransportPort = 7791
	})

	req := httptest.NewRequest(http.MethodGet, "http://198.51.100.20/v1/onboarding", nil)
	req.RemoteAddr = "198.51.100.20:7777"
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("onboarding status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var status onboardingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode onboarding response: %v", err)
	}
	if status.Config.Host != "" {
		t.Fatalf("expected host to be redacted, got %q", status.Config.Host)
	}
	if status.Config.AdvertiseHost != "" {
		t.Fatalf("expected advertise_host to be redacted, got %q", status.Config.AdvertiseHost)
	}
	if status.Config.AdvertisePort != 0 {
		t.Fatalf("expected advertise_port to be redacted, got %d", status.Config.AdvertisePort)
	}
	if status.Config.TailscaleURL != "" {
		t.Fatalf("expected tailscale_url to be redacted, got %q", status.Config.TailscaleURL)
	}
	if status.Config.PeerTransportPort != 0 {
		t.Fatalf("expected peer_transport_port to be redacted, got %d", status.Config.PeerTransportPort)
	}
	if status.Tailscale.DNSName != "" || status.Tailscale.TailnetURL != "" || status.Tailscale.AuthURL != "" || len(status.Tailscale.IPs) != 0 || status.Tailscale.CandidateURL != "" || status.Tailscale.TailnetName != "" {
		t.Fatalf("expected tailscale metadata to be redacted, got %+v", status.Tailscale)
	}
	if len(status.DiscoveredSwarms) != 0 {
		t.Fatalf("expected discovered swarms to be omitted, got %d", len(status.DiscoveredSwarms))
	}
}

func TestOnboardingAllowsSensitiveMetadataWithDesktopSession(t *testing.T) {
	server := newLocalAuthTestServer(t)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmName = "Visible Swarm"
		cfg.Host = "0.0.0.0"
		cfg.Port = 7777
		cfg.DesktopPort = 5555
		cfg.AdvertiseHost = "192.168.1.55"
		cfg.AdvertisePort = 7777
		cfg.TailscaleURL = "https://example.tailnet.ts.net"
		cfg.PeerTransportPort = 7791
	})

	token, expiresAt, err := server.desktopLocalSessions.Ensure(time.Now())
	if err != nil {
		t.Fatalf("ensure desktop local session: %v", err)
	}
	cookie := buildDesktopLocalSessionCookie(token, expiresAt, false)

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v1/onboarding", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Origin", "http://127.0.0.1:5555")
	req.Header.Set("Referer", "http://127.0.0.1:5555/app")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("onboarding status with session = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var status onboardingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode onboarding response: %v", err)
	}
	if status.Config.Host != "0.0.0.0" {
		t.Fatalf("expected host to remain visible, got %q", status.Config.Host)
	}
	if status.Config.AdvertiseHost != "192.168.1.55" {
		t.Fatalf("expected advertise_host to remain visible, got %q", status.Config.AdvertiseHost)
	}
	if status.Config.AdvertisePort != 7777 {
		t.Fatalf("expected advertise_port to remain visible, got %d", status.Config.AdvertisePort)
	}
	if status.Config.TailscaleURL != "https://example.tailnet.ts.net" {
		t.Fatalf("expected tailscale_url to remain visible, got %q", status.Config.TailscaleURL)
	}
	if status.Config.PeerTransportPort != 7791 {
		t.Fatalf("expected peer_transport_port to remain visible, got %d", status.Config.PeerTransportPort)
	}
}

func TestSwarmDiscoveryRedactsSensitiveMetadataWithoutAuth(t *testing.T) {
	server := newLocalAuthTestServer(t)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.Child = false
		cfg.NetworkMode = startupconfig.NetworkModeTailscale
		cfg.SwarmName = "Discovery Swarm"
		cfg.TailscaleURL = "https://example.tailnet.ts.net"
	})

	req := httptest.NewRequest(http.MethodGet, "http://198.51.100.20/v1/swarm/discovery", nil)
	req.RemoteAddr = "198.51.100.20:7777"
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("discovery status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var status swarmDiscoveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode discovery response: %v", err)
	}
	if !status.OK {
		t.Fatalf("expected ok discovery response, got %+v", status)
	}
	if status.SwarmID != "" || status.Name != "" || status.Role != "" || status.Endpoint != "" || status.TransportMode != "" || len(status.RendezvousTransports) != 0 {
		t.Fatalf("expected discovery metadata to be redacted, got %+v", status)
	}
}

func TestSwarmDiscoveryAllowsSensitiveMetadataWithDesktopSession(t *testing.T) {
	server := newLocalAuthTestServer(t)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.Child = false
		cfg.NetworkMode = startupconfig.NetworkModeTailscale
		cfg.SwarmName = "Discovery Swarm"
		cfg.TailscaleURL = "https://example.tailnet.ts.net"
	})

	token, expiresAt, err := server.desktopLocalSessions.Ensure(time.Now())
	if err != nil {
		t.Fatalf("ensure desktop local session: %v", err)
	}
	cookie := buildDesktopLocalSessionCookie(token, expiresAt, false)

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v1/swarm/discovery", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Origin", "http://127.0.0.1:5555")
	req.Header.Set("Referer", "http://127.0.0.1:5555/app")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("discovery status with session = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var status swarmDiscoveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode discovery response: %v", err)
	}
	if status.SwarmID == "" {
		t.Fatalf("expected swarm_id to remain visible, got %+v", status)
	}
	if status.Name != "Discovery Swarm" {
		t.Fatalf("expected name to remain visible, got %q", status.Name)
	}
	if status.Role != bootstrapRoleMaster {
		t.Fatalf("expected role to remain visible, got %q", status.Role)
	}
	if status.Endpoint != "https://example.tailnet.ts.net" {
		t.Fatalf("expected endpoint to remain visible, got %q", status.Endpoint)
	}
	if status.TransportMode != startupconfig.NetworkModeTailscale {
		t.Fatalf("expected transport mode to remain visible, got %q", status.TransportMode)
	}
	if len(status.RendezvousTransports) == 0 {
		t.Fatalf("expected rendezvous transports to remain visible, got %+v", status)
	}
}
