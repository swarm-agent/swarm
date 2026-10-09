package egress

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// intercept terminates TLS from the sandbox for an allowed host, then proxies
// each request to the real upstream (verifying the upstream's own certificate)
// with the stand-in swapped for the secret, scrubbing the secret out of the
// response. The agent presents the gateway's leaf, which the sandbox trusts
// only because Swarm put the CA there for this granted sandbox.
func (g *Gateway) intercept(client net.Conn, sandbox Sandbox, host string, grants []Grant) {
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	leaf, err := g.ca.Leaf(host)
	if err != nil {
		return
	}
	tlsClient := tls.Server(client, &tls.Config{Certificates: []tls.Certificate{*leaf}, MinVersion: tls.VersionTLS12})
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	if err := tlsClient.Handshake(); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	defer tlsClient.Close()

	upstream := &http.Transport{
		DialContext:         g.dial,
		TLSClientConfig:     &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: g.upstreamRoots},
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        4,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	defer upstream.CloseIdleConnections()

	br := bufio.NewReader(tlsClient)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		keepAlive := g.serveOne(tlsClient, upstream, req, sandbox, host, grants)
		if !keepAlive {
			return
		}
	}
}

func (g *Gateway) serveOne(client *tls.Conn, upstream *http.Transport, req *http.Request, sandbox Sandbox, host string, grants []Grant) bool {
	// Build the pairs present in this request's headers or body so only the
	// secrets the agent actually used are swapped, and only those scrubbed.
	bodyBuf := &bytes.Buffer{}
	if req.Body != nil {
		_, _ = io.Copy(bodyBuf, io.LimitReader(req.Body, 32<<20))
		_ = req.Body.Close()
	}
	headerBlob := headerBytes(req)
	var swap, scrub [][2][]byte
	used := map[string]struct{}{}
	for _, grant := range grants {
		if bytes.Contains(headerBlob, grant.Placeholder) || bytes.Contains(bodyBuf.Bytes(), grant.Placeholder) {
			swap = append(swap, [2][]byte{grant.Placeholder, grant.Value})
			scrub = append(scrub, [2][]byte{grant.Value, grant.Placeholder})
			used[grant.Name] = struct{}{}
		}
	}

	swapHeaders(req, swap)
	outBody := replaceAll(bodyBuf.Bytes(), swap)

	outReq, err := http.NewRequest(req.Method, "https://"+host+req.URL.RequestURI(), bytes.NewReader(outBody))
	if err != nil {
		writeHTTPError(client, http.StatusBadRequest)
		return false
	}
	copyHeaders(outReq.Header, req.Header)
	outReq.Header.Del("Proxy-Authorization")
	outReq.ContentLength = int64(len(outBody))
	outReq.Host = host

	resp, err := upstream.RoundTrip(outReq)
	outcome := "injected"
	if len(used) == 0 {
		outcome = "passed_no_secret"
	}
	if err != nil {
		g.logEach(sandbox, used, host, req, "upstream_error")
		writeHTTPError(client, http.StatusBadGateway)
		return false
	}
	g.logEach(sandbox, used, host, req, outcome)

	resp.Body = newScrubReader(resp.Body, scrub)
	// Scrubbing can change the body length, so the original length is wrong;
	// send with unknown length (chunked) instead.
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	resp.TransferEncoding = nil
	keepAlive := !req.Close && resp.ProtoAtLeast(1, 1)
	if err := resp.Write(client); err != nil {
		return false
	}
	return keepAlive
}

func (g *Gateway) logEach(sandbox Sandbox, used map[string]struct{}, host string, req *http.Request, outcome string) {
	if len(used) == 0 {
		g.logger.LogUse(sandbox.Account, "", host, req.Method, req.URL.Path, outcome)
		return
	}
	for name := range used {
		g.logger.LogUse(sandbox.Account, name, host, req.Method, req.URL.Path, outcome)
	}
}

func swapHeaders(req *http.Request, pairs [][2][]byte) {
	if len(pairs) == 0 {
		return
	}
	for key, values := range req.Header {
		for i, v := range values {
			req.Header[key][i] = string(replaceAll([]byte(v), pairs))
		}
	}
}

func headerBytes(req *http.Request) []byte {
	var b bytes.Buffer
	_ = req.Header.Write(&b)
	return b.Bytes()
}

func copyHeaders(dst, src http.Header) {
	hopByHop := map[string]struct{}{
		"connection": {}, "proxy-connection": {}, "keep-alive": {}, "te": {},
		"trailer": {}, "transfer-encoding": {}, "upgrade": {}, "proxy-authorization": {},
	}
	for key, values := range src {
		if _, skip := hopByHop[strings.ToLower(key)]; skip {
			continue
		}
		for _, v := range values {
			dst.Add(key, v)
		}
	}
}

func writeHTTPError(client *tls.Conn, status int) {
	resp := &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Content-Length": {"0"}}, Body: io.NopCloser(bytes.NewReader(nil))}
	_ = resp.Write(client)
}

func decodeBasic(encoded string) (string, string, bool) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", "", false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	return user, pass, ok
}
