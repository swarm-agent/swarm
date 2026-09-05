package htmlcapture

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Requirement: CSS/WAAPI seek must yield stable pixels at each exact timestamp,
// including backwards seeks, without hiding independently changing author code.
// Threat: compositor sampling can lag a main-thread acknowledgement. The real
// browser captureAnimationFrame boundary is the narrowest observable proof;
// this system-runtime test is not part of a hermetic critical tier.
func TestAnimationCSSSeekStablePixelsAndRejectsMutation(t *testing.T) {
	renderer := requireAnimationRuntime(t)
	html := []byte(`<!doctype html><html><head><style>html,body{margin:0;overflow:hidden;background:#08111f}#box{position:absolute;left:100px;top:100px;width:200px;height:200px;background:#7af0c5;animation:motion 3s ease-in-out infinite alternate}@keyframes motion{from{transform:translate3d(0,0,0) scale(.8);opacity:.35;box-shadow:0 0 8px #9b8cff}to{transform:translate3d(300px,80px,0) scale(1.2);opacity:.9;box-shadow:0 0 25px #9b8cff}}</style><script>globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready(){return {duration_ms:4000,fps:30}},async seek(time){for(const a of document.getAnimations()){a.pause();a.currentTime=time}await new Promise(r=>setTimeout(r,0));void document.documentElement.offsetHeight;document.documentElement.dataset.swarmAnimationTimeMs=String(time);return {time_ms:time}}});</script></head><body><div id="box"></div></body></html>`)
	result, err := renderer.PreflightAnimation(context.Background(), AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": html}, DurationMS: 4000, FPS: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PreviewPNG) == 0 {
		t.Fatal("missing stable CSS preview")
	}
	// A separate page exercises the strict audit directly; disabling compositor
	// animation must not turn the stability gate into unconditional success.
	ctx, cancel := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(renderer.BinaryPath), chromedp.Flag("no-sandbox", false), chromedp.Flag("disable-threaded-animation", true))...)
	defer cancel()
	page, closePage := chromedp.NewContext(ctx)
	defer closePage()
	page, deadline := context.WithTimeout(page, 30*time.Second)
	defer deadline()
	origin, shutdown, err := serveFiles(page, map[string][]byte{"index.html": html})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()
	if err := chromedp.Run(page, chromedp.EmulateViewport(Width, Height), chromedp.Navigate(origin+"/index.html"), chromedp.Evaluate(`globalThis.__SWARM_ANIMATION_V1__={seek:async time=>{for(const a of document.getAnimations()){a.pause();a.currentTime=time}document.documentElement.dataset.swarmAnimationTimeMs=String(time);return {time_ms:time}}}`, nil)); err != nil {
		t.Fatal(err)
	}
	var first, middle []byte
	for _, ts := range []int{0, 2000, 3967, 0} {
		frame, _, err := captureAnimationFrame(page, ts, true)
		if err != nil {
			t.Fatal(err)
		}
		if ts == 0 {
			if first == nil {
				first = frame
			} else if same, err := equalPixels(first, frame, Width, Height); err != nil || !same {
				t.Fatal("backwards seek changed frame zero")
			}
		}
		if ts == 2000 {
			middle = frame
		}
	}
	if bytes.Equal(first, middle) {
		t.Fatal("distinct timestamps did not animate")
	}
	if err := chromedp.Run(page, chromedp.Evaluate(`globalThis.__SWARM_ANIMATION_V1__.seek=async time=>{document.documentElement.dataset.swarmAnimationTimeMs=String(time);let n=0;setInterval(()=>document.getElementById("box").style.backgroundColor="rgb("+(++n%255)+",0,0)",1);return {time_ms:time}}`, nil)); err != nil {
		t.Fatal(err)
	}
	_, _, err = captureAnimationFrame(page, 0, true)
	if err == nil {
		t.Fatal("changing author pixels were accepted")
	}
	if got, ok := err.(*Error); !ok || got.Code != "animation_frame_unstable" {
		t.Fatalf("unexpected rejection: %v", err)
	}
}
