package run

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
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
	for _, want := range []string{"selected_project_id=vproj_selected", "selected_revision_id=vrev_selected", "typed source_video operations", "manage_video action=inspect_context first", "Verify the durable project with manage_video", "visual review objects", "never prose-only storyboards or detached HTML/Markdown deliverables", "actual ready image/* or silent video/mp4 artifact for every planned part", "propose_plan once", "convert_artifact_v2", "plan.kind=revision", "select which proposed replacement parts to accept"} {
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
	for _, want := range []string{"selected_step_anchor=step-bass-design", "selected_playhead_ms=12500", "Preserve supplied stable step anchors", "Create only the requested replacement visual"} {
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
func TestArtifactHelpGuidesManagedArtifactParts(t *testing.T) {
	help := tool.ArtifactHelpText("animation")
	for _, want := range []string{
		"Direct native HTML accepts animation_profile motion_ui",
		"data-swarm-capture-ui are excluded from derived Parts",
		"explicit Parts must target output regions only",
		"remaining meaningful output regions still required",
		"explicit kind=temporal Parts with stable output-region IDs",
		"start_ms/end_ms within the manifest duration",
		"Never edit the server-owned swarm-artifact.json",
		"retain the exact draft and repair source through its authorized handle",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("artifact help missing animation parts guidance %q", want)
		}
	}
}

func TestVideoHelpGuidesNormalizedHTMLStillExportAndPendingVideoPlan(t *testing.T) {
	help := tool.VideoHelpText()
	for _, want := range []string{
		"convert_artifact_v2",
		"convert_artifact_v3",
		"propose_plan",
		"create_edit_proposal",
		"inspect_composition",
		"list_source_roots and browse_source",
		"update_composition",
		"AI must never accept them or start final rendering",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("video help missing workflow guidance %q", want)
		}
	}
}

func TestArtifactHelpGuidesDeterministicHTMLAnimationExport(t *testing.T) {
	help := tool.ArtifactHelpText("animation")
	for _, want := range []string{
		"exactly one #swarm-animation-manifest",
		"application/json, version swarm.animation/v1",
		"duration_ms >= 100, fps 1-60, ceil(duration_ms*fps/1000) <= 36000 at 1920x1080",
		"matching ready()",
		"seek must pause all rAF/timers and deterministically render the exact timestamp",
		"Never edit the server-owned swarm-artifact.json",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("artifact help missing HTML animation workflow guidance %q", want)
		}
	}
}

func TestArtifactHelpGuidesVideoGenerationAudioAndSoundDirecting(t *testing.T) {
	help := tool.ArtifactHelpText("video")
	for _, want := range []string{
		"video models like Veo 3.1 natively generate audio and video in one pass; leaving sound unprompted causes hallucinated audio",
		"Always direct the soundscape:",
		"concrete action-tied sound effects (e.g. SFX: heavy metallic latch engaging, boots crunching on gravel)",
		"ambient acoustic scale and room tone (e.g. Ambient noise: deep subterranean rumble, wet cavern drips)",
		"musical score mood (e.g. Music: dark ambient synth with driving sub-bass or Music: none)",
		"Never use quotation marks in video prompts unless spoken dialogue is explicitly requested",
		"Ambient noise: near-total silence, dead room tone, no background music, no dialogue",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("artifact help missing video audio guidance %q", want)
		}
	}
}

func TestArtifactHelpGuidesVideoChainingAndAudioOverride(t *testing.T) {
	help := tool.ArtifactHelpText("video")
	for _, want := range []string{
		"For multi-part video generation, chaining, and audio continuity across multiple clips:",
		"chain_from with the previous video's exact ready reference",
		"manage_artifact action=chain_video",
		"audio_mode ('mix_ducked' to layer continuous music at 100% with ducked native Foley sound effects at 35%, or 'override' for pure music replacement)",
		"manage_artifact action=extract_video_frame with frame='last' or 'first'",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("artifact help missing video chaining guidance %q", want)
		}
	}
}

func TestArtifactHelpGuidesAudioGenerationAndMultipleSoundClips(t *testing.T) {
	help := tool.ArtifactHelpText("audio")
	for _, want := range []string{
		"Before generating audio, call manage_artifact action=\"audio_capabilities\"",
		"When the user asks to generate audio, sound clips, music, or sound effects, use manage_artifact action=generate_audio",
		"The AI can generate multiple sound clips or audio variations in ONE tool call:",
		"prompts: [\"...\", \"...\"] (up to 8 clips",
		"specify count: N (1 to 8) to generate multiple variations from a single prompt",
		"Desktop renders an interactive Sound Clips selector so users can preview and play each clip directly",
		"To iterate, remix, or continue an existing audio artifact, provide source_session_id, source_collection_id, source_variant_id, and source_event_seq",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("artifact help missing audio generation guidance %q", want)
		}
	}
}

func TestMasterHarnessPromptGuidesAudioCapabilitiesPreflight(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, want := range []string{
		"action='audio_capabilities'",
		"inspect the configured audio model, duration limits",
		`manage_artifact (audio capabilities): {"action":"audio_capabilities"}`,
		`manage_artifact (generate audio): {"action":"generate_audio"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master harness prompt missing audio preflight guidance %q", want)
		}
	}
}

func TestMasterHarnessPromptGuidesSpecializedToolHelp(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, want := range []string{
		"Specialized domain tools return their own workflow instructions and schemas on demand:",
		"manage_artifact action=\"help\"",
		"manage_video action=\"help\"",
		"manage-theme action=\"inspect\"",
		"manage-skill action=\"inspect\"",
		"manage_environments action=\"help\"",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master prompt missing specialized tool help guidance %q", want)
		}
	}
}

func TestMasterHarnessPromptRestoresVideoStudioInstructions(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, want := range []string{
		"Video Studio (`manage_video`)",
		"completely different from generating a single AI video",
		"ordered timeline parts (clips)",
		"manage_video action='create_project'",
		"manage_video action='propose_plan'",
		"manage_video (create project)",
		"manage_video (propose visual plan with parts)",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master prompt missing video studio instruction %q", want)
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
