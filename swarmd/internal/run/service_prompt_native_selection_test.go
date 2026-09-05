package run

import (
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: durable native selections become provider-only exact source context.
// Threat: losing Part targeting, echoing internal instructions into stored chat,
// or routing a native revision through legacy authority. buildInput is the narrow
// provider projection boundary; it must not mutate the source message.
func TestNativeArtifactSelectionProviderContext(t *testing.T) {
	commit := strings.Repeat("a", 40)
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "source", ArtifactID: "artifact", RevisionRef: "revision-" + commit, CommitOID: commit, ProjectionSeq: 17, TargetPartIDs: &[]string{"pricing"}, Action: "use"}
	messages := []pebblestore.MessageSnapshot{{Role: "user", Content: "Make it blue.", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{ref}}}
	input := buildInput(messages)
	text := input[0]["content"].([]map[string]any)[0]["text"].(string)
	for _, want := range []string{"Make it blue.", "artifact_v3_source=", `"target_part_ids":["pricing"]`, `"projection_seq":17`, "revision-" + commit, "read_v3/revise_v3"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if messages[0].Content != "Make it blue." || messages[0].ArtifactSelections[0].PendingRequest != "" {
		t.Fatal("provider context polluted visible message")
	}
	if strings.Contains(text, "Use manage_artifact get/read") || strings.Contains(text, "collection_id=") {
		t.Fatal("native selection received legacy routing")
	}
	ref.CollectionID = "legacy"
	if got := AttachedArtifactSelectionsForProvider([]pebblestore.SessionArtifactSelectionReference{ref}); got != "" {
		t.Fatal("mixed selection projected")
	}
	ref.CollectionID = ""
	ref.ProjectionSeq = 0
	if got := AttachedArtifactSelectionsForProvider([]pebblestore.SessionArtifactSelectionReference{ref}); got != "" {
		t.Fatal("unresolved selection projected")
	}
}
