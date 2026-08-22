package htmlcapture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	AnimationVersion       = "swarm.animation/v1"
	MaxAnimationDurationMS = 10_000
	MaxAnimationFPS        = 30
	MaxAnimationFrames     = 300
	MaxMP4Bytes            = 512 << 20
	animationTotalTimeout  = 2 * time.Minute
	animationReadyTimeout  = 5 * time.Second
	animationFrameTimeout  = 2 * time.Second
)

type AnimationRequest struct {
	Entry      string
	Files      map[string][]byte
	DurationMS int
	FPS        int
}

type AnimationResult struct {
	MP4        []byte
	DurationMS int
	FPS        int
	FrameCount int
}

type AnimationRenderer interface {
	RenderAnimation(context.Context, AnimationRequest) (AnimationResult, error)
}

// RenderAnimation captures an author-controlled deterministic timeline. The page
// owns authored motion, while the renderer owns every sampled timestamp and the
// final silent MP4 encoding.
func (r *ChromedpRenderer) RenderAnimation(parent context.Context, req AnimationRequest) (AnimationResult, error) {
	if r == nil || r.BinaryPath == "." || r.CacheRoot == "." || strings.TrimSpace(r.EncoderPath) == "" {
		return AnimationResult{}, NewError("animation_renderer_unavailable", "trusted HTML animation renderer is not configured")
	}
	if err := validateSystemExecutable(r.BinaryPath); err != nil {
		return AnimationResult{}, NewError("animation_renderer_unavailable", "system-managed sandboxed browser is unavailable")
	}
	if err := validateSystemExecutable(r.EncoderPath); err != nil {
		return AnimationResult{}, NewError("animation_encoder_unavailable", "system-managed MP4 encoder is unavailable")
	}
	frameCount, err := validateAnimationRequest(req)
	if err != nil {
		return AnimationResult{}, err
	}
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-parent.Done():
		return AnimationResult{}, NewError("animation_timeout", "animation request was cancelled before renderer capacity became available")
	}

	ctx, cancel := context.WithTimeout(parent, animationTotalTimeout)
	defer cancel()
	if err := os.MkdirAll(r.CacheRoot, 0o700); err != nil {
		return AnimationResult{}, NewError("animation_renderer_unavailable", "private animation cache is unavailable")
	}
	cacheInfo, err := os.Lstat(r.CacheRoot)
	if err != nil || cacheInfo.Mode()&os.ModeSymlink != 0 || !cacheInfo.IsDir() || os.Chmod(r.CacheRoot, 0o700) != nil {
		return AnimationResult{}, NewError("animation_renderer_unavailable", "private animation cache is unavailable")
	}
	jobDir, err := os.MkdirTemp(r.CacheRoot, "animation-job-")
	if err != nil {
		return AnimationResult{}, NewError("animation_renderer_unavailable", "private animation job could not be created")
	}
	_ = os.Chmod(jobDir, 0o700)
	defer os.RemoveAll(jobDir)
	frameDir := filepath.Join(jobDir, "frames")
	if err := os.Mkdir(frameDir, 0o700); err != nil {
		return AnimationResult{}, NewError("animation_renderer_unavailable", "private animation frame directory could not be created")
	}

	origin, shutdown, err := serveFiles(ctx, req.Files)
	if err != nil {
		return AnimationResult{}, NewError("animation_renderer_failed", "animation source server could not start")
	}
	defer shutdown()

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(r.BinaryPath),
		chromedp.UserDataDir(filepath.Join(jobDir, "profile")),
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", false),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-component-update", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-extensions", true),
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
	blocked, blockedReason := false, ""
	markBlocked := func(reason string) {
		blockedMu.Lock()
		defer blockedMu.Unlock()
		blocked = true
		if blockedReason == "" {
			blockedReason = reason
		}
	}
	blockedAttempt := func() (bool, string) {
		blockedMu.Lock()
		defer blockedMu.Unlock()
		return blocked, blockedReason
	}
	chromedp.ListenBrowser(browserCtx, func(ev any) {
		switch event := ev.(type) {
		case *browser.EventDownloadWillBegin:
			markBlocked("download")
		case *target.EventTargetCreated:
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
		return AnimationResult{}, newErrorWithCause("animation_renderer_failed", "sandboxed browser could not start", err)
	}
	docCtx, docCancel := context.WithTimeout(browserCtx, documentTimeout)
	err = chromedp.Run(docCtx,
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*"}}),
		browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorDeny).WithEventsEnabled(true),
		chromedp.EmulateViewport(Width, Height),
		chromedp.Navigate(origin+"/"+req.Entry),
		chromedp.WaitReady("body", chromedp.ByQuery),
	)
	docCancel()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(docCtx.Err(), context.DeadlineExceeded) {
			return AnimationResult{}, NewError("animation_timeout", "animation document did not become ready before the fixed deadline")
		}
		return AnimationResult{}, newErrorWithCause("animation_renderer_failed", "animation document could not be loaded", err)
	}
	if wasBlocked, reason := blockedAttempt(); wasBlocked {
		return AnimationResult{}, newErrorWithCause("animation_network_blocked", "animation document attempted a prohibited network request", errors.New(reason))
	}
	if err := prepareAnimation(browserCtx, req); err != nil {
		return AnimationResult{}, err
	}

	canonicalLocation := origin + "/" + req.Entry
	for index := 0; index < frameCount; index++ {
		timeMS := index * 1000 / req.FPS
		frame, err := captureAnimationFrame(browserCtx, timeMS)
		if err != nil {
			return AnimationResult{}, err
		}
		if wasBlocked, reason := blockedAttempt(); wasBlocked {
			return AnimationResult{}, newErrorWithCause("animation_network_blocked", "animation document attempted a prohibited network request", errors.New(reason))
		}
		var location string
		if navErr := chromedp.Run(browserCtx, chromedp.Location(&location)); navErr != nil || location != canonicalLocation {
			return AnimationResult{}, NewError("animation_seek_failed", "animation runtime attempted to navigate away from its canonical document")
		}
		name := filepath.Join(frameDir, fmt.Sprintf("frame-%06d.png", index))
		if err := os.WriteFile(name, frame, 0o600); err != nil {
			return AnimationResult{}, NewError("animation_renderer_failed", "captured animation frame could not be stored privately")
		}
	}

	outputPath := filepath.Join(jobDir, "animation.mp4")
	if err := encodeAnimation(ctx, r.EncoderPath, filepath.Join(frameDir, "frame-%06d.png"), outputPath, req.FPS, frameCount); err != nil {
		return AnimationResult{}, err
	}
	mp4, err := os.ReadFile(outputPath)
	if err != nil || len(mp4) == 0 || len(mp4) > MaxMP4Bytes {
		return AnimationResult{}, NewError("animation_mp4_invalid", "encoded MP4 is missing or exceeds fixed bounds")
	}
	return AnimationResult{MP4: mp4, DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount}, nil
}

