package htmlcapture

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

func TestValidateAnimationRequestBounds(t *testing.T) {
	valid := AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": []byte("ok")}, DurationMS: 1200, FPS: 24}
	if frames, err := validateAnimationRequest(valid); err != nil || frames != 29 {
		t.Fatalf("valid request frames=%d err=%v", frames, err)
	}
	long := AnimationRequest{Entry: "index.html", Files: valid.Files, DurationMS: 74_920, FPS: 60}
	if frames, err := validateAnimationRequest(long); err != nil || frames != 4496 {
		t.Fatalf("long request frames=%d err=%v", frames, err)
	}
	for _, invalid := range []AnimationRequest{
		{Entry: "index.html", Files: map[string][]byte{"index.html": []byte{}}, DurationMS: 1000, FPS: 30},
		{Entry: "missing.html", Files: valid.Files, DurationMS: 1000, FPS: 30},
		{Entry: "   ", Files: valid.Files, DurationMS: 1000, FPS: 30},
		{Entry: "index.html", Files: valid.Files, DurationMS: 99, FPS: 30},
		{Entry: "index.html", Files: valid.Files, DurationMS: MaxAnimationDurationMS + 1, FPS: 30},
		{Entry: "index.html", Files: valid.Files, DurationMS: 1000, FPS: MaxAnimationFPS + 1},
	} {
		if _, err := validateAnimationRequest(invalid); err == nil {
			t.Fatalf("expected bounds error for %+v", invalid)
		}
	}
}

func TestAnimationSafeMessagesDoNotEchoAuthorContent(t *testing.T) {
	for _, code := range []string{"animation_bind_timeout", "animation_manifest_mismatch", "animation_seek_failed", "animation_viewport_overflow", "private source"} {
		message := animationSafeMessage(code)
		if message == "" || strings.Contains(message, code) {
			t.Fatalf("unsafe message for %q: %q", code, message)
		}
	}
}

func TestAnimationBootstrapUsesBoundedBindDeadline(t *testing.T) {
	script := animationBootstrap()
	if !strings.Contains(script, "},4000);") || strings.Contains(script, "%!") || !strings.Contains(script, "DOMContentLoaded") {
		t.Fatalf("bootstrap does not contain the canonical bind deadline")
	}
	if strings.Index(script, "__SWARM_ANIMATION_BIND__") < 0 || strings.Index(script, "DOMContentLoaded") < 0 || !strings.Contains(script, "writable:false,configurable:false") {
		t.Fatalf("bootstrap publication contract is incomplete")
	}
}

func TestBoundedAnimationDiagnostics(t *testing.T) {
	group := make([]AnimationDiagnostic, maxAnimationDiagnosticItems+5)
	if got := boundedAnimationDiagnostics(group); len(got) != maxAnimationDiagnosticItems {
		t.Fatalf("diagnostic count = %d", len(got))
	}
	if got := boundedAnimationDiagnostics(nil, group[:2]); len(got) != 2 {
		t.Fatalf("diagnostic merge count = %d", len(got))
	}
	longSelector := "\x00" + strings.Repeat("x", maxAnimationSelectorBytes+50)
	nan := math.NaN()
	diagnostic := diagnosticFromAudit("invalid-stage", nil, animationAudit{Code: "ok", Outcome: strings.Repeat("x", 49), Selector: longSelector, Pseudo: "::invalid", Bounds: &AnimationBounds{Left: nan}})
	if len(diagnostic.Selector) != maxAnimationSelectorBytes || strings.ContainsRune(diagnostic.Selector, '\x00') || diagnostic.Pseudo != "" || diagnostic.Stage != "renderer" || diagnostic.Outcome != "invalid_outcome" || diagnostic.Bounds != nil {
		t.Fatalf("diagnostic sanitization = %+v", diagnostic)
	}
}

func TestAnimationRenderTimeoutScalesWithDeclaredFrameCount(t *testing.T) {
	if got := animationRenderTimeout(300); got != animationMinimumTimeout {
		t.Fatalf("small render timeout = %s", got)
	}
	if got := animationRenderTimeout(MaxAnimationFrames); got <= animationMinimumTimeout {
		t.Fatalf("maximum-quality render timeout did not scale: %s", got)
	}
}

