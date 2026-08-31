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
		"parser-executed classic `head` script",
		"immediately calls globalThis.__SWARM_ANIMATION_BIND__(runtime) when that trusted binder exists",
		"otherwise assigning the runtime to globalThis.__SWARM_ANIMATION_V1__ for standalone preview",
		"never defer bootstrap behind a module, event, promise, import, or asset load",
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

func TestMasterHarnessPreservesOneClipIterationTopology(t *testing.T) {
	prompt := masterHarnessPrompt(t.TempDir())
	for _, expected := range []string{
		"one video clip with multiple iterations",
		"one stable video-plan part",
		"not a full-song timeline",
		"no longer than 12 seconds",
		"status=awaiting_selection",
		"manage_artifact derive_text",
		"consume only its explicitly returned successful ready artifact_references",
		"export_html_animation_fallback on one valid candidate",
		"source_audio clip already trimmed to the same intro window in initial_timeline",
		"publish_workspace on the exact workspace file or package with animation_profile",
		"representative seek preflight before queueing",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("master harness missing one-clip iteration requirement %q", expected)
		}
	}
}

func TestVideoStudioMessageContextBoundsBlockedHTMLClipWork(t *testing.T) {
	prompt := videoStudioMessageContextForProvider(map[string]any{
		"creative_mode":     "video",
		"video_project_id":  "project-1",
		"video_revision_id": "revision-1",
	})
	for _, expected := range []string{
		"revision may append a genuinely new stable part",
		"make at most one materially different correction",
		"stop immediately and report the failing action",
		"Never spend repeated turns recreating equivalent artifacts",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Video Studio context missing bounded blocker guidance %q: %s", expected, prompt)
		}
	}
}

func TestMasterHarnessAllowsAtomicMultiPartHTMLVideoProposals(t *testing.T) {
	prompt := masterHarnessPrompt(t.TempDir())
	for _, expected := range []string{
		"one or more such parts atomically only through manage_video propose_html_iteration",
		"one exact image fallback per part",
		"one or more stable-id parts",
		"2 to 16 compatible ready animation_candidates",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("master harness missing multi-part HTML proposal guidance %q", expected)
		}
	}
	if strings.Contains(prompt, "propose_html_iteration rejects image-only downgrade, multiple parts") {
		t.Fatal("master harness still forbids multi-part HTML proposals")
	}
}

func TestVideoStudioMessageContextPrefersLiveHTMLCandidatePreview(t *testing.T) {
	prompt := videoStudioMessageContextForProvider(map[string]any{
		"creative_mode":     "video",
		"video_project_id":  "project-1",
		"video_revision_id": "revision-1",
	})
	for _, expected := range []string{
		"and animation_candidates",
		"propose_html_iteration",
		"select_animation_candidate",
		"promote_animation_derivative",
		"previews the selected HTML live in a sandboxed swarm-player/v1 iframe while soundtrack audio follows the same playhead",
		"Do not export HTML to MP4 merely for live review",
		"do not submit text/html through replace_source",
		"MP4 derivative is required",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Video Studio context missing live HTML guidance %q: %s", expected, prompt)
		}
	}
}
