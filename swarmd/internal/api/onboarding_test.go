package api

import "testing"

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
