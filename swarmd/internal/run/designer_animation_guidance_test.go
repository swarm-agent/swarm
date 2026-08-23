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
		"must start natively",
		"must not require a Play button",
		"separate random-access capture interface",
		"do not use an external wall-clock seek driver",
		"do not create a second scheduler",
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
