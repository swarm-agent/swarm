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
	"unicode/utf8"

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
	AnimationVersion            = "swarm.animation/v1"
	MaxAnimationDurationMS      = 10 * 60 * 1000
	MaxAnimationFPS             = 60
	MaxAnimationFrames          = 36_000
	MaxAnimationSegmentFrames   = 300
	MaxMP4Bytes                 = 512 << 20
	animationMinimumTimeout     = 30 * time.Minute
	animationTimeoutPerFrame    = 250 * time.Millisecond
	animationBindTimeoutMS      = 4_000
	animationSeekTimeoutMS      = 4_000
	animationReadyTimeout       = 7 * time.Second
	animationFrameTimeout       = 6 * time.Second
	animationStableTimeout      = 10 * time.Second
	maxAnimationBoundsChecks    = 5_000
	maxAnimationBoundsReports   = 8
	maxAnimationDiagnosticItems = 32
	maxAnimationSelectorBytes   = 240
)

type AnimationProgress struct {
	Stage     string
	Completed int
	Total     int
	Elapsed   time.Duration
}

type AnimationRequest struct {
	Entry      string
	Files      map[string][]byte
	DurationMS int
	FPS        int
	Progress   func(AnimationProgress)
}

// AnimationBounds uses CSS pixel coordinates in the fixed 1920x1080 viewport.
type AnimationBounds struct {
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Right  float64 `json:"right"`
	Bottom float64 `json:"bottom"`
}

// AnimationDiagnostic is intentionally sanitized and bounded. It never carries
// author exceptions, source snippets, URLs, or browser/encoder output.
type AnimationDiagnostic struct {
	Stage         string           `json:"stage"`
	Outcome       string           `json:"outcome"`
	TimestampMS   *int             `json:"timestamp_ms,omitempty"`
	Selector      string           `json:"selector,omitempty"`
	Pseudo        string           `json:"pseudo,omitempty"`
	Bounds        *AnimationBounds `json:"bounds,omitempty"`
	Lifecycle     []string         `json:"lifecycle,omitempty"`
	ScanTruncated bool             `json:"scan_truncated,omitempty"`
}

type AnimationInspectionFrame struct {
	Slot        string
	TimestampMS int
	PNG         []byte
}

type AnimationResult struct {
	MP4              []byte
	PreviewPNG       []byte
	InspectionFrames []AnimationInspectionFrame
	DurationMS       int
	FPS              int
	FrameCount       int
	Timings          map[string]time.Duration
	Diagnostics      []AnimationDiagnostic
}

type AnimationRenderer interface {
	PreflightAnimation(context.Context, AnimationRequest) (AnimationResult, error)
	RenderAnimation(context.Context, AnimationRequest) (AnimationResult, error)
}

// PreflightAnimation verifies the server-owned bootstrap, exact runtime contract,
// representative deterministic seeks, stable pixels, and viewport containment
// without launching a full frame capture or MP4 encode.
func (r *ChromedpRenderer) PreflightAnimation(parent context.Context, req AnimationRequest) (AnimationResult, error) {
	return r.renderAnimation(parent, req, true)
}

// RenderAnimation captures an author-controlled deterministic timeline. The page
// owns authored motion, while the renderer owns every sampled timestamp and the
// final silent MP4 encoding.
func (r *ChromedpRenderer) RenderAnimation(parent context.Context, req AnimationRequest) (AnimationResult, error) {
	return r.renderAnimation(parent, req, false)
}