func requireAnimationRuntime(t *testing.T) *ChromedpRenderer {
	t.Helper()
	if _, err := os.Stat(SystemChromePath); err != nil {
		t.Skipf("system-managed Chrome unavailable: %v", err)
	}
	return NewChromedpRenderer(SystemChromePath, t.TempDir())
}

func preflightHTML(t *testing.T, source string, durationMS, fps int) (AnimationResult, error) {
	t.Helper()
	renderer := requireAnimationRuntime(t)
	return renderer.PreflightAnimation(context.Background(), AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": []byte(source)}, DurationMS: durationMS, FPS: fps})
}

func TestAnimationBootstrapBindsExactlyOnce(t *testing.T) {
	script := `<!doctype html><html><head><meta charset="utf-8"><script>
const runtime={version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}};
if (!globalThis.__SWARM_ANIMATION_BIND__(runtime)) throw new Error("first bind rejected");
if (globalThis.__SWARM_ANIMATION_BIND__(runtime)) throw new Error("second bind accepted");
</script><style>html,body{margin:0;width:100%;height:100%;background:#08111f}</style></head><body><div id="box"></div></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	if err != nil {
		t.Fatalf("PreflightAnimation: %v", err)
	}
	if len(result.PreviewPNG) == 0 || len(result.Diagnostics) < 7 || len(result.Diagnostics) > 10 || result.Diagnostics[0].Outcome != "ready" {
		t.Fatalf("unexpected preflight result: %+v", result)
	}
	if strings.Join(result.Diagnostics[0].Lifecycle, ",") != "bind_claimed,bound,duplicate_bind" {
		t.Fatalf("unexpected bootstrap lifecycle: %+v", result.Diagnostics[0].Lifecycle)
	}
}

func TestAnimationRenderStillRequiresEncoder(t *testing.T) {
	if _, err := os.Stat(SystemChromePath); err != nil {
		t.Skipf("system-managed Chrome unavailable: %v", err)
	}
	renderer := &ChromedpRenderer{BinaryPath: SystemChromePath, EncoderPath: ".", CacheRoot: t.TempDir(), sem: make(chan struct{}, 1), preflightSem: make(chan struct{}, 1)}
	_, err := renderer.RenderAnimation(context.Background(), AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": []byte("ok")}, DurationMS: 400, FPS: 10})
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_encoder_unavailable" {
		t.Fatalf("error = %v", err)
	}
}

func TestAnimationPreflightDoesNotRequireEncoder(t *testing.T) {
	if _, err := os.Stat(SystemChromePath); err != nil {
		t.Skipf("system-managed Chrome unavailable: %v", err)
	}
	renderer := NewChromedpRenderer(SystemChromePath, t.TempDir())
	renderer.EncoderPath = "."
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}});</script></head><body></body></html>`
	if _, err := renderer.PreflightAnimation(context.Background(), AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": []byte(script)}, DurationMS: 400, FPS: 10}); err != nil {
		t.Fatalf("preflight requires encoder: %v", err)
	}
}

func TestAnimationBootstrapReportsMissingBeforeDOMContentLoaded(t *testing.T) {
	result, err := preflightHTML(t, `<!doctype html><html><head><style>html,body{margin:0;width:100%;height:100%;background:#08111f}</style></head><body></body></html>`, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_runtime_missing_before_dom_content_loaded" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Outcome != "missing_before_dom_content_loaded" || strings.Join(result.Diagnostics[0].Lifecycle, ",") != "missing_before_dom_content_loaded" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestAnimationBootstrapRejectsInvalidBindPayload(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1"});</script></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_not_ready" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Outcome != "invalid" || strings.Join(result.Diagnostics[0].Lifecycle, ",") != "invalid" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestAnimationBootstrapReportsDeferredModuleBindTimeout(t *testing.T) {
	script := `<!doctype html><html><head><script type="module">globalThis.__SWARM_ANIMATION_BIND__((async()=>{await new Promise(()=>{}); return null})());</script></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_bind_timeout" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Outcome != "bind_timeout" || strings.Join(result.Diagnostics[0].Lifecycle, ",") != "bind_claimed,bind_timeout" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestAnimationPreflightRejectsReadyRejection(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){throw new Error("private source")},seek(){return {time_ms:0}}});</script></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_not_ready" || strings.Contains(captureErr.Error(), "private source") {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Outcome != "ready_rejected" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestAnimationPreflightRejectsReadyTimeout(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return new Promise(()=>{})},seek(){return {time_ms:0}}});</script></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_timeout" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Outcome != "ready_timeout" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestAnimationPreflightRejectsSeekAckMismatch(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(){return {time_ms:-1}}});</script></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_seek_failed" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
	if len(result.Diagnostics) < 2 || result.Diagnostics[1].Outcome != "seek_ack_mismatch" || result.Diagnostics[1].TimestampMS == nil || *result.Diagnostics[1].TimestampMS != 0 {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestAnimationPreflightRejectsSeekRejectionWithoutEcho(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(){throw new Error("private seek")}});</script></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_seek_failed" || strings.Contains(captureErr.Error(), "private seek") {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
	if len(result.Diagnostics) < 2 || result.Diagnostics[1].Outcome != "seek_rejected" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestAnimationPreflightRejectsSeekTimeout(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(){return new Promise(()=>{})}});</script></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_seek_failed" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
	if len(result.Diagnostics) < 2 || result.Diagnostics[1].Outcome != "seek_timeout" || result.Diagnostics[1].TimestampMS == nil || *result.Diagnostics[1].TimestampMS != 0 {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestAnimationPreflightRejectsMalformedReadyAck(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10,extra:true}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}});</script></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_manifest_mismatch" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
}

func TestAnimationPreflightRejectsFPSMismatch(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:11}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}});</script></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_manifest_mismatch" || strings.Join(result.Diagnostics[0].Lifecycle, ",") != "bind_claimed,bound" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
}

func TestAnimationPreflightRejectsManifestMismatch(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:401,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}});</script></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_manifest_mismatch" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
}

func TestAnimationPreflightRejectsMissingAssets(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}});</script></head><body><img src="missing.png" alt=""></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_not_ready" || len(result.Diagnostics) != 1 || result.Diagnostics[0].Outcome != "assets_not_ready" {
		t.Fatalf("error = %v; diagnostics=%+v", err, result.Diagnostics)
	}
}

func TestAnimationPreflightRejectsVisibleBlockingUI(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}});</script></head><body><div role="dialog" aria-modal="true" style="width:100px;height:100px"></div></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_blocked" || len(result.Diagnostics) != 1 || result.Diagnostics[0].Outcome != "blocking_ui" {
		t.Fatalf("error = %v; diagnostics=%+v", err, result.Diagnostics)
	}
}

func TestAnimationPreflightRemovesCaptureUIBeforeAudit(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}});</script></head><body><div data-swarm-capture-ui style="position:fixed;left:-10px;top:0;width:20px;height:20px"></div></body></html>`
	if result, err := preflightHTML(t, script, 400, 10); err != nil {
		t.Fatalf("capture UI was included in preflight: %v; %+v", err, result.Diagnostics)
	}
}

