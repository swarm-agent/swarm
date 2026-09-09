package htmlcapture

import (
	"fmt"
	"testing"
	"time"
)

// Requirement: native authored duration limits fit real renderer admission,
// frame accounting, encoding cadence, buffering and deadlines. Threat: raising
// conversion admission only moves the failure to a hidden renderer budget or
// silently downgrades 60 FPS. Pure budget tests exercise the exact production
// validators without launching browsers or encoding thousands of frames.
func TestNativeAuthoredDurationRendererBudget(t *testing.T) {
	for _, tc := range []struct {
		duration, frames, fps int
		timeout               time.Duration
	}{
		{85000, 5100, 60, 30 * time.Minute}, {120000, 7200, 60, 35 * time.Minute},
		{900000, 27000, 30, 117*time.Minute + 30*time.Second}, {1200000, 36000, 30, 155 * time.Minute},
	} {
		t.Run(fmt.Sprint(tc.duration), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`<script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":%d,"fps":%d}</script>`, tc.duration, tc.fps))
			d, f, present, err := AnimationTiming(body)
			if err != nil || !present || d != tc.duration || f != tc.fps {
				t.Fatalf("authored timing: %v", err)
			}
			req := AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": body}, DurationMS: d, FPS: f, OutputFPS: f, Quality: AnimationQualityStandard}
			frames, err := validateAnimationRequest(req)
			if err != nil || frames != tc.frames || frames > MaxAnimationFrames {
				t.Fatalf("frame budget: %d %v", frames, err)
			}
			encoding, err := resolveAnimationEncoding(req)
			if err != nil || encoding.FPS != tc.fps || encoding.Workers != 3 || encoding.BufferFrames != 6 || encoding.Workers > MaxAnimationCaptureWorkers {
				t.Fatalf("encoding budget: %+v %v", encoding, err)
			}
			if got := animationRenderTimeout(frames); got != tc.timeout {
				t.Fatalf("timeout=%v want=%v", got, tc.timeout)
			}
		})
	}
	for _, duration := range []int{99, 600001, 900000, int(^uint(0) >> 1)} {
		body := []byte(fmt.Sprintf(`<script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":%d,"fps":60}</script>`, duration))
		if _, _, _, err := AnimationTiming(body); err == nil {
			t.Fatalf("out-of-contract duration %d admitted", duration)
		}
	}
}
