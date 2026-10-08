// Package egress is the gateway sandboxes send outbound HTTP(S) through so a
// secret can be used without the agent seeing it. The agent's code sees only a
// stand-in; for a request to a website a granted secret allows, the gateway
// terminates TLS with its private CA, swaps the stand-in for the real value,
// scrubs the value out of the response, and logs the use. Everything else is
// tunnelled unopened to public addresses only. Swarm keeps the secret and the
// CA key; neither enters a sandbox.
package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Grant is one secret a sandbox may use, as the gateway needs it.
type Grant struct {
	Name        string // stand-in and slot name
	Placeholder []byte // what the agent's code sends
	Value       []byte // what goes to the allowed host
	Hosts       map[string]struct{}
	// ExpiresAtUnixMilli is when the grant stops injecting, even in a sandbox
	// still running. 0 means no expiry.
	ExpiresAtUnixMilli int64
}

// Sandbox is the set of secrets granted to one project's sandbox.
type Sandbox struct {
	Account       string
	WorkspacePath string
	Grants        []Grant
}

// Resolver supplies the current sandbox for a gateway token, or ok=false.
// Tokens are minted per sandbox when it starts and dropped when it stops.
type Resolver interface {
	ResolveSandbox(token string) (Sandbox, bool)
}

// UseLogger records one gateway use (never the value).
type UseLogger interface {
	LogUse(account, name, host, method, path, outcome string)
}

// Gateway is the egress proxy. Start it with Serve on a listener bound to the
// sandbox bridge address.
type Gateway struct {
	ca       *CA
	resolver Resolver
	logger   UseLogger

	mu       sync.Mutex
	inflight sync.WaitGroup

	// dial connects to a checked upstream. Production uses dialPublic (public
	// addresses only); tests override it to reach a loopback test server.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// upstreamRoots verifies upstream certificates; nil uses the system roots.
	// Tests set it to trust a test upstream's CA.
	upstreamRoots *x509.CertPool
}

func NewGateway(ca *CA, resolver Resolver, logger UseLogger) *Gateway {
	return &Gateway{ca: ca, resolver: resolver, logger: logger, dial: dialPublic}
}

// Serve accepts proxy connections until the listener is closed.
func (g *Gateway) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		g.inflight.Add(1)
		go func() {
			defer g.inflight.Done()
			g.handleConn(conn)
		}()
	}
}

func (g *Gateway) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	// The sandbox authenticates with its per-sandbox token via standard proxy
	// auth. Without a valid one the gateway is a closed door.
	sandbox, ok := g.resolver.ResolveSandbox(proxyToken(req))
	if !ok {
		writeProxyResponse(conn, http.StatusProxyAuthRequired, "Proxy-Authenticate: Basic realm=\"swarm\"\r\n")
		return
	}
	if req.Method != http.MethodConnect {
		// Plain-HTTP proxying is not offered: a secret would travel in clear
		// text, and agents should use https. Everything goes through CONNECT.
		writeProxyResponse(conn, http.StatusMethodNotAllowed, "")
		return
	}
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		writeProxyResponse(conn, http.StatusBadRequest, "")
		return
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))

	// If a granted secret names this host, open the tunnel to read and swap;
	// otherwise pass the bytes through to a public address, unopened.
	grants := grantsForHost(sandbox, host)
	if len(grants) == 0 || port != "443" {
		g.tunnel(conn, req.Host, sandbox, host)
		return
	}
	g.intercept(conn, sandbox, host, grants)
}

func grantsForHost(s Sandbox, host string) []Grant {
	now := nowMilli()
	var out []Grant
	for _, grant := range s.Grants {
		if grant.ExpiresAtUnixMilli != 0 && grant.ExpiresAtUnixMilli <= now {
			continue // expired: the secret is no longer injected
		}
		if _, ok := grant.Hosts[host]; ok {
			out = append(out, grant)
		}
	}
	return out
}

// nowMilli is overridable in tests.
var nowMilli = func() int64 { return time.Now().UnixMilli() }

// tunnel connects straight through to a public address without reading the
// stream. No secret can be injected, so nothing is logged as used.
func (g *Gateway) tunnel(client net.Conn, hostport string, sandbox Sandbox, host string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	upstream, err := g.dial(ctx, "tcp", hostport)
	cancel()
	if err != nil {
		g.logger.LogUse(sandbox.Account, "", host, "CONNECT", "", refusalOutcome(err))
		writeProxyResponse(client, http.StatusBadGateway, "")
		return
	}
	defer upstream.Close()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(upstream, client); closeWrite(upstream) }()
	go func() { defer wg.Done(); _, _ = io.Copy(client, upstream); closeWrite(client) }()
	wg.Wait()
}

func refusalOutcome(err error) string {
	if err == nil {
		return "upstream_error"
	}
	return "refused:" + err.Error()
}

func writeProxyResponse(conn net.Conn, status int, extraHeaders string) {
	fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\n%sContent-Length: 0\r\n\r\n", status, http.StatusText(status), extraHeaders)
}

func proxyToken(req *http.Request) string {
	const prefix = "Basic "
	h := req.Header.Get("Proxy-Authorization")
	if strings.HasPrefix(h, prefix) {
		if user, pass, ok := decodeBasic(h[len(prefix):]); ok {
			if user == "swarm" {
				return pass
			}
			return user
		}
	}
	return ""
}

func closeWrite(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

var _ = tls.VersionTLS12