func TestAnimationPreflightRejectsMalformedSeekAck(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs,extra:true}}});</script></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_seek_failed" || len(result.Diagnostics) < 2 || result.Diagnostics[1].Outcome != "seek_ack_mismatch" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
}

func TestAnimationPreflightReportsElementOverflow(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}});</script>
<style>html,body{margin:0;width:100%;height:100%;background:#08111f}#outside{position:absolute;left:-2px;top:0;width:20px;height:20px}</style></head><body><div id="outside"></div></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_viewport_overflow" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
	found := false
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Stage == "viewport" && diagnostic.Selector == "#outside" && diagnostic.Bounds != nil && diagnostic.Bounds.Left == -2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing element bounds diagnostic: %+v", result.Diagnostics)
	}
}

func TestAnimationPreflightReportsFixedRightInsetPseudoOverflow(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}});</script>
<style>html,body{margin:0;width:100%;height:100%;background:#08111f}body::after{content:"";position:fixed;right:-2px;top:0;width:20px;height:20px;background:#fff}</style></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_viewport_overflow" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
	found := false
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Pseudo == "::after" && diagnostic.Bounds != nil && diagnostic.Bounds.Right == Width+2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing right-inset pseudo diagnostic: %+v", result.Diagnostics)
	}
}

func TestAnimationPreflightReportsScrollOverflow(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}});</script></head><body style="margin:0"><div style="position:fixed;left:0;top:0;width:1921px;height:10px"></div></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_viewport_overflow" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
	found := false
	for _, diagnostic := range result.Diagnostics {
		found = found || diagnostic.Outcome == "scroll_overflow"
	}
	if !found {
		t.Fatalf("missing scroll overflow diagnostic: %+v", result.Diagnostics)
	}
}

