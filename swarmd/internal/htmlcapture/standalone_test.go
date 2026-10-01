package htmlcapture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
)

// Requirement: browser launches must not inherit daemon credentials or lose
// parent-death cleanup. Threat: allocator environment merging. Boundary:
// isolateBrowserCommand; command inspection is the narrow hermetic layer.
func TestStandaloneCommandEnvironment(t *testing.T) {
	t.Setenv("SWARM_TEST_PRIVATE_VALUE", "not-a-real-secret")
	cmd := &exec.Cmd{Env: []string{"SWARM_TEST_PRIVATE_VALUE=not-a-real-secret"}}
	isolateBrowserCommand(cmd)
	if cmd.Env == nil || len(cmd.Env) != 0 || cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatal("browser environment must be explicitly empty with parent-death cleanup")
	}
}

// Requirement: only allowlisted diagnostic codes escape CaptureStandalone.
// Threat: exception/URL leakage and repairing infrastructure faults. Boundary:
// standaloneDiagnostics/standaloneFailure; event injection tests this projection
// without claiming a browser run or CSP enforcement proof.
func TestStandaloneDiagnosticBoundary(t *testing.T) {
	for _, event := range []any{
		&cdpruntime.EventExceptionThrown{ExceptionDetails: &cdpruntime.ExceptionDetails{Text: "private authored exception"}},
		&log.EventEntryAdded{Entry: &log.Entry{Source: log.SourceSecurity, Text: "private URL"}},
		&network.EventLoadingFailed{BlockedReason: network.BlockedReasonCsp, ErrorText: "private URL"},
	} {
		var d standaloneDiagnostics
		d.observe(event)
		err := standaloneFailure(context.Background(), d.failure())
		var safe *StandaloneError
		if !errors.As(err, &safe) || safe.FailureClass != "content" || strings.Contains(err.Error(), "private") || errors.Unwrap(err) != nil {
			t.Fatalf("unsafe diagnostic: %v", err)
		}
	}
	for _, code := range []string{"capture_renderer_failed", "capture_renderer_unavailable", "capture_timeout", "authored-arbitrary-code"} {
		err := standaloneFailure(context.Background(), newErrorWithCause(code, "private", errors.New("private")))
		if err.(*StandaloneError).FailureClass != "infrastructure" || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "authored") || errors.Unwrap(err) != nil {
			t.Fatalf("unsafe infrastructure classification: %v", err)
		}
	}
	var d standaloneDiagnostics
	d.observe(&log.EventEntryAdded{Entry: &log.Entry{Source: log.SourceJavascript, Text: "console.error is not an exception"}})
	if d.failure() != nil { t.Fatal("console text must not become an exception") }
}

// Requirement: deny subsequent navigation, foreign URLs and non-GET requests.
// Threat: same-origin self-navigation bypass or credential exfiltration.
// Boundary: standaloneRequestAllowed; exact request decisions are hermetic.
func TestStandaloneRequestBoundary(t *testing.T) {
	origin := "http://127.0.0.1:12345/token"
	initial := false
	request := func(url, method string, kind network.ResourceType) bool {
		return standaloneRequestAllowed(&fetch.EventRequestPaused{Request: &network.Request{URL: url, Method: method}, ResourceType: kind}, origin, "index.html", "http://127.0.0.1:12345/favicon.ico", &initial)
	}
	if !request(origin+"/index.html", "GET", network.ResourceTypeDocument) { t.Fatal("initial document rejected") }
	for _, tc := range []struct{ url, method string; kind network.ResourceType }{
		{origin+"/index.html", "GET", network.ResourceTypeDocument},
		{origin+"/index.html", "POST", network.ResourceTypeFetch},
		{"http://127.0.0.1:12345/foreign/index.html", "GET", network.ResourceTypeScript},
		{"https://example.invalid/private", "GET", network.ResourceTypeImage},
		{"file:///etc/passwd", "GET", network.ResourceTypeDocument},
	} {
		if request(tc.url, tc.method, tc.kind) { t.Fatal("prohibited request allowed") }
	}
}

