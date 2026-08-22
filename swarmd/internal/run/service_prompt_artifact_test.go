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
		"materialize the selected complete exact reference",
		"atomic materialize_batch",
		"normal workspace read/edit/write tools",
		"publish_workspace",
		"all four source_* lineage fields",
		"artifact remains available but is too large for bounded tool output",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master prompt missing artifact workflow guidance %q", want)
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
