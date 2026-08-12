package pebblestore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyV3ArtifactLifecycleIsAtomicIdempotentAndMetadataOnly(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-session")

	create := V3SessionMutationInput{
		SessionID: "artifact-session", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-create", PayloadHash: "artifact-create-hash", Kind: V3SessionMutationCreateArtifact, NowUnixMs: 2000,
		Artifact: &V3ArtifactMutation{
			Collection: SessionArtifactCollection{ID: "collection-1", Name: "Landing page", AccountScopeID: "model-forged", SessionID: "other"},
			Variant: &SessionArtifactVariant{ID: "variant-1", CollectionID: "collection-1", AccountScopeID: "model-forged", SessionID: "other", Filename: "landing.html", MediaType: "text/html", Lineage: SessionArtifactLineage{RunID: "run-1"}},
		},
	}
	first, err := sessions.ApplyV3SessionMutation(create)
	if err != nil { t.Fatalf("create artifact: %v", err) }
	if first.Artifact == nil || first.Artifact.Collection.Status != SessionArtifactStatusStaging || first.Artifact.Variant == nil || first.Artifact.Variant.Status != SessionArtifactStatusStaging {
		t.Fatalf("artifact projection = %+v", first.Artifact)
	}
	if first.Artifact.Collection.AccountScopeID != "account-1" || first.Artifact.Collection.SessionID != "artifact-session" || first.Artifact.Variant.AccountScopeID != "account-1" {
		t.Fatalf("artifact ownership was not server-derived: %+v", first.Artifact)
	}
	if strings.Contains(string(first.Event.Payload), "storage_path") || strings.Contains(string(first.Event.Payload), "/home/") {
		t.Fatalf("event leaked storage metadata: %s", first.Event.Payload)
	}
	var eventPayload map[string]any
	if err := json.Unmarshal(first.Event.Payload, &eventPayload); err != nil { t.Fatal(err) }
	artifactPayload, ok := eventPayload["artifact"].(map[string]any)
	if !ok || artifactPayload["collection"] == nil { t.Fatalf("event lacks artifact projection: %v", eventPayload) }

	replayed, err := sessions.ApplyV3SessionMutation(create)
	if err != nil { t.Fatalf("replay artifact create: %v", err) }
	if !replayed.Replayed || replayed.PrimarySeq != first.PrimarySeq || replayed.Artifact == nil || replayed.Artifact.Collection.ID != "collection-1" {
		t.Fatalf("replayed artifact = %+v", replayed)
	}
	collections, err := sessions.ListSessionArtifactCollections("account-1", "artifact-session", SessionArtifactStatusStaging, 10)
	if err != nil || len(collections) != 1 { t.Fatalf("staging collections = %+v err=%v", collections, err) }
	variants, err := sessions.ListSessionArtifactVariants("account-1", "artifact-session", "collection-1", 10)
	if err != nil || len(variants) != 1 { t.Fatalf("variants = %+v err=%v", variants, err) }

	finalized, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-session", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-finalize", PayloadHash: "artifact-finalize-hash", Kind: V3SessionMutationFinalizeArtifact, NowUnixMs: 3000,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1"}, Variant: &SessionArtifactVariant{ID: "variant-1", CollectionID: "collection-1", Filename: "landing.html", MediaType: "text/html", DigestSHA256: strings.Repeat("a", 64), Size: 123}},
	})
	if err != nil { t.Fatalf("finalize artifact: %v", err) }
	if finalized.Artifact == nil || finalized.Artifact.Variant.Status != SessionArtifactStatusReady || finalized.Event.EventType != "session.artifact.finalized" { t.Fatalf("finalized = %+v", finalized) }

	selected, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-session", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-select", PayloadHash: "artifact-select-hash", Kind: V3SessionMutationSelectArtifact, NowUnixMs: 4000,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1"}, Selection: &SessionArtifactSelectionReference{SessionID: "artifact-session", CollectionID: "collection-1", VariantID: "variant-1"}},
	})
	if err != nil { t.Fatalf("select artifact: %v", err) }
	if selected.Artifact == nil || selected.Artifact.Selection == nil || selected.Artifact.Collection.SelectedVariantID != "variant-1" { t.Fatalf("selected = %+v", selected.Artifact) }

	ready, err := sessions.ListSessionArtifactCollections("account-1", "artifact-session", SessionArtifactStatusReady, 10)
	if err != nil || len(ready) != 1 || ready[0].SelectedVariantID != "variant-1" { t.Fatalf("ready collections = %+v err=%v", ready, err) }
}

func TestApplyV3ArtifactLifecycleRejectsUnsafeOrInvalidMetadata(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-invalid")
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-invalid", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "stage-invalid-digest", PayloadHash: "stage-invalid-digest", Kind: V3SessionMutationCreateArtifact,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "digest-collection", Name: "digest"}, Variant: &SessionArtifactVariant{ID: "digest-variant"}},
	}); err != nil { t.Fatalf("stage digest fixture: %v", err) }

	cases := []struct {
		name string
		input V3SessionMutationInput
		want string
	}{
		{name: "raw event payload", input: V3SessionMutationInput{Kind: V3SessionMutationCreateArtifact, EventPayload: json.RawMessage(`{"storage_path":"/private"}`), Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection", Name: "name"}}}, want: "derived"},
		{name: "unsafe id", input: V3SessionMutationInput{Kind: V3SessionMutationCreateArtifact, Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "../outside", Name: "name"}}}, want: "unsupported"},
		{name: "unsafe filename", input: V3SessionMutationInput{Kind: V3SessionMutationCreateArtifact, Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection", Name: "name"}, Variant: &SessionArtifactVariant{ID: "variant", Filename: "../outside"}}}, want: "basename"},
		{name: "invalid digest", input: V3SessionMutationInput{Kind: V3SessionMutationFinalizeArtifact, Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "digest-collection"}, Variant: &SessionArtifactVariant{ID: "digest-variant", Filename: "x", MediaType: "text/plain", DigestSHA256: "bad", Size: 1}}}, want: "requires filename"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.input.SessionID = "artifact-invalid"
			tc.input.UserID = "user-1"
			tc.input.AccountScopeID = "account-1"
			tc.input.ClientRequestID = "request-" + strings.ReplaceAll(tc.name, " ", "-")
			tc.input.PayloadHash = "hash-" + strings.ReplaceAll(tc.name, " ", "-")
			if _, err := sessions.ApplyV3SessionMutation(tc.input); err == nil || !strings.Contains(err.Error(), tc.want) { t.Fatalf("err=%v want %q", err, tc.want) }
		})
	}
}