func validateAnimationRequest(req AnimationRequest) (int, error) {
	if req.Entry == "" || len(req.Files) == 0 || req.DurationMS < 100 || req.DurationMS > MaxAnimationDurationMS || req.FPS < 1 || req.FPS > MaxAnimationFPS {
		return 0, NewError("animation_source_limit_exceeded", "animation request exceeds fixed renderer bounds")
	}
	frameCount := (req.DurationMS*req.FPS + 999) / 1000
	if frameCount < 1 || frameCount > MaxAnimationFrames {
		return 0, NewError("animation_source_limit_exceeded", "animation frame count exceeds fixed renderer bounds")
	}
	return frameCount, nil
}

type animationAudit struct {
	Code string `json:"code"`
}

func prepareAnimation(browserCtx context.Context, req AnimationRequest) error {
	ctx, cancel := context.WithTimeout(browserCtx, animationReadyTimeout)
	defer cancel()
	var audit animationAudit
	expression := fmt.Sprintf(`(async () => {
const api=globalThis.__SWARM_ANIMATION_V1__;
if (!api || api.version!==%q || typeof api.ready!=="function" || typeof api.seek!=="function") return {code:"animation_runtime_missing"};
let ack; try { ack=await api.ready(); } catch (_) { return {code:"animation_not_ready"}; }
if (!ack || Object.keys(ack).length!==2 || ack.duration_ms!==%d || ack.fps!==%d) return {code:"animation_not_ready"};
try { await document.fonts.ready; await Promise.all(Array.from(document.images, async img => { if (!img.complete || img.naturalWidth===0) throw new Error(); await img.decode(); })); } catch (_) { return {code:"animation_not_ready"}; }
const visible=node=>{const s=getComputedStyle(node),r=node.getBoundingClientRect();return s.display!=="none"&&s.visibility!=="hidden"&&Number(s.opacity)!==0&&r.width>0&&r.height>0};
let blockers=Array.from(document.querySelectorAll('[data-swarm-capture-blocking],[role="dialog"][aria-modal="true"],dialog[open]'));
try { blockers=blockers.concat(Array.from(document.querySelectorAll(':popover-open'))); } catch (_) {}
if (blockers.some(visible)) return {code:"animation_blocked"};
document.querySelectorAll('[data-swarm-capture-ui]').forEach(node=>node.remove());
if (document.activeElement && document.activeElement.blur) document.activeElement.blur();
const selection=getSelection(); if(selection) selection.removeAllRanges();
const transparent=color=>color==='transparent'||/^rgba\([^)]*,\s*0(?:\.0+)?\s*\)$/.test(color);
const needsOpaqueCanvas=transparent(getComputedStyle(document.documentElement).backgroundColor)&&transparent(getComputedStyle(document.body).backgroundColor);
const style=document.createElement('style'); style.textContent='*,*::before,*::after{scroll-behavior:auto!important;caret-color:transparent!important;cursor:none!important;pointer-events:none!important}html,body{width:1920px!important;height:1080px!important;max-width:1920px!important;max-height:1080px!important;margin:0!important;overflow:hidden!important}'+(needsOpaqueCanvas?'html{background:#fff!important}':''); document.head.append(style);
if (document.documentElement.scrollWidth>1920 || document.documentElement.scrollHeight>1080 || document.body.scrollWidth>1920 || document.body.scrollHeight>1080) return {code:"animation_blocked"};
return {code:"ok"};
})()`, AnimationVersion, req.DurationMS, req.FPS)
	if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &audit, awaitPromise)); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return NewError("animation_timeout", "animation runtime did not become ready before the fixed deadline")
		}
		return newErrorWithCause("animation_renderer_failed", "animation readiness evaluation failed", err)
	}
	if audit.Code != "ok" {
		if audit.Code == "" {
			audit.Code = "animation_renderer_failed"
		}
		return NewError(audit.Code, animationSafeMessage(audit.Code))
	}
	return nil
}