func TestAnimationPreflightReportsPseudoElementOverflow(t *testing.T) {
	script := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}});</script>
<style>html,body{margin:0;width:100%;height:100%;background:#08111f}body::after{content:"";position:fixed;inset:-1px;background:#fff}</style></head><body></body></html>`
	result, err := preflightHTML(t, script, 400, 10)
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_viewport_overflow" {
		t.Fatalf("error = %v; result=%+v", err, result)
	}
	if len(result.Diagnostics) > 2+maxAnimationBoundsReports {
		t.Fatalf("overflow diagnostics exceeded fixed bound: %+v", result.Diagnostics)
	}
	found := false
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Stage == "viewport" && diagnostic.Pseudo == "::after" && diagnostic.Bounds != nil {
			found = true
			if diagnostic.Bounds.Left != -1 || diagnostic.Bounds.Top != -1 || diagnostic.Bounds.Right != Width+1 || diagnostic.Bounds.Bottom != Height+1 {
				t.Fatalf("bounds = %+v", diagnostic.Bounds)
			}
		}
	}
	if !found {
		t.Fatalf("missing pseudo-element bounds diagnostic: %+v", result.Diagnostics)
	}
}

func TestChromedpRendererCapturesDeterministicAnimationWithSystemRuntimes(t *testing.T) {
	if _, err := os.Stat(SystemChromePath); err != nil {
		t.Skipf("system-managed Chrome unavailable: %v", err)
	}
	if renderer := NewChromedpRenderer(SystemChromePath, t.TempDir()); renderer.EncoderPath == "." {
		t.Skip("system-managed FFmpeg unavailable")
	}
	html := []byte(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);document.getElementById("box").style.transform="translateX("+(timeMs/2)+"px)";return {time_ms:timeMs}}});</script>
<style>html,body{width:100%;height:100%;margin:0;overflow:hidden;background:#08111f}#box{width:240px;height:240px;background:#7dd3fc}</style></head><body><div id="box"></div></body></html>`)
	renderer := NewChromedpRenderer(SystemChromePath, t.TempDir())
	var browserOutput strings.Builder
	renderer.browserOutput = &browserOutput
	var progress []AnimationProgress
	result, err := renderer.RenderAnimation(context.Background(), AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": html}, DurationMS: 400, FPS: 10, Progress: func(update AnimationProgress) { progress = append(progress, update) }})
	if err != nil {
		t.Fatalf("RenderAnimation: %v; cause: %v; browser output: %s", err, errors.Unwrap(err), browserOutput.String())
	}
	if result.DurationMS != 400 || result.FPS != 10 || result.FrameCount != 4 || len(result.MP4) < 12 || !bytes.Equal(result.MP4[4:8], []byte("ftyp")) {
		t.Fatalf("result = %+v, bytes=%d", result, len(result.MP4))
	}
	seenCaptureProgress := false
	lastCapture := 0
	for _, update := range progress {
		if update.Stage != "frame_capture" {
			continue
		}
		seenCaptureProgress = true
		if update.Completed < lastCapture {
			t.Fatalf("frame progress regressed: %+v", progress)
		}
		lastCapture = update.Completed
	}
	if !seenCaptureProgress || lastCapture != result.FrameCount || result.Timings["readiness_preflight"] <= 0 {
		t.Fatalf("missing measured render progress: progress=%+v timings=%+v", progress, result.Timings)
	}
	if len(result.Diagnostics) < 7 || len(result.Diagnostics) > 10 {
		t.Fatalf("production diagnostics must remain representative and bounded: %+v", result.Diagnostics)
	}
	seekCount := 0
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Stage == "seek" {
			seekCount++
		}
	}
	if seekCount != 3 {
		t.Fatalf("expected exactly three representative seek diagnostics: %+v", result.Diagnostics)
	}
}
