package pebblestore

import (
	"strings"
	"testing"
)

func TestArtifactGitProjectionRejectsLocatorOnlyPartAuthority(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-git-parts")
	apply := func(request, kind string, mutation V3ArtifactMutation) V3SessionMutationResult {
		result, err := applyV3ArtifactMutationForTest(sessions, V3SessionMutationInput{SessionID: "artifact-git-parts", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: request, PayloadHash: request, Kind: kind, Artifact: &mutation})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	part := SessionArtifactPart{ID: "hero", Label: "Hero", Kind: "spatial", Description: "Exact hero bounds", X: .1, Y: .2, Width: .7, Height: .5}
	apply("part-create", V3SessionMutationCreateArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "parts", Name: "Parts"}, Variant: &SessionArtifactVariant{ID: "with-part", Filename: "part.html", MediaType: "text/html", Parts: []SessionArtifactPart{part}}})
	ready := apply("part-ready", V3SessionMutationFinalizeArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "parts"}, Variant: &SessionArtifactVariant{ID: "with-part", Filename: "part.html", MediaType: "text/html", DigestSHA256: strings.Repeat("a", 64), Size: 1}})
	ref := SessionArtifactSelectionReference{SessionID: "artifact-git-parts", CollectionID: "parts", VariantID: "with-part", EventSeq: ready.Artifact.Variant.EventSeq, PartID: "hero"}
	validated, err := sessions.ValidateSessionArtifactMessageSelections("account-1", "user-1", []SessionArtifactSelectionReference{ref})
	if err == nil || !strings.Contains(err.Error(), "part was not found") || len(validated) != 0 {
		t.Fatalf("locator-only part gained composition authority: %#v err=%v", validated, err)
	}
}
