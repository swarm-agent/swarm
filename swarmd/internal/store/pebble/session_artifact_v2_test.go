package pebblestore

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

// Requirement: Artifact V2 allocation and every intermediate state must commit
// exact records, V3 event/projection, idempotency, and realtime outbox together.
// Threat: duplicate requests, restart, cross-account envelopes, or a failed
// batch could expose partial or foreign working state. This store-layer test is
// the narrowest proof because ApplyV3SessionMutation owns that atomic boundary.
func TestArtifactV2CreateReplayRestartIsolationAndAtomicFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact-v2.pebble")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(store)
	createV3SessionForStoreTest(t, sessions, "artifact-v2-session", "user-1", "account-1")

	working := ArtifactV2WorkingArtifact{SchemaVersion: ArtifactV2SchemaVersion, ID: "artv2_one", AccountScopeID: "account-1", UserID: "user-1", SessionID: "artifact-v2-session", Kind: "document", State: ArtifactV2StateAllocated, PolicyRevision: "policy-1", CapabilityClass: "designer", CreationRequestID: "create-artifact", Revision: 1}
	mutation := ArtifactV2Mutation{Working: &working}
	raw, _ := json.Marshal(mutation)
	input := V3SessionMutationInput{SessionID: working.SessionID, UserID: working.UserID, AccountScopeID: working.AccountScopeID, ClientRequestID: "create-artifact", IdempotencyKey: "create-artifact", PayloadHash: string(raw), Kind: V3SessionMutationArtifactV2WorkingCreated, ArtifactV2: &mutation, NowUnixMs: 2000}
	result, err := sessions.ApplyV3SessionMutation(input)
	if err != nil || result.ArtifactV2 == nil || result.ArtifactV2.State != ArtifactV2StateAllocated || result.RealtimeOutbox == nil {
		t.Fatalf("artifact v2 create result=%+v err=%v", result, err)
	}
	replayed, err := sessions.ApplyV3SessionMutation(input)
	if err != nil || !replayed.Replayed || replayed.PrimarySeq != result.PrimarySeq || replayed.ArtifactV2 == nil || replayed.ArtifactV2.ArtifactID != working.ID {
		t.Fatalf("artifact v2 replay=%+v err=%v", replayed, err)
	}
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: working.SessionID, UserID: "user-2", AccountScopeID: "account-2", ClientRequestID: "foreign", IdempotencyKey: "foreign", PayloadHash: "foreign", Kind: V3SessionMutationArtifactV2WorkingCreated, ArtifactV2: &ArtifactV2Mutation{Working: &ArtifactV2WorkingArtifact{SchemaVersion: ArtifactV2SchemaVersion, ID: "artv2_foreign", AccountScopeID: "account-2", UserID: "user-2", SessionID: working.SessionID, Kind: "document", State: ArtifactV2StateAllocated, PolicyRevision: "policy-1", CapabilityClass: "designer", CreationRequestID: "foreign", Revision: 1}}}); err == nil {
		t.Fatal("cross-account artifact v2 create succeeded")
	}
	if _, ok, err := sessions.GetArtifactV2Working("account-2", "artv2_foreign"); err != nil || ok {
		t.Fatalf("cross-account rejection changed state ok=%v err=%v", ok, err)
	}

	restore := sessions.SetArtifactV2CommitHookForTest(func(string) error { return errors.New("injected artifact v2 commit failure") })
	failed := working
	failed.ID, failed.CreationRequestID = "artv2_failed", "failed"
	failedMutation := ArtifactV2Mutation{Working: &failed}
	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: failed.SessionID, UserID: failed.UserID, AccountScopeID: failed.AccountScopeID, ClientRequestID: "failed", IdempotencyKey: "failed", PayloadHash: "failed", Kind: V3SessionMutationArtifactV2WorkingCreated, ArtifactV2: &failedMutation})
	restore()
	if err == nil {
		t.Fatal("injected artifact v2 commit failure succeeded")
	}
	if _, ok, err := sessions.GetArtifactV2Working("account-1", failed.ID); err != nil || ok {
		t.Fatalf("failed mutation exposed state ok=%v err=%v", ok, err)
	}
	if _, ok, err := sessions.GetV3SessionOperationIdempotencyRecord("account-1", failed.SessionID, V3SessionMutationArtifactV2WorkingCreated, "failed"); err != nil || ok {
		t.Fatalf("failed mutation exposed idempotency ok=%v err=%v", ok, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions = NewSessionStore(store)
	persisted, ok, err := sessions.GetArtifactV2Working("account-1", working.ID)
	if err != nil || !ok || persisted.EventSeq != result.PrimarySeq || persisted.Revision != 1 {
		t.Fatalf("restart state=%+v ok=%v err=%v", persisted, ok, err)
	}
}

// Requirement: composition head movement is complete and compare-and-swap
// bound. Threat: a stale or partial selection could move the accepted head or
// replace only a subset of parts. Store-layer preflight is the narrowest place
// to assert rejection plus exact no-state-change postconditions.
func TestArtifactV2CompositionStaleCASLeavesHeadUnchanged(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForStoreTest(t, sessions, "artifact-v2-cas", "user-1", "account-1")
	working := ArtifactV2WorkingArtifact{SchemaVersion: ArtifactV2SchemaVersion, ID: "artv2_cas", AccountScopeID: "account-1", UserID: "user-1", SessionID: "artifact-v2-cas", Kind: "document", State: ArtifactV2StateAuthoring, PolicyRevision: "policy-1", CapabilityClass: "designer", CreationRequestID: "create", Revision: 1}
	applyArtifactV2TestMutation(t, sessions, "create", V3SessionMutationArtifactV2WorkingCreated, ArtifactV2Mutation{Working: &working})
	part := ArtifactV2Part{ID: "partv2_a", ArtifactID: working.ID, Key: "a", MediaClass: "text", Order: 1}
	working.Revision = 2
	expected := uint64(1)
	applyArtifactV2TestMutation(t, sessions, "part", V3SessionMutationArtifactV2PartDeclared, ArtifactV2Mutation{Working: &working, Part: &part, ExpectedWorkingRevision: &expected})
	revision := ArtifactV2PartRevision{ID: "prev2_a", ArtifactID: working.ID, PartID: part.ID, Blob: ArtifactV2BlobReceipt{RepositoryID: "repo", CommitOID: "0123456789012345678901234567890123456789", BlobOID: "0123456789012345678901234567890123456789", DigestSHA256: "digest", Size: 1, MediaType: "text/plain"}}
	working.Revision = 3
	expected = 2
	applyArtifactV2TestMutation(t, sessions, "revision", V3SessionMutationArtifactV2PartRevisionAppended, ArtifactV2Mutation{Working: &working, PartRevision: &revision, ExpectedWorkingRevision: &expected})
	selection := ArtifactV2CompositionPart{PartID: part.ID, PartRevisionID: revision.ID, DigestSHA256: revision.Blob.DigestSHA256}
	composition := ArtifactV2Composition{ID: "compv2_a", ArtifactID: working.ID, PolicyRevision: working.PolicyRevision, ConstructionVersion: "concat-v2", Parts: []ArtifactV2CompositionPart{selection}}
	composition.DigestSHA256 = ArtifactV2CompositionDigest(composition.PolicyRevision, composition.ConstructionVersion, composition.Parts)
	working.Revision = 4
	expected, head := 3, uint64(0)
	applyArtifactV2TestMutation(t, sessions, "composition", V3SessionMutationArtifactV2CompositionHeadAdvanced, ArtifactV2Mutation{Working: &working, Composition: &composition, ExpectedWorkingRevision: &expected, ExpectedCompositionHeadRevision: &head, AdvanceCompositionHead: true})
	before, _, _ := sessions.GetArtifactV2Working("account-1", working.ID)
	stale := before
	stale.Revision++
	bad := composition
	bad.ID = "compv2_stale"
	bad.DigestSHA256 = ArtifactV2CompositionDigest(bad.PolicyRevision, bad.ConstructionVersion, bad.Parts)
	wrongHead := uint64(0)
	_, err := sessions.ApplyV3SessionMutation(artifactV2TestInput("stale", V3SessionMutationArtifactV2CompositionHeadAdvanced, ArtifactV2Mutation{Working: &stale, Composition: &bad, ExpectedWorkingRevision: &before.Revision, ExpectedCompositionHeadRevision: &wrongHead, AdvanceCompositionHead: true}))
	if err == nil {
		t.Fatal("stale composition head mutation succeeded")
	}
	after, _, _ := sessions.GetArtifactV2Working("account-1", working.ID)
	if after.Revision != before.Revision || after.CompositionHead == nil || before.CompositionHead == nil || *after.CompositionHead != *before.CompositionHead {
		t.Fatalf("stale CAS changed head before=%+v after=%+v", before, after)
	}
	if _, ok, _ := sessions.GetArtifactV2Composition("account-1", working.ID, bad.ID); ok {
		t.Fatal("stale CAS persisted candidate composition")
	}
}

func applyArtifactV2TestMutation(t *testing.T, sessions *SessionStore, requestID, kind string, mutation ArtifactV2Mutation) V3SessionMutationResult {
	t.Helper()
	result, err := sessions.ApplyV3SessionMutation(artifactV2TestInput(requestID, kind, mutation))
	if err != nil {
		t.Fatalf("apply %s: %v", kind, err)
	}
	return result
}
func artifactV2TestInput(requestID, kind string, mutation ArtifactV2Mutation) V3SessionMutationInput {
	return V3SessionMutationInput{SessionID: "artifact-v2-cas", UserID: "user-1", AccountScopeID: "account-1", ClientRequestID: requestID, IdempotencyKey: requestID, PayloadHash: requestID, Kind: kind, ArtifactV2: &mutation, NowUnixMs: 2000}
}
