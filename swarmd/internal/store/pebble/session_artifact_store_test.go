package pebblestore

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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
			Variant:    &SessionArtifactVariant{ID: "variant-1", CollectionID: "collection-1", AccountScopeID: "model-forged", SessionID: "other", Filename: "landing.html", MediaType: "text/html", Lineage: SessionArtifactLineage{RunID: "run-1"}},
		},
	}
	first, err := sessions.ApplyV3SessionMutation(create)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if first.Artifact == nil || first.Artifact.Collection.Status != SessionArtifactStatusStaging || first.Artifact.Variant == nil || first.Artifact.Variant.Status != SessionArtifactStatusStaging {
		t.Fatalf("artifact projection = %+v", first.Artifact)
	}
	if first.Artifact.Collection.AccountScopeID != "account-1" || first.Artifact.Collection.SessionID != "artifact-session" || first.Artifact.Variant.AccountScopeID != "account-1" {
		t.Fatalf("artifact ownership was not server-derived: %+v", first.Artifact)
	}
	if first.Artifact.Collection.VariantCount != 1 || first.Artifact.Collection.StagingCount != 1 {
		t.Fatalf("artifact progress after create = %+v", first.Artifact.Collection)
	}
	if strings.Contains(string(first.Event.Payload), "storage_path") || strings.Contains(string(first.Event.Payload), "/home/") {
		t.Fatalf("event leaked storage metadata: %s", first.Event.Payload)
	}
	var eventPayload map[string]any
	if err := json.Unmarshal(first.Event.Payload, &eventPayload); err != nil {
		t.Fatal(err)
	}
	artifactPayload, ok := eventPayload["artifact"].(map[string]any)
	if !ok || artifactPayload["collection"] == nil {
		t.Fatalf("event lacks artifact projection: %v", eventPayload)
	}

	replayed, err := sessions.ApplyV3SessionMutation(create)
	if err != nil {
		t.Fatalf("replay artifact create: %v", err)
	}
	if !replayed.Replayed || replayed.PrimarySeq != first.PrimarySeq || replayed.Artifact == nil || replayed.Artifact.Collection.ID != "collection-1" {
		t.Fatalf("replayed artifact = %+v", replayed)
	}
	collections, err := sessions.ListSessionArtifactCollections("account-1", "artifact-session", SessionArtifactStatusStaging, 10)
	if err != nil || len(collections) != 1 {
		t.Fatalf("staging collections = %+v err=%v", collections, err)
	}
	variants, err := sessions.ListSessionArtifactVariants("account-1", "artifact-session", "collection-1", 10)
	if err != nil || len(variants) != 1 {
		t.Fatalf("variants = %+v err=%v", variants, err)
	}

	finalized, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-session", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-finalize", PayloadHash: "artifact-finalize-hash", Kind: V3SessionMutationFinalizeArtifact, NowUnixMs: 3000,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1"}, Variant: &SessionArtifactVariant{ID: "variant-1", CollectionID: "collection-1", Filename: "landing.html", MediaType: "text/html", DigestSHA256: strings.Repeat("a", 64), Size: 123}},
	})
	if err != nil {
		t.Fatalf("finalize artifact: %v", err)
	}
	if finalized.Artifact == nil || finalized.Artifact.Variant.Status != SessionArtifactStatusReady || finalized.Event.EventType != "session.artifact.finalized" {
		t.Fatalf("finalized = %+v", finalized)
	}
	if finalized.Artifact.Collection.ReadyCount != 1 || finalized.Artifact.Collection.StagingCount != 0 {
		t.Fatalf("finalized progress = %+v", finalized.Artifact.Collection)
	}

	selected, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-session", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-select", PayloadHash: "artifact-select-hash", Kind: V3SessionMutationSelectArtifact, NowUnixMs: 4000,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1"}, Selection: &SessionArtifactSelectionReference{SessionID: "artifact-session", CollectionID: "collection-1", VariantID: "variant-1"}},
	})
	if err != nil {
		t.Fatalf("select artifact: %v", err)
	}
	if selected.Artifact == nil || selected.Artifact.Selection == nil || selected.Artifact.Collection.SelectedVariantID != "variant-1" {
		t.Fatalf("selected = %+v", selected.Artifact)
	}

	ready, err := sessions.ListSessionArtifactCollections("account-1", "artifact-session", SessionArtifactStatusReady, 10)
	if err != nil || len(ready) != 1 || ready[0].SelectedVariantID != "variant-1" {
		t.Fatalf("ready collections = %+v err=%v", ready, err)
	}
}

