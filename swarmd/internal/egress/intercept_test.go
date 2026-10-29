package egress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Purpose: the secret gateway (Gateway.intercept / serveOne) must never
// forward a request body that differs from what the sandbox sent, except for
// swapping a granted stand-in. Regression: bodies over 32 MiB were cut at
// exactly 33,554,432 bytes and sent upstream as complete (corrupt video
// uploads). These tests drive a real gateway listener, a real TLS upstream
// and a real Go HTTP client through CONNECT, because the defect lives in how
// serveOne reads and frames the body; a unit test of a helper could not show
// what the upstream receives.

const (
	testHost        = "example.com" // in httptest's certificate
	testPlaceholder = "swarm-secret://API_TOKEN"
	testSecret      = "real-secret-value-0123456789"
)

type upstreamRecord struct {
	auth     string
	length   int
	sum      [32]byte
	body     []byte // kept only for small bodies
	declared int64
}

type gatewayFixture struct {
	client *http.Client

	mu        sync.Mutex
	received  []upstreamRecord // complete bodies only
	started   int              // requests whose headers reached the upstream
	outcomes  []string
	echoValue bool
}

type fixedResolver struct{ sandbox Sandbox }

func (r fixedResolver) ResolveSandbox(token string) (Sandbox, bool) {
	return r.sandbox, token == "tok"
}

type recordLogger struct{ f *gatewayFixture }

func (l recordLogger) LogUse(_, _, _, _, _, outcome string) {
	l.f.mu.Lock()
	l.f.outcomes = append(l.f.outcomes, outcome)
	l.f.mu.Unlock()
}

func newGatewayFixture(t *testing.T) *gatewayFixture {
	t.Helper()
	f := &gatewayFixture{}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.started++
		f.mu.Unlock()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "incomplete body", http.StatusBadRequest)
			return
		}
		rec := upstreamRecord{auth: r.Header.Get("Authorization"), length: len(body), sum: sha256.Sum256(body), declared: r.ContentLength}
		if len(body) < 1<<20 {
			rec.body = body
		}
		f.mu.Lock()
		f.received = append(f.received, rec)
		f.mu.Unlock()
		// Echo the credential so response scrubbing is observable.
		_, _ = io.WriteString(w, "ok "+rec.auth)
	}))
	upstream.StartTLS()
	t.Cleanup(upstream.Close)

	ca, err := LoadOrCreateCA(filepath.Join(t.TempDir(), "egress"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox := Sandbox{Account: "acct", Grants: []Grant{{
		Name:        "API_TOKEN",
		Placeholder: []byte(testPlaceholder),
		Value:       []byte(testSecret),
		Hosts:       map[string]struct{}{testHost: {}},
	}}}
	gw := NewGateway(ca, fixedResolver{sandbox: sandbox}, recordLogger{f: f})
	upstreamAddr := upstream.Listener.Addr().String()
	gw.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, upstreamAddr)
	}
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	gw.upstreamRoots = roots

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gw.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })

	sandboxRoots := x509.NewCertPool()
	if !sandboxRoots.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("gateway CA did not parse")
	}
	proxyURL := &url.URL{Scheme: "http", User: url.UserPassword("swarm", "tok"), Host: ln.Addr().String()}
	f.client = &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: sandboxRoots, MinVersion: tls.VersionTLS12},
		},
	}
	t.Cleanup(f.client.CloseIdleConnections)
	return f
}

// bigBody is deterministic, never contains the stand-in, and is not a
// repeating pattern aligned to 32 MiB, so a cut or shifted body changes the sum.
func bigBody(size int) []byte {
	b := make([]byte, size)
	x := uint32(2463534242)
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}

// onlyReader hides the concrete type so the client cannot learn the length
// and sends the body chunked.
type onlyReader struct{ r io.Reader }

func (o onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) }

func (f *gatewayFixture) post(t *testing.T, body []byte, chunked bool) (int, string) {
	t.Helper()
	var reader io.Reader = bytes.NewReader(body)
	if chunked {
		reader = onlyReader{r: bytes.NewReader(body)}
	}
	req, err := http.NewRequest(http.MethodPost, "https://"+testHost+"/upload", reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testPlaceholder)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("request through gateway: %v", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, string(out)
}

func (f *gatewayFixture) snapshot() ([]upstreamRecord, int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]upstreamRecord(nil), f.received...), f.started, append([]string(nil), f.outcomes...)
}

