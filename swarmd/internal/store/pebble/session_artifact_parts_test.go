package pebblestore

import (
	"strings"
	"testing"
)

func TestInitialArtifactCompositionPersistsExactPartsAtomicallyAndSurvivesRestart(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "part-composition")
	digestA, digestB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	definitions := []SessionArtifactPartDefinition{
		{ArtifactChainID: "chain-1", ID: "hero", OwnerSessionID: "part-composition", Label: "Hero", Locator: &SessionArtifactPartLocator{Kind: "spatial", Width: 1, Height: .5}},
		{ArtifactChainID: "chain-1", ID: "footer", OwnerSessionID: "part-composition", Label: "Footer"},
	}
	revisions := []SessionArtifactPartRevision{
		{ArtifactChainID: "chain-1", PartID: "hero", ID: "hero-r1", OwnerSessionID: "part-composition", DigestSHA256: digestA, Size: 4, MediaType: "text/plain"},
		{ArtifactChainID: "chain-1", PartID: "footer", ID: "footer-r1", OwnerSessionID: "part-composition", DigestSHA256: digestB, Size: 6, MediaType: "text/plain"},
	}
	composition := SessionArtifactComposition{ID: "composition-1", ArtifactChainID: "chain-1", OwnerSessionID: "part-composition", Construction: SessionArtifactConstruction{Kind: "concat-v1", Entries: []SessionArtifactConstructionEntry{{PartID: "hero"}, {PartID: "footer"}}}, Parts: []SessionArtifactCompositionPart{
		{PartID: "hero", DefinitionOwnerSessionID: "part-composition", Revision: revisions[0].Reference()},
		{PartID: "footer", DefinitionOwnerSessionID: "part-composition", Revision: revisions[1].Reference()},
	}}
	result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "part-composition", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "composition-create", PayloadHash: "composition-create", Kind: V3SessionMutationCreateArtifact, Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1", Name: "Composed"}, Variant: &SessionArtifactVariant{ID: "variant-1", Filename: "preview.txt", MediaType: "text/plain", ArtifactChainID: "chain-1", PartDefinitions: definitions, Composition: &composition}, PartDefinitions: definitions, PartRevisions: revisions, Composition: &composition}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifact == nil || result.Artifact.Composition == nil || result.Artifact.Variant == nil || result.Artifact.Variant.PartGraphState != SessionArtifactGraphAuthoritative || len(result.Artifact.PartDefinitions) != 2 || len(result.Artifact.PartRevisions) != 2 {
		t.Fatalf("composition projection = %#v", result.Artifact)
	}
	stored, ok, err := sessions.GetSessionArtifactComposition("account-1", "user-1", "part-composition", "chain-1", "composition-1")
	if err != nil || !ok || len(stored.Parts) != 2 || stored.Parts[0].PartID != "hero" || stored.Parts[1].PartID != "footer" {
		t.Fatalf("stored composition = %#v ok=%t err=%v", stored, ok, err)
	}
	revision, ok, err := sessions.GetSessionArtifactPartRevision("account-1", "user-1", "part-composition", "chain-1", "hero", "hero-r1")
	if err != nil || !ok || revision.DigestSHA256 != digestA || revision.Size != 4 {
		t.Fatalf("stored revision = %#v ok=%t err=%v", revision, ok, err)
	}
}

func TestArtifactCompositionFailsClosedForLocatorOnlyMissingForeignOrMismatchedParts(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "part-invalid")
	legacy, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "part-invalid", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "legacy", PayloadHash: "legacy", Kind: V3SessionMutationCreateArtifact, Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "legacy", Name: "Legacy"}, Variant: &SessionArtifactVariant{ID: "legacy", Parts: []SessionArtifactPart{{ID: "hero", Label: "Hero", Kind: "semantic"}}}}})
	if err != nil || legacy.Artifact == nil || legacy.Artifact.Variant == nil || legacy.Artifact.Variant.PartGraphState != SessionArtifactGraphLegacyUnproven {
		t.Fatalf("legacy classification = %#v err=%v", legacy.Artifact, err)
	}
	ref := SessionArtifactPartRevisionReference{ArtifactChainID: "chain-1", PartID: "hero", PartRevisionID: "missing", OwnerSessionID: "part-invalid", DigestSHA256: strings.Repeat("a", 64), Size: 1, MediaType: "text/plain"}
	composition := SessionArtifactComposition{ID: "missing-composition", ArtifactChainID: "chain-1", OwnerSessionID: "part-invalid", Construction: SessionArtifactConstruction{Kind: "concat-v1", Entries: []SessionArtifactConstructionEntry{{PartID: "hero"}}}, Parts: []SessionArtifactCompositionPart{{PartID: "hero", DefinitionOwnerSessionID: "part-invalid", Revision: ref}}}
	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "part-invalid", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "missing", PayloadHash: "missing", Kind: V3SessionMutationCreateArtifact, Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "missing", Name: "Missing"}, Variant: &SessionArtifactVariant{ID: "missing", ArtifactChainID: "chain-1", Composition: &composition}, Composition: &composition}})
	if err == nil || !strings.Contains(err.Error(), "missing or unauthenticated part definition") {
		t.Fatalf("missing composition error = %v", err)
	}
	if _, ok, readErr := sessions.GetSessionArtifactCollection("account-1", "part-invalid", "missing"); readErr != nil || ok {
		t.Fatalf("failed composition partially persisted: ok=%t err=%v", ok, readErr)
	}
}
