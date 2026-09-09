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
	for _, want := range []string{"selected_project_id=vproj_selected", "selected_revision_id=vrev_selected", "typed source_video operations", "manage_video action=inspect_context first", "Verify the durable project with manage_video", "visual review objects", "never prose-only storyboards or detached HTML/Markdown deliverables", "actual ready 16:9 image slide for every planned part", "plan.kind=initial", "complete exact ready visual reference", "plan.kind=revision", "select which proposed replacement parts to accept"} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider content missing %q: %s", want, content)
		}
	}
}

func TestBuildInputProjectsDurableVideoLibraryAttachmentSystemContext(t *testing.T) {
	input := buildInput([]pebblestore.MessageSnapshot{{
		Role: "system", Content: "Attached the selected exact video revision.",
		Metadata: map[string]any{"source": "video_library_attachment", "creative_mode": "video", "video_project_id": "destination-project", "video_revision_id": "destination-revision"},
	}})
	if len(input) != 1 {
		t.Fatalf("input = %#v", input)
	}
	content := input[0]["content"].([]map[string]any)[0]["text"].(string)
	for _, want := range []string{"[system] Attached the selected exact video revision.", "Durable Video Studio attachment", "persisted with the session", "selected_project_id=destination-project", "selected_revision_id=destination-revision"} {
		if !strings.Contains(content, want) {
			t.Fatalf("durable video attachment context missing %q: %s", want, content)
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

func TestBuildInputProjectsSelectedStoryboardPartContext(t *testing.T) {
	input := buildInput([]pebblestore.MessageSnapshot{{
		Role: "user", Content: "Replace this storyboard section with the filmed take.", Metadata: map[string]any{
			"creative_mode": "video", "video_project_id": "vproj_selected", "video_revision_id": "vrev_selected", "video_selection_kind": "iteration",
			"video_storyboard_part_id": "intro", "video_storyboard_capture_state_id": "opening", "video_storyboard_production_state": "pending",
			"video_storyboard_filming_requirements": []any{"Locked camera", "Hold final pose"},
		},
	}})
	content := input[0]["content"].([]map[string]any)[0]["text"].(string)
	for _, want := range []string{"selected_storyboard_part_id=intro", "selected_storyboard_capture_state_id=opening", "selected_storyboard_production_state=pending", `selected_storyboard_filming_requirements=["Locked camera","Hold final pose"]`, "Preserve this stable storyboard part", "exact source/still lineage"} {
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

func TestMasterHarnessPromptRequiresExactRenderedPixelVerification(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, want := range []string{
		"use media_inspect with the complete exact ready artifact reference",
		"inspect every exact ready image state",
		"clipping and overflow",
		"aspect ratio and object sizing",
		"requested-element fidelity",
		"text legibility",
		"unintended overlaps",
		"scrollbars or capture chrome/overlays",
		"each state against its brief",
		"renderer does not judge aesthetics",
		"none of those checks substitutes for pixel inspection",
		"new exact-lineage derived revision",
		"never mutate or silently replace the published variant",
		"single-publication Designer repaired its already-published output",
		"report the specific visual defect and bounded limitation honestly",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master prompt missing rendered visual verification guidance %q", want)
		}
	}
}

// Requirement: masterHarnessPrompt explains output-only Part derivation so authors
// cannot request capture controls as immutable targets. This text-contract test
// proves guidance only; tool allocation and browser rejection tests prove behavior.
func TestMasterHarnessPromptGuidesManagedArtifactParts(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, want := range []string{
		"For direct native HTML with animation_profile motion_ui",
		"data-swarm-capture-ui are excluded from derived Parts",
		"explicit Parts must target output regions only",
		"remaining meaningful output regions still required",
		"explicit kind=temporal Parts with stable output-region IDs",
		"start_ms/end_ms within the manifest duration",
		"Never edit the server-owned swarm-artifact.json",
		"retain the exact draft and repair source through its authorized handle",
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
		"#swarm-storyboard-manifest",
		"swarm.storyboard/v1",
		"non-empty filming_requirements",
		"storyboard_handoff",
		"manage_video import_storyboard",
		"Do not stop after HTML authoring or still export",
		"never accept or start final rendering for the user while pending storyboard placeholders remain",
		"manage_video inspect_composition",
		"list_source_roots and browse_source",
		"exact expected_revision_id",
		"update_composition",
		"use inspect_frames on the exact working revision",
		"AI must never accept the proposal or start final rendering",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master prompt missing HTML still workflow guidance %q", want)
		}
	}
}

// Requirement: masterHarnessPrompt distinguishes deterministic native preview
// from live autoplay and retains canonical timing/manifest authority. This narrow
// guidance test does not establish runtime seek or pixel-stability evidence.
func TestMasterHarnessPromptGuidesDeterministicHTMLAnimationExport(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, want := range []string{
		"exactly one #swarm-animation-manifest",
		"application/json, version swarm.animation/v1",
		"duration_ms >=100, fps 1–60, ceil(duration_ms*fps/1000) <=36000 at 1920x1080",
		"matching ready()",
		"acknowledging time_ms",
		"preserve normal controls/autoplay outside capture",
		"renderer-selected deterministic seek, not wall-clock autoplay",
		"seek(ms) must pause every rAF/timer autoplay source",
		"Never edit the server-owned swarm-artifact.json",
		"Omit parts for one whole-animation midpoint sample",
		"even without explicit temporal Parts",
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
