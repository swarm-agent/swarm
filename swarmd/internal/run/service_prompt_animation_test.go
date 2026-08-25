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

func TestMasterHarnessPrefersLiveHTMLVideoStudioPreviewBeforeExport(t *testing.T) {
	prompt := masterHarnessPrompt(t.TempDir())
	for _, expected := range []string{
		"submit the pending proposal before any HTML-to-MP4 export",
		"plays the selected HTML live in a sandboxed swarm-player/v1 iframe while soundtrack audio follows the same playhead",
		"Do not call export_html_animation merely to preview HTML with soundtrack",
		"never claim that live HTML plus soundtrack preview is unsupported",
		"explicit durable acceptance/promotion or final rendering",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("master harness missing live HTML Video Studio guidance %q", expected)
		}
	}
}

func TestVideoStudioMessageContextPrefersLiveHTMLCandidatePreview(t *testing.T) {
	prompt := videoStudioMessageContextForProvider(map[string]any{
		"creative_mode":     "video",
		"video_project_id":  "project-1",
		"video_revision_id": "revision-1",
	})
	for _, expected := range []string{
		"attach them as animation_candidates",
		"previews the selected HTML live in a sandboxed swarm-player/v1 iframe while soundtrack audio follows the same playhead",
		"Do not export HTML to MP4 merely for live review",
		"do not submit text/html through replace_source",
		"durable acceptance/promotion or final rendering",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Video Studio context missing live HTML guidance %q: %s", expected, prompt)
		}
	}
}
