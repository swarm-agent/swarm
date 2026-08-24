package tool

import (
	"context"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestFocusedPartContextRejectsWholeArtifactAndCallerAuthority(t *testing.T) {
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(&artifact.Authority{})
	scope := WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{UserID: "user-1", AccountScopeID: "account-1"}}
	source := pebblestore.SessionArtifactSelectionReference{SessionID: "source", CollectionID: "source-collection", VariantID: "source-variant", EventSeq: 7}
	composition := pebblestore.SessionArtifactComposition{ID: "composition-1", ArtifactChainID: "chain-1", OwnerSessionID: "source", Parts: []pebblestore.SessionArtifactCompositionPart{{PartID: "hero", DefinitionOwnerSessionID: "source", Revision: pebblestore.SessionArtifactPartRevisionReference{ArtifactChainID: "chain-1", PartID: "hero", PartRevisionID: "hero-r1", OwnerSessionID: "source", DigestSHA256: strings.Repeat("a", 64), Size: 4, MediaType: "text/plain"}}}}
	definition := pebblestore.SessionArtifactPartDefinition{ID: "hero", ArtifactChainID: "chain-1", OwnerSessionID: "source", Label: "Hero"}
	revision := composition.Parts[0].Revision
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "parent-1", ChildSessionID: "child-1", TaskCallID: "task-1", CollectionID: "candidate-collection", VariantID: "candidate-1", ArtifactStepID: "step-1", CandidateIndex: 1, SourceArtifact: &source, SourceComposition: &composition, SourcePartDefinition: &definition, SourcePartRevision: &revision, PartID: "hero"})

	if _, err := runtime.executeManageArtifact(ctx, scope, "whole", map[string]any{"action": "read", "session_id": "source", "collection_id": "source-collection", "variant_id": "source-variant", "event_seq": 7}); err == nil || !strings.Contains(err.Error(), "permits only") {
		t.Fatalf("whole-artifact read error = %v", err)
	}
	if _, err := runtime.executeManageArtifact(ctx, scope, "redirect", map[string]any{"action": "publish_part", "content": "new", "collection_id": "other"}); err == nil || !strings.Contains(err.Error(), "caller-authored field") {
		t.Fatalf("redirected part publication error = %v", err)
	}
}

func TestFocusedPartProtocolRequiresReadBeforeSinglePublish(t *testing.T) {
	runtime := NewRuntime(1)
	run := ArtifactRunContext{TaskCallID: "task", ChildSessionID: "child", CollectionID: "collection", VariantID: "variant"}
	if _, err := runtime.beginFocusedPartPublish(run); err == nil || !strings.Contains(err.Error(), "read_part first") {
		t.Fatalf("publish before read error = %v", err)
	}
	if err := runtime.markFocusedPartRead(run); err != nil {
		t.Fatal(err)
	}
	finish, err := runtime.beginFocusedPartPublish(run)
	if err != nil {
		t.Fatal(err)
	}
	finish(true)
	if _, err := runtime.beginFocusedPartPublish(run); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("duplicate publication error = %v", err)
	}
}

func TestFocusedPartRunRequiresConsistentExactAuthority(t *testing.T) {
	source := pebblestore.SessionArtifactSelectionReference{SessionID: "source", CollectionID: "c", VariantID: "v", EventSeq: 1}
	composition := pebblestore.SessionArtifactComposition{ArtifactChainID: "chain-1"}
	definition := pebblestore.SessionArtifactPartDefinition{ID: "hero"}
	revision := pebblestore.SessionArtifactPartRevisionReference{ArtifactChainID: "chain-1", PartID: "footer"}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SourceArtifact: &source, SourceComposition: &composition, SourcePartDefinition: &definition, SourcePartRevision: &revision, PartID: "hero", CollectionID: "c", VariantID: "v", ArtifactStepID: "step", CandidateIndex: 1})
	if _, err := managedArtifactFocusedPartRun(ctx); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("inconsistent authority error = %v", err)
	}
}