func captureAnimationFrame(browserCtx context.Context, timeMS int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(browserCtx, animationFrameTimeout)
	defer cancel()
	var audit animationAudit
	expression := fmt.Sprintf(`(async () => {
const time=%d, api=globalThis.__SWARM_ANIMATION_V1__;
let ack; try { ack=await api.seek(time); } catch (_) { return {code:"animation_seek_failed"}; }
if (!ack || Object.keys(ack).length!==1 || ack.time_ms!==time || document.documentElement.dataset.swarmAnimationTimeMs!==String(time)) return {code:"animation_seek_failed"};
for (const animation of document.getAnimations()) animation.pause();
return {code:"ok"};
})()`, timeMS)
	if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &audit, awaitPromise)); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, NewError("animation_timeout", "animation frame seek exceeded the fixed deadline")
		}
		return nil, newErrorWithCause("animation_renderer_failed", "animation frame evaluation failed", err)
	}
	if audit.Code != "ok" {
		return nil, NewError(audit.Code, animationSafeMessage(audit.Code))
	}
	first, err := animationScreenshot(ctx)
	if err != nil {
		return nil, err
	}
	select {
	case <-time.After(10 * time.Millisecond):
	case <-ctx.Done():
		return nil, NewError("animation_timeout", "animation frame stability audit timed out")
	}
	second, err := animationScreenshot(ctx)
	if err != nil {
		return nil, err
	}
	stable, err := equalPixels(first, second)
	if err != nil {
		return nil, NewError("animation_png_invalid", "renderer returned an invalid PNG frame")
	}
	if !stable {
		return nil, NewError("animation_frame_unstable", "animation changed after the renderer selected a deterministic timestamp")
	}
	return second, nil
}

func awaitPromise(params *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
	return params.WithAwaitPromise(true).WithReturnByValue(true)
}

func animationScreenshot(ctx context.Context) ([]byte, error) {
	var data []byte
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(execCtx context.Context) error {
		var captureErr error
		data, captureErr = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).WithCaptureBeyondViewport(false).Do(execCtx)
		return captureErr
	})); err != nil {
		return nil, newErrorWithCause("animation_renderer_failed", "renderer could not capture an animation frame", err)
	}
	if len(data) == 0 || len(data) > MaxPNGBytes {
		return nil, NewError("animation_png_invalid", "animation PNG frame exceeded fixed bounds")
	}
	return data, nil
}

func encodeAnimation(ctx context.Context, encoderPath, inputPattern, outputPath string, fps, frames int) error {
	args := []string{
		"-v", "error", "-nostdin", "-y",
		"-framerate", fmt.Sprint(fps), "-start_number", "0", "-i", inputPattern,
		"-frames:v", fmt.Sprint(frames), "-an", "-c:v", "libx264", "-preset", "medium",
		"-pix_fmt", "yuv420p", "-threads", "1", "-fflags", "+bitexact", "-flags:v", "+bitexact",
		"-map_metadata", "-1", "-movflags", "+faststart", outputPath,
	}
	output := &boundedCommandOutput{remaining: 16 << 10}
	cmd := exec.CommandContext(ctx, encoderPath, args...)
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return NewError("animation_timeout", "MP4 encoding exceeded the fixed deadline")
		}
		return newErrorWithCause("animation_encode_failed", "trusted MP4 encoder failed", errors.New(strings.TrimSpace(output.String())))
	}
	return nil
}

func validateSystemExecutable(name string) error {
	info, err := os.Lstat(name)
	stat, statOK := infoSysStat(info)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 || !statOK || stat.Uid != 0 {
		return errors.New("unsafe executable")
	}
	return nil
}

type boundedCommandOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int
}

func (b *boundedCommandOutput) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(payload)
	if len(payload) > b.remaining {
		payload = payload[:b.remaining]
	}
	if len(payload) > 0 {
		_, _ = b.buffer.Write(payload)
		b.remaining -= len(payload)
	}
	return original, nil
}

func (b *boundedCommandOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func animationSafeMessage(code string) string {
	switch code {
	case "animation_runtime_missing":
		return "animation runtime is missing or incompatible"
	case "animation_not_ready":
		return "animation runtime did not acknowledge its declared duration and FPS"
	case "animation_seek_failed":
		return "animation runtime did not acknowledge the renderer-controlled timestamp"
	case "animation_blocked":
		return "animation document contains blocking UI or exceeds the fixed viewport"
	default:
		return "trusted HTML animation capture failed"
	}
}
