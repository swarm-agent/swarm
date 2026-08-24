package run

import (
	"strings"
	"testing"
)

func TestWriteDesignerAnimationGuidanceIncludesCommonPerformanceContract(t *testing.T) {
	var b strings.Builder
	writeDesignerAnimationGuidance(&b, "motion_ui")
	prompt := b.String()
	for _, expected := range []string{
		"display-refresh playback",
		"without hard-coding 60 FPS",
		"monotonic timestamps",
		"one authoritative animation scheduler",
		"cancel the scheduler",
		"precompute and cache",
		"avoid per-frame allocation",
		"Batch reads before writes",
		"transform and opacity",
		"sustained frame time",
		"motion_ui performance tips",
		"one requestAnimationFrame owner",
		"not live merely because it implements swarm.animation/v1",
		"not required to call seek() continuously",
		"must start visibly animating",
		"without a Play button",
		"one pure renderAt(timeMs) function",
		"start exactly one requestAnimationFrame loop",
		"derive time from performance.now() and a rebased epoch",
		"document.visibilitychange",
		"rebase the epoch from the paused animation time",
		"install globalThis.__SWARM_ANIMATION_V1__ before DOMContentLoaded",
		"seek(timeMs) clamps the requested time",
		"must not start a second loop",
		"never rely on the host to advance time",
		"sections and managed temporal parts describe targetable time ranges; they do not animate the document",
		"exact same id, label, start_ms, and end_ms",
		"ordinary preview playback must still come from the artifact-owned scheduler",
		"install the swarm-player/v1 message listener before DOMContentLoaded",
		"{protocol: 'swarm-player/v1', id: request.id, ok: true, result}",
		"never substitute request_id, type, or a top-level manifest",
		"Studio may send stop immediately before describe on first load",
		"resume the one live RAF scheduler",
		"buttons and active states must use the same manifest boundaries",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("animation guidance missing %q: %s", expected, prompt)
		}
	}
}

func TestWriteDesignerAnimationGuidanceAddsProfileSpecificTips(t *testing.T) {
	tests := []struct {
		profile string
		want    []string
	}{
		{profile: "spatial_3d", want: []string{"bare specifier `three`", "Reuse geometries", "instancing", "dispose every GPU resource"}},
		{profile: "vector_playback", want: []string{"native timeline", "second JavaScript RAF", "destroy the player"}},
		{profile: "final_render", want: []string{"native MP4 playback", "poster/static first frame", "release playback resources"}},
	}
	for _, test := range tests {
		t.Run(test.profile, func(t *testing.T) {
			var b strings.Builder
			writeDesignerAnimationGuidance(&b, test.profile)
			prompt := b.String()
			for _, expected := range test.want {
				if !strings.Contains(prompt, expected) {
					t.Fatalf("%s guidance missing %q: %s", test.profile, expected, prompt)
				}
			}
		})
	}
}

func TestWriteDesignerAnimationGuidanceSkipsMissingProfile(t *testing.T) {
	var b strings.Builder
	writeDesignerAnimationGuidance(&b, " ")
	if b.Len() != 0 {
		t.Fatalf("missing profile emitted guidance: %q", b.String())
	}
}
