package run

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