func TestArtifactLineageIndexesDesignerRoutingDimensions(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-lineage")
	lineage := SessionArtifactLineage{ParentSessionID: "artifact-lineage", TaskCallID: "call-mixedcase", ProgramID: "program-1", ProgramJobID: "job-1", ChildSessionID: "child-1", IterationID: "iteration-1", IterationIndex: 2}
	collectionLineage := lineage
	collectionLineage.ProgramJobID, collectionLineage.ChildSessionID, collectionLineage.IterationID, collectionLineage.IterationIndex = "", "", "", 0
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-lineage", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "lineage-create", PayloadHash: "lineage-create", Kind: V3SessionMutationCreateArtifact,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1", Name: "Designer alternatives", Lineage: collectionLineage}, Variant: &SessionArtifactVariant{ID: "variant-1", Filename: "design.html", MediaType: "text/html", Lineage: lineage}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []struct{ dimension, value string }{{"parent_session", "artifact-lineage"}, {"task_call", "call-mixedcase"}, {"program", "program-1"}, {"program_job", "job-1"}, {"child_session", "child-1"}, {"iteration", "iteration-1"}} {
		variants, err := sessions.ListSessionArtifactVariantsByLineage("account-1", "artifact-lineage", filter.dimension, filter.value, 10)
		if err != nil || len(variants) != 1 || variants[0].ID != "variant-1" {
			t.Fatalf("lineage %s=%s: %+v err=%v", filter.dimension, filter.value, variants, err)
		}
		if filter.dimension != "task_call" {
			continue
		}
		other, err := sessions.ListSessionArtifactVariantsByLineage("account-1", "artifact-lineage", filter.dimension, "CALL-MIXEDCASE", 10)
		if err != nil || len(other) != 0 {
			t.Fatalf("lineage values must remain case-sensitive: %+v err=%v", other, err)
		}
	}
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-lineage", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "lineage-create-2", PayloadHash: "lineage-create-2", Kind: V3SessionMutationCreateArtifact,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-2", Name: "Designer alternatives", Lineage: collectionLineage}, Variant: &SessionArtifactVariant{ID: "variant-2", Filename: "design-2.html", MediaType: "text/html", Lineage: lineage}},
	}); err != nil {
		t.Fatal(err)
	}
	variants, err := sessions.ListSessionArtifactVariantsByLineage("account-1", "artifact-lineage", "task_call", "call-mixedcase", 10)
	if err != nil || len(variants) != 2 || variants[0].CollectionID == variants[1].CollectionID {
		t.Fatalf("cross-collection lineage variants = %+v err=%v", variants, err)
	}
}

func TestConcurrentArtifactLifecycleMutationsRemainIdempotent(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-concurrent")
	input := V3SessionMutationInput{
		SessionID: "artifact-concurrent", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-concurrent-create", PayloadHash: "artifact-concurrent-create-hash", Kind: V3SessionMutationCreateArtifact, NowUnixMs: 2000,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1", Name: "Concurrent"}, Variant: &SessionArtifactVariant{ID: "variant-1", Filename: "note.txt", MediaType: "text/plain"}},
	}

	const writers = 8
	start := make(chan struct{})
	results := make(chan V3SessionMutationResult, writers)
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := sessions.ApplyV3SessionMutation(input)
			results <- result
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent artifact mutation: %v", err)
		}
	}
	var primarySeq uint64
	fresh := 0
	for result := range results {
		if primarySeq == 0 {
			primarySeq = result.PrimarySeq
		}
		if result.PrimarySeq != primarySeq {
			t.Fatalf("primary seq = %d, want %d", result.PrimarySeq, primarySeq)
		}
		if !result.Replayed {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh mutations = %d, want 1", fresh)
	}
	variants, err := sessions.ListSessionArtifactVariants("account-1", "artifact-concurrent", "collection-1", 10)
	if err != nil || len(variants) != 1 {
		t.Fatalf("variants = %+v err=%v", variants, err)
	}
}

