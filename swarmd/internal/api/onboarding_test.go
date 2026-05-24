package api

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestClassifyTailscaleServeMode(t *testing.T) {
	desktopProxy := "http://127.0.0.1:5555"
	apiProxy := "http://127.0.0.1:7781"
	peerProxy := "http://127.0.0.1:7791"

	tests := []struct {
		name     string
		proxy    string
		wantMode string
	}{
		{name: "desktop", proxy: desktopProxy, wantMode: "desktop"},
		{name: "api", proxy: apiProxy, wantMode: "api"},
		{name: "peer transport", proxy: peerProxy, wantMode: "peer_transport"},
		{name: "other", proxy: "http://127.0.0.1:9999", wantMode: "other"},
		{name: "empty", proxy: "", wantMode: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyTailscaleServeMode(tt.proxy, desktopProxy, apiProxy, peerProxy); got != tt.wantMode {
				t.Fatalf("classifyTailscaleServeMode(%q) = %q, want %q", tt.proxy, got, tt.wantMode)
			}
		})
	}
}

func TestTailscaleServeProxyTargetPrefersConfiguredHost(t *testing.T) {
	status := tailscaleServeStatusWire{
		Web: map[string]tailscaleServeWebStatusWire{
			"saved.tailnet.ts.net:443": {
				Handlers: map[string]tailscaleServeHandlerWire{
					"/": {Proxy: "http://127.0.0.1:5555"},
				},
			},
			"dns.tailnet.ts.net:443": {
				Handlers: map[string]tailscaleServeHandlerWire{
					"/": {Proxy: "http://127.0.0.1:7791"},
				},
			},
		},
	}

	got := tailscaleServeProxyTarget(status, "https://saved.tailnet.ts.net", "dns.tailnet.ts.net")
	if got != "http://127.0.0.1:5555" {
		t.Fatalf("tailscaleServeProxyTarget() = %q, want %q", got, "http://127.0.0.1:5555")
	}
}

func TestHTTPProxyTargetUsesConfiguredHost(t *testing.T) {
	got := httpProxyTarget("127.0.0.2", 5555)
	if got != "http://127.0.0.2:5555" {
		t.Fatalf("httpProxyTarget() = %q, want %q", got, "http://127.0.0.2:5555")
	}
}

func TestTailscaleServeCommandUsesDesktopPort(t *testing.T) {
	got := tailscaleServeCommand(startupconfig.FileConfig{Host: "127.0.0.1", DesktopPort: 5555})
	if got != "tailscale serve --bg http://127.0.0.1:5555" {
		t.Fatalf("tailscaleServeCommand() = %q", got)
	}
}

func TestRequireTailscaleServeReadyForPairingRejectsMissingServe(t *testing.T) {
	cfg := startupconfig.FileConfig{NetworkMode: startupconfig.NetworkModeTailscale, Host: "127.0.0.1", DesktopPort: 5555}
	err := requireTailscaleServeReadyForPairing(cfg, onboardingResponse{Tailscale: onboardingTailscalePayload{Serve: onboardingTailscaleServePayload{Command: tailscaleServeCommand(cfg)}}})
	if err == nil {
		t.Fatal("expected missing Tailscale Serve to reject pairing")
	}
	if !strings.Contains(err.Error(), "tailscale serve --bg http://127.0.0.1:5555") {
		t.Fatalf("error = %q, want serve command", err.Error())
	}
}

func TestRequireTailscaleServeReadyForPairingAcceptsDesktopServe(t *testing.T) {
	cfg := startupconfig.FileConfig{NetworkMode: startupconfig.NetworkModeTailscale, Host: "127.0.0.1", DesktopPort: 5555}
	serve := detectTailscaleServe(cfg, onboardingTailscalePayload{})
	serve.Configured = true
	serve.Ready = true
	serve.Mode = "desktop"
	serve.ProxyTarget = "http://127.0.0.1:5555"
	if err := requireTailscaleServeReadyForPairing(cfg, onboardingResponse{Tailscale: onboardingTailscalePayload{Available: true, Connected: true, Serve: serve}}); err != nil {
		t.Fatalf("ready Tailscale Serve rejected: %v", err)
	}
}