func (r *ChromedpRenderer) renderAnimation(parent context.Context, req AnimationRequest, preflightOnly bool) (AnimationResult, error) {
	startedAt := time.Now()
	timings := make(map[string]time.Duration)
	emit := func(stage string, completed, total int) {
		if req.Progress != nil {
			req.Progress(AnimationProgress{Stage: stage, Completed: completed, Total: total, Elapsed: time.Since(startedAt)})
		}
	}
	if r == nil || strings.TrimSpace(r.BinaryPath) == "" || strings.TrimSpace(r.CacheRoot) == "" || r.BinaryPath == "." || r.CacheRoot == "." || r.sem == nil || r.preflightSem == nil {
		return AnimationResult{}, NewError("animation_renderer_unavailable", "trusted HTML animation renderer is not configured")
	}
	if err := validateSystemExecutable(r.BinaryPath); err != nil {
		return AnimationResult{}, NewError("animation_renderer_unavailable", "system-managed sandboxed browser is unavailable")
	}
	if !preflightOnly && validateSystemExecutable(r.EncoderPath) != nil {
		return AnimationResult{}, NewError("animation_encoder_unavailable", "system-managed MP4 encoder is unavailable")
	}
	frameCount, err := validateAnimationRequest(req)
	if err != nil {
		return AnimationResult{}, err
	}
	capacity := r.sem
	if preflightOnly {
		capacity = r.preflightSem
	}
	if capacity == nil {
		return AnimationResult{}, NewError("animation_renderer_unavailable", "trusted HTML animation renderer has no bounded capacity")
	}
	queueStartedAt := time.Now()
	emit("queue_wait", 0, 1)
	queueHeartbeat := time.NewTicker(5 * time.Second)
	defer queueHeartbeat.Stop()
	for {
		select {
		case capacity <- struct{}{}:
			timings["queue_wait"] = time.Since(queueStartedAt)
			emit("queue_wait", 1, 1)
			defer func() { <-capacity }()
			goto rendererCapacityAcquired
		case <-queueHeartbeat.C:
			emit("queue_wait", 0, 1)
		case <-parent.Done():
			return AnimationResult{}, NewError("animation_timeout", "animation request was cancelled before renderer capacity became available")
		}
	}

rendererCapacityAcquired:

	ctx, cancel := context.WithTimeout(parent, animationRenderTimeout(frameCount))
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
	if err := installAnimationBootstrap(browserCtx); err != nil {
		return AnimationResult{}, err
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
	preflightStartedAt := time.Now()
	emit("readiness_preflight", 0, 1)
	readinessDiagnostic, err := prepareAnimation(browserCtx, req)
	if err != nil {
		return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: []AnimationDiagnostic{readinessDiagnostic}}, err
	}
	timings["readiness_preflight"] = time.Since(preflightStartedAt)
	emit("readiness_preflight", 1, 1)
	diagnostics := []AnimationDiagnostic{readinessDiagnostic}
	if preflightOnly {
		timestamps := []AnimationInspectionFrame{
			{Slot: "start", TimestampMS: 0},
			{Slot: "middle", TimestampMS: (frameCount / 2) * 1000 / req.FPS},
			{Slot: "exit", TimestampMS: (frameCount - 1) * 1000 / req.FPS},
		}
		frames := make([]AnimationInspectionFrame, 0, len(timestamps))
		var preview []byte
		for _, inspection := range timestamps {
			frame, frameDiagnostics, err := captureAnimationFrame(browserCtx, inspection.TimestampMS, true)
			diagnostics = append(diagnostics, frameDiagnostics...)
			if err != nil {
				return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, err
			}
			inspection.PNG = append([]byte(nil), frame...)
			frames = append(frames, inspection)
			if preview == nil {
				preview = append([]byte(nil), frame...)
			}
			if wasBlocked, reason := blockedAttempt(); wasBlocked {
				return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, newErrorWithCause("animation_network_blocked", "animation document attempted a prohibited network request", errors.New(reason))
			}
		}
		return AnimationResult{PreviewPNG: preview, InspectionFrames: frames, DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, nil
	}

	// Preserve full stability audits at representative timestamps, then capture
	// each production frame once. Previously every one of up to 36,000 frames
	// paid for two full-size PNG screenshots and a pixel decode/compare.
	auditStartedAt := time.Now()
	emit("deterministic_preflight", 0, 3)
	representative := []int{0, (frameCount / 2) * 1000 / req.FPS, (frameCount - 1) * 1000 / req.FPS}
	seenRepresentative := make(map[int]struct{}, len(representative))
	audited := 0
	for _, timeMS := range representative {
		if _, seen := seenRepresentative[timeMS]; seen {
			continue
		}
		seenRepresentative[timeMS] = struct{}{}
		_, frameDiagnostics, err := captureAnimationFrame(browserCtx, timeMS, true)
		diagnostics = append(diagnostics, frameDiagnostics...)
		if err != nil {
			return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, err
		}
		audited++
		emit("deterministic_preflight", audited, len(representative))
	}
	timings["deterministic_preflight"] = time.Since(auditStartedAt)

	captureStartedAt := time.Now()
	emit("frame_capture", 0, frameCount)
	canonicalLocation := origin + "/" + req.Entry
	segmentPaths := make([]string, 0, (frameCount+MaxAnimationSegmentFrames-1)/MaxAnimationSegmentFrames)
	for segmentStart := 0; segmentStart < frameCount; segmentStart += MaxAnimationSegmentFrames {
		segmentFrames := min(MaxAnimationSegmentFrames, frameCount-segmentStart)
		frameDir := filepath.Join(jobDir, fmt.Sprintf("frames-%06d", segmentStart))
		if err := os.Mkdir(frameDir, 0o700); err != nil {
			return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, NewError("animation_renderer_unavailable", "private animation frame directory could not be created")
		}
		for localIndex := 0; localIndex < segmentFrames; localIndex++ {
			globalIndex := segmentStart + localIndex
			timeMS := globalIndex * 1000 / req.FPS
			frame, frameDiagnostics, err := captureAnimationFrame(browserCtx, timeMS, false)
			if err != nil {
				return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics, frameDiagnostics)}, err
			}
			if wasBlocked, reason := blockedAttempt(); wasBlocked {
				return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, newErrorWithCause("animation_network_blocked", "animation document attempted a prohibited network request", errors.New(reason))
			}
			var location string
			if navErr := chromedp.Run(browserCtx, chromedp.Location(&location)); navErr != nil || location != canonicalLocation {
				return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, NewError("animation_seek_failed", "animation runtime attempted to navigate away from its canonical document")
			}
			name := filepath.Join(frameDir, fmt.Sprintf("frame-%06d.png", localIndex))
			if err := os.WriteFile(name, frame, 0o600); err != nil {
				return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, NewError("animation_renderer_failed", "captured animation frame could not be stored privately")
			}
			emit("frame_capture", globalIndex+1, frameCount)
		}
		timings["frame_capture"] += time.Since(captureStartedAt)
		encodeStartedAt := time.Now()
		emit("segment_encode", len(segmentPaths), (frameCount+MaxAnimationSegmentFrames-1)/MaxAnimationSegmentFrames)
		segmentPath := filepath.Join(jobDir, fmt.Sprintf("segment-%06d.mp4", len(segmentPaths)))
		if err := encodeAnimation(ctx, r.EncoderPath, filepath.Join(frameDir, "frame-%06d.png"), segmentPath, req.FPS, segmentFrames); err != nil {
			return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, err
		}
		if err := os.RemoveAll(frameDir); err != nil {
			return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, NewError("animation_renderer_failed", "private animation frame segment could not be removed")
		}
		segmentPaths = append(segmentPaths, segmentPath)
		timings["segment_encode"] += time.Since(encodeStartedAt)
		emit("segment_encode", len(segmentPaths), (frameCount+MaxAnimationSegmentFrames-1)/MaxAnimationSegmentFrames)
		captureStartedAt = time.Now()
	}

	concatStartedAt := time.Now()
	emit("segment_concatenation", 0, 1)
	outputPath := filepath.Join(jobDir, "animation.mp4")
	if err := concatAnimationSegments(ctx, r.EncoderPath, jobDir, segmentPaths, outputPath); err != nil {
		return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, err
	}
	timings["segment_concatenation"] = time.Since(concatStartedAt)
	emit("segment_concatenation", 1, 1)
	mp4, err := os.ReadFile(outputPath)
	if err != nil || len(mp4) == 0 || len(mp4) > MaxMP4Bytes {
		return AnimationResult{DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, NewError("animation_mp4_invalid", "encoded MP4 is missing or exceeds fixed bounds")
	}
	return AnimationResult{MP4: mp4, DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: frameCount, Timings: timings, Diagnostics: boundedAnimationDiagnostics(diagnostics)}, nil
}

