package htmlcapture

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Requirement: RenderAnimation must capture a complete 42-second deterministic
// timeline with bounded concurrent pages, unchanged 1080p/high encoding and
// strict representative stability audits. This real system-runtime boundary
// detects screenshot/paint stalls absent from preflight-only tests; it does not
// establish behavior of any private artifact and is not a hermetic-tier test.
func TestAnimationCapture42SecondTimeline(t *testing.T) {
	r := requireAnimationRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	source := `<!doctype html><html><head><style>html,body{margin:0;background:#08111f}#box{position:absolute;left:100px;top:100px;width:200px;height:200px;background:#7af0c5;animation:motion 42s linear both}@keyframes motion{to{transform:translateX(1200px)}}</style><script>globalThis.__SWARM_ANIMATION_V1__={version:"swarm.animation/v1",ready(){return {duration_ms:42000,fps:30}},seek(time){for(const a of document.getAnimations()){a.pause();a.currentTime=time}return {time_ms:time,scene_id:"motion"}}};</script></head><body><div id="box"></div></body></html>`
	last := time.Now()
	result, err := r.RenderAnimation(ctx, AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": []byte(source)}, DurationMS: 42000, FPS: 30, Quality: AnimationQualityHigh, Progress: func(p AnimationProgress) {
		if time.Since(last) > 10*time.Second || p.Completed == p.Total {
			t.Logf("%s %d/%d elapsed=%s", p.Stage, p.Completed, p.Total, p.Elapsed)
			last = time.Now()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.DurationMS != 42000 || result.FPS != 30 || result.FrameCount != 1260 || result.Quality != AnimationQualityHigh || len(result.MP4) == 0 || len(result.PreviewPNG) == 0 {
		t.Fatalf("incomplete capture: duration=%d fps=%d frames=%d", result.DurationMS, result.FPS, result.FrameCount)
	}
}

// Requirement: a failed producer must cancel and join sibling CDP work before
// returning; animationCaptureContext must preserve executor values. This unit
// layer injects the cancellation race without a browser or wall-clock sleeps.
func TestAnimationCapturePipelineCancellationJoinsPages(t *testing.T) {
	type key struct{}
	page := context.WithValue(context.Background(), key{}, "executor")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := make(chan struct{})
	var exited atomic.Bool
	failure := errors.New("capture failure")
	err := runOrderedFramePipeline(ctx, 2, 2, 2, func(pipeline context.Context, worker, index int) ([]byte, error) {
		if index == 0 {
			<-started
			return nil, failure
		}
		frame, stop := animationCaptureContext(page, pipeline)
		defer stop()
		if frame.Value(key{}) != "executor" {
			return nil, errors.New("lost executor")
		}
		close(started)
		<-frame.Done()
		exited.Store(true)
		return nil, frame.Err()
	}, func([]byte) error { t.Error("partial output written"); return nil }, nil)
	if !errors.Is(err, failure) || !exited.Load() {
		t.Fatalf("err=%v sibling exited=%t", err, exited.Load())
	}
	if page.Err() != nil {
		t.Fatal("pipeline cancelled page owner")
	}
}

// Requirement: timeout attribution at captureAnimationFrame must distinguish
// local shared frame expiry from render/parent deadline and cancellation, with
// no private exception leakage. Injected expired contexts are the narrowest
// deterministic proof; non-timeout stability/ack failures must remain intact.
func TestAnimationCaptureTimeoutScope(t *testing.T) {
	for _, scope := range []string{"frame_deadline", "render_deadline", "parent_deadline", "parent_cancelled"} {
		t.Run(scope, func(t *testing.T) {
			parent := context.Background()
			switch scope {
			case "render_deadline":
				var cancel context.CancelFunc
				parent, cancel = context.WithDeadlineCause(parent, time.Now().Add(-time.Second), errAnimationRenderDeadline)
				defer cancel()
			case "parent_deadline":
				var cancel context.CancelFunc
				parent, cancel = context.WithDeadline(parent, time.Now().Add(-time.Second))
				defer cancel()
			case "parent_cancelled":
				var cancel context.CancelFunc
				parent, cancel = context.WithCancel(parent)
				cancel()
			}
			frame, cancel := context.WithDeadline(parent, time.Now().Add(-time.Second))
			defer cancel()
			err := animationFrameFailure(parent, frame, errors.New("private exception"), "screenshot", 21000, 6*time.Second, 6*time.Second)
			if !strings.Contains(err.Error(), "scope="+scope) || !strings.Contains(err.Error(), "stage=screenshot timestamp_ms=21000") || strings.Contains(err.Error(), "private exception") {
				t.Fatal(err)
			}
		})
	}
	original := NewError("animation_frame_unstable", "unstable")
	if got := animationFrameFailure(context.Background(), context.Background(), original, "screenshot", 0, time.Second, 0); got != original {
		t.Fatal("non-timeout replaced")
	}
}
