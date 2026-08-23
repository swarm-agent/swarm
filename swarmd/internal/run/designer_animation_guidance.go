package run

import "strings"

// writeDesignerAnimationGuidance appends the invariant authoring guidance that
// accompanies every backend-resolved Designer animation profile. The resolved
// profile remains the resource authority; this text turns its budgets into
// concrete hot-loop, frame-pacing, and lifecycle practices for the worker.
func writeDesignerAnimationGuidance(b *strings.Builder, profileID string) {
	if b == nil || strings.TrimSpace(profileID) == "" {
		return
	}
	b.WriteString("- animation runtime contract: use only the pinned local runtime(s); no CDN, remote imports, arbitrary package execution, or network access. Honor lifecycle and resource budgets, pause offscreen, stop when hidden, provide a static first frame for reduced motion, and clean up tickers, listeners, workers, geometries, materials, textures, and WebGL contexts. Use CSS/WAAPI/SVG for motion_ui, optional Three.js only for spatial_3d, dotLottie/Rive only for imported vector_playback, and MP4 playback for final_render. Import only assets the user owns or has licensed for the intended commercial use and redistribution; never source marketplace/community assets by default.\n")
	b.WriteString("- animation quality and frame-pacing contract: optimize for smooth display-refresh playback without hard-coding 60 FPS or claiming a measured frame rate without profiling evidence. Keep one authoritative animation scheduler; drive progress from monotonic timestamps rather than per-frame increments; clamp or rebase elapsed time after pauses so resume never jumps. For finite motion, settle on a static final frame and cancel the scheduler.\n")
	b.WriteString("- animation hot-loop contract: precompute and cache invariant geometry, paths, gradients, sprites, text metrics, and lookup data. Reuse buffers and objects; avoid per-frame allocation, DOM creation/removal, selector or layout reads, style-string churn, asset decoding, shader compilation, and repeated listener registration. Batch reads before writes. Prefer transform and opacity for DOM motion, avoid large paint-heavy animated blur/filter/box-shadow/clip-path effects, cap canvas resolution and device pixel ratio to the profile budgets, and reduce scene detail or update frequency when sustained frame time exceeds the available refresh budget.\n")

	switch strings.TrimSpace(profileID) {
	case "motion_ui":
		b.WriteString("- motion_ui performance tips: use CSS/WAAPI transform and opacity for simple element motion, Canvas 2D for dense changing scenes, and SVG primarily for static or low-count vector structure. Do not mutate many DOM/SVG attributes or filters every frame. Size canvases from their CSS display size at the capped DPR, pre-render invariant layers or sprites to reusable buffers, and keep one requestAnimationFrame owner for Canvas work.\n")
		b.WriteString("- motion_ui live-preview contract: the artifact's authored animation scheduler is the sole playback engine and must start natively when its managed preview document becomes visible and motion is allowed; it must not require a Play button or other capture UI that embedding may hide. Pause while hidden, resume from monotonic time without a jump, and honor reduced motion with the static first frame. If the artifact also implements swarm.animation/v1 for deterministic export, keep ready()/seek() as a separate random-access capture interface; do not use an external wall-clock seek driver or create a second scheduler for live playback.\n")
	case "spatial_3d":
		b.WriteString("- Three.js authoring contract: the pinned Three.js runtime is an ES module supplied through Swarm's trusted import map. Use a nonce-preserved `<script type=\"module\">` and import from the bare specifier `three`; never use a CDN URL or remote import. Reuse geometries, materials, textures, vectors, and render targets; prefer instancing for repeated meshes; compile and load before the hot loop; resize only when the display size changes; cap renderer pixel ratio to the profile budget; and dispose every GPU resource on teardown.\n")
	case "vector_playback":
		b.WriteString("- vector_playback performance tips: use the pinned player's native timeline instead of a second JavaScript RAF or polling loop, keep imported artboard/layer/path complexity bounded, avoid duplicated live players, and stop and destroy the player when hidden, offscreen, or unmounted.\n")
	case "final_render":
		b.WriteString("- final_render performance tips: use native MP4 playback rather than recreating rendered motion with live DOM, Canvas, or WebGL work; provide an intentional poster/static first frame and release playback resources when hidden, offscreen, or unmounted.\n")
	}
}
