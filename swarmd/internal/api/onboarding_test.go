package api

import (
	"net/http"
	"os"
	"path/filepath"
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

func TestConfiguredOnboardingResponseUsesConfigOnlyForTailscale(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "tailscale-invoked")
	t.Setenv("FAKE_TAILSCALE_MARKER", marker)
	t.Setenv("PATH", dir)
	if err := os.WriteFile(filepath.Join(dir, "tailscale"), []byte("#!/bin/sh\nprintf invoked >> \"$FAKE_TAILSCALE_MARKER\"\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake tailscale: %v", err)
	}

	server := newLocalAuthTestServer(t)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmName = "test69"
		cfg.SwarmMode = true
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
	if status.Tailscale.Error != "" {
		t.Fatalf("tailscale error = %q, want empty config-only response", status.Tailscale.Error)
	}
	if status.Tailscale.TailnetURL != "https://roy.tail2ff467.ts.net" {
		t.Fatalf("tailnet url = %q, want configured URL", status.Tailscale.TailnetURL)
	}
	if status.Tailscale.Serve.Error != "" {
		t.Fatalf("tailscale serve error = %q, want empty config-only response", status.Tailscale.Serve.Error)
	}
	if status.Tailscale.Serve.ExpectedDesktopProxy != "http://127.0.0.1:25606" {
		t.Fatalf("desktop proxy = %q, want configured desktop proxy", status.Tailscale.Serve.ExpectedDesktopProxy)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("tailscale command was invoked during configured onboarding response")
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
