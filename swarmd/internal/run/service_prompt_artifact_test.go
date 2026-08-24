package run

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBuildInputProjectsAttachedArtifactSelectionsWithoutBytes(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{{
		Role:    "user",
		Content: "Please inspect this design.",
		ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{{
			SessionID: "source-session", CollectionID: "collection-1", VariantID: "variant-2", EventSeq: 41,
			Label: "Compact navigation", Description: "Reviewed option", Action: "use",
		}},
	}}
	input := buildInput(messages)
	if len(input) != 1 {
		t.Fatalf("input = %#v", input)
	}
	content := input[0]["content"].([]map[string]any)[0]["text"].(string)
	for _, want := range []string{"Compact navigation", "Reviewed option", "session_id=source-session", "collection_id=collection-1", "variant_id=variant-2", "event_seq=41", "manage_artifact get/read", "application/zip", "selected ready image can be remixed repeatedly", "image_capabilities", "generate_image", "source_event_seq", "do not re-prompt from scratch"} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider content missing %q: %s", want, content)
		}
	}
	for _, forbidden := range []string{"digest_sha256", "storage_path", "blob_key", `"content":"<html>`} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("provider content exposed %q: %s", forbidden, content)
		}
	}
}

func TestBuildInputProjectsPendingArtifactStudioUpdateWithoutVisiblePromptDump(t *testing.T) {
	input := buildInput([]pebblestore.MessageSnapshot{{
		Role: "user", Content: "Make it cleaner.", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{{
			SessionID: "source-session", CollectionID: "collection-1", VariantID: "variant-2", EventSeq: 41,
			Label: "Active branch", Action: "use", PendingRequest: "Create five alternatives for section 03B from this exact head.",
		}},
	}})
	content := input[0]["content"].([]map[string]any)[0]["text"].(string)
	for _, want := range []string{"Make it cleaner.", "Pending Artifact Studio update", "Create five alternatives for section 03B", "session_id=source-session"} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider content missing %q: %s", want, content)
		}
	}
}

func TestBuildInputProjectsAuthoritativeChainedIterationSelectionBeforePendingTarget(t *testing.T) {
	input := buildInput([]pebblestore.MessageSnapshot{{
		Role: "user", Content: "Fix 3A and show me particle swarm finders.", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{{
			SessionID: "source-session", CollectionID: "collection-1", VariantID: "variant-3", EventSeq: 42,
			Label: "Iteration 3: Luminous Branching Paths", Action: "use",
			IterationID: "iteration-3", IterationIndex: 3, IterationLabel: "Luminous Branching Paths", IterationTheme: "branching paths",
			IterationSectionID: "step-03-find", IterationSectionLabel: "03A · FIND · PARALLEL FINDERS", IterationSectionStartMs: 21000, IterationSectionEndMs: 28000,
			PendingRequest: "Create five alternatives for section 03C.",
		}},
	}})
	content := input[0]["content"].([]map[string]any)[0]["text"].(string)
	for _, want := range []string{"Selected chained iteration metadata", "iteration_id=iteration-3", "iteration_index=3", `iteration_label="Luminous Branching Paths"`, `selected_iteration_section_target={"id":"step-03-find","label":"03A · FIND · PARALLEL FINDERS","start_ms":21000,"end_ms":28000}`, "distinct from any pending next-step target", "Create five alternatives for section 03C"} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider content missing %q: %s", want, content)
		}
	}
	if strings.Index(content, "Selected chained iteration metadata") > strings.Index(content, "Pending Artifact Studio update") {
		t.Fatalf("selected iteration metadata must precede pending target context: %s", content)
	}
}

func TestPendingArtifactStudioUpdateRequiresUseAction(t *testing.T) {
	selection := map[string]any{
		"session_id": "source-session", "collection_id": "collection-1", "variant_id": "variant-2", "event_seq": 41,
		"label": "Active branch", "action": "select", "pending_request": "Hidden update",
	}
	if got := attachedArtifactSelectionsForProvider(map[string]any{"artifact_selections": []any{selection}}); got != "" {
		t.Fatalf("pending request projected without use action: %q", got)
	}
}