func animationRenderTimeout(frameCount int) time.Duration {
	timeout := 5*time.Minute + time.Duration(frameCount)*animationTimeoutPerFrame
	if timeout < animationMinimumTimeout {
		return animationMinimumTimeout
	}
	return timeout
}

func validateAnimationRequest(req AnimationRequest) (int, error) {
	entry, entryExists := req.Files[req.Entry]
	if strings.TrimSpace(req.Entry) == "" || len(req.Files) == 0 || !entryExists || entry == nil || len(entry) == 0 || req.DurationMS < 100 || req.DurationMS > MaxAnimationDurationMS || req.FPS < 1 || req.FPS > MaxAnimationFPS {
		return 0, NewError("animation_source_limit_exceeded", "animation request exceeds fixed renderer bounds")
	}
	frameCount := (req.DurationMS*req.FPS + 999) / 1000
	if frameCount < 1 || frameCount > MaxAnimationFrames {
		return 0, NewError("animation_source_limit_exceeded", fmt.Sprintf("animation frame count exceeds the fixed %d-frame renderer bound", MaxAnimationFrames))
	}
	return frameCount, nil
}

// The bootstrap is installed through Page.addScriptToEvaluateOnNewDocument, so
// it exists synchronously before any author classic/module script can execute.
const animationBootstrapScriptPrefix = `(function () {
"use strict";
const prior=globalThis.__SWARM_ANIMATION_V1__;
let claimed=false, settled=false, bound=null, resolveBind;
const lifecycle=[];
const allowedOutcomes=new Set(["bind_claimed","duplicate_bind","bound","invalid","bind_rejected","bind_timeout","missing_before_dom_content_loaded"]);
const record=outcome=>{if(lifecycle.length<8&&allowedOutcomes.has(outcome))lifecycle.push(outcome)};
const boundPromise=new Promise(resolve=>{ resolveBind=resolve; });
const valid=api=>!!api&&api.version==="swarm.animation/v1"&&typeof api.ready==="function"&&typeof api.seek==="function";
const settle=(outcome,api)=>{
  if (settled) return false;
  settled=true;
  record(outcome);
  if (outcome==="bound") bound=api;
  resolveBind({outcome});
  return outcome==="bound";
};
const bind=candidate=>{
  if (claimed||settled) { record("duplicate_bind"); return false; }
  if (!candidate||typeof candidate.then!=="function") {
    if (!valid(candidate)) { settle("invalid"); return false; }
    claimed=true; record("bind_claimed"); settle("bound",candidate); return true;
  }
  claimed=true; record("bind_claimed");
  Promise.resolve(candidate).then(api=>settle(valid(api)?"bound":"invalid",api),()=>settle("bind_rejected"));
  return true;
};
addEventListener("DOMContentLoaded",()=>{if(!claimed)settle("missing_before_dom_content_loaded")},{once:true});
setTimeout(()=>{if(!settled)settle(claimed?"bind_timeout":"missing_before_dom_content_loaded")},`

