package htmlcapture

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const (
	Width            = 1920
	Height           = 1080
	MaxStates        = 16
	MaxPNGBytes      = 16 << 20
	SystemChromePath = "/opt/google/chrome/chrome"
	totalTimeout     = 45 * time.Second
	documentTimeout  = 5 * time.Second
	stateTimeout     = 5 * time.Second
)

type Request struct {
	Entry    string
	Files    map[string][]byte
	StateIDs []string
	// RequiredSelectors must resolve to visible elements fully contained inside
	// the requested viewport before any ready evidence is returned.
	RequiredSelectors []string
	// ViewportWidth/ViewportHeight optionally tighten the capture below the fixed
	// renderer maximum. Both must be set together; callers cannot exceed 1920x1080.
	ViewportWidth  int
	ViewportHeight int
}

type Result struct {
	StateID string
	PNG     []byte
}

type Renderer interface {
	Capture(context.Context, Request) ([]Result, error)
}

type Error struct {
	Code        string
	SafeMessage string
	cause       error
}

func (e *Error) Error() string                 { return e.Code + ": " + e.SafeMessage }
func (e *Error) Unwrap() error                 { return e.cause }
func (e *Error) SafeDiagnosticCode() string    { return e.Code }
func (e *Error) SafeDiagnosticMessage() string { return e.SafeMessage }

func NewError(code, message string) error { return &Error{Code: code, SafeMessage: message} }
func newErrorWithCause(code, message string, cause error) error {
	return &Error{Code: code, SafeMessage: message, cause: cause}
}

// ChromedpRenderer owns a fresh system-managed Chrome process for each request.
// BinaryPath and CacheRoot are daemon-authored constructor inputs and are never
// accepted from a tool call or artifact content. Production uses SystemChromePath,
// whose Ubuntu package path is covered by the host's Chrome AppArmor userns policy.
type ChromedpRenderer struct {
	BinaryPath    string
	EncoderPath   string
	CacheRoot     string
	browserOutput io.Writer
	sem           chan struct{}
	preflightSem  chan struct{}
}

func NewChromedpRenderer(binaryPath, cacheRoot string) *ChromedpRenderer {
	return NewChromedpRendererWithConcurrency(binaryPath, cacheRoot, 1)
}

func NewChromedpRendererWithConcurrency(binaryPath, cacheRoot string, concurrency int) *ChromedpRenderer {
	encoderPath, _ := exec.LookPath("ffmpeg")
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 4 {
		concurrency = 4
	}
	// Preflight uses the same daemon-owned bounded capacity as full capture. This
	// lets one regular managed-Designer wave validate independent animations in
	// parallel without exceeding the existing host-size and four-worker cap.
	return &ChromedpRenderer{BinaryPath: filepath.Clean(strings.TrimSpace(binaryPath)), EncoderPath: filepath.Clean(strings.TrimSpace(encoderPath)), CacheRoot: filepath.Clean(strings.TrimSpace(cacheRoot)), sem: make(chan struct{}, concurrency), preflightSem: make(chan struct{}, concurrency)}
}