func TestBuildInputProjectsSelectedVideoProjectAndRevisionContext(t *testing.T) {
	input := buildInput([]pebblestore.MessageSnapshot{{
		Role: "user", Content: "Make the transition longer.", Metadata: map[string]any{
			"creative_mode": "video", "video_project_id": "vproj_selected", "video_revision_id": "vrev_selected",
		},
	}})
	if len(input) != 1 {
		t.Fatalf("input = %#v", input)
	}
	content := input[0]["content"].([]map[string]any)[0]["text"].(string)
	for _, want := range []string{"selected_project_id=vproj_selected", "selected_revision_id=vrev_selected", "typed source_video operations", "Verify the durable project with manage_video", "visual review objects", "never prose-only storyboards or detached HTML/Markdown deliverables", "actual ready 16:9 image slide for every planned part", "plan.kind=initial", "complete exact ready visual reference", "plan.kind=revision", "select which proposed replacement parts to accept"} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider content missing %q: %s", want, content)
		}
	}
}

func TestBuildInputProjectsSelectedVideoStepAndPlayheadContext(t *testing.T) {
	input := buildInput([]pebblestore.MessageSnapshot{{
		Role: "user", Content: "Add a visual here.", Metadata: map[string]any{
			"creative_mode": "video", "video_project_id": "vproj_selected", "video_revision_id": "vrev_selected",
			"video_anchor_clip_id": "step-bass-design", "video_playhead_ms": float64(12500),
		},
	}})
	content := input[0]["content"].([]map[string]any)[0]["text"].(string)
	for _, want := range []string{"selected_step_anchor=step-bass-design", "selected_playhead_ms=12500", "Preserve supplied stable step anchors", "create only the requested replacement visual"} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider content missing %q: %s", want, content)
		}
	}
}

func TestBuildInputProjectsSelectedVideoTransitionContext(t *testing.T) {
	input := buildInput([]pebblestore.MessageSnapshot{{
		Role: "user", Content: "Make this transition slower.", Metadata: map[string]any{
			"creative_mode": "video", "video_project_id": "vproj_selected", "video_revision_id": "vrev_selected",
			"video_anchor_clip_id": "step-2", "video_playhead_ms": float64(9000), "video_selection_kind": "transition",
			"video_transition_id": "transition-1", "video_transition_kind": "crossfade",
			"video_transition_from_clip_id": "step-1", "video_transition_to_clip_id": "step-2",
			"video_transition_duration_ms": float64(350),
		},
	}})
	content := input[0]["content"].([]map[string]any)[0]["text"].(string)
	for _, want := range []string{"selected_context_kind=transition", "selected_transition_id=transition-1", "selected_transition_kind=crossfade", "selected_transition_from_step=step-1", "selected_transition_to_step=step-2", "selected_transition_duration_ms=350"} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider content missing %q: %s", want, content)
		}
	}
}

func TestMasterHarnessPromptGuidesPriorArtifactWorkspaceWorkflow(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, want := range []string{
		"use manage_artifact search with bounded filters instead of scanning transcripts, session folders, or storage paths",
		"ask the user to disambiguate equally plausible human-named matches",
		"copy next_cursor back unchanged as cursor",
		"publish it with manage_artifact create/create_package",
		"do not materialize, stage, or duplicate it in the workspace merely for submission",
		"materialize the selected complete exact reference",
		"atomic materialize_batch",
		"normal workspace read/edit/write tools",
		"Use publish_workspace only when the intended end product is a workspace file or package",
		"all four source_* lineage fields",
		"artifact remains available but is too large for bounded tool output",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master prompt missing artifact workflow guidance %q", want)
		}
	}
}