const animationBootstrapScriptSuffix = `);
const runtime={
  version:"swarm.animation/v1",
  bind,
  ready:async()=>{
    const outcome=await boundPromise;
    if (outcome.outcome!=="bound") return {__swarm_outcome:outcome.outcome};
    try { return await bound.ready(); } catch (_) { return {__swarm_outcome:"ready_rejected"}; }
  },
  seek:async timeMs=>{
    if (!bound) return {__swarm_outcome:"runtime_unbound"};
    try { return await bound.seek(timeMs); } catch (_) { return {__swarm_outcome:"seek_rejected"}; }
  }
};
Object.defineProperty(globalThis,"__SWARM_ANIMATION_V1__",{value:runtime,writable:false,configurable:false,enumerable:true});
Object.defineProperty(globalThis,"__SWARM_ANIMATION_BIND__",{value:bind,writable:false,configurable:false,enumerable:false});
Object.defineProperty(globalThis,"__SWARM_ANIMATION_BOOTSTRAP_V1__",{value:{version:"swarm.animation.bootstrap/v1",get settled(){return settled},get lifecycle(){return lifecycle.slice()}},writable:false,configurable:false,enumerable:false});
if (valid(prior)) bind(prior);
})();`

func animationBootstrap() string {
	return animationBootstrapScriptPrefix + fmt.Sprint(animationBindTimeoutMS) + animationBootstrapScriptSuffix
}

func installAnimationBootstrap(browserCtx context.Context) error {
	ctx, cancel := context.WithTimeout(browserCtx, documentTimeout)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(execCtx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(animationBootstrap()).Do(execCtx)
		return err
	})); err != nil {
		return newErrorWithCause("animation_renderer_failed", "trusted animation bootstrap could not be installed", err)
	}
	return nil
}

type animationAudit struct {
	Code          string           `json:"code"`
	Outcome       string           `json:"outcome,omitempty"`
	Selector      string           `json:"selector,omitempty"`
	Pseudo        string           `json:"pseudo,omitempty"`
	Bounds        *AnimationBounds `json:"bounds,omitempty"`
	Lifecycle     []string         `json:"lifecycle,omitempty"`
	ScanTruncated bool             `json:"scan_truncated,omitempty"`
}

func prepareAnimation(browserCtx context.Context, req AnimationRequest) (AnimationDiagnostic, error) {
	ctx, cancel := context.WithTimeout(browserCtx, animationReadyTimeout)
	defer cancel()
	var audit animationAudit
	expression := fmt.Sprintf(`(async () => {
const api=globalThis.__SWARM_ANIMATION_V1__, bootstrap=globalThis.__SWARM_ANIMATION_BOOTSTRAP_V1__;
if (!bootstrap || bootstrap.version!=="swarm.animation.bootstrap/v1" || !api || api.version!==%q || typeof api.ready!=="function" || typeof api.seek!=="function" || typeof api.bind!=="function") return {code:"animation_bootstrap_missing",outcome:"bootstrap_missing"};
if (!Array.isArray(bootstrap.lifecycle) || bootstrap.lifecycle.length>8 || bootstrap.lifecycle.some(item=>typeof item!=="string"||item.length>40)) return {code:"animation_bootstrap_missing",outcome:"lifecycle_invalid"};
let ack; try { ack=await api.ready(); } catch (_) { return {code:"animation_not_ready",outcome:"ready_rejected"}; }
if (ack && typeof ack.__swarm_outcome==="string") {
  const lifecycle=bootstrap.lifecycle;
  if (ack.__swarm_outcome==="missing_before_dom_content_loaded") return {code:"animation_runtime_missing_before_dom_content_loaded",outcome:ack.__swarm_outcome,lifecycle};
  if (ack.__swarm_outcome==="bind_timeout") return {code:"animation_bind_timeout",outcome:ack.__swarm_outcome,lifecycle};
  return {code:"animation_not_ready",outcome:ack.__swarm_outcome,lifecycle};
}
const finalLifecycle=bootstrap.lifecycle;
if (finalLifecycle.filter(item=>item==="bound").length!==1||finalLifecycle[0]!=="bind_claimed") return {code:"animation_bootstrap_missing",outcome:"lifecycle_invalid",lifecycle:finalLifecycle};
if (!ack || Object.keys(ack).length!==2 || ack.duration_ms!==%d || ack.fps!==%d) return {code:"animation_manifest_mismatch",outcome:"manifest_mismatch",lifecycle:finalLifecycle};
try { await document.fonts.ready; await Promise.all(Array.from(document.images, async img => { if (!img.complete || img.naturalWidth===0) throw new Error(); await img.decode(); })); } catch (_) { return {code:"animation_not_ready",outcome:"assets_not_ready",lifecycle:finalLifecycle}; }
const visible=node=>{const s=getComputedStyle(node),r=node.getBoundingClientRect();return s.display!=="none"&&s.visibility!=="hidden"&&Number(s.opacity)!==0&&r.width>0&&r.height>0};
let blockers=Array.from(document.querySelectorAll('[data-swarm-capture-blocking],[role="dialog"][aria-modal="true"],dialog[open]'));
try { blockers=blockers.concat(Array.from(document.querySelectorAll(':popover-open'))); } catch (_) {}
if (blockers.some(visible)) return {code:"animation_blocked",outcome:"blocking_ui",lifecycle:finalLifecycle};
document.querySelectorAll('[data-swarm-capture-ui]').forEach(node=>node.remove());
if (document.activeElement && document.activeElement.blur) document.activeElement.blur();
const selection=getSelection(); if(selection) selection.removeAllRanges();
const transparent=color=>color==='transparent'||/^rgba\([^)]*,\s*0(?:\.0+)?\s*\)$/.test(color);
const needsOpaqueCanvas=transparent(getComputedStyle(document.documentElement).backgroundColor)&&transparent(getComputedStyle(document.body).backgroundColor);
const style=document.createElement('style'); style.setAttribute('data-swarm-renderer-style','animation-v1'); style.textContent='*,*::before,*::after{scroll-behavior:auto!important;caret-color:transparent!important;cursor:none!important;pointer-events:none!important}html,body{width:1920px!important;height:1080px!important;max-width:1920px!important;max-height:1080px!important;margin:0!important;overflow:hidden!important}'+(needsOpaqueCanvas?'html{background:#fff!important}':''); document.head.append(style);
return {code:"ok",outcome:"ready",lifecycle:finalLifecycle};
})()`, AnimationVersion, req.DurationMS, req.FPS)
	if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &audit, awaitPromise)); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			diagnostic := AnimationDiagnostic{Stage: "readiness", Outcome: "ready_timeout"}
			return diagnostic, NewError("animation_timeout", "animation runtime did not become ready before the fixed deadline")
		}
		diagnostic := AnimationDiagnostic{Stage: "readiness", Outcome: "evaluation_failed"}
		return diagnostic, newErrorWithCause("animation_renderer_failed", "animation readiness evaluation failed", err)
	}
	diagnostic := diagnosticFromAudit("readiness", nil, audit)
	if audit.Code != "ok" {
		if audit.Code == "" {
			audit.Code = "animation_renderer_failed"
			diagnostic.Outcome = "evaluation_failed"
		}
		return diagnostic, NewError(audit.Code, animationSafeMessage(audit.Code))
	}
	if len(diagnostic.Lifecycle) < 2 || len(diagnostic.Lifecycle) > 8 || diagnostic.Lifecycle[0] != "bind_claimed" || diagnostic.Lifecycle[1] != "bound" {
		return AnimationDiagnostic{Stage: "readiness", Outcome: "lifecycle_invalid"}, NewError("animation_bootstrap_missing", animationSafeMessage("animation_bootstrap_missing"))
	}
	for _, outcome := range diagnostic.Lifecycle {
		valid := outcome == "bind_claimed" || outcome == "duplicate_bind" || outcome == "bound" || outcome == "invalid" || outcome == "bind_rejected" || outcome == "bind_timeout" || outcome == "missing_before_dom_content_loaded"
		if len(outcome) > 40 || !valid {
			return AnimationDiagnostic{Stage: "readiness", Outcome: "lifecycle_invalid"}, NewError("animation_bootstrap_missing", animationSafeMessage("animation_bootstrap_missing"))
		}
	}
	return diagnostic, nil
}

