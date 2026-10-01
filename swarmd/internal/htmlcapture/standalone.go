package htmlcapture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image/png"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const MaxStandaloneHTMLBytes = 4 << 20

// StandaloneRequest is a trusted daemon API, not an authored render contract.
// Only one self-contained UTF-8 document is served, unchanged.
type StandaloneRequest struct {
	HTML           []byte
	ViewportWidth  int
	ViewportHeight int
}

type StandaloneResult struct {
	PNG          []byte
	SourceSHA256 string
}

// StandaloneError contains only fixed codes, with no raw browser cause chain.
// Only FailureClass == "content" is eligible for generated-content repair.
type StandaloneError struct {
	Code         string
	FailureClass string
}

func (e *StandaloneError) Error() string                 { return e.Code }
func (e *StandaloneError) SafeDiagnosticCode() string    { return e.Code }
func (e *StandaloneError) SafeDiagnosticMessage() string { return e.Code }

// CaptureStandalone checks bounded load/runtime readiness and captures the visible
// viewport. Scrollable content is allowed. Success is NOT aesthetic verification,
// an exhaustive interaction test, or evidence about timers firing after capture.
func (r *ChromedpRenderer) CaptureStandalone(ctx context.Context, req StandaloneRequest) (StandaloneResult, error) {
	if len(req.HTML) == 0 || len(req.HTML) > MaxStandaloneHTMLBytes || !utf8.Valid(req.HTML) || bytes.IndexByte(req.HTML, 0) >= 0 ||
		((req.ViewportWidth != 0 || req.ViewportHeight != 0) && (req.ViewportWidth <= 0 || req.ViewportHeight <= 0 || req.ViewportWidth > Width || req.ViewportHeight > Height)) {
		return StandaloneResult{}, &StandaloneError{Code: "standalone_input_invalid", FailureClass: "input"}
	}
	source := bytes.Clone(req.HTML)
	digest := sha256.Sum256(source)
	results, err := r.capture(ctx, Request{Entry: "index.html", Files: map[string][]byte{"index.html": source}, StateIDs: []string{"standalone"}, ViewportWidth: req.ViewportWidth, ViewportHeight: req.ViewportHeight}, true)
	if err != nil {
		return StandaloneResult{}, standaloneFailure(ctx, err)
	}
	return StandaloneResult{PNG: results[0].PNG, SourceSHA256: hex.EncodeToString(digest[:])}, nil
}

func standaloneFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &StandaloneError{Code: "standalone_cancelled", FailureClass: "infrastructure"}
	}
	var captureErr *Error
	code := "standalone_browser_failed"
	class := "infrastructure"
	if errors.As(err, &captureErr) {
		switch captureErr.Code {
		case "standalone_runtime_exception", "standalone_security_violation", "standalone_not_ready":
			code, class = captureErr.Code, "content"
		case "capture_network_blocked", "capture_state_select_failed":
			code, class = "standalone_security_violation", "content"
		case "capture_renderer_unavailable":
			code = "standalone_browser_unavailable"
		case "capture_timeout":
			// A hung browser cannot safely be attributed to authored content.
			code = "standalone_timeout"
		}
	}
	return &StandaloneError{Code: code, FailureClass: class}
}

// chromedp v0.14.2 prepends os.Environ when len(cmd.Env)>0. A non-nil,
// zero-length environment is the only supported way to prevent inheritance.
// Do not add chromedp.Env options without revisiting this boundary. Preserve
// Linux parent-death cleanup, which ModifyCmdFunc otherwise replaces.
func isolateBrowserCommand(cmd *exec.Cmd) {
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

type standaloneDiagnostics struct {
	mu                sync.Mutex
	runtimeException  bool
	securityViolation bool
}

func (d *standaloneDiagnostics) observe(ev any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch event := ev.(type) {
	case *cdpruntime.EventExceptionThrown:
		// Includes uncaught exceptions and unhandled promise rejections.
		d.runtimeException = true
	case *log.EventEntryAdded:
		if event.Entry != nil && event.Entry.Source == log.SourceSecurity {
			d.securityViolation = true
		}
	case *network.EventLoadingFailed:
		if event.BlockedReason == network.BlockedReasonCsp {
			d.securityViolation = true
		}
	}
}

func (d *standaloneDiagnostics) failure() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.securityViolation {
		return NewError("standalone_security_violation", "standalone_security_violation")
	}
	if d.runtimeException {
		return NewError("standalone_runtime_exception", "standalone_runtime_exception")
	}
	return nil
}

func enableStandaloneDiagnostics(ctx context.Context) error {
	return chromedp.Run(ctx, cdpruntime.Enable(), log.Enable(), network.Enable())
}

// The one initial document is the only permitted navigation, even to the same
// URL. CDP target callbacks are serialized. CSP denies frames, workers, forms,
// popups and external resource access before they can create other targets.
func standaloneRequestAllowed(event *fetch.EventRequestPaused, origin, entry, favicon string, initial *bool) bool {
	if event.Request == nil || event.Request.Method != "GET" {
		return false
	}
	url := event.Request.URL
	if event.ResourceType == network.ResourceTypeDocument {
		if *initial || url != origin+"/"+entry {
			return false
		}
		*initial = true
		return true
	}
	return url == favicon || strings.HasPrefix(url, origin+"/")
}

func captureStandaloneViewport(parent context.Context, width, height int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, stateTimeout)
	defer cancel()
	var ready bool
	// No manifest/global API is injected and no authored DOM/style is changed.
	// A renderer evaluation failure is conservatively infrastructure, not repair.
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(async()=>{
		if(document.readyState!=="complete" || !document.body) return false;
		try { await document.fonts.ready; await Promise.all(Array.from(document.images, img=>img.decode())); }
		catch (_) { return false; }
		await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
		return true;
	})()`, &ready, func(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams { return p.WithAwaitPromise(true).WithReturnByValue(true) })); err != nil {
		return nil, NewError("capture_renderer_failed", "standalone evaluation failed")
	}
	if !ready {
		return nil, NewError("standalone_not_ready", "standalone_not_ready")
	}
	data, err := screenshot(ctx, width, height)
	if err != nil {
		return nil, err
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != width || config.Height != height {
		return nil, NewError("capture_png_invalid", "invalid viewport PNG")
	}
	return data, nil
}
