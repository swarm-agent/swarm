package run

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestTaskDelegationTranscriptProjectsAttachedArtifactSelections(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{{
		Role:    "user",
		Content: "Revise the selected design.",
		ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{{
			SessionID: "source-session", CollectionID: "collection-1", VariantID: "variant-2", EventSeq: 41,
			Label: "Compact navigation", Description: "Reviewed option", Action: "use",
		}},
	}}

	transcript := buildTaskParentTranscriptContext(messages)
	for _, want := range []string{"Revise the selected design.", "Compact navigation", "session_id=source-session", "collection_id=collection-1", "variant_id=variant-2", "event_seq=41", "manage_artifact get/read"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("delegation transcript missing %q: %s", want, transcript)
		}
	}
	for _, forbidden := range []string{"digest_sha256", "storage_path", "blob_key"} {
		if strings.Contains(transcript, forbidden) {
			t.Fatalf("delegation transcript exposed %q: %s", forbidden, transcript)
		}
	}
}

func TestTaskDelegationTranscriptKeepsReferencesWhenUserTextIsTruncated(t *testing.T) {
	message := pebblestore.MessageSnapshot{
		Role: "user", Content: strings.Repeat("design prose ", taskDelegationTranscriptMsgChars),
		ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{{
			SessionID: "source-session", CollectionID: "collection-1", VariantID: "variant-2", EventSeq: 41,
		}},
	}
	if got := formatTaskDelegationTranscriptMessage(message); !strings.Contains(got, "session_id=source-session") {
		t.Fatalf("bounded delegation message truncated the attached reference: %q", got)
	}
}

func TestTaskDelegationTranscriptKeepsArtifactOnlyUserMessage(t *testing.T) {
	message := pebblestore.MessageSnapshot{
		Role: "user",
		ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{{
			SessionID: "source-session", CollectionID: "collection-1", VariantID: "variant-2", EventSeq: 41,
		}},
	}
	if got := formatTaskDelegationTranscriptMessage(message); !strings.Contains(got, "session_id=source-session") {
		t.Fatalf("artifact-only message was dropped: %q", got)
	}
}

func TestLatestTaskArtifactUseSelectionPreservesTypedPart(t *testing.T) {
	part := pebblestore.SessionArtifactPart{ID: "hero", Label: "Hero", Kind: "selector", Selector: "#hero"}
	messages := []pebblestore.MessageSnapshot{{Role: "user", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{{SessionID: "s", CollectionID: "c", VariantID: "v", EventSeq: 9, Action: "use", PartID: "hero", Part: &part}}}}
	selected := latestTaskArtifactUseSelection(messages)
	if selected == nil || selected.Part == nil || *selected.Part != part {
		t.Fatalf("selection = %#v", selected)
	}
	target := taskSectionTargetFromArtifactPart(*selected.Part)
	if target.Kind != "selector" || target.Selector != "#hero" {
		t.Fatalf("target = %#v", target)
	}
}

func TestEqualTaskSectionTargetIgnoresAuthenticatedDisplayDescription(t *testing.T) {
	requested := &taskSwarmSectionTarget{ID: "signal", Label: "Signal", Kind: "temporal", EndMs: 2000}
	bound := &taskSwarmSectionTarget{ID: "signal", Label: "Signal", Kind: "temporal", Description: "Opening Signal section.", EndMs: 2000}
	if !equalTaskSectionTarget(requested, bound) {
		t.Fatal("task-callable locator should match the authenticated target even though display description is server-only")
	}
	mismatch := *requested
	mismatch.EndMs = 2001
	if equalTaskSectionTarget(&mismatch, bound) {
		t.Fatal("typed locator differences must still fail closed")
	}
}

func TestManageArtifactToolOutputIsStructured(t *testing.T) {
	output := `{"action":"create","artifact":{"collection_id":"col-1","event_seq":1,"filename":"concept.html","id":"var-1","media_type":"text/html","session_id":"sess-1","status":"ready"},"path_id":"run.manage-artifact.v1","reference":{"collection_id":"col-1","event_seq":1,"session_id":"sess-1","variant_id":"var-1"},"status":"ok","tool":"manage_artifact"}`
	preview, ok := toolHistoryStructuredPayload("manage_artifact", output, `{"action":"create"}`)
	if !ok || preview != output {
		t.Fatalf("toolHistoryStructuredPayload = %q ok=%v, want %q", preview, ok, output)
	}
	previewAlias, ok := toolHistoryStructuredPayload("manage-artifact", output, `{"action":"create"}`)
	if !ok || previewAlias != output {
		t.Fatalf("toolHistoryStructuredPayload with alias = %q ok=%v", previewAlias, ok)
	}
}

// Requirement: a managed Designer handoff must distinguish a concrete render
// failure from a trusted-lineage rejection so the parent can correct the actual
// defect without weakening artifact authority. This unit layer is the narrowest
// proof because the classification is owned by service_tools.go after the
// artifact authority returns the immutable variant.
func TestManagedDesignerArtifactHandoffErrorDistinguishesFailureFromLineage(t *testing.T) {
	run := &tool.ArtifactRunContext{CollectionID: "collection", VariantID: "variant"}
	failed := pebblestore.SessionArtifactVariant{
		ID: "variant", CollectionID: "collection", Status: pebblestore.SessionArtifactStatusFailed, FailureCode: "animation_viewport_overflow",
	}
	if err := managedDesignerArtifactHandoffError(failed, run, true, true); err == nil || !strings.Contains(err.Error(), `failure_code "animation_viewport_overflow"`) || strings.Contains(err.Error(), "lineage") {
		t.Fatalf("concrete artifact failure classification = %v", err)
	}

	ready := failed
	ready.Status = pebblestore.SessionArtifactStatusReady
	ready.FailureCode = ""
	if err := managedDesignerArtifactHandoffError(ready, run, false, true); err == nil || !strings.Contains(err.Error(), "invalid trusted lineage") {
		t.Fatalf("lineage mismatch classification = %v", err)
	}
	if err := managedDesignerArtifactHandoffError(ready, run, true, false); err == nil || !strings.Contains(err.Error(), "invalid trusted composition") {
		t.Fatalf("composition mismatch classification = %v", err)
	}
}