func captureAnimationFrame(browserCtx context.Context, timeMS int, auditStability bool) ([]byte, []AnimationDiagnostic, error) {
	frameTimeout := animationFrameTimeout
	if auditStability {
		frameTimeout = animationStableTimeout
	}
	ctx, cancel := context.WithTimeout(browserCtx, frameTimeout)
	defer cancel()
	timestamp := timeMS
	diagnostics := make([]AnimationDiagnostic, 0, 3)
	var audit animationAudit
	expression := fmt.Sprintf(`(async () => {
const time=%d, api=globalThis.__SWARM_ANIMATION_V1__;
let ack; try { ack=await Promise.race([Promise.resolve().then(()=>api.seek(time)),new Promise(resolve=>setTimeout(()=>resolve({__swarm_outcome:"seek_timeout"}),%d))]); } catch (_) { return {code:"animation_seek_rejected",outcome:"seek_rejected"}; }
if (ack&&typeof ack.__swarm_outcome==="string") {
  if (ack.__swarm_outcome==="seek_rejected") return {code:"animation_seek_rejected",outcome:ack.__swarm_outcome};
  if (ack.__swarm_outcome==="seek_timeout") return {code:"animation_seek_timeout",outcome:ack.__swarm_outcome};
  return {code:"animation_seek_failed",outcome:ack.__swarm_outcome};
}
if (!ack || Object.keys(ack).length!==1 || ack.time_ms!==time || document.documentElement.dataset.swarmAnimationTimeMs!==String(time)) return {code:"animation_seek_ack_mismatch",outcome:"seek_ack_mismatch"};
for (const animation of document.getAnimations()) animation.pause();
return {code:"ok",outcome:"seek_acknowledged"};
})()`, timeMS, animationSeekTimeoutMS)
	if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &audit, awaitPromise)); err != nil {
		diagnostics = append(diagnostics, AnimationDiagnostic{Stage: "seek", Outcome: "evaluation_failed", TimestampMS: &timestamp})
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, diagnostics, NewError("animation_timeout", "animation frame seek exceeded the fixed deadline")
		}
		return nil, diagnostics, newErrorWithCause("animation_renderer_failed", "animation frame evaluation failed", err)
	}
	diagnostics = append(diagnostics, diagnosticFromAudit("seek", &timestamp, audit))
	if audit.Code != "ok" {
		if audit.Code == "" {
			audit.Code = "animation_renderer_failed"
		}
		return nil, diagnostics, NewError(audit.Code, animationSafeMessage(audit.Code))
	}
	if auditStability {
		boundsDiagnostics, err := auditAnimationViewport(ctx, timeMS)
		diagnostics = append(diagnostics, boundsDiagnostics...)
		if err != nil {
			return nil, diagnostics, err
		}
	}
	first, err := animationScreenshot(ctx)
	if err != nil {
		return nil, diagnostics, err
	}
	if !auditStability {
		return first, diagnostics, nil
	}
	select {
	case <-time.After(10 * time.Millisecond):
	case <-ctx.Done():
		return nil, diagnostics, NewError("animation_timeout", "animation frame stability audit timed out")
	}
	second, err := animationScreenshot(ctx)
	if err != nil {
		return nil, diagnostics, err
	}
	stable, err := equalPixels(first, second)
	if err != nil {
		return nil, diagnostics, NewError("animation_png_invalid", "renderer returned an invalid PNG frame")
	}
	if !stable {
		diagnostics = append(diagnostics, AnimationDiagnostic{Stage: "stability", Outcome: "pixels_changed", TimestampMS: &timestamp})
		return nil, diagnostics, NewError("animation_frame_unstable", "animation changed after the renderer selected a deterministic timestamp")
	}
	diagnostics = append(diagnostics, AnimationDiagnostic{Stage: "stability", Outcome: "pixels_stable", TimestampMS: &timestamp})
	return second, diagnostics, nil
}