func TestDetectedCurrentSwarmStateTransportsSkipsTailscaleInLANMode(t *testing.T) {
	transports := detectedCurrentSwarmStateTransports(startupconfig.FileConfig{
		NetworkMode:   startupconfig.NetworkModeLAN,
		AdvertiseHost: "192.0.2.10",
		TailscaleURL:  "https://saved.tailnet.example",
	})

	if len(transports) != 1 {
		t.Fatalf("transports = %d, want 1: %#v", len(transports), transports)
	}
	if transports[0].Kind != startupconfig.NetworkModeLAN {
		t.Fatalf("transport kind = %q, want %q", transports[0].Kind, startupconfig.NetworkModeLAN)
	}
	if transports[0].Primary != "192.0.2.10" {
		t.Fatalf("transport primary = %q, want 192.0.2.10", transports[0].Primary)
	}
}

func TestDetectedTailscaleOnboardingTransportsUsesConfigOnly(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "tailscale-invoked")
	t.Setenv("FAKE_TAILSCALE_MARKER", marker)
	t.Setenv("PATH", dir)
	if err := os.WriteFile(filepath.Join(dir, "tailscale"), []byte("#!/bin/sh\nprintf invoked >> \"$FAKE_TAILSCALE_MARKER\"\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake tailscale: %v", err)
	}

	transports := detectedTailscaleOnboardingTransports(startupconfig.FileConfig{
		NetworkMode:  startupconfig.NetworkModeTailscale,
		TailscaleURL: "https://child.tailnet.example",
	})

	if len(transports) != 1 {
		t.Fatalf("transports = %d, want 1: %#v", len(transports), transports)
	}
	if transports[0].Kind != startupconfig.NetworkModeTailscale {
		t.Fatalf("transport kind = %q, want %q", transports[0].Kind, startupconfig.NetworkModeTailscale)
	}
	if transports[0].Primary != "https://child.tailnet.example" {
		t.Fatalf("transport primary = %q, want configured tailscale URL", transports[0].Primary)
	}
	if len(transports[0].All) != 1 || transports[0].All[0] != "https://child.tailnet.example" {
		t.Fatalf("transport all = %#v, want only configured tailscale URL", transports[0].All)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("tailscale command was invoked during config-only transport detection")
	}
}

func onboardingStringPtr(value string) *string {
	return &value
}

func TestOnboardingResponseDetectsTailscaleServeBeforeManagedHostingEnabled(t *testing.T) {
	dir := t.TempDir()
	writeFakeTailscale(t, dir, `
case "$1 $2 $3" in
  "status --json ")
    cat <<'JSON'
{"Self":{"DNSName":"standalone.tail2ff467.ts.net.","TailscaleIPs":["100.122.157.126"],"Online":true},"CurrentTailnet":{"Name":"example.ts.net"}}
JSON
    ;;
  "serve status --json")
    cat <<'JSON'
{"Web":{"standalone.tail2ff467.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:5555"}}}}}
JSON
    ;;
  *) exit 1 ;;
esac
`)

	server := newLocalAuthTestServer(t)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmName = "standalone"
		cfg.Child = false
		cfg.NetworkMode = startupconfig.NetworkModeLAN
		cfg.Host = "127.0.0.1"
		cfg.Port = 7781
		cfg.DesktopPort = 5555
	})

	status, err := server.onboardingResponse(true)
	if err != nil {
		t.Fatalf("onboardingResponse returned error: %v", err)
	}
	if status.Tailscale.TailnetURL != "https://standalone.tail2ff467.ts.net" {
		t.Fatalf("tailnet url = %q, want detected live URL", status.Tailscale.TailnetURL)
	}
	if !status.Tailscale.Serve.Ready || status.Tailscale.Serve.Mode != "desktop" {
		t.Fatalf("serve status = %#v, want ready desktop before managed hosting is enabled", status.Tailscale.Serve)
	}
}

