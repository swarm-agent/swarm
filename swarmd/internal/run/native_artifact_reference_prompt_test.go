package run

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: native select attachments are reference-only, not implicit edits
// or head acceptance. Threat: composer reference intent lost at provider hydration.
// AttachedArtifactSelectionsForProvider is the narrowest prompt projection boundary.
func TestNativeArtifactReferenceOnlyPrompt(t *testing.T) {
	ids := []string{"option-part"}
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "source", ArtifactID: "artifact", RevisionRef: "revision-" + strings.Repeat("a", 40), CommitOID: strings.Repeat("a", 40), ProjectionSeq: 7, TargetPartIDs: &ids, Action: "select", RevisionIntent: pebblestore.ArtifactV3RevisionFocusedParts}
	got := AttachedArtifactSelectionsForProvider([]pebblestore.SessionArtifactSelectionReference{ref})
	if !strings.Contains(got, "style/example reference only") || !strings.Contains(got, ref.RevisionRef) || !strings.Contains(got, "option-part") || strings.Contains(got, "artifact_v3_source=") {
		t.Fatalf("reference intent lost: %s", got)
	}
	// The dispatch boundary must not reinterpret this reference or revive an
	// older remix attachment as the current request's source authority.
	older := ref
	older.Action = "use"
	if bound := latestTaskArtifactUseSelection([]pebblestore.MessageSnapshot{
		{Role: "user", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{older}},
		{Role: "user", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{ref}},
	}); bound != nil {
		t.Fatal("reference-only request inherited remix authority")
	}
	ref.Action = "use"
	if got := AttachedArtifactSelectionsForProvider([]pebblestore.SessionArtifactSelectionReference{ref}); !strings.Contains(got, "artifact_v3_source=") {
		t.Fatal("edit intent lost")
	}
	ref.PendingRequest = "untrusted instruction"
	if got := AttachedArtifactSelectionsForProvider([]pebblestore.SessionArtifactSelectionReference{ref}); got != "" {
		t.Fatal("mixed envelope admitted")
	}
}