func TestMasterHarnessPromptGuidesManagedArtifactParts(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, want := range []string{
		"Managed artifact `parts` are the durable review/edit targets shown by Artifact Studio",
		"include accurate `parts` in the same manage_artifact create/create_package call",
		"temporal requires start_ms/end_ms in milliseconds",
		"spatial requires normalized x/y/width/height within 0..1",
		"semantic is for a named conceptual region with no stronger locator",
		"Do not omit parts for an explicitly sectioned/editable deliverable",
		"Every derived complete revision must submit its own accurate parts",
		"mirror every canonical manifest section as a temporal part with the exact same id, label, start_ms, and end_ms",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master prompt missing managed artifact parts guidance %q", want)
		}
	}
}

func TestMasterHarnessPromptGuidesNormalizedHTMLStillExportAndPendingVideoPlan(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, want := range []string{
		"normalized swarm.capture/v1 contract",
		"id swarm-capture-manifest",
		"globalThis.__SWARM_CAPTURE_V1__",
		"data-swarm-capture-ui",
		"data-swarm-capture-blocking",
		"action=export_html_stills",
		"complete exact session_id, collection_id, variant_id, and event_seq",
		"managed image/png variants",
		"manage_video propose_plan part.visual values",
		"never accept it or start final rendering for the user",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master prompt missing HTML still workflow guidance %q", want)
		}
	}
}

func TestMasterHarnessPromptGuidesDeterministicHTMLAnimationExport(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, want := range []string{
		"separate swarm.animation/v1 HTML contract",
		"#swarm-animation-manifest",
		"globalThis.__SWARM_ANIMATION_V1__",
		"ready()",
		"seek(timeMs)",
		"deterministic seek API does not create live playback",
		"Artifact Studio is not required to call seek continuously",
		"one self-starting requestAnimationFrame scheduler driven by performance.now()",
		"share one renderAt(timeMs) function with ready/seek/stop",
		"Never publish an animation that only renders frame zero and waits for host seek calls",
		"swarm-player/v1 sandbox bridge before DOMContentLoaded",
		"{protocol: 'swarm-player/v1', id: request.id, ok: true, result}",
		"Artifact Studio may send stop immediately before describe on first load",
		"describe returns that exact manifest in result and must resume the artifact-owned scheduler",
		"section buttons, active states, iteration sections, and managed temporal parts must all use the same IDs and exact time boundaries",
		"action=export_html_animation",
		"renderer samples canonical timestamps",
		"silent managed video/mp4",
		"managed_artifact video timeline clip",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master prompt missing HTML animation workflow guidance %q", want)
		}
	}
}

func TestAttachedArtifactSelectionsProjectsExactTypedPart(t *testing.T) {
	selection := pebblestore.SessionArtifactSelectionReference{SessionID: "source-session", CollectionID: "collection-1", VariantID: "variant-2", EventSeq: 41, Action: "use", PartID: "hero", Part: &pebblestore.SessionArtifactPart{ID: "hero", Label: "Hero", Kind: "spatial", X: .1, Y: .2, Width: .7, Height: .5}}
	got := AttachedArtifactSelectionsForProvider([]pebblestore.SessionArtifactSelectionReference{selection})
	for _, want := range []string{"Selected Artifact Studio part", `"kind":"spatial"`, `"x":0.1`, `"width":0.7`} {
		if !strings.Contains(got, want) {
			t.Fatalf("provider context missing %q: %s", want, got)
		}
	}
}

func TestAttachedArtifactSelectionsRejectsIncompleteOrUnboundedMetadata(t *testing.T) {
	if got := attachedArtifactSelectionsForProvider(map[string]any{"artifact_selections": []any{map[string]any{"session_id": "source-session", "variant_id": "variant-1"}}}); got != "" {
		t.Fatalf("incomplete selection projected: %q", got)
	}
	many := make([]any, maxProviderArtifactSelections+1)
	for index := range many {
		many[index] = map[string]any{"session_id": "source", "collection_id": "collection", "variant_id": "variant", "event_seq": index + 1}
	}
	if got := attachedArtifactSelectionsForProvider(map[string]any{"artifact_selections": many}); got != "" {
		t.Fatalf("unbounded selections projected: %q", got)
	}
}
