package artifact

import (
	"context"
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