// Requirement: reject invalid source/viewport before launching a browser.
// Threat: oversized/binary input or unbounded output. Boundary: CaptureStandalone;
// nil renderer proves invalid input cannot reach browser allocation.
func TestStandaloneInputBoundary(t *testing.T) {
	var r *ChromedpRenderer
	for _, req := range []StandaloneRequest{{}, {HTML: []byte{0xff}}, {HTML: []byte{'a', 0}}, {HTML: bytes.Repeat([]byte{'a'}, MaxStandaloneHTMLBytes+1)}, {HTML: []byte("ok"), ViewportWidth: Width+1, ViewportHeight: Height}, {HTML: []byte("ok"), ViewportWidth: 100}} {
		result, err := r.CaptureStandalone(context.Background(), req)
		if err == nil || err.(*StandaloneError).Code != "standalone_input_invalid" || len(result.PNG) != 0 { t.Fatal("invalid input accepted") }
	}
	result, err := r.CaptureStandalone(context.Background(), StandaloneRequest{HTML: []byte("<body>ok</body>")})
	if err == nil || err.(*StandaloneError).FailureClass != "infrastructure" || len(result.PNG) != 0 { t.Fatal("missing browser not classified") }
}

// Requirement: preserve exact source bytes while enforcing a credential-free,
// opaque-origin sandbox. Threat: source rewriting or relaxed worker/frame CSP.
// Boundary: serveCaptureFiles; actual loopback HTTP is the narrowest header/body
// test. Browser enforcement itself is covered only by the opt-in Chrome test.
func TestStandaloneSourceServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := []byte("<!doctype html><body>unchanged</body>")
	origin, stop, err := serveCaptureFiles(ctx, map[string][]byte{"index.html": source}, true)
	if err != nil { t.Fatal(err) }
	defer stop()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Get(origin+"/index.html")
	if err != nil { t.Fatal(err) }
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !bytes.Equal(body, source) || len(response.Cookies()) != 0 { t.Fatal("source changed or credentials returned") }
	csp := response.Header.Get("Content-Security-Policy")
	for _, directive := range []string{"sandbox allow-scripts", "connect-src 'none'", "worker-src 'none'", "frame-src 'none'", "form-action 'none'"} {
		if !strings.Contains(csp, directive) { t.Fatalf("missing %s", directive) }
	}
	if strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "allow-popups") { t.Fatal("sandbox relaxed") }
	response, err = client.Get(origin+"/missing.html")
	if err != nil { t.Fatal(err) }
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound { t.Fatal("unknown source accessible") }
}

// Requirement: plain scrollable HTML captures without authored API; actual JS
// exceptions/rejections, CSP, workers, popups and navigation must not yield PNG.
// Threat: false ready results or source mutation. Boundary: CaptureStandalone;
// real sandboxed Chrome is necessary, explicitly opt-in and never simulated.
func TestStandaloneChrome(t *testing.T) {
	if os.Getenv("SWARM_HTMLCAPTURE_CHROME_TEST") != "1" { t.Skip("explicit real Chrome opt-in required") }
	r := NewChromedpRenderer(SystemChromePath, t.TempDir())
	for _, tc := range []struct{ name, body string; fail bool }{
		{"scrollable", `<style>body{height:3000px;background:#abc}</style><main>plain HTML</main>`, false},
		{"exception", `<script>throw new Error("private")</script>`, true},
		{"rejection", `<script>Promise.reject(new Error("private"))</script>`, true},
		{"network", `<script>fetch("https://example.invalid/private").catch(()=>{})</script>`, true},
		{"worker", `<script>try{new Worker("data:text/javascript,postMessage(1)")}catch(e){}</script>`, true},
		{"popup", `<script>window.open("https://example.invalid/private")</script>`, true},
		{"navigation", `<script>location.href="https://example.invalid/private"</script>`, true},
		{"image", `<img src="data:image/png;base64,invalid">`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			source := []byte("<!doctype html><html><head></head><body>"+tc.body+"</body></html>")
			before := bytes.Clone(source)
			result, err := r.CaptureStandalone(ctx, StandaloneRequest{HTML: source, ViewportWidth: 640, ViewportHeight: 480})
			if !bytes.Equal(source, before) { t.Fatal("source mutated") }
			if tc.fail {
				var safe *StandaloneError
				if !errors.As(err, &safe) || safe.FailureClass != "content" || len(result.PNG) != 0 || strings.Contains(err.Error(), "private") { t.Fatalf("expected safe content failure, got %v", err) }
				return
			}
			if err != nil { t.Fatal(err) }
			config, err := png.DecodeConfig(bytes.NewReader(result.PNG))
			digest := sha256.Sum256(source)
			if err != nil || config.Width != 640 || config.Height != 480 || result.SourceSHA256 != hex.EncodeToString(digest[:]) { t.Fatal("invalid revision-bound viewport evidence") }
		})
	}
}
