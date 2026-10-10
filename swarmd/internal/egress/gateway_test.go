package egress

// Purpose: the egress gateway lets an agent use a secret without seeing it.
// These tests prove, against a real TLS upstream and a real proxy client, that
// the stand-in reaches the gateway but the real value reaches only an allowed
// host; that a value echoed back is scrubbed before the sandbox sees it; that
// a host with no grant is tunnelled unopened (no swap); that an unknown or
// absent proxy token is refused; and that the public-only guard blocks
// loopback, the tailnet and metadata. The gateway is the whole boundary for
// Phase 2, so it is tested end to end, not by unit stubs.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type staticResolver struct {
	token   string
	sandbox Sandbox
}

func (r staticResolver) ResolveSandbox(token string) (Sandbox, bool) {
	if token != "" && token == r.token {
		return r.sandbox, true
	}
	return Sandbox{}, false
}

type recordLogger struct {
	mu   sync.Mutex
	uses []string
}

func (l *recordLogger) LogUse(account, name, host, method, path, outcome string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.uses = append(l.uses, fmt.Sprintf("%s|%s|%s|%s|%s", name, host, method, path, outcome))
}

func (l *recordLogger) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.uses...)
}

func grant(name, placeholder, value string, hosts ...string) Grant {
	hs := map[string]struct{}{}
	for _, h := range hosts {
		hs[h] = struct{}{}
	}
	return Grant{Name: name, Placeholder: []byte(placeholder), Value: []byte(value), Hosts: hs}
}

// testUpstream starts an HTTPS server with a certificate for host and returns
// it with a cert pool that trusts it.
func testUpstream(t *testing.T, host string, handler http.HandlerFunc) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.Leaf(host)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{*leaf}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM())
	return srv, pool
}

// startGateway runs a gateway that reaches upstreamAddr for any intercepted or
// tunnelled host (so a loopback test server works behind the public guard) and
// trusts upstreamRoots for upstream verification. Returns gateway and proxy URL.
func startGateway(t *testing.T, resolver Resolver, logger UseLogger, upstreamAddr string, upstreamRoots *x509.CertPool) (*Gateway, string) {
	t.Helper()
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := NewGateway(ca, resolver, logger)
	g.upstreamRoots = upstreamRoots
	guard := g.dial
	g.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if upstreamAddr != "" {
			return (&net.Dialer{}).DialContext(ctx, network, upstreamAddr)
		}
		return guard(ctx, network, addr)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = g.Serve(ln) }()
	return g, ln.Addr().String()
}

func proxyClient(t *testing.T, g *Gateway, proxyAddr, token string) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(g.ca.CertPEM()) {
		t.Fatal("bad CA PEM")
	}
	proxyURL := &url.URL{Scheme: "http", Host: proxyAddr, User: url.UserPassword("swarm", token)}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}
}

