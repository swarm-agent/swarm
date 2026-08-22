package runtime

import (
	"errors"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestArtifactMetadataBoundaryPublishesOnlyCommittedOutboxRecords(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	created, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{Title: "Artifacts", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var published []sessionruntime.RealtimeOutboxRecord
	boundary := &artifactMetadataBoundary{Service: sessions}
	boundary.SetPublisher(func(record sessionruntime.RealtimeOutboxRecord) error {
		published = append(published, record)
		return errors.New("wake failed after commit")
	})
	input := pebblestore.V3SessionMutationInput{
		SessionID: created.ID, ClientRequestID: "artifact-stage", IdempotencyKey: "artifact-stage",
		PayloadHash: "artifact-stage", RequestHash: "artifact-stage", Kind: pebblestore.V3SessionMutationCreateArtifact,
		Artifact: &pebblestore.V3ArtifactMutation{
			Collection: pebblestore.SessionArtifactCollection{ID: "collection-1", Name: "Draft"},
			Variant:    &pebblestore.SessionArtifactVariant{ID: "variant-1", CollectionID: "collection-1", Filename: "draft.txt", MediaType: "text/plain"},
		},
	}
	result, err := boundary.ApplySessionMutation(input)
	if err != nil {
		t.Fatalf("apply artifact mutation: %v", err)
	}
	if result.Replayed || len(published) != 1 || published[0].EndpointSeq == 0 {
		t.Fatalf("first mutation result=%#v published=%#v", result, published)
	}

	replayed, err := boundary.ApplySessionMutation(input)
	if err != nil {
		t.Fatalf("replay artifact mutation: %v", err)
	}
	if !replayed.Replayed || len(published) != 1 {
		t.Fatalf("replay result=%#v published=%#v", replayed, published)
	}
}
