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
		"every text/html document declaring swarm.animation/v1 must carry the backend-resolved reviewed animation profile",
		"never publish declared animation HTML through an unprofiled direct create",
		"server-owned animation preflight passed runtime binding, artifact-owned live playback, exact seek, stable-pixel, and viewport-containment checks",
		"exactly three `animation_inspection_references`",
		"start, resolved-phrase/middle, and exit frames",
		"start inspection is exactly time_ms=0",
		"substantive, intentional opening composition",
		"blank, near-black, empty-background, or fade-in-only prelude",
		"Call media_inspect once on each complete exact image reference",
		"never pass the text/html source reference to media_inspect",
		"clipping/overflow",
		"scrollbars/capture chrome",
		"failed slot",
		"requested successful variant slot",
		"simulated_agents as the actual logical agent count",
		"rendered_micro_motes as the actual independently rendered micro-particle count",
		"visual_density_target as the perceptual density goal",
		"state the smaller actual count honestly",
		"motion_ui performance tips",
		"one requestAnimationFrame owner",
		"trusted renderer already captures the document at exactly 1920x1080",
		"direct fixed 1920x1080 stage at x=0/y=0",
		"no responsive scale/min(vw/1920,vh/1080) wrapper",
		"trusted viewport audit rejects every visible element or pseudo-element",
		"html/body margin 0, width/height 100%, overflow hidden, and border-box sizing",
		"use opacity, masks, inset clipping, or fully in-frame transforms",
		"not live merely because it implements swarm.animation/v1",
		"not required to call seek() continuously",
		"must start visibly animating",
		"without a Play button",
		"one pure renderAt(timeMs) function",
		"start exactly one requestAnimationFrame loop",
		"derive time from performance.now() and a rebased epoch",
		"document.visibilitychange",
		"rebase the epoch from the paused animation time",
		"parser-executed classic script in `head`",
		"not a deferred, async, module, DOMContentLoaded, or dynamically inserted script",
		"parser-time bootstrap minimal and syntactically conservative",
		"defer DOM node lookup, Path2D/canvas/scene construction, font/layout work, and scheduler startup",
		"any earlier parse-time exception or syntax error makes the trusted lifecycle indistinguishable from a missing runtime",
		"If globalThis.__SWARM_ANIMATION_BIND__ is a function, call it immediately with the runtime",
		"treat a false return as failure",
		"syntax-check the exact complete classic script",
		"never infer parser success merely because the source text contains __SWARM_ANIMATION_BIND__",
		"do not assign globalThis.__SWARM_ANIMATION_V1__ because trusted preflight owns that immutable proxy",
		"Otherwise, for standalone preview, assign the runtime to globalThis.__SWARM_ANIMATION_V1__",
		"Do not defer either branch behind a promise, event, import, or asset load",
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