func TestManagedAnimatedDesignerPromptRequiresTrustedPreflightAndThreeFrameInspection(t *testing.T) {
	prompt := buildTaskDelegationPrompt(taskDelegationPromptConfig{
		RequestedSubagent:  "designer",
		OutputMode:         taskOutputModeManaged,
		AnimationProfile:   &pebblestore.SessionArtifactAnimationProfile{ProfileID: "motion_ui"},
		ArtifactRunContext: &tool.ArtifactRunContext{SessionID: "parent", CollectionID: "collection", VariantID: "variant", ArtifactStepID: "step", CandidateIndex: 1},
	})
	for _, want := range []string{
		"ready status is necessary but not sufficient",
		"server-owned runtime binding, exact-seek, stable-pixel, and viewport-containment preflight passed",
		"start, resolved-phrase/middle, and exit frames",
		"animation_inspection_references",
		"never pass the text/html source reference to media_inspect",
		"ANIMATION_INSPECTION frame=start|middle|exit status=pass",
		"clipping/overflow",
		"scrollbars/capture chrome",
		"explicit failed slot",
		"do not count it as a successful variant",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("managed animated Designer prompt missing %q: %s", want, prompt)
		}
	}
}

func TestValidateManagedAnimatedDesignerInspectionEvidenceRequiresThreeToolsAndFrameRecords(t *testing.T) {
	checks := "checks=clipping/overflow; sizing/aspect ratio; requested elements; text legibility; unintended overlaps; scrollbars/capture chrome; brief fidelity evidence=clean"
	report := strings.Join([]string{
		"ANIMATION_INSPECTION frame=start status=pass " + checks,
		"ANIMATION_INSPECTION frame=middle status=pass " + checks,
		"ANIMATION_INSPECTION frame=exit status=pass " + checks,
	}, "\n")
	outcome := taskLaunchOutcome{
		MediaInspectCompleted: 3,
		ArtifactReference:     &taskArtifactReference{Status: pebblestore.SessionArtifactStatusReady},
	}
	if err := validateManagedAnimatedDesignerInspectionEvidence(outcome, report); err != nil {
		t.Fatalf("valid three-frame inspection evidence rejected: %v", err)
	}
	outcome.MediaInspectCompleted = 2
	if err := validateManagedAnimatedDesignerInspectionEvidence(outcome, report); err == nil || !strings.Contains(err.Error(), "three successful media_inspect calls") {
		t.Fatalf("missing tool evidence error = %v", err)
	}
	outcome.MediaInspectCompleted = 3
	if err := validateManagedAnimatedDesignerInspectionEvidence(outcome, strings.Replace(report, "frame=middle status=pass", "frame=middle status=fail", 1)); err == nil || !strings.Contains(err.Error(), "middle") {
		t.Fatalf("failed middle frame error = %v", err)
	}
	for _, malformed := range []string{
		strings.Replace(report, "evidence=clean", "evidence=", 1),
		strings.Replace(report, "frame=start status=pass", "frame=startup status=pass", 1),
		strings.Replace(report, "status=pass checks=", "status=pass status=pass checks=", 1),
		strings.Replace(report, "checks=clipping/overflow", "checks=brief fidelity; clipping/overflow", 1),
	} {
		if err := validateManagedAnimatedDesignerInspectionEvidence(outcome, malformed); err == nil {
			t.Fatalf("malformed inspection record accepted: %q", malformed)
		}
	}
}

func TestManagedStaticDesignerPromptDoesNotRequireAnimationInspection(t *testing.T) {
	prompt := buildTaskDelegationPrompt(taskDelegationPromptConfig{
		RequestedSubagent:  "designer",
		OutputMode:         taskOutputModeManaged,
		ArtifactRunContext: &tool.ArtifactRunContext{SessionID: "parent", CollectionID: "collection", VariantID: "variant", ArtifactStepID: "step", CandidateIndex: 1},
	})
	if strings.Contains(prompt, "representative-frame inspection") || strings.Contains(prompt, "resolved-phrase/middle") {
		t.Fatalf("static Designer prompt received animated inspection contract: %s", prompt)
	}
}

func TestBrainstormingArtifactPromptGuidance(t *testing.T) {
	checkpointPrompt, err := renderCheckpointRunPrompt(checkpointRunPromptPayload{
		Checkpoint: pebblestore.SessionPlanCheckpoint{ID: "cp-1", Title: "Brainstorm"},
		Artifacts:  []pebblestore.SessionPlanArtifactReference{{Path: "docs/spec.md", Role: "input"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"prefer self-contained readable HTML for rich visual deliverables and Markdown for simpler documents",
		"managed artifacts remain in the session without repository writes",
		"exact ready reference for managed artifacts",
	} {
		if !strings.Contains(checkpointPrompt, want) {
			t.Fatalf("checkpoint prompt missing artifact guidance %q: %s", want, checkpointPrompt)
		}
	}
}