// TestGatewayStreamsLargeBodyByteIdentical: a 40 MiB upload (over the old
// 32 MiB cut) arrives upstream complete and byte-identical, with Content-Length
// kept when the client sent one and chunked framing when it did not, while the
// stand-in in the Authorization header is still swapped for the secret.
func TestGatewayStreamsLargeBodyByteIdentical(t *testing.T) {
	body := bigBody(40 << 20)
	want := sha256.Sum256(body)
	for _, chunked := range []bool{false, true} {
		name := "content-length"
		if chunked {
			name = "chunked"
		}
		t.Run(name, func(t *testing.T) {
			f := newGatewayFixture(t)
			status, resp := f.post(t, body, chunked)
			if status != http.StatusOK {
				t.Fatalf("status = %d (%q), want 200", status, resp)
			}
			got, _, outcomes := f.snapshot()
			if len(got) != 1 {
				t.Fatalf("upstream received %d complete bodies, want 1", len(got))
			}
			if got[0].length != len(body) || got[0].sum != want {
				t.Fatalf("upstream body length %d (sum match %v), want %d byte-identical", got[0].length, got[0].sum == want, len(body))
			}
			if !chunked && got[0].declared != int64(len(body)) {
				t.Fatalf("upstream Content-Length = %d, want %d", got[0].declared, len(body))
			}
			if got[0].auth != "Bearer "+testSecret {
				t.Fatalf("upstream Authorization = %q, want the secret swapped in", got[0].auth)
			}
			if strings.Contains(resp, testSecret) || !strings.Contains(resp, testPlaceholder) {
				t.Fatalf("response %q: secret not scrubbed back to the stand-in", resp)
			}
			if len(outcomes) != 1 || outcomes[0] != "injected" {
				t.Fatalf("logged outcomes %v, want [injected]", outcomes)
			}
		})
	}
}

// TestGatewaySwapsStandInInSmallBody: the buffered path still swaps the
// stand-in in headers and body, with the body's new length, and scrubs the
// echoed value from the response.
func TestGatewaySwapsStandInInSmallBody(t *testing.T) {
	f := newGatewayFixture(t)
	body := []byte(`{"token":"` + testPlaceholder + `","n":1}`)
	status, resp := f.post(t, body, false)
	if status != http.StatusOK {
		t.Fatalf("status = %d (%q), want 200", status, resp)
	}
	got, _, _ := f.snapshot()
	if len(got) != 1 {
		t.Fatalf("upstream received %d bodies, want 1", len(got))
	}
	wantBody := `{"token":"` + testSecret + `","n":1}`
	if string(got[0].body) != wantBody || got[0].declared != int64(len(wantBody)) {
		t.Fatalf("upstream body %q (Content-Length %d), want %q", got[0].body, got[0].declared, wantBody)
	}
	if got[0].auth != "Bearer "+testSecret {
		t.Fatalf("upstream Authorization = %q, want the secret", got[0].auth)
	}
	if strings.Contains(resp, testSecret) {
		t.Fatalf("response leaks the secret: %q", resp)
	}
}

// TestGatewayRefusesLargeBodyWithStandIn: a body too long to rewrite that
// contains a stand-in (in the buffered part, split across the 32 MiB boundary,
// or past it) is refused with an explicit 413, and the upstream never
// receives a complete body: not the stand-in, not a prefix sent as complete.
func TestGatewayRefusesLargeBodyWithStandIn(t *testing.T) {
	cases := map[string]int{
		"in-buffered-prefix": 1 << 20,
		"across-boundary":    bodyRewriteLimit - 5,
		"past-limit":         36 << 20,
	}
	for name, at := range cases {
		for _, chunked := range []bool{false, true} {
			sub := name + "/content-length"
			if chunked {
				sub = name + "/chunked"
			}
			t.Run(sub, func(t *testing.T) {
				f := newGatewayFixture(t)
				body := bigBody(40 << 20)
				copy(body[at:], testPlaceholder)
				status, resp := f.post(t, body, chunked)
				if status != http.StatusRequestEntityTooLarge {
					t.Fatalf("status = %d, want 413", status)
				}
				if !strings.Contains(resp, "request body over 32 MiB cannot pass the secret gateway") {
					t.Fatalf("413 body %q does not explain the limit", resp)
				}
				got, started, outcomes := f.snapshot()
				if len(got) != 0 {
					t.Fatalf("upstream received %d complete bodies (first %d bytes), want none", len(got), got[0].length)
				}
				if at < bodyRewriteLimit-len(testPlaceholder) && started != 0 {
					t.Fatalf("a stand-in in the buffered part must refuse before contacting the upstream; %d requests reached it", started)
				}
				if len(outcomes) != 1 || outcomes[0] != "refused:body_too_large" {
					t.Fatalf("logged outcomes %v, want [refused:body_too_large]", outcomes)
				}
			})
		}
	}
}

// TestStandInGuardSplitReads: the guard sees a stand-in split across source
// reads one byte at a time, and passes clean data through unchanged.
func TestStandInGuardSplitReads(t *testing.T) {
	grants := []Grant{{Placeholder: []byte(testPlaceholder)}}
	clean := bigBody(1 << 16)
	out, err := io.ReadAll(newStandInGuard(clean[:100], &oneByteReader{data: clean[100:]}, grants))
	if err != nil || !bytes.Equal(out, clean) {
		t.Fatalf("clean data changed: err=%v equal=%v", err, bytes.Equal(out, clean))
	}
	dirty := append(append([]byte("abc"), testPlaceholder...), "xyz"...)
	g := newStandInGuard(dirty[:10], &oneByteReader{data: dirty[10:]}, grants)
	out, err = io.ReadAll(g)
	if err != errStandInStreamed || !g.found() {
		t.Fatalf("err = %v found = %v, want errStandInStreamed", err, g.found())
	}
	if bytes.Contains(out, []byte(testPlaceholder)) {
		t.Fatalf("guard released the whole stand-in: %q", out)
	}
}

type oneByteReader struct{ data []byte }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}