func TestArtifactMutationRejectsCrossAccountSessionOwnership(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-owned")

	for _, tc := range []struct {
		name, userID, accountID string
	}{
		{name: "wrong-account", userID: "user-1", accountID: "account-2"},
		{name: "wrong-user", userID: "user-2", accountID: "account-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
				SessionID: "artifact-owned", UserID: tc.userID, AccountScopeID: tc.accountID,
				ClientRequestID: "artifact-owned-" + tc.name, PayloadHash: "artifact-owned-" + tc.name, Kind: V3SessionMutationCreateArtifact,
				Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-" + tc.accountID, Name: "Forbidden"}},
			})
			if err == nil {
				t.Fatal("artifact mutation accepted mismatched session ownership")
			}
		})
	}
	collections, err := sessions.ListSessionArtifactCollections("account-1", "artifact-owned", "", 10)
	if err != nil || len(collections) != 0 {
		t.Fatalf("unauthorized metadata persisted: %+v err=%v", collections, err)
	}
}

func TestArtifactEventProjectionContainsMetadataButNoBytesOrPrivatePaths(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-projection")
	result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-projection", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-projection-create", PayloadHash: "artifact-projection-create", Kind: V3SessionMutationCreateArtifact,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1", Name: "Metadata only"}, Variant: &SessionArtifactVariant{ID: "variant-1", Filename: "note.txt", MediaType: "text/plain", Presentation: SessionArtifactPresentation{Kind: "text", Label: "Preview", Description: "Safe metadata"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Assert artifact projections never introduce byte/private-path fields. The
	// generic realtime membership may carry the trusted workspace route, so scope
	// that check to its artifact-bearing event and projection.
	for name, value := range map[string]any{"event": result.Event, "projection": result.Projection} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		assertNoArtifactByteFields(t, name, decoded)
	}
	if result.RealtimeOutbox == nil {
		t.Fatal("artifact mutation has no durable realtime outbox")
	}
	for name, value := range map[string]any{"realtime event": result.RealtimeOutbox.Event, "realtime projection": result.RealtimeOutbox.Projection} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		assertNoArtifactByteFields(t, name, decoded)
	}
}

func assertNoArtifactByteFields(t *testing.T, location string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch strings.ToLower(key) {
			case "content", "bytes", "body", "storage_path", "private_path", "filesystem_path":
				t.Fatalf("%s contains private artifact field %q", location, key)
			}
			assertNoArtifactByteFields(t, fmt.Sprintf("%s.%s", location, key), child)
		}
	case []any:
		for i, child := range typed {
			assertNoArtifactByteFields(t, fmt.Sprintf("%s[%d]", location, i), child)
		}
	}
}

