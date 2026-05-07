package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestSwarmRemoteCandidatesListsConnectedTailscaleDevices(t *testing.T) {
	server := newLocalAuthTestServer(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" && r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer remote.Close()
	remotePort := testServerPort(t, remote.URL)
	remotePortInt, err := strconv.Atoi(remotePort)
	if err != nil {
		t.Fatalf("parse remote port %q: %v", remotePort, err)
	}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Port = remotePortInt
		cfg.AdvertisePort = remotePortInt
	})
	server.remoteCandidateProbePorts = []int{remotePortInt}
	installFakeTailscaleStatus(t, `{
		"BackendState":"Running",
		"AuthURL":"https://login.tailscale.example/secret-token",
		"CurrentTailnet":{"Name":"example.ts.net"},
		"Self":{"DNSName":"manager.example.ts.net.","TailscaleIPs":["100.64.0.1"],"Online":true},
		"Peer":{
			"peer1":{"DNSName":"127.0.0.1.","OS":"linux","TailscaleIPs":["127.0.0.1"],"Online":true,"Active":false},
			"peer2":{"DNSName":"offline.example.ts.net.","OS":"windows","TailscaleIPs":["100.64.0.3"],"Online":false,"Active":false},
			"self":{"DNSName":"manager.example.ts.net.","OS":"linux","TailscaleIPs":["100.64.0.1"],"Online":true,"Self":true}
		}
	}`)

	status := getRemoteCandidatesWithDesktopSession(t, server)
	if !status.OK {
		t.Fatalf("ok = false: %+v", status)
	}
	if status.PathID != remoteCandidatesPathID {
		t.Fatalf("path_id = %q, want %q", status.PathID, remoteCandidatesPathID)
	}
	if !status.Tailscale.Available || !status.Tailscale.Connected {
		t.Fatalf("tailscale status = %+v, want available connected", status.Tailscale)
	}
	if status.Tailscale.TailnetName != "example.ts.net" {
		t.Fatalf("tailnet name = %q, want example.ts.net", status.Tailscale.TailnetName)
	}
	if status.Count != 1 || len(status.Candidates) != 1 {
		t.Fatalf("candidates count = %d len=%d payload=%+v, want one online peer", status.Count, len(status.Candidates), status.Candidates)
	}
	candidate := status.Candidates[0]
	if candidate.Name != "127" {
		t.Fatalf("candidate name = %q, want 127", candidate.Name)
	}
	if candidate.Source != startupconfig.NetworkModeTailscale || candidate.TransportMode != startupconfig.NetworkModeTailscale {
		t.Fatalf("candidate transport = source %q mode %q, want tailscale", candidate.Source, candidate.TransportMode)
	}
	if candidate.DNSName != "127.0.0.1" {
		t.Fatalf("candidate dns = %q", candidate.DNSName)
	}
	if candidate.TailnetURL != "https://127.0.0.1" || !strings.HasSuffix(candidate.Endpoint, ":"+remotePort) || !strings.HasPrefix(candidate.Endpoint, "http://") {
		t.Fatalf("candidate endpoints tailnet=%q endpoint=%q port=%s", candidate.TailnetURL, candidate.Endpoint, remotePort)
	}
	if candidate.OS != "linux" {
		t.Fatalf("candidate os = %q, want linux", candidate.OS)
	}
	if !candidate.Online {
		t.Fatalf("candidate online = false")
	}
	if len(candidate.IPs) != 1 || candidate.IPs[0] != "127.0.0.1" {
		t.Fatalf("candidate ips = %#v", candidate.IPs)
	}
	if len(candidate.EndpointCandidates) != 1 || candidate.EndpointCandidates[0].Port != remotePortInt {
		t.Fatalf("endpoint candidates = %#v, want only reachable API candidate on custom port", candidate.EndpointCandidates)
	}
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal candidates: %v", err)
	}
	bodyText := string(body)
	if strings.Contains(bodyText, "secret-token") || strings.Contains(bodyText, "AuthURL") || strings.Contains(bodyText, "auth_url") {
		t.Fatalf("candidate response leaked auth metadata: %s", bodyText)
	}
}

