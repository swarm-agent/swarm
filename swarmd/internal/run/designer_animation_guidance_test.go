package run

import (
	"strings"
	"testing"
)

func TestWriteDesignerAnimationGuidanceUsesArtifactV2CompilerContract(t *testing.T) {
	var b strings.Builder
	writeDesignerAnimationGuidance(&b, "motion_ui")
	prompt := b.String()
	for _, expected := range []string{
		"display-refresh playback",
		"monotonic timestamps",
		"precompute and cache",
		"Artifact V2 animation contract",
		"author only the creative scene and behavior parts",
		"server owns the HTML shell",
		"Never author or publish swarm.animation/v1 HTML",
		"repair only the named exact part revision",
		"motion_ui part contract",
		"typed scene part",
		"typed behavior part",
		"Do not emit HTML, script, manifests, runtime methods, schedulers, browser bridges, capture controls",
		"server-owned fixed stage",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Artifact V2 animation guidance missing %q: %s", expected, prompt)
		}
	}
	for _, forbidden := range []string{
		"trusted manage_artifact publication call",
		"animation_inspection_references",
		"Call media_inspect",
		"parser-executed classic script",
		"__SWARM_ANIMATION_BIND__",
		"swarm-player/v1 message listener",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("Artifact V2 animation guidance retained V1 authoring contract %q: %s", forbidden, prompt)
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