func (r *ChromedpRenderer) Capture(parent context.Context, req Request) ([]Result, error) {
	if r == nil || r.BinaryPath == "." || r.CacheRoot == "." {
		return nil, NewError("capture_renderer_unavailable", "trusted HTML capture renderer is not configured")
	}
	info, err := os.Lstat(r.BinaryPath)
	stat, statOK := infoSysStat(info)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 || !statOK || stat.Uid != 0 {
		return nil, NewError("capture_renderer_unavailable", "system-managed sandboxed browser is unavailable")
	}
	if len(req.StateIDs) < 1 || len(req.StateIDs) > MaxStates || req.Entry == "" || len(req.Files) == 0 || len(req.RequiredSelectors) > 256 {
		return nil, NewError("capture_source_limit_exceeded", "capture request exceeds fixed renderer bounds")
	}
	for _, selector := range req.RequiredSelectors {
		if strings.TrimSpace(selector) == "" || len(selector) > 512 {
			return nil, NewError("capture_source_limit_exceeded", "capture required selector exceeds fixed renderer bounds")
		}
	}
	viewportWidth, viewportHeight := Width, Height
	if req.ViewportWidth != 0 || req.ViewportHeight != 0 {
		if req.ViewportWidth <= 0 || req.ViewportHeight <= 0 || req.ViewportWidth > Width || req.ViewportHeight > Height {
			return nil, NewError("capture_source_limit_exceeded", "capture viewport exceeds fixed renderer bounds")
		}
		viewportWidth, viewportHeight = req.ViewportWidth, req.ViewportHeight
	}
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-parent.Done():
		return nil, NewError("capture_timeout", "capture request was cancelled before renderer capacity became available")
	}

	ctx, cancel := context.WithTimeout(parent, totalTimeout)
	defer cancel()
	if err := os.MkdirAll(r.CacheRoot, 0o700); err != nil {
		return nil, NewError("capture_renderer_unavailable", "private capture cache is unavailable")
	}
	cacheInfo, err := os.Lstat(r.CacheRoot)
	if err != nil || cacheInfo.Mode()&os.ModeSymlink != 0 || !cacheInfo.IsDir() {
		return nil, NewError("capture_renderer_unavailable", "private capture cache is unavailable")
	}
	if err := os.Chmod(r.CacheRoot, 0o700); err != nil {
		return nil, NewError("capture_renderer_unavailable", "private capture cache could not be secured")
	}
	jobDir, err := os.MkdirTemp(r.CacheRoot, "capture-job-")
	if err != nil {
		return nil, NewError("capture_renderer_unavailable", "private capture job could not be created")
	}
	_ = os.Chmod(jobDir, 0o700)
	defer os.RemoveAll(jobDir)

	origin, shutdown, err := serveFiles(ctx, req.Files)
	if err != nil {
		return nil, NewError("capture_renderer_failed", "capture source server could not start")
	}
	defer shutdown()

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(r.BinaryPath),
		chromedp.UserDataDir(filepath.Join(jobDir, "profile")),
		chromedp.Flag("headless", true),
		// chromedp otherwise injects --no-sandbox when the daemon runs as root.
		// Capture must fail closed if the reviewed Chromium sandbox is unavailable.
		chromedp.Flag("no-sandbox", false),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-component-update", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-extensions", true),
		// Undo permissive/DoS-sensitive chromedp defaults for untrusted pages.
		chromedp.Flag("disable-ipc-flooding-protection", false),
		chromedp.Flag("disable-popup-blocking", false),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("force-device-scale-factor", "1"),
		chromedp.Flag("window-size", fmt.Sprintf("%d,%d", Width, Height)),
		chromedp.Flag("host-resolver-rules", "MAP * ~NOTFOUND, EXCLUDE 127.0.0.1"),
		chromedp.Flag("renderer-process-limit", "2"),
	)
	if r.browserOutput != nil {
		opts = append(opts, chromedp.CombinedOutput(r.browserOutput))
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	defer allocCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

	var blockedMu sync.Mutex
	blocked := false
	blockedReason := ""
	markBlocked := func(reason string) {
		blockedMu.Lock()
		blocked = true
		if blockedReason == "" {
			blockedReason = reason
		}
		blockedMu.Unlock()
	}
	chromedp.ListenBrowser(browserCtx, func(ev any) {
		switch event := ev.(type) {
		case *browser.EventDownloadWillBegin:
			markBlocked("download")
		case *target.EventTargetCreated:
			// A page target with an opener is a popup. The initial capture page has
			// no opener and is created before author content can execute.
			if event.TargetInfo.Type == "page" && event.TargetInfo.OpenerID != "" {
				markBlocked("popup")
			}
		}
	})
	faviconURL := origin[:strings.LastIndex(origin, "/")] + "/favicon.ico"
	chromedp.ListenTarget(browserCtx, func(ev any) {
		paused, ok := ev.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		allowed := paused.Request.URL == "about:blank" || paused.Request.URL == faviconURL || paused.Request.URL == origin || strings.HasPrefix(paused.Request.URL, origin+"/")
		if !allowed {
			markBlocked(paused.Request.URL)
		}
		go func() {
			execCtx := cdp.WithExecutor(browserCtx, chromedp.FromContext(browserCtx).Target)
			if allowed {
				_ = fetch.ContinueRequest(paused.RequestID).Do(execCtx)
			} else {
				_ = fetch.FailRequest(paused.RequestID, network.ErrorReasonBlockedByClient).Do(execCtx)
			}
		}()
	})

	if err := chromedp.Run(browserCtx); err != nil {
		return nil, newErrorWithCause("capture_renderer_failed", "sandboxed browser could not start", err)
	}
	docCtx, docCancel := context.WithTimeout(browserCtx, documentTimeout)
	err = chromedp.Run(docCtx,
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*"}}),
		browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorDeny).WithEventsEnabled(true),
		chromedp.EmulateViewport(int64(viewportWidth), int64(viewportHeight)),
		chromedp.Navigate(origin+"/"+req.Entry),
		chromedp.WaitReady("body", chromedp.ByQuery),
	)
	docCancel()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(docCtx.Err(), context.DeadlineExceeded) {
			return nil, NewError("capture_timeout", "capture document did not become ready before the fixed deadline")
		}
		return nil, newErrorWithCause("capture_renderer_failed", "capture document could not be loaded", err)
	}
	blockedMu.Lock()
	wasBlocked := blocked
	wasBlockedReason := blockedReason
	blockedMu.Unlock()
	if wasBlocked {
		return nil, newErrorWithCause("capture_network_blocked", "capture document attempted a prohibited network request", errors.New(wasBlockedReason))
	}

	results := make([]Result, 0, len(req.StateIDs))
	for _, stateID := range req.StateIDs {
		result, err := captureState(browserCtx, stateID, viewportWidth, viewportHeight, req.RequiredSelectors)
		if err != nil {
			return nil, err
		}
		blockedMu.Lock()
		wasBlocked = blocked
		wasBlockedReason = blockedReason
		blockedMu.Unlock()
		if wasBlocked {
			return nil, newErrorWithCause("capture_network_blocked", "capture document attempted a prohibited network request", errors.New(wasBlockedReason))
		}
		var location string
		if navErr := chromedp.Run(browserCtx, chromedp.Location(&location)); navErr != nil || location != origin+"/"+req.Entry {
			return nil, NewError("capture_state_select_failed", "capture state attempted to navigate away from its canonical document")
		}
		results = append(results, Result{StateID: stateID, PNG: result})
	}
	return results, nil
}