// boundedAnimationDiagnostics is the only aggregation path for diagnostics
// originating after readiness; it prevents frame-count-scaled response growth.
func boundedAnimationDiagnostics(groups ...[]AnimationDiagnostic) []AnimationDiagnostic {
	result := make([]AnimationDiagnostic, 0, maxAnimationDiagnosticItems)
	for _, group := range groups {
		remaining := maxAnimationDiagnosticItems - len(result)
		if remaining <= 0 {
			break
		}
		if len(group) > remaining {
			group = group[:remaining]
		}
		for _, diagnostic := range group {
			result = append(result, sanitizeAnimationDiagnostic(diagnostic))
		}
	}
	return result
}

func sanitizeAnimationDiagnostic(diagnostic AnimationDiagnostic) AnimationDiagnostic {
	stage := diagnostic.Stage
	switch stage {
	case "readiness", "seek", "viewport", "stability":
	default:
		stage = "renderer"
	}
	outcome := diagnostic.Outcome
	if len(outcome) == 0 || len(outcome) > 48 {
		outcome = "invalid_outcome"
	}
	lifecycle := diagnostic.Lifecycle
	if len(lifecycle) > 8 {
		lifecycle = lifecycle[:8]
	}
	filteredLifecycle := make([]string, 0, len(lifecycle))
	for _, item := range lifecycle {
		switch item {
		case "bind_claimed", "duplicate_bind", "bound", "invalid", "bind_rejected", "bind_timeout", "missing_before_dom_content_loaded":
			filteredLifecycle = append(filteredLifecycle, item)
		default:
			filteredLifecycle = append(filteredLifecycle, "invalid")
		}
	}
	audit := animationAudit{
		Outcome:       outcome,
		Selector:      diagnostic.Selector,
		Pseudo:        diagnostic.Pseudo,
		Bounds:        diagnostic.Bounds,
		Lifecycle:     filteredLifecycle,
		ScanTruncated: diagnostic.ScanTruncated,
	}
	return diagnosticFromAudit(stage, diagnostic.TimestampMS, audit)
}

func truncateAnimationDiagnosticUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	for len(value) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 {
			return ""
		}
		value = value[:len(value)-size]
	}
	return value
}

func diagnosticFromAudit(stage string, timestampMS *int, audit animationAudit) AnimationDiagnostic {
	switch stage {
	case "readiness", "seek", "viewport", "stability":
	default:
		stage = "renderer"
	}
	outcome := audit.Outcome
	if outcome == "" {
		outcome = audit.Code
	}
	if len(outcome) == 0 || len(outcome) > 48 {
		outcome = "invalid_outcome"
	}
	lifecycle := audit.Lifecycle
	if len(lifecycle) > 8 {
		lifecycle = lifecycle[:8]
	}
	selector := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, audit.Selector)
	if len(selector) > maxAnimationSelectorBytes {
		selector = truncateAnimationDiagnosticUTF8(selector, maxAnimationSelectorBytes)
	}
	pseudo := audit.Pseudo
	if pseudo != "" && pseudo != "::before" && pseudo != "::after" {
		pseudo = ""
	}
	bounds := audit.Bounds
	if bounds != nil {
		values := []float64{bounds.Left, bounds.Top, bounds.Right, bounds.Bottom}
		for _, value := range values {
			if value != value || value < -1_000_000 || value > 1_000_000 {
				bounds = nil
				break
			}
		}
	}
	return AnimationDiagnostic{
		Stage:         stage,
		Outcome:       outcome,
		TimestampMS:   timestampMS,
		Selector:      selector,
		Pseudo:        pseudo,
		Bounds:        bounds,
		Lifecycle:     append([]string{}, lifecycle...),
		ScanTruncated: audit.ScanTruncated,
	}
}

