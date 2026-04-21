package tailscalehttp

import (
	"net/http"
	"testing"
	"time"
)

func TestClientForEndpointUsesConfiguredProxyForTailscaleHost(t *testing.T) {
	t.Setenv(outboundProxyEnvKey, "http://127.0.0.1:1055")
	base := &http.Client{Timeout: 15 * time.Second}
	client, err := ClientForEndpoint("https://host.tailnet.ts.net/v1/test", base)
	if err != nil {
		t.Fatalf("ClientForEndpoint() error = %v", err)
	}
	if client == base {
		t.Fatalf("expected proxied client clone, got original client")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected http transport clone, got %T", client.Transport)
	}
	req, err := http.NewRequest(http.MethodGet, "https://host.tailnet.ts.net/v1/test", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("proxy lookup: %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:1055" {
		t.Fatalf("proxy url = %v, want http://127.0.0.1:1055", proxyURL)
	}
}

func TestClientForEndpointSkipsProxyForNonTailscaleHost(t *testing.T) {
	t.Setenv(outboundProxyEnvKey, "http://127.0.0.1:1055")
	base := &http.Client{Timeout: 15 * time.Second}
	client, err := ClientForEndpoint("https://api.example.com/v1/test", base)
	if err != nil {
		t.Fatalf("ClientForEndpoint() error = %v", err)
	}
	if client != base {
		t.Fatalf("expected original client for non-tailscale endpoint")
	}
}
