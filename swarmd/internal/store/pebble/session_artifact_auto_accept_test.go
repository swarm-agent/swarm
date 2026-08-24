package pebblestore

import (
	"strings"
	"testing"
)

func TestArtifactSoleReadyCandidateBecomesImmediateContinuationHead(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-auto-head")

	apply := func(request, kind string, mutation V3ArtifactMutation) V3SessionMutationResult {
		t.Helper()
		result, err := applyV3ArtifactMutationForTest(sessions, V3SessionMutationInput{
			SessionID: "artifact-auto-head", UserID: "user-1", AccountScopeID: "account-1",
			ClientRequestID: request, PayloadHash: request, Kind: kind, Artifact: &mutation, NowUnixMs: 1000,
		})
		if err != nil {
			t.Fatalf("%s: %v", request, err)
		}
		return result
	}

	apply("root-create", V3SessionMutationCreateArtifact, V3ArtifactMutation{
		Collection: SessionArtifactCollection{ID: "root-collection", Name: "Root"},
		Variant:    &SessionArtifactVariant{ID: "root-variant", Filename: "root.html", MediaType: "text/html", RevisionRoundID: "root-step", AutoAccept: true},
	})
	rootReady := apply("root-ready", V3SessionMutationFinalizeArtifact, V3ArtifactMutation{
		Collection: SessionArtifactCollection{ID: "root-collection"},
		Variant:    &SessionArtifactVariant{ID: "root-variant", Filename: "root.html", MediaType: "text/html", DigestSHA256: strings.Repeat("a", 64), Size: 1},
	})
	root := rootReady.Artifact.Variant
	if rootReady.Artifact.Collection.SelectedVariantID != root.ID || rootReady.Artifact.Chain == nil || rootReady.Artifact.Step == nil {
		t.Fatalf("root finalization did not project an accepted head: %+v", rootReady.Artifact)
	}
	if !sameArtifactReference(rootReady.Artifact.Chain.Head, artifactSelectionForVariant(*root)) || rootReady.Artifact.Step.Accepted == nil || !sameArtifactReference(*rootReady.Artifact.Step.Accepted, artifactSelectionForVariant(*root)) {
		t.Fatalf("root accepted graph = chain=%+v step=%+v root=%+v", rootReady.Artifact.Chain, rootReady.Artifact.Step, root)
	}

	// The exact ready reference returned by initial publication can immediately
	// parent another AI-authored revision without a separate select mutation.
	lineage := SessionArtifactLineage{SourceSessionID: root.SessionID, SourceCollectionID: root.CollectionID, SourceVariantID: root.ID, SourceEventSeq: root.EventSeq}
	apply("revision-create", V3SessionMutationCreateArtifact, V3ArtifactMutation{
		Collection: SessionArtifactCollection{ID: "revision-collection", Name: "Revision"},
		Variant:    &SessionArtifactVariant{ID: "revision-variant", Filename: "revision.html", MediaType: "text/html", RevisionRoundID: "revision-step", Lineage: lineage, AutoAccept: true},
	})
	revisionReady := apply("revision-ready", V3SessionMutationFinalizeArtifact, V3ArtifactMutation{
		Collection: SessionArtifactCollection{ID: "revision-collection"},
		Variant:    &SessionArtifactVariant{ID: "revision-variant", Filename: "revision.html", MediaType: "text/html", DigestSHA256: strings.Repeat("b", 64), Size: 1},
	})
	revision := revisionReady.Artifact.Variant
	if revision.RevisionNumber != 2 || revisionReady.Artifact.Collection.SelectedVariantID != revision.ID || revisionReady.Artifact.Chain == nil || !sameArtifactReference(revisionReady.Artifact.Chain.Head, artifactSelectionForVariant(*revision)) {
		t.Fatalf("revision did not continue and advance the accepted head: %+v", revisionReady.Artifact)
	}

	// Explicitly selecting the already accepted sole candidate remains a valid,
	// idempotent user confirmation rather than conflicting with auto-acceptance.
	selected := apply("revision-select", V3SessionMutationSelectArtifact, V3ArtifactMutation{
		Collection: SessionArtifactCollection{ID: revision.CollectionID},
		Selection:  &SessionArtifactSelectionReference{SessionID: revision.SessionID, CollectionID: revision.CollectionID, VariantID: revision.ID, EventSeq: revision.EventSeq},
	})
	if selected.Artifact.Selection == nil || selected.Artifact.Collection.SelectedVariantID != revision.ID || selected.Artifact.Chain == nil || !sameArtifactReference(selected.Artifact.Chain.Head, artifactSelectionForVariant(*revision)) {
		t.Fatalf("explicit confirmation changed the accepted head: %+v", selected.Artifact)
	}

	// A multi-candidate review round is never auto-accepted. The trusted flag is
	// absent for alternatives, so finalization leaves the head unchanged until an
	// explicit candidate selection advances it.
	for index, id := range []string{"choice-a", "choice-b"} {
		apply("choice-create-"+id, V3SessionMutationCreateArtifact, V3ArtifactMutation{
			Collection: SessionArtifactCollection{ID: "choice-collection", Name: "Choices"},
			Variant:    &SessionArtifactVariant{ID: id, Filename: id + ".html", MediaType: "text/html", RevisionRoundID: "choice-step", CandidateIndex: index + 1, Lineage: SessionArtifactLineage{SourceSessionID: revision.SessionID, SourceCollectionID: revision.CollectionID, SourceVariantID: revision.ID, SourceEventSeq: revision.EventSeq}},
		})
		apply("choice-ready-"+id, V3SessionMutationFinalizeArtifact, V3ArtifactMutation{
			Collection: SessionArtifactCollection{ID: "choice-collection"},
			Variant:    &SessionArtifactVariant{ID: id, Filename: id + ".html", MediaType: "text/html", DigestSHA256: strings.Repeat(string(rune('c'+index)), 64), Size: 1},
		})
	}
	choiceStep, ok, err := sessions.GetSessionArtifactStep("account-1", "user-1", revision.ArtifactChainID, "choice-step")
	if err != nil || !ok || len(choiceStep.Candidates) != 2 || choiceStep.Accepted != nil {
		t.Fatalf("multi-candidate step was implicitly accepted: %+v ok=%t err=%v", choiceStep, ok, err)
	}
	choiceChain, ok, err := sessions.GetSessionArtifactChain("account-1", "user-1", revision.ArtifactChainID)
	if err != nil || !ok || !sameArtifactReference(choiceChain.Head, artifactSelectionForVariant(*revision)) {
		t.Fatalf("multi-candidate finalization moved the accepted head: %+v ok=%t err=%v", choiceChain, ok, err)
	}
}