// auditAnimationViewport performs a bounded DOM/pseudo-element scan and reports
// only sanitized selectors plus numeric bounds for actionable containment failures.
func auditAnimationViewport(ctx context.Context, timeMS int) ([]AnimationDiagnostic, error) {
	var audits []animationAudit
	expression := fmt.Sprintf(`(() => {
const maxChecks=%d,maxReports=%d,nodes=Array.from(document.querySelectorAll('*')).filter(node=>!node.matches('[data-swarm-renderer-style]')),reports=[];
if (document.documentElement.scrollWidth>innerWidth || document.documentElement.scrollHeight>innerHeight || document.body.scrollWidth>innerWidth || document.body.scrollHeight>innerHeight) reports.push({code:'animation_viewport_overflow',outcome:'scroll_overflow'});
const escaped=value=>{try{return CSS.escape(value)}catch(_){return String(value).replace(/[^a-zA-Z0-9_-]/g,'_')}};
const selector=node=>{
  if (node.id) return '#'+escaped(node.id);
  const parts=[]; let current=node;
  while (current&&current.nodeType===1&&parts.length<5) {
    let part=current.localName||'element';
    if (current.classList&&current.classList.length) part+='.'+Array.from(current.classList).slice(0,2).map(escaped).join('.');
    const parent=current.parentElement;
    if (parent) { const peers=Array.from(parent.children).filter(item=>item.localName===current.localName); if (peers.length>1) part+=':nth-of-type('+(peers.indexOf(current)+1)+')'; }
    parts.unshift(part); current=parent;
  }
  return parts.join(' > ').slice(0,%d);
};
const add=(node,pseudo,rect)=>{
  if (reports.length>=maxReports) return;
  reports.push({code:'animation_viewport_overflow',outcome:'bounds_overflow',selector:selector(node),pseudo:pseudo||'',bounds:{left:rect.left,top:rect.top,right:rect.right,bottom:rect.bottom}});
};
const outside=rect=>Number.isFinite(rect.left)&&Number.isFinite(rect.top)&&Number.isFinite(rect.right)&&Number.isFinite(rect.bottom)&&(rect.left<0||rect.top<0||rect.right>innerWidth||rect.bottom>innerHeight);
const count=Math.min(nodes.length,maxChecks);
for (let i=0;i<count&&reports.length<maxReports;i++) {
  const node=nodes[i],style=getComputedStyle(node),rect=node.getBoundingClientRect();
  if (style.display!=='none'&&style.visibility!=='hidden'&&Number(style.opacity)!==0&&rect.width>0&&rect.height>0&&outside(rect)) add(node,'',rect);
  for (const pseudo of ['::before','::after']) {
    if (reports.length>=maxReports) break;
    const ps=getComputedStyle(node,pseudo);
    if (!ps||ps.content==='none'||ps.content==='normal'||ps.display==='none'||ps.visibility==='hidden'||Number(ps.opacity)===0) continue;
    const left=parseFloat(ps.left),top=parseFloat(ps.top),right=parseFloat(ps.right),bottom=parseFloat(ps.bottom),width=parseFloat(ps.width),height=parseFloat(ps.height);
    const hasInset=[left,top,right,bottom].some(Number.isFinite);
    if (!hasInset) continue;
    const pr={left:Number.isFinite(left)?rect.left+left:(Number.isFinite(right)&&Number.isFinite(width)?rect.right-right-width:rect.left),right:Number.isFinite(right)?rect.right-right:(Number.isFinite(width)?rect.left+(Number.isFinite(left)?left:0)+width:rect.right),top:Number.isFinite(top)?rect.top+top:(Number.isFinite(bottom)&&Number.isFinite(height)?rect.bottom-bottom-height:rect.top),bottom:Number.isFinite(bottom)?rect.bottom-bottom:(Number.isFinite(height)?rect.top+(Number.isFinite(top)?top:0)+height:rect.bottom)};
    if (outside(pr)) add(node,pseudo,pr);
  }
}
if (!reports.length) reports.push({code:'ok',outcome:'viewport_contained',scan_truncated:nodes.length>maxChecks});
else reports[reports.length-1].scan_truncated=nodes.length>maxChecks||reports.length>=maxReports;
return reports;
})()`, maxAnimationBoundsChecks, maxAnimationBoundsReports, maxAnimationSelectorBytes)
	if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &audits, func(params *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
		return params.WithReturnByValue(true)
	})); err != nil {
		return []AnimationDiagnostic{{Stage: "viewport", Outcome: "evaluation_failed", TimestampMS: &timeMS}}, newErrorWithCause("animation_renderer_failed", "animation viewport evaluation failed", err)
	}
	if len(audits) == 0 || len(audits) > maxAnimationBoundsReports {
		return []AnimationDiagnostic{{Stage: "viewport", Outcome: "invalid_diagnostics", TimestampMS: &timeMS}}, NewError("animation_renderer_failed", "animation viewport audit returned invalid diagnostics")
	}
	for _, audit := range audits {
		if audit.Code != "ok" && audit.Code != "animation_viewport_overflow" {
			return []AnimationDiagnostic{{Stage: "viewport", Outcome: "invalid_diagnostics", TimestampMS: &timeMS}}, NewError("animation_renderer_failed", "animation viewport audit returned invalid diagnostics")
		}
		if audit.Code == "ok" && audit.Outcome != "viewport_contained" {
			return []AnimationDiagnostic{{Stage: "viewport", Outcome: "invalid_diagnostics", TimestampMS: &timeMS}}, NewError("animation_renderer_failed", "animation viewport audit returned invalid diagnostics")
		}
		if audit.Code == "animation_viewport_overflow" && audit.Outcome != "scroll_overflow" && audit.Outcome != "bounds_overflow" {
			return []AnimationDiagnostic{{Stage: "viewport", Outcome: "invalid_diagnostics", TimestampMS: &timeMS}}, NewError("animation_renderer_failed", "animation viewport audit returned invalid diagnostics")
		}
		if audit.Outcome == "bounds_overflow" && audit.Bounds == nil {
			return []AnimationDiagnostic{{Stage: "viewport", Outcome: "invalid_diagnostics", TimestampMS: &timeMS}}, NewError("animation_renderer_failed", "animation viewport audit returned invalid diagnostics")
		}
	}
	diagnostics := make([]AnimationDiagnostic, 0, len(audits))
	overflow := false
	for _, audit := range audits {
		diagnostics = append(diagnostics, diagnosticFromAudit("viewport", &timeMS, audit))
		overflow = overflow || audit.Code != "ok"
	}
	if overflow {
		return boundedAnimationDiagnostics(diagnostics), NewError("animation_viewport_overflow", animationSafeMessage("animation_viewport_overflow"))
	}
	return boundedAnimationDiagnostics(diagnostics), nil
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
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return nil, NewError("animation_timeout", "animation frame capture exceeded the quality-adjusted deadline")
		}
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
		"-frames:v", fmt.Sprint(frames), "-an", "-c:v", "libx264", "-preset", "veryfast",
		"-pix_fmt", "yuv420p", "-threads", "2", "-fflags", "+bitexact", "-flags:v", "+bitexact",
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

