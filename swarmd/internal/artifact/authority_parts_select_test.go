package artifact

import (
	"context"
	"fmt"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAuthoritySelectsAndLocksExactPartRevisionsAtomically(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	chainID := pebblestore.RootSessionArtifactChainID(principal.SessionID, "source", "source-variant")
	source, err := authority.CreateInitialComposition(context.Background(), principal, CreateInitialCompositionInput{
		CreateInput:     CreateInput{RequestID: "source", CollectionID: "source", CollectionName: "Source", VariantID: "source-variant", Filename: "complete.txt", MediaType: "text/plain", AutoAccept: true},
		ArtifactChainID: chainID, CompositionID: "source-composition", Parts: []InitialPartInput{
			{Definition: pebblestore.SessionArtifactPartDefinition{ID: "hero", Label: "Hero"}, RevisionID: "hero-r1", MediaType: "text/plain", Body: []byte("hero")},
			{Definition: pebblestore.SessionArtifactPartDefinition{ID: "footer", Label: "Footer"}, RevisionID: "footer-r1", MediaType: "text/plain", Body: []byte("footer")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata.sourceCollection, metadata.sourceVariant = metadata.collection, source
	metadata.collection = pebblestore.SessionArtifactCollection{ID: "selections", AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Name: "Selections", Status: pebblestore.SessionArtifactStatusStaging}
	metadata.variant = pebblestore.SessionArtifactVariant{}
	choices := []PartRevisionChoiceInput{
		{PartID: "hero", Revision: source.Composition.Parts[0].Revision, RevisionEventSeq: metadata.partRevisions[principal.SessionID+"\x00"+chainID+"\x00hero\x00hero-r1"].EventSeq, Locked: true},
		{PartID: "footer", Revision: source.Composition.Parts[1].Revision, RevisionEventSeq: metadata.partRevisions[principal.SessionID+"\x00"+chainID+"\x00footer\x00footer-r1"].EventSeq, Locked: true},
	}
	selected, err := authority.SelectPartRevisions(context.Background(), principal, SelectPartRevisionsInput{RequestID: "lock", CollectionID: "selections", VariantID: "locked", ArtifactStepID: "lock-step", SourceArtifact: pebblestore.SessionArtifactSelectionReference{SessionID: source.SessionID, CollectionID: source.CollectionID, VariantID: source.ID, EventSeq: source.EventSeq}, SourceComposition: *source.Composition, Choices: choices})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Composition == nil || len(selected.Composition.ParentCommitOIDs) == 0 || !selected.Composition.Parts[0].Locked || !selected.Composition.Parts[1].Locked {
		t.Fatalf("selected=%#v", selected)
	}
	if selected.Composition.Parts[0].Revision != source.Composition.Parts[0].Revision || selected.Composition.Parts[1].Revision != source.Composition.Parts[1].Revision {
		t.Fatal("selection copied or changed exact part revisions")
	}
	replayed, err := authority.SelectPartRevisions(context.Background(), principal, SelectPartRevisionsInput{RequestID: "lock", CollectionID: "selections", VariantID: "locked", ArtifactStepID: "lock-step", SourceArtifact: pebblestore.SessionArtifactSelectionReference{SessionID: source.SessionID, CollectionID: source.CollectionID, VariantID: source.ID, EventSeq: source.EventSeq}, SourceComposition: *source.Composition, Choices: choices})
	if err != nil || replayed.ID != selected.ID {
		t.Fatalf("idempotent replay=%#v err=%v", replayed, err)
	}
}

func TestAuthorityCompletesThreePartSiblingSelectionAndContinuedIteration(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	chainID := pebblestore.RootSessionArtifactChainID(principal.SessionID, "source", "source-variant")
	root, err := authority.CreateInitialComposition(context.Background(), principal, CreateInitialCompositionInput{
		CreateInput:     CreateInput{RequestID: "source", CollectionID: "source", CollectionName: "Source", VariantID: "source-variant", Filename: "complete.txt", MediaType: "text/plain", AutoAccept: true},
		ArtifactChainID: chainID, CompositionID: "source-composition", Parts: []InitialPartInput{
			{Definition: pebblestore.SessionArtifactPartDefinition{ID: "header", Label: "Header"}, RevisionID: "header-r1", MediaType: "text/plain", Body: []byte("header")},
			{Definition: pebblestore.SessionArtifactPartDefinition{ID: "body", Label: "Body"}, RevisionID: "body-r1", MediaType: "text/plain", Body: []byte("body")},
			{Definition: pebblestore.SessionArtifactPartDefinition{ID: "footer", Label: "Footer"}, RevisionID: "footer-r1", MediaType: "text/plain", Body: []byte("footer")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootReference := pebblestore.SessionArtifactSelectionReference{SessionID: root.SessionID, CollectionID: root.CollectionID, VariantID: root.ID, EventSeq: root.EventSeq}
	rootBody := root.Composition.Parts[1].Revision
	headerBlob, footerBlob := root.Composition.Parts[0].Revision.BlobOID, root.Composition.Parts[2].Revision.BlobOID
	metadata.sourceCollection, metadata.sourceVariant = metadata.collection, root
	metadata.collection = pebblestore.SessionArtifactCollection{ID: "candidates", AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Name: "Body alternatives", Status: pebblestore.SessionArtifactStatusStaging}
	metadata.variant = pebblestore.SessionArtifactVariant{}

	candidates := make([]pebblestore.SessionArtifactVariant, 0, 3)
	for index, body := range []string{"body alpha", "body beta", "body gamma"} {
		metadata.variant = pebblestore.SessionArtifactVariant{}
		candidate, createErr := authority.PublishPartReplacement(context.Background(), principal, PublishPartReplacementInput{
			RequestID: fmt.Sprintf("body-candidate-%d", index+1), CallID: "designer-swarm-1", CollectionID: "candidates", VariantID: fmt.Sprintf("candidate-%d", index+1),
			ArtifactStepID: "body-turn-1", CandidateIndex: index + 1, SourceArtifact: rootReference, SourceComposition: *root.Composition,
			PartDefinition: root.PartDefinitions[1], SourcePartRevision: rootBody, MediaType: "text/plain", Body: []byte(body),
		})
		if createErr != nil {
			t.Fatalf("create sibling %d: %v", index+1, createErr)
		}
		if candidate.Composition.Parts[0].Revision.BlobOID != headerBlob || candidate.Composition.Parts[2].Revision.BlobOID != footerBlob {
			t.Fatalf("candidate %d rewrote untouched blobs: %#v", index+1, candidate.Composition.Parts)
		}
		if len(candidate.ParentCommitOIDs) != 1 || candidate.ParentCommitOIDs[0] != root.CommitOID {
			t.Fatalf("candidate %d is not a sibling of root: %#v", index+1, candidate.ParentCommitOIDs)
		}
		candidates = append(candidates, candidate)
	}

	chosenBody := candidates[1].Composition.Parts[1].Revision
	chosenRevision := metadata.partRevisions[chosenBody.OwnerSessionID+"\x00"+chosenBody.ArtifactChainID+"\x00"+chosenBody.PartID+"\x00"+chosenBody.PartRevisionID]
	metadata.variant = pebblestore.SessionArtifactVariant{}
	metadata.collection = pebblestore.SessionArtifactCollection{ID: "selections", AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Name: "Selections", Status: pebblestore.SessionArtifactStatusStaging}
	accepted, err := authority.SelectPartRevisions(context.Background(), principal, SelectPartRevisionsInput{
		RequestID: "accept-body-beta", CollectionID: "selections", VariantID: "accepted-body-beta", ArtifactStepID: "body-lock-1",
		SourceArtifact: rootReference, SourceComposition: *root.Composition,
		Choices: []PartRevisionChoiceInput{{PartID: "body", Revision: chosenBody, RevisionEventSeq: chosenRevision.EventSeq, Locked: true}},
	})
	if err != nil {
		t.Fatalf("select exact body candidate: %v", err)
	}
	if !accepted.Composition.Parts[1].Locked || accepted.Composition.Parts[1].Revision != chosenBody {
		t.Fatalf("accepted composition did not lock exact candidate: %#v", accepted.Composition)
	}
	if len(accepted.ParentCommitOIDs) != 2 || accepted.ParentCommitOIDs[0] != root.CommitOID || accepted.ParentCommitOIDs[1] != candidates[1].CommitOID {
		t.Fatalf("accepted composition is not the expected multi-parent merge: %#v", accepted.ParentCommitOIDs)
	}
	repo, err := authority.repository(context.Background(), accepted.RepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if official, officialErr := repo.Official(context.Background()); officialErr != nil || official != accepted.CommitOID {
		t.Fatalf("official=%q want=%q err=%v", official, accepted.CommitOID, officialErr)
	}

	acceptedReference := pebblestore.SessionArtifactSelectionReference{SessionID: accepted.SessionID, CollectionID: accepted.CollectionID, VariantID: accepted.ID, EventSeq: accepted.EventSeq}
	metadata.sourceCollection, metadata.sourceVariant = metadata.collection, accepted
	metadata.collection = pebblestore.SessionArtifactCollection{ID: "continued", AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Name: "Continued", Status: pebblestore.SessionArtifactStatusStaging}
	metadata.variant = pebblestore.SessionArtifactVariant{}
	continued, err := authority.PublishPartReplacement(context.Background(), principal, PublishPartReplacementInput{
		RequestID: "continue-footer", CallID: "designer-swarm-2", CollectionID: "continued", VariantID: "footer-candidate", ArtifactStepID: "footer-turn-2", CandidateIndex: 1,
		SourceArtifact: acceptedReference, SourceComposition: *accepted.Composition, PartDefinition: root.PartDefinitions[2], SourcePartRevision: accepted.Composition.Parts[2].Revision,
		MediaType: "text/plain", Body: []byte("new footer"),
	})
	if err != nil {
		t.Fatalf("continue from accepted head: %v", err)
	}
	if len(continued.ParentCommitOIDs) != 1 || continued.ParentCommitOIDs[0] != accepted.CommitOID || continued.Composition.Parts[1].Revision != chosenBody || !continued.Composition.Parts[1].Locked {
		t.Fatalf("continued iteration lost accepted lineage or lock: %#v", continued)
	}
}

func TestAuthorityPartSelectionRejectsDuplicateAndStaleRevisionIdentity(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	chainID := pebblestore.RootSessionArtifactChainID(principal.SessionID, "source", "source-variant")
	source, err := authority.CreateInitialComposition(context.Background(), principal, CreateInitialCompositionInput{CreateInput: CreateInput{RequestID: "source", CollectionID: "source", CollectionName: "Source", VariantID: "source-variant", Filename: "complete.txt", MediaType: "text/plain", AutoAccept: true}, ArtifactChainID: chainID, CompositionID: "source-composition", Parts: []InitialPartInput{{Definition: pebblestore.SessionArtifactPartDefinition{ID: "hero", Label: "Hero"}, RevisionID: "hero-r1", MediaType: "text/plain", Body: []byte("hero")}, {Definition: pebblestore.SessionArtifactPartDefinition{ID: "footer", Label: "Footer"}, RevisionID: "footer-r1", MediaType: "text/plain", Body: []byte("footer")}}})
	if err != nil {
		t.Fatal(err)
	}
	metadata.sourceCollection, metadata.sourceVariant = metadata.collection, source
	metadata.collection = pebblestore.SessionArtifactCollection{ID: "selections", AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Name: "Selections", Status: pebblestore.SessionArtifactStatusStaging}
	metadata.variant = pebblestore.SessionArtifactVariant{}
	ref := source.Composition.Parts[0].Revision
	seq := metadata.partRevisions[principal.SessionID+"\x00"+chainID+"\x00hero\x00hero-r1"].EventSeq
	base := SelectPartRevisionsInput{RequestID: "bad", CollectionID: "selections", VariantID: "bad", ArtifactStepID: "bad-step", SourceArtifact: pebblestore.SessionArtifactSelectionReference{SessionID: source.SessionID, CollectionID: source.CollectionID, VariantID: source.ID, EventSeq: source.EventSeq}, SourceComposition: *source.Composition}
	base.Choices = []PartRevisionChoiceInput{{PartID: "hero", Revision: ref, RevisionEventSeq: seq}, {PartID: "hero", Revision: ref, RevisionEventSeq: seq}}
	if _, err := authority.SelectPartRevisions(context.Background(), principal, base); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error=%v", err)
	}
	base.Choices = []PartRevisionChoiceInput{{PartID: "hero", Revision: ref, RevisionEventSeq: seq + 1}}
	if _, err := authority.SelectPartRevisions(context.Background(), principal, base); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale error=%v", err)
	}
}