func TestDeleteSessionPurgesArtifactMetadataAndIndexes(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-delete")
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-delete", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-delete-create", PayloadHash: "artifact-delete-create", Kind: V3SessionMutationCreateArtifact,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1", Name: "Artifact"}, Variant: &SessionArtifactVariant{ID: "variant-1", Filename: "note.txt", MediaType: "text/plain"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.DeleteSession("artifact-delete"); err != nil {
		t.Fatal(err)
	}
	for name, prefix := range map[string]string{
		"collection":        SessionArtifactCollectionPrefix("account-1", "artifact-delete"),
		"collection status": SessionArtifactCollectionStatusSessionPrefix("account-1", "artifact-delete"),
		"variant":           SessionArtifactVariantSessionPrefix("account-1", "artifact-delete"),
		"variant status":    SessionArtifactVariantStatusSessionPrefix("account-1", "artifact-delete"),
		"variant digest":    SessionArtifactVariantDigestSessionPrefix("account-1", "artifact-delete"),
		"variant lineage":   SessionArtifactVariantLineageSessionPrefix("account-1", "artifact-delete"),
	} {
		if err := store.IteratePrefix(prefix, 10, func(string, []byte) error { t.Fatalf("%s metadata remains", name); return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestArchiveAndUnarchiveSessionPreserveArtifactMetadata(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-archive")
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-archive", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-archive-create", PayloadHash: "artifact-archive-create", Kind: V3SessionMutationCreateArtifact,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1", Name: "Artifact"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.ArchiveSessions([]string{"artifact-archive"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := sessions.GetSessionArtifactCollection("account-1", "artifact-archive", "collection-1"); err != nil || !ok {
		t.Fatalf("archived artifact metadata missing: ok=%t err=%v", ok, err)
	}
	tombstone, ok, err := sessions.GetV3SessionTombstone("artifact-archive")
	if err != nil || !ok {
		t.Fatalf("archived tombstone missing: ok=%t err=%v", ok, err)
	}
	if err := sessions.ReactivateArchivedSessions([]string{"artifact-archive"}, map[string]int64{"artifact-archive": tombstone.UpdatedAt}); err != nil {
		t.Fatalf("reactivate archived session: %v", err)
	}
	if _, ok, err := sessions.GetSessionArtifactCollection("account-1", "artifact-archive", "collection-1"); err != nil || !ok {
		t.Fatalf("unarchived artifact metadata missing: ok=%t err=%v", ok, err)
	}
}

func TestArtifactUnavailableMutationRecordsHonestTerminalState(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-unavailable")
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-unavailable", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-unavailable-create", PayloadHash: "artifact-unavailable-create", Kind: V3SessionMutationCreateArtifact,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "legacy-collection", Name: "Legacy"}, Variant: &SessionArtifactVariant{ID: "legacy-variant", Filename: "missing.html", MediaType: "text/html"}},
	}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-unavailable", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-unavailable-terminal", PayloadHash: "artifact-unavailable-terminal", Kind: V3SessionMutationUnavailableArtifact,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "legacy-collection"}, Variant: &SessionArtifactVariant{ID: "legacy-variant", FailureCode: "legacy_source_unavailable"}},
	}); err != nil {
		t.Fatalf("mark unavailable: %v", err)
	}
	variant, ok, err := sessions.GetSessionArtifactVariant("account-1", "artifact-unavailable", "legacy-collection", "legacy-variant")
	if err != nil || !ok {
		t.Fatalf("get unavailable artifact: ok=%v err=%v", ok, err)
	}
	if variant.Status != SessionArtifactStatusUnavailable || variant.FailureCode != "legacy_source_unavailable" || variant.DigestSHA256 != "" || variant.Size != 0 {
		t.Fatalf("unavailable variant = %+v", variant)
	}
	collections, err := sessions.ListSessionArtifactCollections("account-1", "artifact-unavailable", SessionArtifactStatusUnavailable, 10)
	if err != nil || len(collections) != 1 || collections[0].ID != "legacy-collection" {
		t.Fatalf("unavailable collection index = %+v err=%v", collections, err)
	}
}

