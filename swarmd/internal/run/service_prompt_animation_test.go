package run

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

func TestMasterHarnessRequiresDesignerAnimationContract(t *testing.T) {
	prompt := masterHarnessPrompt(t.TempDir())
	for _, expected := range []string{
		"For animated output, always pass the narrowest applicable animation_profile",
		"motion_ui for CSS/WAAPI/SVG/Canvas UI motion",
		"spatial_3d for pinned local Three.js",
		"vector_playback for licensed dotLottie/Rive imports",
		"final_render for MP4 playback",
		`"animation_profile":{"profile":"motion_ui"}`,
		"Managed Designers use only the context-bound artifact_v3_author capability",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("master harness missing animation requirement %q", expected)
		}
	}
	for _, forbidden := range []string{
		"one or more such parts atomically only through manage_video propose_html_iteration",
		"manage_artifact export_html_animation_fallback on one valid candidate",
		"publish_workspace on the exact workspace file or package with animation_profile",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("master harness retained V1 managed animation workflow %q", forbidden)
		}
	}
}

func TestVideoHelpRoutesArtifactV2VideoConversionWithoutManualArrays(t *testing.T) {
	help := tool.VideoHelpText()
	for _, expected := range []string{
		"manage_video convert_artifact_v2",
		"callers must not export V1 HTML, reconstruct arrays, or mix collection/variant references into the V2 path",
		"resulting proposal remains pending for user review",
		"Server owns the fallback and pending candidate set",
		"do not derive V1 HTML, allocate replacement variants, or export MP4 merely for live preview",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("video help missing Artifact V2 Video Studio guidance %q", expected)
		}
	}
}

func TestVideoHelpRoutesNativeArtifactV3VideoConversion(t *testing.T) {
	help := tool.VideoHelpText()
	for _, expected := range []string{
		"manage_video convert_artifact_v3",
		"artifact_v3_session_id, artifact_v3_artifact_id, artifact_v3_revision_ref, project_id, and base_revision_id",
		"injects deterministic animation timing only into ephemeral render bytes",
		"exactly one pending artifact_v3_conversion proposal",
		"must not author plan arrays or translate through V1/V2 identity",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("video help missing Artifact V3 video guidance %q", expected)
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

func TestVideoStudioMessageContextUsesArtifactV2Conversion(t *testing.T) {
	prompt := videoStudioMessageContextForProvider(map[string]any{
		"creative_mode":     "video",
		"video_project_id":  "project-1",
		"video_revision_id": "revision-1",
	})
	for _, expected := range []string{
		"manage_video convert_artifact_v2",
		"exact Artifact V2 artifact and published-head identities",
		"server constructs stable parts, duration, candidate sets, and exact fallbacks",
		"Do not export HTML to MP4 merely for live review",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Video Studio context missing V2 guidance %q: %s", expected, prompt)
		}
	}
}
