package pebblestore

import (
	"reflect"
	"testing"
)

// Requirement: only durable same-account Designer task lineage permits a parent
// to recover child source. Threat: forged, malformed or foreign session metadata
// grants cross-session access. The store helper is shared by read-only status
// and the serialized resume mutation; rejected checks must leave snapshots exact.
func TestArtifactV3RecoveryProducerLineage(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, s, "child")
	child, found, err := s.GetSession("child")
	if err != nil || !found { t.Fatal(err) }
	child.Metadata = map[string]any{"parent_session_id": "parent", "lineage_kind": "delegated_subagent", "subagent": "designer"}
	if err := s.UpdateSession(child); err != nil { t.Fatal(err) }
	if err := s.ValidateArtifactV3DraftProducer("account-1", "user-1", "parent", "child"); err != nil { t.Fatal(err) }
	for _, metadata := range []map[string]any{
		{"parent_session_id": "foreign", "lineage_kind": "delegated_subagent", "subagent": "designer"},
		{"parent_session_id": []string{"parent"}, "lineage_kind": "delegated_subagent", "subagent": "designer"},
		{"parent_session_id": "parent", "lineage_kind": "session_deploy", "subagent": "designer"},
		{"parent_session_id": "parent", "lineage_kind": "delegated_subagent", "subagent": "coder"},
	} {
		child.Metadata = metadata
		if err := s.UpdateSession(child); err != nil { t.Fatal(err) }
		before, _, _ := s.GetSession("child")
		if err := s.ValidateArtifactV3DraftProducer("account-1", "user-1", "parent", "child"); err == nil { t.Fatal("invalid lineage accepted") }
		after, _, _ := s.GetSession("child")
		if !reflect.DeepEqual(before, after) { t.Fatal("rejected lineage mutated session") }
	}
	if err := s.ValidateArtifactV3DraftProducer("foreign", "user-1", "parent", "child"); err == nil { t.Fatal("foreign account accepted") }
}

// Requirement: serialized child recovery checks producer liveness and all CAS,
// preserves exact project bytes, and invalidates readiness. Calling the mutation
// validator directly isolates admission from Git/rendering; the existing runtime
// resume lifecycle test owns publication/rebuild and old-handle integration.
func TestArtifactV3RecoveryChildResumeAdmission(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	for _, id := range []string{"parent", "child"} { createV3SessionForTest(t, s, id) }
	child, _, _ := s.GetSession("child")
	child.Metadata = map[string]any{"parent_session_id": "parent", "lineage_kind": "delegated_subagent", "subagent": "designer"}
	if err := s.UpdateSession(child); err != nil { t.Fatal(err) }
	setRun := func(session, run, status string) {
		t.Helper()
		_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: session, AccountScopeID: "account-1", UserID: "user-1", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: run+status, PayloadHash: run+status, RunIntent: &V3SessionRunIntent{SessionID: session, AccountScopeID: "account-1", UserID: "user-1", RunID: run, Status: status}})
		if err != nil { t.Fatal(err) }
	}
	setRun("child", "old-run", V3RunIntentPendingExecutor)
	setRun("child", "old-run", V3RunIntentRunning)
	setRun("parent", "new-run", V3RunIntentPendingExecutor)
	setRun("parent", "new-run", V3RunIntentRunning)
	old := ArtifactV3DraftProjection{GrantID: "old", Sequence: 2, Digest: "unchanged", Grant: []byte(`{"ID":"old","ExpiresAt":1}`), State: []byte(`{"ProducerSessionID":"child","ProducerRunID":"old-run","Publishing":false,"Finished":null,"Gate":null,"Project":{"asset":"exact"}}`)}
	next := old
	next.GrantID, next.ExpiresAt = "new", 2000
	next.Grant = []byte(`{"ID":"new","ExpiresAt":2000}`)
	next.State = []byte(`{"ProducerSessionID":"parent","ProducerRunID":"new-run","Publishing":false,"Finished":null,"Gate":null,"Project":{"asset":"exact"}}`)
	repository := ArtifactV3RepositoryProjection{EventSeq: 9, Drafts: map[string]ArtifactV3DraftProjection{"old": old}}
	input := V3SessionMutationInput{SessionID: "parent", AccountScopeID: "account-1", UserID: "user-1", ArtifactV3: &ArtifactV3Mutation{ExpectedDraftSequence: 2, Resume: &ArtifactV3DraftResume{GrantID: "old", ProjectionSeq: 9, ProducerRunID: "new-run"}}}
	if err := s.validateArtifactV3DraftResume(input, repository, next, 1000); err == nil { t.Fatal("active producer admitted") }
	setRun("child", "old-run", V3RunIntentCompleted)
	if err := s.validateArtifactV3DraftResume(input, repository, next, 1000); err != nil { t.Fatal(err) }
	for _, mutate := range []func(*ArtifactV3DraftProjection){
		func(d *ArtifactV3DraftProjection) { d.Digest = "changed" },
		func(d *ArtifactV3DraftProjection) { d.State = []byte(`{"ProducerSessionID":"parent","ProducerRunID":"new-run","Publishing":false,"Finished":null,"Gate":{},"Project":{"asset":"exact"}}`) },
	} {
		bad := next
		mutate(&bad)
		if err := s.validateArtifactV3DraftResume(input, repository, bad, 1000); err == nil { t.Fatal("changed bytes/readiness admitted") }
	}
	input.ArtifactV3.Resume.ProjectionSeq--
	if err := s.validateArtifactV3DraftResume(input, repository, next, 1000); err == nil { t.Fatal("stale projection admitted") }
	if !reflect.DeepEqual(repository.Drafts["old"], old) || len(repository.Drafts) != 1 { t.Fatal("admission mutated retained draft") }
}
