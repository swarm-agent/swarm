package pebblestore

import (
	"fmt"
	"testing"
)

func TestCreateArtifactCollectionHasNoSessionLifetimeCeiling(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	const sessionID = "artifact-unbounded-collections"
	createV3SessionForTest(t, sessions, sessionID)

	for index := 1; index <= 64; index++ {
		collection := SessionArtifactCollection{
			Version: SessionArtifactVersion, ID: fmt.Sprintf("collection-%03d", index),
			AccountScopeID: "account-1", SessionID: sessionID,
			Status: SessionArtifactStatusStaging, Name: fmt.Sprintf("Collection %d", index),
		}
		if err := store.PutJSON(KeySessionArtifactCollection("account-1", sessionID, collection.ID), collection); err != nil {
			t.Fatal(err)
		}
	}

	mutation := V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-065", Name: "Collection 65"}}
	result, err := applyV3ArtifactMutationForTest(sessions, V3SessionMutationInput{
		SessionID: sessionID, UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "create-65", PayloadHash: "create-65", Kind: V3SessionMutationCreateArtifact,
		Artifact: &mutation,
	})
	if err != nil {
		t.Fatalf("create collection beyond former ceiling: %v", err)
	}
	if result.Artifact == nil || result.Artifact.Collection.ID != "collection-065" {
		t.Fatalf("created artifact projection = %+v", result.Artifact)
	}
	collections, err := sessions.ListAllSessionArtifactCollections("account-1", sessionID, "")
	if err != nil || len(collections) != 65 {
		t.Fatalf("all collections: count=%d err=%v", len(collections), err)
	}
}

func TestRepairArtifactCollectionsHasNoSessionLifetimeCeiling(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	const sessionID = "artifact-unbounded-repair"
	createV3SessionForTest(t, sessions, sessionID)

	for index := 1; index <= 65; index++ {
		collection := SessionArtifactCollection{
			Version: SessionArtifactVersion, ID: fmt.Sprintf("collection-%03d", index),
			AccountScopeID: "account-1", SessionID: sessionID,
			Status: SessionArtifactStatusStaging, Name: fmt.Sprintf("Collection %d", index),
		}
		if err := store.PutJSON(KeySessionArtifactCollection("account-1", sessionID, collection.ID), collection); err != nil {
			t.Fatal(err)
		}
	}

	report, err := sessions.RepairSessionArtifactCollections(sessionID)
	if err != nil {
		t.Fatalf("repair collections beyond former ceiling: %v", err)
	}
	if report.CollectionsVisited != 65 {
		t.Fatalf("collections visited = %d; want 65", report.CollectionsVisited)
	}
}
