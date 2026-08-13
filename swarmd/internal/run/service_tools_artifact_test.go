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