type browserAudit struct {
	Code string `json:"code"`
}

func captureState(browserCtx context.Context, stateID string, viewportWidth, viewportHeight int, requiredSelectors []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(browserCtx, stateTimeout)
	defer cancel()
	var audit browserAudit
	if requiredSelectors == nil {
		requiredSelectors = []string{}
	}
	selectors, err := json.Marshal(requiredSelectors)
	if err != nil {
		return nil, NewError("capture_source_limit_exceeded", "capture required selectors are invalid")
	}
	expression := fmt.Sprintf(`(async () => {
const id=%q, api=globalThis.__SWARM_CAPTURE_V1__;
if (!api || api.version!=="swarm.capture/v1" || typeof api.select!=="function" || typeof api.ready!=="function") return {code:"capture_runtime_missing"};
try { await api.select(id); } catch (_) { return {code:"capture_state_select_failed"}; }
if (document.documentElement.dataset.swarmCaptureState!==id) return {code:"capture_state_select_failed"};
let ack; try { ack=await api.ready(id); } catch (_) { return {code:"capture_state_not_ready"}; }
if (!ack || Object.keys(ack).length!==1 || ack.state_id!==id) return {code:"capture_state_not_ready"};
try { await document.fonts.ready; await Promise.all(Array.from(document.images, async img => { if (!img.complete || img.naturalWidth===0) throw new Error(); await img.decode(); })); } catch (_) { return {code:"capture_state_not_ready"}; }
const visible=node=>{const s=getComputedStyle(node),r=node.getBoundingClientRect();return s.display!=="none"&&s.visibility!=="hidden"&&Number(s.opacity)!==0&&r.width>0&&r.height>0};
let blockers=Array.from(document.querySelectorAll('[data-swarm-capture-blocking],[role="dialog"][aria-modal="true"],dialog[open]'));
try { blockers=blockers.concat(Array.from(document.querySelectorAll(':popover-open'))); } catch (_) {}
if (blockers.some(visible)) return {code:"capture_state_blocked"};
document.querySelectorAll('[data-swarm-capture-ui]').forEach(node=>node.remove());
if (document.activeElement && document.activeElement.blur) document.activeElement.blur();
const selection=getSelection(); if(selection) selection.removeAllRanges();
for (const animation of document.getAnimations()) animation.cancel();
const transparent=color=>color==='transparent'||/^rgba\([^)]*,\s*0(?:\.0+)?\s*\)$/.test(color);
const needsOpaqueCanvas=transparent(getComputedStyle(document.documentElement).backgroundColor)&&transparent(getComputedStyle(document.body).backgroundColor);
const width=%d,height=%d,requiredSelectors=%s;
if (document.documentElement.scrollWidth>width || document.documentElement.scrollHeight>height || document.body.scrollWidth>width || document.body.scrollHeight>height) return {code:"capture_viewport_overflow"};
for (const selector of requiredSelectors) { let node; try { node=document.querySelector(selector); } catch (_) { return {code:"capture_required_element_invalid"}; } if (!node || !visible(node)) return {code:"capture_required_element_missing"}; const r=node.getBoundingClientRect(); if (r.left<0 || r.top<0 || r.right>width || r.bottom>height) return {code:"capture_required_element_clipped"}; }
const style=document.createElement('style'); style.textContent='*,*::before,*::after{animation:none!important;transition:none!important;scroll-behavior:auto!important;caret-color:transparent!important;cursor:none!important;pointer-events:none!important}html,body{width:'+width+'px!important;height:'+height+'px!important;max-width:'+width+'px!important;max-height:'+height+'px!important;margin:0!important;overflow:hidden!important}'+(needsOpaqueCanvas?'html{background:#fff!important}':''); document.head.append(style);
return {code:"ok"};
})()`, stateID, viewportWidth, viewportHeight, selectors)
	if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &audit, func(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
		return p.WithAwaitPromise(true).WithReturnByValue(true)
	})); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, NewError("capture_timeout", "capture state did not stabilize before the fixed deadline")
		}
		return nil, newErrorWithCause("capture_renderer_failed", "capture state evaluation failed", err)
	}
	if audit.Code != "ok" {
		if audit.Code == "" {
			audit.Code = "capture_renderer_failed"
		}
		return nil, NewError(audit.Code, safeMessage(audit.Code))
	}
	first, err := screenshot(ctx, viewportWidth, viewportHeight)
	if err != nil {
		return nil, err
	}
	select {
	case <-time.After(100 * time.Millisecond):
	case <-ctx.Done():
		return nil, NewError("capture_timeout", "capture stability audit timed out")
	}
	second, err := screenshot(ctx, viewportWidth, viewportHeight)
	if err != nil {
		return nil, err
	}
	stable, err := equalPixels(first, second, viewportWidth, viewportHeight)
	if err != nil {
		return nil, NewError("capture_png_invalid", "renderer returned an invalid PNG sample")
	}
	if !stable {
		return nil, NewError("capture_state_unstable", "capture state changed during the fixed stability audit")
	}
	return second, nil
}