func TestConfiguredOnboardingResponseRefreshesTailscaleStatus(t *testing.T) {
	dir := t.TempDir()
	writeFakeTailscale(t, dir, `
case "$1 $2 $3" in
  "status --json ")
    cat <<'JSON'
{"Self":{"DNSName":"roy.tail2ff467.ts.net.","TailscaleIPs":["100.122.157.125"],"Online":true},"CurrentTailnet":{"Name":"roycohen1@gmail.com"}}
JSON
    ;;
  "serve status --json")
    cat <<'JSON'
{"Web":{"roy.tail2ff467.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:25606"}}}}}
JSON
    ;;
  *) exit 1 ;;
esac
`)

	server := newLocalAuthTestServer(t)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmName = "test69"
		cfg.Child = true
		cfg.NetworkMode = startupconfig.NetworkModeTailscale
		cfg.Host = "127.0.0.1"
		cfg.Port = 20606
		cfg.DesktopPort = 25606
		cfg.AdvertiseHost = "127.0.0.1"
		cfg.AdvertisePort = 20606
		cfg.TailscaleURL = "https://roy.tail2ff467.ts.net"
		cfg.BypassPermissions = true
		cfg.PeerTransportPort = 30606
	})

	status, err := server.onboardingResponse(true)
	if err != nil {
		t.Fatalf("onboardingResponse returned error: %v", err)
	}
	if status.NeedsOnboarding {
		t.Fatalf("needs_onboarding = true, want false")
	}
	if status.Config.SwarmName != "test69" || !status.Config.Child {
		t.Fatalf("config identity = %#v, want configured child", status.Config)
	}
	if !status.Tailscale.Connected {
		t.Fatalf("tailscale connected = false, want true: %#v", status.Tailscale)
	}
	if status.Tailscale.TailnetURL != "https://roy.tail2ff467.ts.net" {
		t.Fatalf("tailnet url = %q, want configured URL", status.Tailscale.TailnetURL)
	}
	if status.Tailscale.DNSName != "roy.tail2ff467.ts.net" {
		t.Fatalf("dns name = %q, want live DNS name", status.Tailscale.DNSName)
	}
	if !status.Tailscale.Serve.Ready || status.Tailscale.Serve.Mode != "desktop" {
		t.Fatalf("serve status = %#v, want ready desktop", status.Tailscale.Serve)
	}
	if status.Tailscale.Serve.ExpectedDesktopProxy != "http://127.0.0.1:25606" {
		t.Fatalf("desktop proxy = %q, want configured desktop proxy", status.Tailscale.Serve.ExpectedDesktopProxy)
	}
}

func TestLANAddressFilteringExcludesTailscaleAndBridgeNetworks(t *testing.T) {
	interfaces := []lanInterfaceAddrs{
		{Interface: net.Interface{Name: "eth0", Flags: net.FlagUp}, Addrs: []net.Addr{testIPAddr("10.0.0.1")}},
		{Interface: net.Interface{Name: "tailscale0", Flags: net.FlagUp}, Addrs: []net.Addr{testIPAddr("100.116.139.106")}},
		{Interface: net.Interface{Name: "docker0", Flags: net.FlagUp}, Addrs: []net.Addr{testIPAddr("172.17.0.1")}},
		{Interface: net.Interface{Name: "br-test", Flags: net.FlagUp}, Addrs: []net.Addr{testIPAddr("172.18.0.1")}},
		{Interface: net.Interface{Name: "wlan0", Flags: net.FlagUp}, Addrs: []net.Addr{testIPAddr("192.168.1.10")}},
	}

	got := lanAddressesFromInterfaces(interfaces)
	want := []string{"10.0.0.1", "192.168.1.10"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lan addresses = %#v, want %#v", got, want)
	}
}

func testIPAddr(value string) net.Addr {
	return &net.IPNet{IP: net.ParseIP(value), Mask: net.CIDRMask(24, 32)}
}

func writeFakeTailscale(t *testing.T, dir, script string) {
	t.Helper()
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", dir, os.PathListSeparator, os.Getenv("PATH")))
	if err := os.WriteFile(filepath.Join(dir, "tailscale"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("write fake tailscale: %v", err)
	}
}

func TestHTTPClientForTailscaleOutboundProxyUsesConfiguredProxy(t *testing.T) {
	t.Setenv("SWARM_TAILSCALE_OUTBOUND_PROXY", "http://127.0.0.1:1055")

	client, err := httpClientForTailscaleOutboundProxy("https://dev-hel1.tail617a4d.ts.net", []onboardingTransportPayload{{
		Kind: startupconfig.NetworkModeTailscale,
	}})
	if err != nil {
		t.Fatalf("httpClientForTailscaleOutboundProxy returned error: %v", err)
	}
	if client == nil {
		t.Fatal("expected proxy client")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	req, err := http.NewRequest(http.MethodGet, "https://dev-hel1.tail617a4d.ts.net/readyz", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("proxy lookup: %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:1055" {
		t.Fatalf("proxy url = %#v, want http://127.0.0.1:1055", proxyURL)
	}
}

func TestHTTPClientForTailscaleOutboundProxySkipsNonTailscaleEndpoints(t *testing.T) {
	t.Setenv("SWARM_TAILSCALE_OUTBOUND_PROXY", "http://127.0.0.1:1055")

	client, err := httpClientForTailscaleOutboundProxy("https://api.openai.com", nil)
	if err != nil {
		t.Fatalf("httpClientForTailscaleOutboundProxy returned error: %v", err)
	}
	if client != nil {
		t.Fatalf("expected no proxy client for non-tailscale endpoint, got %#v", client)
	}
}
