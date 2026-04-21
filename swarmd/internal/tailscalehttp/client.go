package tailscalehttp

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

const outboundProxyEnvKey = "SWARM_TAILSCALE_OUTBOUND_PROXY"

func ClientForEndpoint(endpoint string, base *http.Client) (*http.Client, error) {
	if base == nil {
		base = &http.Client{}
	}
	proxyAddr := strings.TrimSpace(os.Getenv(outboundProxyEnvKey))
	if proxyAddr == "" || !EndpointLooksLikeTailscale(endpoint) {
		return base, nil
	}
	proxyURL, err := url.Parse(proxyAddr)
	if err != nil {
		return nil, err
	}
	transport := cloneTransport(base.Transport)
	transport.Proxy = http.ProxyURL(proxyURL)
	cloned := *base
	cloned.Transport = transport
	return &cloned, nil
}

func EndpointLooksLikeTailscale(endpoint string) bool {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(parsed.Hostname())), ".")
	return strings.HasSuffix(host, ".ts.net")
}

func cloneTransport(base http.RoundTripper) *http.Transport {
	if typed, ok := base.(*http.Transport); ok && typed != nil {
		return typed.Clone()
	}
	return http.DefaultTransport.(*http.Transport).Clone()
}
