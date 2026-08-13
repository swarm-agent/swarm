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
	for _, want := range []string{"Compact navigation", "Reviewed option", "session_id=source-session", "collection_id=collection-1", "variant_id=variant-2", "event_seq=41", "manage_artifact get/read", "application/zip", "source_event_seq"} {
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
