package run

import (
	"strings"
	"testing"
)

func TestMasterHarnessRequiresProfiledDesignerAnimationGuidance(t *testing.T) {
	prompt := masterHarnessPrompt(t.TempDir())
	for _, expected := range []string{
		"For animated output, always pass the narrowest applicable animation_profile",
		"concrete animation-quality, frame-pacing, hot-loop, caching, adaptive-quality",
		"do not rely on vague words such as optimal, smooth, or high FPS",
		"motion_ui for CSS/WAAPI/SVG/Canvas UI motion",
		"spatial_3d for pinned local Three.js",
		"vector_playback for licensed dotLottie/Rive imports",
		"final_render for MP4 playback",
		`"animation_profile":{"profile":"motion_ui"}`,
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("master harness missing Designer animation requirement %q", expected)
		}
	}
}
