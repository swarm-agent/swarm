package artifact

import (
	"context"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAuthorityCreatesInitialCompositionFromIndependentPartBytes(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	chainID := pebblestore.RootSessionArtifactChainID(principal.SessionID, "composed", "variant-composed")
	created, err := authority.CreateInitialComposition(context.Background(), principal, CreateInitialCompositionInput{
		CreateInput:     CreateInput{RequestID: "initial-composition", CollectionID: "composed", CollectionName: "Composed", VariantID: "variant-composed", Filename: "preview.txt", MediaType: "text/plain", AutoAccept: true},
		ArtifactChainID: chainID, CompositionID: "composition-1", Construction: pebblestore.SessionArtifactConstruction{Kind: "concat-v1", Entries: []pebblestore.SessionArtifactConstructionEntry{{PartID: "hero"}, {PartID: "footer"}}},
		Parts: []InitialPartInput{
			{Definition: pebblestore.SessionArtifactPartDefinition{ID: "hero", Label: "Hero", Locator: &pebblestore.SessionArtifactPartLocator{Kind: "semantic"}}, RevisionID: "hero-r1", MediaType: "text/plain", Body: []byte("hero")},
			{Definition: pebblestore.SessionArtifactPartDefinition{ID: "footer", Label: "Footer"}, RevisionID: "footer-r1", MediaType: "text/plain", Body: []byte("footer")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "variant-composed" || created.Composition == nil || len(created.Composition.Parts) != 2 {
		t.Fatalf("created composition = %#v", created)
	}
	reference := created.Composition.Parts[0].Revision
	body, revision, err := authority.ReadPartRevision(context.Background(), principal, reference, 32)
	if err != nil || string(body) != "hero" || revision.ID != "hero-r1" {
		t.Fatalf("read part = %q revision=%#v err=%v", body, revision, err)
	}
	if metadata.variant.DigestSHA256 == "" || metadata.variant.Size <= 0 {
		t.Fatalf("deterministic composition projection metadata missing: %#v", metadata.variant)
	}
	if metadata.readyCalls != 1 || created.Status != pebblestore.SessionArtifactStatusReady || created.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative {
		t.Fatalf("initial composition was not finalized ready: created=%#v calls=%d", created, metadata.readyCalls)
	}
}

func TestAuthorityReadsInitialCompositionThroughDeclaredConstructor(t *testing.T) {
	authority, _, principal := authorityFixture(t)
	chainID := pebblestore.RootSessionArtifactChainID(principal.SessionID, "constructed", "variant")
	created, err := authority.CreateInitialComposition(context.Background(), principal, CreateInitialCompositionInput{
		CreateInput:     CreateInput{RequestID: "constructed", CollectionID: "constructed", CollectionName: "Constructed", VariantID: "variant", Filename: "complete.txt", MediaType: "text/plain"},
		ArtifactChainID: chainID, CompositionID: "composition", Construction: pebblestore.SessionArtifactConstruction{Kind: "concat-v1", Entries: []pebblestore.SessionArtifactConstructionEntry{{PartID: "footer"}, {PartID: "hero"}}},
		Parts: []InitialPartInput{{Definition: pebblestore.SessionArtifactPartDefinition{ID: "hero", Label: "Hero"}, RevisionID: "hero-r1", MediaType: "text/plain", Body: []byte("hero")}, {Definition: pebblestore.SessionArtifactPartDefinition{ID: "footer", Label: "Footer"}, RevisionID: "footer-r1", MediaType: "text/plain", Body: []byte("footer")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := authority.ReadReference(context.Background(), principal, pebblestore.SessionArtifactSelectionReference{SessionID: created.SessionID, CollectionID: created.CollectionID, VariantID: created.ID, EventSeq: created.EventSeq}, 64)
	if err != nil || string(body) != "footerhero" {
		t.Fatalf("constructed body=%q err=%v", body, err)
	}
}

func TestAuthorityPublishesAtomicGroupedMultipartReplacement(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	chainID := pebblestore.RootSessionArtifactChainID(principal.SessionID, "source", "source-variant")
	source, err := authority.CreateInitialComposition(context.Background(), principal, CreateInitialCompositionInput{
		CreateInput:     CreateInput{RequestID: "source", CollectionID: "source", CollectionName: "Source", VariantID: "source-variant", Filename: "complete.txt", MediaType: "text/plain"},
		ArtifactChainID: chainID, CompositionID: "source-composition", Construction: pebblestore.SessionArtifactConstruction{Kind: "concat-v1", Entries: []pebblestore.SessionArtifactConstructionEntry{{PartID: "hero"}, {PartID: "footer"}}},
		Parts: []InitialPartInput{{Definition: pebblestore.SessionArtifactPartDefinition{ID: "hero", Label: "Hero"}, RevisionID: "hero-r1", MediaType: "text/plain", Body: []byte("hero")}, {Definition: pebblestore.SessionArtifactPartDefinition{ID: "footer", Label: "Footer"}, RevisionID: "footer-r1", MediaType: "text/plain", Body: []byte("footer")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata.sourceCollection, metadata.sourceVariant = metadata.collection, source
	metadata.collection = pebblestore.SessionArtifactCollection{ID: "candidates", AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Name: "Candidates", Status: pebblestore.SessionArtifactStatusStaging}
	metadata.variant = pebblestore.SessionArtifactVariant{}
	created, err := authority.PublishPartReplacements(context.Background(), principal, PublishPartReplacementsInput{
		RequestID: "turn-1", CollectionID: "candidates", VariantID: "candidate-1", ArtifactStepID: "step-1", IterationTurnID: "turn-1", IterationGroupID: "group-1", CandidateIndex: 1,
		SourceArtifact: pebblestore.SessionArtifactSelectionReference{SessionID: source.SessionID, CollectionID: source.CollectionID, VariantID: source.ID, EventSeq: source.EventSeq}, SourceComposition: *source.Composition,
		Replacements: []PartReplacementInput{{PartDefinition: source.PartDefinitions[0], SourcePartRevision: source.Composition.Parts[0].Revision, MediaType: "text/plain", Body: []byte("new hero")}, {PartDefinition: source.PartDefinitions[1], SourcePartRevision: source.Composition.Parts[1].Revision, MediaType: "text/plain", Body: []byte("new footer"), Locked: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Composition == nil || len(created.Composition.ParentCommitOIDs) != 1 || created.Composition.IterationGroupID != "group-1" || len(created.Composition.Parts) != 2 || !created.Composition.Parts[1].Locked {
		t.Fatalf("created=%#v", created)
	}
	if created.GraphState != pebblestore.SessionArtifactGraphAuthoritative || created.ParentArtifact == nil || *created.ParentArtifact != (pebblestore.SessionArtifactSelectionReference{SessionID: source.SessionID, CollectionID: source.CollectionID, VariantID: source.ID, EventSeq: source.EventSeq}) || created.RevisionNumber != source.RevisionNumber+1 || created.ArtifactStepID != "step-1" || created.RevisionRoundID != "step-1" {
		t.Fatalf("replacement graph identity = %#v", created)
	}
	for _, slot := range created.Composition.Parts {
		revision := metadata.partRevisions[slot.Revision.OwnerSessionID+"\x00"+slot.Revision.ArtifactChainID+"\x00"+slot.Revision.PartID+"\x00"+slot.Revision.PartRevisionID]
		if len(revision.ParentCommitOIDs) != 1 || revision.IterationTurnID != "turn-1" || revision.IterationGroupID != "group-1" {
			t.Fatalf("revision=%#v", revision)
		}
	}
}

func TestAuthorityPublishesReplacementIntoPreallocatedManagedVariantWithoutMutatingLineage(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	chainID := pebblestore.RootSessionArtifactChainID(principal.SessionID, "source", "source-variant")
	source, err := authority.CreateInitialComposition(context.Background(), principal, CreateInitialCompositionInput{
		CreateInput:     CreateInput{RequestID: "source", CollectionID: "source", CollectionName: "Source", VariantID: "source-variant", Filename: "complete.txt", MediaType: "text/plain"},
		ArtifactChainID: chainID, CompositionID: "source-composition", Construction: pebblestore.SessionArtifactConstruction{Kind: "concat-v1", Entries: []pebblestore.SessionArtifactConstructionEntry{{PartID: "hero"}, {PartID: "footer"}}},
		Parts: []InitialPartInput{{Definition: pebblestore.SessionArtifactPartDefinition{ID: "hero", Label: "Hero"}, RevisionID: "hero-r1", MediaType: "text/plain", Body: []byte("hero")}, {Definition: pebblestore.SessionArtifactPartDefinition{ID: "footer", Label: "Footer"}, RevisionID: "footer-r1", MediaType: "text/plain", Body: []byte("footer")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata.sourceCollection, metadata.sourceVariant = metadata.collection, source
	principal.TaskCallID, principal.ChildSessionID = "call-1", "child-1"
	principal.IterationGroupID, principal.IterationID, principal.IterationIndex = "group-1", "iteration-1", 1
	principal.PartID, principal.PartLabel, principal.PartKind = "hero", "Hero", "semantic"
	sourceReference := pebblestore.SessionArtifactSelectionReference{SessionID: source.SessionID, CollectionID: source.CollectionID, VariantID: source.ID, EventSeq: source.EventSeq}
	placeholderLineage := authority.lineage(principal, CreateInput{SourceSessionID: sourceReference.SessionID, SourceCollectionID: sourceReference.CollectionID, SourceVariantID: sourceReference.VariantID, SourceEventSeq: sourceReference.EventSeq})
	metadata.collection = pebblestore.SessionArtifactCollection{ID: "candidates", AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Name: "Candidates", Status: pebblestore.SessionArtifactStatusStaging}
	placeholderRequirements := &pebblestore.SessionArtifactOutputRequirements{Width: 1920, Height: 1080, AspectRatio: "16:9", Orientation: "landscape", ResolutionSource: "dimensions", RegistryVersion: "requirements-v1"}
	placeholderAnimation := &pebblestore.SessionArtifactAnimationProfile{ProfileID: "motion_ui", RegistryVersion: "animation-v1", RuntimeKind: "native_css_waapi_svg", Budgets: pebblestore.SessionArtifactAnimationBudgets{MaxSimultaneousLivePreviews: 3, MaxDevicePixelRatio: 2, MaxCanvasPixels: 4194304, PauseWhenOffscreen: true, StopWhenDocumentHidden: true, ReducedMotionBehavior: "static_first_frame"}}
	metadata.variant = pebblestore.SessionArtifactVariant{ID: "candidate-1", CollectionID: metadata.collection.ID, AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Status: pebblestore.SessionArtifactStatusStaging, Lineage: placeholderLineage, OutputRequirements: placeholderRequirements, AnimationProfile: placeholderAnimation}
	principal.RunID, principal.PlanID, principal.CheckpointID, principal.AttemptID = "child-run", "child-plan", "child-checkpoint", "child-attempt"

	created, err := authority.PublishPartReplacement(context.Background(), principal, PublishPartReplacementInput{
		RequestID: "turn-1", CallID: "call-1", CollectionID: metadata.collection.ID, VariantID: metadata.variant.ID, ArtifactStepID: "step-1", CandidateIndex: 1,
		SourceArtifact: sourceReference, SourceComposition: *source.Composition, PartDefinition: source.PartDefinitions[0], SourcePartRevision: source.Composition.Parts[0].Revision, MediaType: "text/plain", Body: []byte("new hero"),
	})
	if err != nil {
		t.Fatalf("publish replacement into preallocated variant: %v", err)
	}
	if created.Status != pebblestore.SessionArtifactStatusReady || created.Lineage != placeholderLineage {
		t.Fatalf("published replacement lineage = %#v, want immutable placeholder %#v", created.Lineage, placeholderLineage)
	}
	if !equalOutputRequirements(created.OutputRequirements, placeholderRequirements) || !equalAnimationProfile(created.AnimationProfile, placeholderAnimation) {
		t.Fatalf("published replacement requirements/profile = %#v / %#v, want immutable placeholders %#v / %#v", created.OutputRequirements, created.AnimationProfile, placeholderRequirements, placeholderAnimation)
	}
	if created.Composition == nil || created.Composition.Parts[0].Revision == source.Composition.Parts[0].Revision || created.Composition.Parts[1] != source.Composition.Parts[1] {
		t.Fatalf("published replacement composition = %#v", created.Composition)
	}
}

func TestAuthorityRejectsUnsupportedConstruction(t *testing.T) {
	authority, _, principal := authorityFixture(t)
	chainID := pebblestore.RootSessionArtifactChainID(principal.SessionID, "unsupported", "variant")
	_, err := authority.CreateInitialComposition(context.Background(), principal, CreateInitialCompositionInput{
		CreateInput:     CreateInput{RequestID: "unsupported", CollectionID: "unsupported", CollectionName: "Unsupported", VariantID: "variant", Filename: "complete.bin", MediaType: "application/octet-stream"},
		ArtifactChainID: chainID, CompositionID: "composition", Construction: pebblestore.SessionArtifactConstruction{Kind: "guessed-v1"},
		Parts: []InitialPartInput{{Definition: pebblestore.SessionArtifactPartDefinition{ID: "a", Label: "A"}, RevisionID: "a-r1", MediaType: "text/plain", Body: []byte("a")}, {Definition: pebblestore.SessionArtifactPartDefinition{ID: "b", Label: "B"}, RevisionID: "b-r1", MediaType: "text/plain", Body: []byte("b")}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error=%v", err)
	}
}
