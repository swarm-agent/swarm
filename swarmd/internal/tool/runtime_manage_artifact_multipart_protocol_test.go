package tool

import (
	"context"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManagedArtifactMultipartRunRequiresExactBoundedSelection(t *testing.T) {
	source := pebblestore.SessionArtifactSelectionReference{SessionID: "source", CollectionID: "collection", VariantID: "variant", EventSeq: 1}
	composition := pebblestore.SessionArtifactComposition{ArtifactChainID: "chain", Parts: []pebblestore.SessionArtifactCompositionPart{{PartID: "hero"}, {PartID: "footer"}}}
	definitions := []pebblestore.SessionArtifactPartDefinition{{ID: "hero"}, {ID: "footer"}}
	revisions := []pebblestore.SessionArtifactPartRevisionReference{{ArtifactChainID: "chain", PartID: "hero"}, {ArtifactChainID: "chain", PartID: "footer"}}
	run := ArtifactRunContext{SourceArtifact: &source, SourceComposition: &composition, SourcePartDefinitions: definitions, SourcePartRevisions: revisions, CollectionID: "destination", VariantID: "candidate", ArtifactStepID: "step", CandidateIndex: 1}
	resolved, err := managedArtifactFocusedPartRun(WithArtifactRunContext(context.Background(), run))
	if err != nil || len(resolved.SourcePartDefinitions) != 2 || resolved.SourcePartRevision != nil {
		t.Fatalf("resolved multipart context = %#v, %v", resolved, err)
	}

	run.SourcePartRevisions[1].ArtifactChainID = "other"
	if _, err := managedArtifactFocusedPartRun(WithArtifactRunContext(context.Background(), run)); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("cross-chain selection error = %v", err)
	}
}

func TestManagedArtifactMultipartProtocolRejectsSinglePartActions(t *testing.T) {
	source := pebblestore.SessionArtifactSelectionReference{SessionID: "source", CollectionID: "collection", VariantID: "variant", EventSeq: 1}
	composition := pebblestore.SessionArtifactComposition{ArtifactChainID: "chain"}
	run := ArtifactRunContext{
		SourceArtifact: &source, SourceComposition: &composition,
		SourcePartDefinitions: []pebblestore.SessionArtifactPartDefinition{{ID: "hero"}, {ID: "footer"}},
		SourcePartRevisions:   []pebblestore.SessionArtifactPartRevisionReference{{ArtifactChainID: "chain", PartID: "hero"}, {ArtifactChainID: "chain", PartID: "footer"}},
		CollectionID:          "destination", VariantID: "candidate", ArtifactStepID: "step", CandidateIndex: 1,
	}
	runtime := &Runtime{}
	ctx := WithArtifactRunContext(context.Background(), run)
	if _, err := runtime.readManagedArtifactPart(ctx, structPrincipal(), map[string]any{"action": "read_part"}); err == nil || !strings.Contains(err.Error(), "use read_parts") {
		t.Fatalf("read_part multipart error = %v", err)
	}
	if _, err := runtime.publishManagedArtifactPart(ctx, structPrincipal(), "call", "request", map[string]any{"action": "publish_part", "content": "x"}); err == nil || !strings.Contains(err.Error(), "use publish_parts") {
		t.Fatalf("publish_part multipart error = %v", err)
	}
}

func structPrincipal() artifact.Principal { return artifact.Principal{} }
