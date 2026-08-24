package pebblestore

import (
	"strings"
	"testing"
)

func TestUpdateArtifactMovesTerminalStatusBackToStaging(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-update-progress")
	apply := func(request, kind string, variant SessionArtifactVariant) V3SessionMutationResult {
		t.Helper()
		mutation := V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1", Name: "Update progress"}, Variant: &variant}
		result, err := applyV3ArtifactMutationForTest(sessions, V3SessionMutationInput{
			SessionID: "artifact-update-progress", UserID: "user-1", AccountScopeID: "account-1",
			ClientRequestID: request, PayloadHash: request, Kind: kind,
			Artifact: &mutation,
		})
		if err != nil {
			t.Fatalf("%s: %v", request, err)
		}
		return result
	}
	apply("create", V3SessionMutationCreateArtifact, SessionArtifactVariant{ID: "variant-1", Filename: "note.txt", MediaType: "text/plain"})
	failed := apply("fail", V3SessionMutationFailArtifact, SessionArtifactVariant{ID: "variant-1", FailureCode: "first_attempt_failed"})
	if failed.Artifact.Collection.FailedCount != 1 || failed.Artifact.Collection.StagingCount != 0 {
		t.Fatalf("failed progress = %+v", failed.Artifact.Collection)
	}
	updated := apply("update", V3SessionMutationUpdateArtifact, SessionArtifactVariant{ID: "variant-1", Filename: "note.txt", MediaType: "text/plain"})
	if updated.Artifact.Variant.Status != SessionArtifactStatusStaging || updated.Artifact.Collection.StagingCount != 1 || updated.Artifact.Collection.FailedCount != 0 || updated.Artifact.Collection.VariantCount != 1 {
		t.Fatalf("updated progress = %+v variant=%+v", updated.Artifact.Collection, updated.Artifact.Variant)
	}
	finalized := apply("finalize", V3SessionMutationFinalizeArtifact, SessionArtifactVariant{ID: "variant-1", Filename: "note.txt", MediaType: "text/plain", DigestSHA256: strings.Repeat("c", 64), Size: 4})
	if finalized.Artifact.Collection.ReadyCount != 1 || finalized.Artifact.Collection.StagingCount != 0 || finalized.Artifact.Collection.FailedCount != 0 {
		t.Fatalf("final progress = %+v", finalized.Artifact.Collection)
	}
}

func TestRepairSessionArtifactCollectionsOmitsHistoricalRowsWithoutGitIdentity(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-repair-historical")
	collection := SessionArtifactCollection{Version: SessionArtifactVersion, ID: "collection-1", AccountScopeID: "account-1", SessionID: "artifact-repair-historical", Status: SessionArtifactStatusStaging, Name: "Historical", VariantCount: 1, StagingCount: 1}
	variant := SessionArtifactVariant{Version: SessionArtifactVersion, ID: "variant-1", CollectionID: collection.ID, AccountScopeID: collection.AccountScopeID, SessionID: collection.SessionID, Status: SessionArtifactStatusStaging}
	if err := store.PutJSON(KeySessionArtifactCollection(collection.AccountScopeID, collection.SessionID, collection.ID), collection); err != nil {
		t.Fatal(err)
	}
	if err := store.PutJSON(KeySessionArtifactVariant(variant.AccountScopeID, variant.SessionID, variant.CollectionID, variant.ID), variant); err != nil {
		t.Fatal(err)
	}
	report, err := sessions.RepairSessionArtifactCollections(collection.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if report.InvalidVariantsOmitted != 1 || report.CollectionsRepaired != 1 {
		t.Fatalf("repair report = %+v", report)
	}
	repaired, ok, err := sessions.GetSessionArtifactCollection(collection.AccountScopeID, collection.SessionID, collection.ID)
	if err != nil || !ok || repaired.VariantCount != 0 || repaired.StagingCount != 0 {
		t.Fatalf("repaired=%+v ok=%t err=%v", repaired, ok, err)
	}
}

func TestRepairSessionArtifactCollectionsDerivesProgressFromVariants(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-repair-progress")
	mutation := V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1", Name: "Repair"}, Variant: &SessionArtifactVariant{ID: "variant-1", Filename: "note.txt", MediaType: "text/plain"}}
	if _, err := applyV3ArtifactMutationForTest(sessions, V3SessionMutationInput{
		SessionID: "artifact-repair-progress", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "create", PayloadHash: "create", Kind: V3SessionMutationCreateArtifact,
		Artifact: &mutation,
	}); err != nil {
		t.Fatal(err)
	}
	variant, ok, err := sessions.GetSessionArtifactVariant("account-1", "artifact-repair-progress", "collection-1", "variant-1")
	if err != nil || !ok {
		t.Fatalf("load variant: ok=%t err=%v", ok, err)
	}
	variant.GraphState, variant.RepositoryID, variant.CommitOID = SessionArtifactGraphProjection, "repair-repository", strings.Repeat("d", 64)
	if err := store.PutJSON(KeySessionArtifactVariant(variant.AccountScopeID, variant.SessionID, variant.CollectionID, variant.ID), variant); err != nil {
		t.Fatal(err)
	}
	corrupt := SessionArtifactCollection{
		Version: SessionArtifactVersion, ID: "collection-1", AccountScopeID: "account-1", SessionID: "artifact-repair-progress",
		Status: SessionArtifactStatusFailed, Name: "Repair", VariantCount: 1, FailedCount: 1, SelectedVariantID: "missing",
	}
	if err := store.PutJSON(KeySessionArtifactCollection("account-1", "artifact-repair-progress", "collection-1"), corrupt); err != nil {
		t.Fatal(err)
	}
	report, err := sessions.RepairSessionArtifactCollections("artifact-repair-progress")
	if err != nil {
		t.Fatal(err)
	}
	if report.CollectionsVisited != 1 || report.CollectionsRepaired != 1 {
		t.Fatalf("repair report = %+v", report)
	}
	collection, ok, err := sessions.GetSessionArtifactCollection("account-1", "artifact-repair-progress", "collection-1")
	if err != nil || !ok {
		t.Fatalf("repaired collection: ok=%t err=%v", ok, err)
	}
	if collection.VariantCount != 1 || collection.StagingCount != 1 || collection.FailedCount != 0 || collection.Status != SessionArtifactStatusStaging || collection.SelectedVariantID != "" {
		t.Fatalf("repaired collection = %+v", collection)
	}
}