func screenshot(ctx context.Context, viewportWidth, viewportHeight int) ([]byte, error) {
	var data []byte
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(execCtx context.Context) error {
		var captureErr error
		data, captureErr = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).WithCaptureBeyondViewport(false).Do(execCtx)
		return captureErr
	})); err != nil {
		return nil, newErrorWithCause("capture_renderer_failed", "renderer could not capture the requested state", err)
	}
	if len(data) == 0 || len(data) > MaxPNGBytes {
		return nil, NewError("capture_png_invalid", "renderer PNG exceeded fixed bounds")
	}
	return data, nil
}

func equalPixels(left, right []byte, viewportWidth, viewportHeight int) (bool, error) {
	decode := func(data []byte) (*image.RGBA, error) {
		reader := bytes.NewReader(data)
		img, err := png.Decode(reader)
		if err != nil || reader.Len() != 0 {
			return nil, errors.New("invalid PNG sample")
		}
		if img.Bounds().Dx() != viewportWidth || img.Bounds().Dy() != viewportHeight {
			return nil, errors.New("dimension mismatch")
		}
		rgba := image.NewRGBA(image.Rect(0, 0, viewportWidth, viewportHeight))
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		return rgba, nil
	}
	a, err := decode(left)
	if err != nil {
		return false, err
	}
	b, err := decode(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(a.Pix, b.Pix), nil
}

func serveFiles(ctx context.Context, files map[string][]byte) (string, func(), error) {
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "GET" {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	})
	prefix := "/" + token + "/"
	mux.HandleFunc(prefix, func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, prefix)
		if req.Method != "GET" || name == "" || path.Clean(name) != name || strings.Contains(name, "\\") {
			http.NotFound(w, req)
			return
		}
		body, ok := files[name]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; media-src 'none'; connect-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'self'; form-action 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		switch strings.ToLower(filepath.Ext(name)) {
		case ".html", ".htm":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case ".css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case ".js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		_, _ = w.Write(body)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go func() { _ = server.Serve(listener) }()
	go func() { <-ctx.Done(); _ = server.Close() }()
	origin := "http://" + listener.Addr().String() + "/" + token
	return origin, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}, nil
}

func infoSysStat(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

func safeMessage(code string) string {
	switch code {
	case "capture_runtime_missing":
		return "capture runtime is missing or incompatible"
	case "capture_state_select_failed":
		return "capture state selection failed"
	case "capture_state_not_ready":
		return "capture state did not report complete readiness"
	case "capture_state_blocked":
		return "capture state contains blocking UI"
	case "capture_viewport_overflow":
		return "capture document overflows the required viewport"
	case "capture_required_element_invalid":
		return "capture required Part selector is invalid"
	case "capture_required_element_missing":
		return "capture required Part is missing or not visible"
	case "capture_required_element_clipped":
		return "capture required Part is clipped outside the required viewport"
	default:
		return "trusted HTML capture failed"
	}
}