func TestArtifactVariantLookupAndDeleteMutationsRepairIndexesAndSelection(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-metadata-delete")
	apply := func(request, kind string, artifact V3ArtifactMutation, now int64) V3SessionMutationResult {
		t.Helper()
		result, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "artifact-metadata-delete", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: request, PayloadHash: request, Kind: kind, Artifact: &artifact, NowUnixMs: now})
		if err != nil {
			t.Fatalf("%s: %v", request, err)
		}
		return result
	}
	apply("create-delete", V3SessionMutationCreateArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1", Name: "Delete"}, Variant: &SessionArtifactVariant{ID: "variant-1", Filename: "one.txt", MediaType: "text/plain"}}, 2000)
	apply("finalize-delete", V3SessionMutationFinalizeArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1"}, Variant: &SessionArtifactVariant{ID: "variant-1", Filename: "one.txt", MediaType: "text/plain", DigestSHA256: strings.Repeat("b", 64), Size: 3}}, 3000)
	apply("select-delete", V3SessionMutationSelectArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1"}, Selection: &SessionArtifactSelectionReference{CollectionID: "collection-1", VariantID: "variant-1"}}, 4000)
	variant, ok, err := sessions.GetSessionArtifactVariantByID("account-1", "artifact-metadata-delete", "variant-1")
	if err != nil || !ok || variant.CollectionID != "collection-1" {
		t.Fatalf("opaque lookup = %+v ok=%t err=%v", variant, ok, err)
	}
	deleted := apply("delete-variant", V3SessionMutationDeleteArtifactVariant, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1"}, Variant: &SessionArtifactVariant{ID: "variant-1"}}, 5000)
	if deleted.Event.EventType != "session.artifact.variant.deleted" || deleted.Artifact == nil || deleted.Artifact.Collection.SelectedVariantID != "" || deleted.Artifact.Collection.VariantCount != 0 {
		t.Fatalf("delete result = %+v", deleted)
	}
	if _, ok, err := sessions.GetSessionArtifactVariantByID("account-1", "artifact-metadata-delete", "variant-1"); err != nil || ok {
		t.Fatalf("deleted opaque lookup ok=%t err=%v", ok, err)
	}
	for _, key := range []string{KeySessionArtifactVariantStatus("account-1", "artifact-metadata-delete", SessionArtifactStatusReady, "collection-1", "variant-1"), KeySessionArtifactVariantDigest("account-1", "artifact-metadata-delete", strings.Repeat("b", 64), "collection-1", "variant-1")} {
		if _, ok, err := store.GetBytes(key); err != nil || ok {
			t.Fatalf("index %q remains ok=%t err=%v", key, ok, err)
		}
	}
	collectionDeleted := apply("delete-collection", V3SessionMutationDeleteArtifactCollection, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1"}}, 6000)
	if collectionDeleted.Event.EventType != "session.artifact.collection.deleted" {
		t.Fatalf("collection delete event = %q", collectionDeleted.Event.EventType)
	}
	if _, ok, err := sessions.GetSessionArtifactCollection("account-1", "artifact-metadata-delete", "collection-1"); err != nil || ok {
		t.Fatalf("deleted collection ok=%t err=%v", ok, err)
	}
	if collectionDeleted.RealtimeOutbox == nil || collectionDeleted.RealtimeOutbox.Event.EventType != "session.artifact.collection.deleted" {
		t.Fatalf("collection delete lacks realtime outbox: %+v", collectionDeleted.RealtimeOutbox)
	}
}

func TestDeleteArtifactCollectionRemovesEveryVariantIndex(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-collection-delete")
	for index, id := range []string{"variant-1", "variant-2"} {
		request := fmt.Sprintf("create-%d", index)
		name := ""
		if index == 0 {
			name = "Delete all"
		}
		_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "artifact-collection-delete", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: request, PayloadHash: request, Kind: V3SessionMutationCreateArtifact, Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1", Name: name}, Variant: &SessionArtifactVariant{ID: id}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "artifact-collection-delete", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "delete-all", PayloadHash: "delete-all", Kind: V3SessionMutationDeleteArtifactCollection, Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	variants, err := sessions.ListSessionArtifactVariants("account-1", "artifact-collection-delete", "collection-1", 10)
	if err != nil || len(variants) != 0 {
		t.Fatalf("variants = %+v err=%v", variants, err)
	}
	if err := store.IteratePrefix(SessionArtifactVariantStatusSessionPrefix("account-1", "artifact-collection-delete"), 10, func(string, []byte) error { t.Error("variant status index remains"); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestSessionArtifactMessageSelectionsValidateOwnershipReadinessAndSequence(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-selection-source")
	apply := func(request, kind string, artifact V3ArtifactMutation) {
		t.Helper()
		if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "artifact-selection-source", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: request, PayloadHash: request, Kind: kind, Artifact: &artifact}); err != nil {
			t.Fatal(err)
		}
	}
	apply("selection-create", V3SessionMutationCreateArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "selection-collection", Name: "Selections"}, Variant: &SessionArtifactVariant{ID: "selection-variant", Filename: "design.txt", MediaType: "text/plain"}})
	staging, ok, err := sessions.GetSessionArtifactVariant("account-1", "artifact-selection-source", "selection-collection", "selection-variant")
	if err != nil || !ok {
		t.Fatalf("get staging variant: ok=%t err=%v", ok, err)
	}
	stagingRef := SessionArtifactSelectionReference{SessionID: staging.SessionID, CollectionID: staging.CollectionID, VariantID: staging.ID, EventSeq: staging.EventSeq}
	if _, err := sessions.ValidateSessionArtifactMessageSelections("account-1", "user-1", []SessionArtifactSelectionReference{stagingRef}); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("staging selection error = %v", err)
	}
	apply("selection-finalize", V3SessionMutationFinalizeArtifact, V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "selection-collection"}, Variant: &SessionArtifactVariant{ID: "selection-variant", Filename: "design.txt", MediaType: "text/plain", DigestSHA256: strings.Repeat("a", 64), Size: 6}})
	variant, ok, err := sessions.GetSessionArtifactVariant("account-1", "artifact-selection-source", "selection-collection", "selection-variant")
	if err != nil || !ok {
		t.Fatalf("get ready variant: ok=%t err=%v", ok, err)
	}
	ref := SessionArtifactSelectionReference{SessionID: variant.SessionID, CollectionID: variant.CollectionID, VariantID: variant.ID, EventSeq: variant.EventSeq, Action: "use"}
	got, err := sessions.ValidateSessionArtifactMessageSelections("account-1", "user-1", []SessionArtifactSelectionReference{ref})
	if err != nil || len(got) != 1 || got[0].VariantID != variant.ID {
		t.Fatalf("validate ready selection = %+v err=%v", got, err)
	}
	stale := ref
	stale.EventSeq++
	if _, err := sessions.ValidateSessionArtifactMessageSelections("account-1", "user-1", []SessionArtifactSelectionReference{stale}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale selection error = %v", err)
	}
	if _, err := sessions.ValidateSessionArtifactMessageSelections("account-2", "user-2", []SessionArtifactSelectionReference{ref}); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("cross-account selection error = %v", err)
	}
}

