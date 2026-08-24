package artifact

import (
	"context"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAuthorityCreatesInitialCompositionFromIndependentPartBytes(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	chainID := pebblestore.RootSessionArtifactChainID(principal.SessionID, "composed", "variant-composed")
	created, err := authority.CreateInitialComposition(context.Background(), principal, CreateInitialCompositionInput{
		CreateInput:     CreateInput{RequestID: "initial-composition", CollectionID: "composed", CollectionName: "Composed", VariantID: "variant-composed", Filename: "preview.txt", MediaType: "text/plain", AutoAccept: true},
		ArtifactChainID: chainID, CompositionID: "composition-1",
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