func TestSwarmRemoteCandidatesSkipsOnlinePeerWhenSwarmdUnreachable(t *testing.T) {
	server := newLocalAuthTestServer(t)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Port = 9
		cfg.AdvertisePort = 9
	})
	installFakeTailscaleStatus(t, `{
		"BackendState":"Running",
		"CurrentTailnet":{"Name":"example.ts.net"},
		"Self":{"DNSName":"manager.example.ts.net.","TailscaleIPs":["100.64.0.1"],"Online":true},
		"Peer":{
			"peer1":{"DNSName":"managed-one.example.ts.net.","OS":"linux","TailscaleIPs":["100.64.0.2"],"Online":true,"Active":false}
		}
	}`)

	status := getRemoteCandidatesWithDesktopSession(t, server)
	if status.Count != 0 || len(status.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want none when swarmd probe fails", status.Candidates)
	}
}

func TestSwarmRemoteCandidatesNoTailscaleCommand(t *testing.T) {
	server := newLocalAuthTestServer(t)
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("TAILSCALE_AUTH_URL", "https://login.tailscale.example/secret-token")

	status := getRemoteCandidatesWithDesktopSession(t, server)
	if !status.OK {
		t.Fatalf("ok = false: %+v", status)
	}
	if !status.Tailscale.Available {
		t.Fatalf("tailscale available = false, want true from auth url")
	}
	if status.Tailscale.Connected {
		t.Fatalf("tailscale connected = true, want false")
	}
	if status.Count != 0 || len(status.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want none", status.Candidates)
	}
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal candidates: %v", err)
	}
	if strings.Contains(string(body), "secret-token") {
		t.Fatalf("candidate response leaked auth url: %s", string(body))
	}
}

func TestSwarmRemoteCandidatesDisconnectedTailscale(t *testing.T) {
	server := newLocalAuthTestServer(t)
	installFakeTailscaleStatus(t, `not logged in; visit https://login.tailscale.example/secret-token`)

	status := getRemoteCandidatesWithDesktopSession(t, server)
	if !status.OK {
		t.Fatalf("ok = false: %+v", status)
	}
	if !status.Tailscale.Available {
		t.Fatalf("tailscale available = false, want true when command exists")
	}
	if status.Tailscale.Connected {
		t.Fatalf("tailscale connected = true, want false")
	}
	if status.Tailscale.Error != "tailscale status unavailable" {
		t.Fatalf("tailscale error = %q, want redacted unavailable message", status.Tailscale.Error)
	}
	if status.Count != 0 || len(status.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want none", status.Candidates)
	}
}

func testServerPort(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse test server url %q: %v", rawURL, err)
	}
	port := parsed.Port()
	if port == "" {
		t.Fatalf("test server url %q had no port", rawURL)
	}
	return port
}

func installFakeTailscaleStatus(t *testing.T, output string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	exitCode := "0"
	if !strings.HasPrefix(strings.TrimSpace(output), "{") {
		exitCode = "1"
	}
	script := "#!/bin/sh\ncat <<'EOF'\n" + output + "\nEOF\nexit " + exitCode + "\n"
	if err := os.WriteFile(filepath.Join(dir, "tailscale"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tailscale: %v", err)
	}
}

func getRemoteCandidatesWithDesktopSession(t *testing.T, server *Server) remoteCandidatesResponse {
	t.Helper()
	token, expiresAt, err := server.desktopLocalSessions.Ensure(time.Now())
	if err != nil {
		t.Fatalf("ensure desktop local session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v1/swarm/remote-candidates", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Origin", "http://127.0.0.1:5555")
	req.Header.Set("Referer", "http://127.0.0.1:5555/app")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(buildDesktopLocalSessionCookie(token, expiresAt, false))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("remote candidates status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var status remoteCandidatesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode remote candidates response: %v", err)
	}
	return status
}
