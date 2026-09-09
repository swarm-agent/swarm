package htmlcapture

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Requirement: RenderAnimation must finish consecutive frames even when a
// deterministic scene holds its pixels unchanged. Multiple worker tabs share
// Chrome's compositor, so an acknowledgement/RAF is not proof that screenshot
// readback will finish. This real-browser regression exercises the production
// pipeline (not only its first-frame preflight); no private source is used.
func TestAnimationConcurrentHeldFrames(t *testing.T) {
	r := requireAnimationRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	source := `<!doctype html><html><head><style>html,body{margin:0;background:#08111f}#box{position:absolute;left:100px;top:100px;width:600px;height:600px;background:linear-gradient(45deg,#7af0c5,#9b8cff);filter:blur(2px);transform:translateZ(0)}</style><script>globalThis.__SWARM_ANIMATION_V1__={version:"swarm.animation/v1",ready(){return true},seek(time){return {time_ms:time,scene_id:"hold"}}};</script></head><body><div id="box"></div></body></html>`
	result, err := r.RenderAnimation(ctx, AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": []byte(source)}, DurationMS: 2000, FPS: 30, AllowBooleanReady: true, Quality: AnimationQualityHigh})
	if err != nil {
		t.Fatal(err)
	}
	if result.FrameCount != 60 || result.FPS != 30 || result.Quality != AnimationQualityHigh || len(result.MP4) == 0 {
		t.Fatal("incomplete held-frame render")
	}
}

// Requirement: concurrent tab capture must return each tab's exact selected
// pixels, not the foreground sibling's surface, including backwards seeks.
// The real CDP/frame/pipeline boundary is needed to catch compositor cross-talk;
// this public-safe test is not a substitute for the private-source retry.
func TestAnimationCompositorTabPixels(t *testing.T) {
	r := requireAnimationRuntime(t)
	parent, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alloc, stop := chromedp.NewExecAllocator(parent, append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(r.BinaryPath), chromedp.Flag("no-sandbox", false), chromedp.Flag("disable-threaded-animation", true))...)
	defer stop()
	browser, closeBrowser := chromedp.NewContext(alloc)
	defer closeBrowser()
	browser = withAnimationCaptureGate(browser)
	if err := chromedp.Run(browser); err != nil {
		t.Fatal(err)
	}
	pages := []context.Context{browser}
	for i := 1; i < 3; i++ {
		p, closePage := chromedp.NewContext(browser)
		defer closePage()
		pages = append(pages, p)
	}
	for i, p := range pages {
		script := fmt.Sprintf(`document.body.style.margin='0';globalThis.__SWARM_ANIMATION_V1__={seek(time){document.body.style.backgroundColor='rgb(%d,'+time+',80)';document.documentElement.dataset.swarmAnimationTimeMs=String(time);return {time_ms:time}}}`, 40+i*60)
		if err := chromedp.Run(p, chromedp.EmulateViewport(Width, Height), chromedp.Evaluate(script, nil)); err != nil {
			t.Fatal(err)
		}
	}
	err := runOrderedFramePipeline(parent, 18, 3, 6, func(ctx context.Context, worker, index int) ([]byte, error) {
		frameCtx, done := animationCaptureContext(pages[worker], ctx)
		defer done()
		timestamp := (index % 6) * 33
		frame, _, err := captureAnimationFrame(frameCtx, timestamp, true)
		if err != nil {
			return nil, err
		}
		img, err := png.Decode(bytes.NewReader(frame))
		if err != nil {
			return nil, err
		}
		red, green, blue, _ := img.At(100, 100).RGBA()
		if img.Bounds().Dx() != Width || img.Bounds().Dy() != Height || red>>8 != uint32(40+worker*60) || green>>8 != uint32(timestamp) || blue>>8 != 80 {
			return nil, fmt.Errorf("wrong tab/time pixels at frame %d: %d,%d,%d", index, red>>8, green>>8, blue>>8)
		}
		return []byte{byte(index)}, nil
	}, func([]byte) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
}