func concatAnimationSegments(ctx context.Context, encoderPath, jobDir string, segments []string, outputPath string) error {
	if len(segments) == 0 {
		return NewError("animation_encode_failed", "trusted MP4 encoder produced no bounded segments")
	}
	if len(segments) == 1 {
		if err := os.Rename(segments[0], outputPath); err != nil {
			return newErrorWithCause("animation_encode_failed", "trusted MP4 segment could not be finalized", err)
		}
		return nil
	}
	var list strings.Builder
	for index := range segments {
		fmt.Fprintf(&list, "file 'segment-%06d.mp4'\n", index)
	}
	listPath := filepath.Join(jobDir, "segments.txt")
	if err := os.WriteFile(listPath, []byte(list.String()), 0o600); err != nil {
		return NewError("animation_encode_failed", "trusted MP4 segment list could not be stored privately")
	}
	args := []string{"-v", "error", "-nostdin", "-y", "-f", "concat", "-safe", "1", "-i", listPath, "-an", "-c", "copy", "-map_metadata", "-1", "-movflags", "+faststart", outputPath}
	output := &boundedCommandOutput{remaining: 16 << 10}
	cmd := exec.CommandContext(ctx, encoderPath, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = jobDir, output, output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return NewError("animation_timeout", "MP4 concatenation exceeded the fixed deadline")
		}
		return newErrorWithCause("animation_concat_failed", "trusted MP4 segment concatenation failed", errors.New(strings.TrimSpace(output.String())))
	}
	return nil
}

func validateSystemExecutable(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("unsafe executable")
	}
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
	case "animation_bootstrap_missing":
		return "trusted animation bootstrap is missing"
	case "animation_runtime_missing":
		return "animation runtime is missing or incompatible"
	case "animation_runtime_missing_before_dom_content_loaded":
		return "animation runtime did not bind before DOMContentLoaded"
	case "animation_bind_timeout":
		return "animation runtime did not bind before the fixed deadline"
	case "animation_manifest_mismatch":
		return "animation runtime duration or FPS does not match the declared manifest"
	case "animation_not_ready":
		return "animation runtime did not acknowledge complete readiness"
	case "animation_timeout":
		return "animation runtime exceeded the fixed readiness deadline"
	case "animation_seek_rejected":
		return "animation runtime rejected the renderer-controlled timestamp"
	case "animation_seek_timeout":
		return "animation runtime did not settle the renderer-controlled timestamp before the fixed deadline"
	case "animation_seek_ack_mismatch", "animation_seek_failed":
		return "animation runtime returned an invalid renderer-controlled timestamp acknowledgement"
	case "animation_blocked":
		return "animation document contains blocking UI"
	case "animation_viewport_overflow":
		return "animation document renders visible content outside the fixed viewport"
	default:
		return "trusted HTML animation capture failed"
	}
}
