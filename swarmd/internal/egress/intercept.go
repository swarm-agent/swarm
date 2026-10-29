package egress

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
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
	//
	// A body is read into memory up to bodyRewriteLimit so a stand-in in it can
	// be swapped with an exact Content-Length. A longer body (a media upload)
	// is streamed to the upstream unchanged and never cut: a stand-in in it
	// cannot be swapped, so the request is refused rather than sent wrong.
	bodyBuf := &bytes.Buffer{}
	streamed := false
	if req.Body != nil {
		n, err := io.Copy(bodyBuf, io.LimitReader(req.Body, bodyRewriteLimit+1))
		if err != nil {
			_ = req.Body.Close()
			writeHTTPError(client, http.StatusBadRequest)
			return false
		}
		streamed = n > bodyRewriteLimit
		if !streamed {
			_ = req.Body.Close()
		} else {
			defer req.Body.Close()
		}
	}
	if streamed && containsAnyPlaceholder(bodyBuf.Bytes(), grants) {
		g.logEach(sandbox, nil, host, req, "refused:body_too_large")
		writeBodyTooLarge(client)
		return false
	}
	headerBlob := headerBytes(req)
	var swap, scrub [][2][]byte
	used := map[string]struct{}{}
	for _, grant := range grants {
		if bytes.Contains(headerBlob, grant.Placeholder) || (!streamed && bytes.Contains(bodyBuf.Bytes(), grant.Placeholder)) {
			swap = append(swap, [2][]byte{grant.Placeholder, grant.Value})
			scrub = append(scrub, [2][]byte{grant.Value, grant.Placeholder})
			used[grant.Name] = struct{}{}
		}
	}

	swapHeaders(req, swap)

	var outBody io.Reader
	var outLength int64
	var guard *standInGuard
	if streamed {
		// The buffered prefix is known clean; the guard checks the rest
		// (including a stand-in split across the boundary) as it streams.
		guard = newStandInGuard(bodyBuf.Bytes(), req.Body, grants)
		outBody = guard
		outLength = req.ContentLength // -1 (unknown) is sent chunked
	} else {
		b := replaceAll(bodyBuf.Bytes(), swap)
		outBody = bytes.NewReader(b)
		outLength = int64(len(b))
	}

	outReq, err := http.NewRequest(req.Method, "https://"+host+req.URL.RequestURI(), outBody)
	if err != nil {
		writeHTTPError(client, http.StatusBadRequest)
		return false
	}
	copyHeaders(outReq.Header, req.Header)
	outReq.Header.Del("Proxy-Authorization")
	outReq.ContentLength = outLength
	outReq.Host = host

	resp, err := upstream.RoundTrip(outReq)
	outcome := "injected"
	if len(used) == 0 {
		outcome = "passed_no_secret"
	}
	if guard != nil && guard.found() {
		// The upstream saw an aborted request, never a complete one.
		if err == nil {
			_ = resp.Body.Close()
		}
		g.logEach(sandbox, used, host, req, "refused:body_too_large")
		writeBodyTooLarge(client)
		return false
	}
	if err != nil {
		g.logEach(sandbox, used, host, req, "upstream_error")
		writeHTTPError(client, http.StatusBadGateway)
		return false
	}
	g.logEach(sandbox, used, host, req, outcome)

	// A site may echo the value back in a header as well as in the body.
	for key, values := range resp.Header {
		for i, v := range values {
			resp.Header[key][i] = string(replaceAll([]byte(v), scrub))
		}
	}
	if req.Method == http.MethodHead || !responseHasBody(resp.StatusCode) {
		_ = resp.Body.Close()
		resp.Body = http.NoBody
	} else {
		// Scrubbing can change the body length, so the original length is
		// wrong: send it chunked, which also ends the response without closing
		// the connection (an unframed body would end only when it closes).
		resp.Body = newScrubReader(resp.Body, scrub)
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		resp.TransferEncoding = []string{"chunked"}
	}
	// A streamed body may not have been read to its end (the upstream can
	// answer early), so the connection cannot be reused for another request.
	keepAlive := !req.Close && resp.ProtoAtLeast(1, 1) && !streamed
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

// responseHasBody reports whether a response with this status carries a body
// (RFC 9110: 1xx, 204 and 304 never do).
func responseHasBody(status int) bool {
	return status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified
}

// bodyRewriteLimit is the most of a request body the gateway holds in memory
// to swap a stand-in in it. Longer bodies are streamed, not cut.
const bodyRewriteLimit = 32 << 20

// errBodyTooLarge is what the caller reads when its body needs a stand-in
// swapped but is too long to hold in memory.
var errBodyTooLarge = fmt.Sprintf("request body over %d MiB cannot pass the secret gateway with a secret stand-in in it; send the stand-in in a header or a smaller body\n", bodyRewriteLimit>>20)

func writeBodyTooLarge(client *tls.Conn) {
	resp := &http.Response{
		StatusCode:    http.StatusRequestEntityTooLarge,
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"text/plain; charset=utf-8"}, "Connection": {"close"}},
		ContentLength: int64(len(errBodyTooLarge)),
		Body:          io.NopCloser(strings.NewReader(errBodyTooLarge)),
		Close:         true,
	}
	_ = resp.Write(client)
}

func containsAnyPlaceholder(data []byte, grants []Grant) bool {
	for _, grant := range grants {
		if len(grant.Placeholder) > 0 && bytes.Contains(data, grant.Placeholder) {
			return true
		}
	}
	return false
}

// standInGuard streams prefix then src, refusing to release any byte of a
// stand-in: it holds back enough bytes that one split across reads is still
// seen, and on finding one returns errStandInStreamed so the upstream request
// is aborted instead of completed.
type standInGuard struct {
	src     io.Reader
	grants  []Grant
	hold    int
	buf     []byte
	srcDone bool
	mu      sync.Mutex
	hit     bool
}

var errStandInStreamed = errors.New("secret stand-in in a request body too large to rewrite")

func newStandInGuard(prefix []byte, src io.Reader, grants []Grant) *standInGuard {
	hold := 0
	for _, grant := range grants {
		if len(grant.Placeholder)-1 > hold {
			hold = len(grant.Placeholder) - 1
		}
	}
	return &standInGuard{src: src, grants: grants, hold: hold, buf: prefix}
}

func (s *standInGuard) found() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hit
}

func (s *standInGuard) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if s.found() {
			return 0, errStandInStreamed
		}
		// Release what cannot be the start of a stand-in still arriving.
		ready := len(s.buf) - s.hold
		if s.srcDone {
			ready = len(s.buf)
		}
		if ready > 0 {
			n := copy(p, s.buf[:ready])
			s.buf = s.buf[n:]
			return n, nil
		}
		if s.srcDone {
			return 0, io.EOF
		}
		chunk := make([]byte, 256<<10)
		n, err := s.src.Read(chunk)
		if n > 0 {
			// Scan the held tail with the new bytes so a split one is seen.
			window := append(append(make([]byte, 0, len(s.buf)+n), s.buf...), chunk[:n]...)
			if containsAnyPlaceholder(window, s.grants) {
				s.mu.Lock()
				s.hit = true
				s.mu.Unlock()
				return 0, errStandInStreamed
			}
			s.buf = window
		}
		if err == io.EOF {
			s.srcDone = true
		} else if err != nil {
			return 0, err
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
