package pebblestore

import (
	"strings"
	"testing"
)

func TestBackendArtifactNativePersistenceSelectionAndRestartContract(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-persistence-contract")

	apply := func(request, kind string, mutation V3ArtifactMutation) V3SessionMutationResult {
		t.Helper()
		result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
			SessionID: "artifact-persistence-contract", UserID: "user-1", AccountScopeID: "account-1",
			ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RequestHash: request,
			Kind: kind, Artifact: &mutation,
		})
		if err != nil {
			t.Fatalf("%s: %v", request, err)
		}
		if result.RealtimeOutbox == nil {
			t.Fatalf("%s did not persist a durable realtime outbox", request)
		}
		return result
	}

	apply("contract-create", V3SessionMutationCreateArtifact, V3ArtifactMutation{
		Collection: SessionArtifactCollection{ID: "collection", Name: "Alternatives"},
		Variant: &SessionArtifactVariant{ID: "variant", CollectionID: "collection", Filename: "design.txt", MediaType: "text/plain"},
	})
	ready := apply("contract-ready", V3SessionMutationFinalizeArtifact, V3ArtifactMutation{
		Collection: SessionArtifactCollection{ID: "collection"},
		Variant: &SessionArtifactVariant{ID: "variant", CollectionID: "collection", Filename: "design.txt", MediaType: "text/plain", DigestSHA256: strings.Repeat("a", 64), Size: 6},
	})
	if ready.Artifact == nil || ready.Artifact.Variant == nil || ready.Artifact.Variant.Status != SessionArtifactStatusReady {
		t.Fatalf("ready projection = %#v", ready.Artifact)
	}
	readySeq := ready.Artifact.Variant.EventSeq
	selected := apply("contract-select", V3SessionMutationSelectArtifact, V3ArtifactMutation{
		Collection: SessionArtifactCollection{ID: "collection"},
		Selection: &SessionArtifactSelectionReference{SessionID: "artifact-persistence-contract", CollectionID: "collection", VariantID: "variant", EventSeq: readySeq, Action: "use"},
	})
	if selected.Artifact == nil || selected.Artifact.Selection == nil || selected.Artifact.Collection.SelectedVariantID != "variant" {
		t.Fatalf("selection projection = %#v", selected.Artifact)
	}

	restarted := NewSessionStore(store)
	variant, ok, err := restarted.GetSessionArtifactVariant("account-1", "artifact-persistence-contract", "collection", "variant")
	if err != nil || !ok || variant.Status != SessionArtifactStatusReady || variant.EventSeq != readySeq {
		t.Fatalf("restarted variant = %#v ok=%t err=%v", variant, ok, err)
	}
	collection, ok, err := restarted.GetSessionArtifactCollection("account-1", "artifact-persistence-contract", "collection")
	if err != nil || !ok || collection.SelectedVariantID != "variant" || collection.EventSeq != selected.Artifact.Selection.EventSeq {
		t.Fatalf("restarted collection = %#v ok=%t err=%v", collection, ok, err)
	}

	readyRef := SessionArtifactSelectionReference{SessionID: variant.SessionID, CollectionID: variant.CollectionID, VariantID: variant.ID, EventSeq: readySeq, Action: "use"}
	validated, err := restarted.ValidateSessionArtifactMessageSelections("account-1", "user-1", []SessionArtifactSelectionReference{readyRef})
	if err != nil || len(validated) != 1 || validated[0].EventSeq != readySeq {
		t.Fatalf("exact ready attachment = %#v err=%v", validated, err)
	}
	stale := readyRef
	stale.EventSeq = readySeq + 999
	if _, err := restarted.ValidateSessionArtifactMessageSelections("account-1", "user-1", []SessionArtifactSelectionReference{stale}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale attachment error = %v", err)
	}
	if _, err := restarted.ValidateSessionArtifactMessageSelections("account-2", "user-2", []SessionArtifactSelectionReference{readyRef}); err == nil {
		t.Fatal("cross-account attachment was accepted")
	}
}

func TestBackendArtifactSessionDeletionPurgesNativeMetadata(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-delete-contract")
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-delete-contract", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "delete-create", PayloadHash: "delete-create", Kind: V3SessionMutationCreateArtifact,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection", Name: "Delete"}, Variant: &SessionArtifactVariant{ID: "variant", Filename: "delete.txt", MediaType: "text/plain"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.DeleteSession("artifact-delete-contract"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := sessions.GetSessionArtifactCollection("account-1", "artifact-delete-contract", "collection"); err != nil || ok {
		t.Fatalf("deleted collection remains: ok=%t err=%v", ok, err)
	}
	if _, ok, err := sessions.GetSessionArtifactVariantByID("account-1", "artifact-delete-contract", "variant"); err != nil || ok {
		t.Fatalf("deleted variant remains: ok=%t err=%v", ok, err)
	}
}
