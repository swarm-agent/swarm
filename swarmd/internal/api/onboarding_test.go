package api

import (
	"net/http"
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