func TestApplyV3ArtifactLifecycleRejectsUnsafeOrInvalidMetadata(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-invalid")
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-invalid", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "stage-invalid-digest", PayloadHash: "stage-invalid-digest", Kind: V3SessionMutationCreateArtifact,
		Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "digest-collection", Name: "digest"}, Variant: &SessionArtifactVariant{ID: "digest-variant"}},
	}); err != nil {
		t.Fatalf("stage digest fixture: %v", err)
	}

	cases := []struct {
		name  string
		input V3SessionMutationInput
		want  string
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
			if _, err := sessions.ApplyV3SessionMutation(tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
		})
	}
}

func TestArtifactFinalizationPreservesOutputRequirements(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-requirements")
	requirements := &SessionArtifactOutputRequirements{
		PresetID: "x_header", Width: 1500, Height: 500, AspectRatio: "3:1",
		Orientation: "landscape", ResolutionSource: "preset", RegistryVersion: "2026-08-14.v1",
	}
	create, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-requirements", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-requirements-create", PayloadHash: "artifact-requirements-create", Kind: V3SessionMutationCreateArtifact,
		Artifact: &V3ArtifactMutation{
			Collection: SessionArtifactCollection{ID: "collection", Name: "Header"},
			Variant: &SessionArtifactVariant{ID: "variant", OutputRequirements: requirements, Presentation: SessionArtifactPresentation{Width: 1500, Height: 500}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if create.Artifact == nil || create.Artifact.Variant == nil || create.Artifact.Variant.OutputRequirements == nil {
		t.Fatalf("create projection = %#v", create.Artifact)
	}
	finalized, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "artifact-requirements", UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "artifact-requirements-finalize", PayloadHash: "artifact-requirements-finalize", Kind: V3SessionMutationFinalizeArtifact,
		Artifact: &V3ArtifactMutation{
			Collection: SessionArtifactCollection{ID: "collection"},
			Variant: &SessionArtifactVariant{ID: "variant", Filename: "header.svg", MediaType: "image/svg+xml", DigestSHA256: strings.Repeat("a", 64), Size: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	variant := finalized.Artifact.Variant
	if variant == nil || variant.OutputRequirements == nil || *variant.OutputRequirements != *requirements || variant.Presentation.Width != 1500 || variant.Presentation.Height != 500 {
		t.Fatalf("finalized variant = %#v", variant)
	}
}

func TestArtifactFinalizationRejectsOutputRequirementOverride(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-requirements-override")
	requirements := &SessionArtifactOutputRequirements{PresetID: "x_header", Width: 1500, Height: 500, AspectRatio: "3:1", Orientation: "landscape", ResolutionSource: "preset", RegistryVersion: "2026-08-14.v1"}
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "artifact-requirements-override", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "create", PayloadHash: "create", Kind: V3SessionMutationCreateArtifact, Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection", Name: "Header"}, Variant: &SessionArtifactVariant{ID: "variant", OutputRequirements: requirements, Presentation: SessionArtifactPresentation{Width: 1500, Height: 500}}}})
	if err != nil {
		t.Fatal(err)
	}
	override := *requirements
	override.PresetID, override.Width, override.Height, override.AspectRatio, override.Orientation = "square_1080", 1080, 1080, "1:1", "square"
	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "artifact-requirements-override", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: "finalize", PayloadHash: "finalize", Kind: V3SessionMutationFinalizeArtifact, Artifact: &V3ArtifactMutation{Collection: SessionArtifactCollection{ID: "collection"}, Variant: &SessionArtifactVariant{ID: "variant", Filename: "header.svg", MediaType: "image/svg+xml", DigestSHA256: strings.Repeat("b", 64), Size: 1, OutputRequirements: &override}}})
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("override err = %v", err)
	}
}