// The response must also end where it ends: a body sent without framing on a
// kept-alive connection never ends for the client, so every call through the
// gateway hung until the client gave up. Each request here must complete
// within the client's timeout, a second request must reuse the connection,
// and bodyless responses (HEAD, 204) must come back well-formed. A value
// echoed in a response header is scrubbed too.
func TestGatewaySwapsSecretForAllowedHostAndScrubsResponse(t *testing.T) {
	const host = "api.secret.example"
	var mu sync.Mutex
	var gotAuth []string
	srv, roots := testUpstream(t, host, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("X-Echo", r.Header.Get("Authorization"))
		if r.URL.Path == "/empty" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		fmt.Fprintf(w, "you sent %s", r.Header.Get("Authorization")) // echo, to prove scrub
	})
	resolver := staticResolver{token: "tok-1", sandbox: Sandbox{
		Account: "acct", WorkspacePath: "/p",
		Grants: []Grant{grant("STRIPE_KEY", "swarm-secret://STRIPE_KEY", "sk_live_REAL", host)},
	}}
	logger := &recordLogger{}
	g, proxyAddr := startGateway(t, resolver, logger, srv.Listener.Addr().String(), roots)

	client := proxyClient(t, g, proxyAddr, "tok-1")
	client.Timeout = 3 * time.Second
	conns := map[string]struct{}{}
	call := func(method, path string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, "https://"+host+path, nil)
		req.Header.Set("Authorization", "Bearer swarm-secret://STRIPE_KEY")
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
			conns[info.Conn.LocalAddr().String()] = struct{}{}
		}}
		resp, err := client.Do(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("%s %s: reading the response did not finish: %v", method, path, err)
		}
		if echo := resp.Header.Get("X-Echo"); echo != "Bearer swarm-secret://STRIPE_KEY" {
			t.Fatalf("%s %s: response header not scrubbed to the stand-in: %q", method, path, echo)
		}
		return resp, string(body)
	}

	for i := 0; i < 2; i++ {
		_, body := call("GET", "/v1/charge")
		if strings.Contains(body, "sk_live_REAL") {
			t.Fatalf("real secret leaked back to the sandbox: %q", body)
		}
		if body != "you sent Bearer swarm-secret://STRIPE_KEY" {
			t.Fatalf("response not scrubbed to the stand-in: %q", body)
		}
	}
	if len(conns) != 1 {
		t.Fatalf("second request did not reuse the connection: %d connections", len(conns))
	}
	if resp, body := call("HEAD", "/v1/charge"); resp.StatusCode != http.StatusOK || body != "" {
		t.Fatalf("HEAD = %d %q", resp.StatusCode, body)
	}
	if resp, body := call("GET", "/empty"); resp.StatusCode != http.StatusNoContent || body != "" {
		t.Fatalf("204 = %d %q", resp.StatusCode, body)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, auth := range gotAuth {
		if auth != "Bearer sk_live_REAL" {
			t.Fatalf("upstream did not receive the real secret: %q", auth)
		}
	}
	if uses := logger.all(); len(uses) != 4 || !strings.Contains(uses[0], "STRIPE_KEY|"+host+"|GET|/v1/charge|injected") {
		t.Fatalf("uses not logged as injected: %v", uses)
	}
}

func TestGatewayRefusesWithoutValidToken(t *testing.T) {
	srv, roots := testUpstream(t, "api.secret.example", func(w http.ResponseWriter, r *http.Request) {})
	resolver := staticResolver{token: "good", sandbox: Sandbox{Account: "a"}}
	g, proxyAddr := startGateway(t, resolver, &recordLogger{}, srv.Listener.Addr().String(), roots)
	for _, tok := range []string{"", "wrong"} {
		_, err := proxyClient(t, g, proxyAddr, tok).Get("https://api.secret.example/")
		if err == nil {
			t.Fatalf("token %q was accepted", tok)
		}
	}
}

func TestGatewayGuardBlocksPrivateAndMetadata(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "169.254.169.254", "100.100.100.100", "10.0.0.1", "192.168.1.1", "::1", "fd00::1"} {
		if publicAddr(netip.MustParseAddr(addr)) {
			t.Fatalf("%s treated as public", addr)
		}
	}
	for _, addr := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicAddr(netip.MustParseAddr(addr)) {
			t.Fatalf("%s treated as non-public", addr)
		}
	}
}

func TestGatewayTunnelsHostWithoutGrantUnopened(t *testing.T) {
	const host = "nogrant.example"
	srv, roots := testUpstream(t, host, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "auth=%s", r.Header.Get("Authorization"))
	})
	resolver := staticResolver{token: "tok", sandbox: Sandbox{
		Account: "a", Grants: []Grant{grant("K", "stand-in", "REAL", "other.example")},
	}}
	logger := &recordLogger{}
	_, proxyAddr := startGateway(t, resolver, logger, srv.Listener.Addr().String(), roots)

	// The tunnel passes raw TLS bytes through; the client must trust the
	// upstream's own cert, not the gateway CA.
	proxyURL := &url.URL{Scheme: "http", Host: proxyAddr, User: url.UserPassword("swarm", "tok")}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}
	req, _ := http.NewRequest("GET", "https://"+host+"/", nil)
	req.Header.Set("Authorization", "stand-in")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "auth=stand-in" {
		t.Fatalf("expected untouched tunnel, got %q", body)
	}
	for _, u := range logger.all() {
		if strings.Contains(u, "injected") {
			t.Fatalf("a tunnelled host must not log an injection: %v", logger.all())
		}
	}
}
