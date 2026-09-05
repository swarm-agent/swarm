package run

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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

// Requirement: one or two narration targets hydrated by the durable selection
// authority must reach the provider in one exact native source envelope. Threat:
// dropping a second scene or inventing direction targets. buildInput is the narrow
// projection boundary; it must also leave the durable user message unchanged.
func TestNativeNarrationSelectionProviderTargets(t *testing.T) {
	for _, targets := range [][]string{{"narration-opening"}, {"narration-opening", "narration-resolve"}} {
		t.Run(strings.Join(targets, "+"), func(t *testing.T) {
			commit := strings.Repeat("a", 40)
			ref := pebblestore.SessionArtifactSelectionReference{SessionID: "source", ArtifactID: "narration", RevisionRef: "revision-" + commit, CommitOID: commit, ProjectionSeq: 17, TargetPartIDs: &targets, Action: "use"}
			messages := []pebblestore.MessageSnapshot{{Role: "user", Content: "Shorten the selected narration.", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{ref}}}
			before, _ := json.Marshal(messages)
			input := buildInput(messages)
			text := input[0]["content"].([]map[string]any)[0]["text"].(string)
			marker := "artifact_v3_source="
			start := strings.Index(text, marker)
			if start < 0 {
				t.Fatal("missing native source context")
			}
			var source struct {
				Session    string   `json:"session_id"`
				Artifact   string   `json:"artifact_id"`
				Commit     string   `json:"commit_oid"`
				Projection uint64   `json:"projection_seq"`
				Targets    []string `json:"target_part_ids"`
			}
			if err := json.NewDecoder(strings.NewReader(text[start+len(marker):])).Decode(&source); err != nil {
				t.Fatal(err)
			}
			if source.Session != "source" || source.Artifact != "narration" || source.Commit != commit || source.Projection != 17 || !reflect.DeepEqual(source.Targets, targets) {
				t.Fatalf("wrong exact source: %+v", source)
			}
			after, _ := json.Marshal(messages)
			if string(before) != string(after) {
				t.Fatal("provider context mutated user message")
			}
		})
	}
}
