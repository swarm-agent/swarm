package pebblestore

import (
	"fmt"
	"strings"
	"testing"
)

func TestArtifactGitGraphTenCandidatesFinalizeThenAcceptOnce(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-git-graph")
	apply := func(request, kind string, mutation V3ArtifactMutation) (V3SessionMutationResult, error) {
		return sessions.ApplyV3SessionMutation(V3SessionMutationInput{
			SessionID: "artifact-git-graph", UserID: "user-1", AccountScopeID: "account-1",
			ClientRequestID: request, PayloadHash: request, Kind: kind, Artifact: &mutation, NowUnixMs: 1000,
		})
	}
	var ready []SessionArtifactVariant
	for index := 1; index <= 10; index++ {
		id := fmt.Sprintf("candidate-%02d", index)
		created, err := apply("create-"+id, V3SessionMutationCreateArtifact, V3ArtifactMutation{
			Collection: SessionArtifactCollection{ID: "round-one", Name: "Round one"},
			Variant:    &SessionArtifactVariant{ID: id, Filename: id + ".html", MediaType: "text/html", RevisionRoundID: "step-one", CandidateIndex: index},
		})
		if err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if created.Artifact == nil || created.Artifact.Chain == nil || created.Artifact.Step == nil {
			t.Fatalf("create projection %s = %#v", id, created.Artifact)
		}
		finalized, err := apply("ready-"+id, V3SessionMutationFinalizeArtifact, V3ArtifactMutation{
			Collection: SessionArtifactCollection{ID: "round-one"},
			Variant:    &SessionArtifactVariant{ID: id, Filename: id + ".html", MediaType: "text/html", DigestSHA256: strings.Repeat(fmt.Sprintf("%x", index%16), 64), Size: int64(index)},
		})
		if err != nil {
			t.Fatalf("finalize %s: %v", id, err)
		}
		ready = append(ready, *finalized.Artifact.Variant)
	}
	chain, ok, err := sessions.GetSessionArtifactChain("account-1", "user-1", ready[0].ArtifactChainID)
	if err != nil || !ok {
		t.Fatalf("chain: ok=%t err=%v", ok, err)
	}
	if chain.GraphState != SessionArtifactGraphAuthoritative || chain.Head.VariantID != "" {
		t.Fatalf("finalization advanced head: %+v", chain)
	}
	restarted := NewSessionStore(store)
	persistedStep, persistedOK, persistedErr := restarted.GetSessionArtifactStep("account-1", "user-1", chain.ID, "step-one")
	if persistedErr != nil || !persistedOK || len(persistedStep.Candidates) != 10 {
		t.Fatalf("restarted step = %+v ok=%t err=%v", persistedStep, persistedOK, persistedErr)
	}
	step, ok, err := sessions.GetSessionArtifactStep("account-1", "user-1", chain.ID, "step-one")
	if err != nil || !ok || len(step.Candidates) != 10 || step.Accepted != nil {
		t.Fatalf("step = %+v ok=%t err=%v", step, ok, err)
	}
	for index, candidate := range step.Candidates {
		if candidate.VariantID != fmt.Sprintf("candidate-%02d", index+1) || candidate.EventSeq == 0 {
			t.Fatalf("candidate %d = %+v", index, candidate)
		}
	}
	chosen := ready[4]
	accepted, err := apply("accept-five", V3SessionMutationSelectArtifact, V3ArtifactMutation{
		Collection: SessionArtifactCollection{ID: chosen.CollectionID},
		Selection:  &SessionArtifactSelectionReference{SessionID: chosen.SessionID, CollectionID: chosen.CollectionID, VariantID: chosen.ID, EventSeq: chosen.EventSeq},
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Artifact.Chain.Head.VariantID != chosen.ID || accepted.Artifact.Step.Accepted.VariantID != chosen.ID {
		t.Fatalf("accept projection = %+v", accepted.Artifact)
	}
	if _, err := apply("accept-conflict", V3SessionMutationSelectArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: ready[5].CollectionID}, Selection: &SessionArtifactSelectionReference{SessionID: ready[5].SessionID, CollectionID: ready[5].CollectionID, VariantID: ready[5].ID, EventSeq: ready[5].EventSeq}}); err == nil || !strings.Contains(err.Error(), "already accepted") {
		t.Fatalf("conflicting acceptance error = %v", err)
	}
	used, err := apply("use-head", V3SessionMutationSelectArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: chosen.CollectionID}, Selection: &SessionArtifactSelectionReference{SessionID: chosen.SessionID, CollectionID: chosen.CollectionID, VariantID: chosen.ID, EventSeq: chosen.EventSeq, Action: "use"}})
	if err != nil {
		t.Fatalf("use accepted head: %v", err)
	}
	if used.Artifact == nil || used.Artifact.Selection == nil || used.Artifact.Selection.Action != "use" || used.Artifact.Chain == nil || used.Artifact.Chain.Head.VariantID != chosen.ID {
		t.Fatalf("use projection = %+v", used.Artifact)
	}
	if _, err := apply("use-unaccepted", V3SessionMutationSelectArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: ready[5].CollectionID}, Selection: &SessionArtifactSelectionReference{SessionID: ready[5].SessionID, CollectionID: ready[5].CollectionID, VariantID: ready[5].ID, EventSeq: ready[5].EventSeq, Action: "use"}}); err == nil || !strings.Contains(err.Error(), "accepted chain head") {
		t.Fatalf("unaccepted use error = %v", err)
	}

	// Two later turns may start from the same head, but accepting one makes the
	// other's captured parent stale.
	var branches []SessionArtifactVariant
	for index, stepID := range []string{"parallel-a", "parallel-b"} {
		id := fmt.Sprintf("branch-%d", index+1)
		lineage := SessionArtifactLineage{SourceSessionID: chosen.SessionID, SourceCollectionID: chosen.CollectionID, SourceVariantID: chosen.ID, SourceEventSeq: chosen.EventSeq}
		if _, err := apply("create-"+id, V3SessionMutationCreateArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: id, Name: id}, Variant: &SessionArtifactVariant{ID: id, Filename: id + ".html", MediaType: "text/html", RevisionRoundID: stepID, Lineage: lineage}}); err != nil {
			t.Fatal(err)
		}
		finalized, err := apply("ready-"+id, V3SessionMutationFinalizeArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: id}, Variant: &SessionArtifactVariant{ID: id, Filename: id + ".html", MediaType: "text/html", DigestSHA256: strings.Repeat(fmt.Sprintf("%x", index+11), 64), Size: 1}})
		if err != nil {
			t.Fatal(err)
		}
		branches = append(branches, *finalized.Artifact.Variant)
	}
	if _, err := apply("accept-branch-a", V3SessionMutationSelectArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: branches[0].CollectionID}, Selection: &SessionArtifactSelectionReference{SessionID: branches[0].SessionID, CollectionID: branches[0].CollectionID, VariantID: branches[0].ID, EventSeq: branches[0].EventSeq}}); err != nil {
		t.Fatal(err)
	}
	if _, err := apply("accept-stale-branch-b", V3SessionMutationSelectArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: branches[1].CollectionID}, Selection: &SessionArtifactSelectionReference{SessionID: branches[1].SessionID, CollectionID: branches[1].CollectionID, VariantID: branches[1].ID, EventSeq: branches[1].EventSeq}}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale acceptance error = %v", err)
	}
}

func TestArtifactGitGraphTypedPartAndLegacyCompatibility(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-git-parts")
	apply := func(request, kind string, mutation V3ArtifactMutation) V3SessionMutationResult {
		result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "artifact-git-parts", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: request, PayloadHash: request, Kind: kind, Artifact: &mutation})
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

	legacy := *ready.Artifact.Variant
	legacy.ArtifactChainID, legacy.ArtifactStepID, legacy.GraphState = "", "", ""
	legacy.ParentArtifact = nil
	projected, chain, err := sessions.ProjectSessionArtifactVariantChain("account-1", "user-1", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if projected.GraphState != SessionArtifactGraphLegacyUnproven || chain.GraphState != SessionArtifactGraphLegacyUnproven || chain.Head.VariantID != "" || chain.Root.VariantID != "" {
		t.Fatalf("legacy projection fabricated graph facts: variant=%+v chain=%+v", projected, chain)
	}
}
