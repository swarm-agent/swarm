package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifactv2"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func newArtifactV2APITestServer(t *testing.T) (*Server, *artifactv2.Service, artifactv2.Principal) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "artifact-v2-api.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	created, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "artifact-v2-api", AccountScopeID: "account-1", UserID: "user-1", Title: "Artifact V2", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	blobs := &artifactV2APIBlobStore{}
	service := artifactv2.NewService(sessions, sessions, sessions, blobs)
	server := NewServer(nil, nil, nil, nil, sessions, nil, nil, nil, nil, nil, nil, nil, nil)
	server.SetArtifactV2Service(service)
	return server, service, artifactv2.Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: created.ID, ActorClass: "test"}
}

type artifactV2APIBlobStore struct{}

func (*artifactV2APIBlobStore) PutImmutable(_ context.Context, _ artifactv2.Principal, artifactID, partID, mediaType string, body []byte) (pebblestore.ArtifactV2BlobReceipt, error) {
	return pebblestore.ArtifactV2BlobReceipt{RepositoryID: "repo-" + artifactID, CommitOID: strings.Repeat("a", 40), BlobOID: strings.Repeat("b", 40), DigestSHA256: strings.Repeat("c", 64), Size: int64(len(body)), MediaType: mediaType}, nil
}
func (*artifactV2APIBlobStore) GetExact(context.Context, artifactv2.Principal, pebblestore.ArtifactV2BlobReceipt) ([]byte, error) {
	return []byte("body"), nil
}

func TestArtifactV2SessionCatalogShowsAllocatedWorkImmediately(t *testing.T) {
	server, service, principal := newArtifactV2APITestServer(t)
	working, err := service.CreateWorking(context.Background(), principal, artifactv2.CreateWorkingInput{RequestID: "create", ArtifactKind: "managed_creative", PolicyRevision: "policy", CapabilityClass: "managed", IntentReference: "Hero concept"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v3/sessions/artifact-v2-api/artifact-v2", nil)
	rec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		OK        bool                              `json:"ok"`
		Artifacts []sessionsV3ArtifactV2CatalogItem `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || len(payload.Artifacts) != 1 || !payload.Artifacts[0].ReadOnly || payload.Artifacts[0].Working.ID != working.ID || payload.Artifacts[0].Projection.State != pebblestore.ArtifactV2StateAllocated {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestArtifactV2PreviewRejectsInvalidWorkingStateWithoutExposingBytes(t *testing.T) {
	server, service, principal := newArtifactV2APITestServer(t)
	working, err := service.CreateWorking(context.Background(), principal, artifactv2.CreateWorkingInput{RequestID: "preview-create", ArtifactKind: "animation", PolicyRevision: "policy", CapabilityClass: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v3/sessions/artifact-v2-api/artifact-v2/"+working.ID+"/preview", nil)
	rec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(rec, withTestPrincipal(req))
	if rec.Code != http.StatusConflict || strings.Contains(rec.Body.String(), "body") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	after, _, _ := server.sessions.GetArtifactV2Working("account-1", working.ID)
	if after.Revision != working.Revision || after.CompositionHead != nil {
		t.Fatalf("rejected preview changed state: before=%+v after=%+v", working, after)
	}
}

func TestArtifactV2WriteEndpointIsGoneAndChangesNoHead(t *testing.T) {
	server, service, principal := newArtifactV2APITestServer(t)
	working, err := service.CreateWorking(context.Background(), principal, artifactv2.CreateWorkingInput{RequestID: "create", ArtifactKind: "managed_creative", PolicyRevision: "policy", CapabilityClass: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	part, err := service.DeclarePart(context.Background(), principal, artifactv2.DeclarePartInput{RequestID: "part", ArtifactID: working.ID, ExpectedRevision: working.Revision, Key: "hero", Label: "Hero", MediaClass: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	working, _, _ = server.sessions.GetArtifactV2Working("account-1", working.ID)
	revision, err := service.AppendPartRevision(context.Background(), principal, artifactv2.AppendPartRevisionInput{RequestID: "revision", ArtifactID: working.ID, PartID: part.ID, ExpectedWorkingRevision: working.Revision, MediaType: "text/html", Body: []byte("hero")})
	if err != nil {
		t.Fatal(err)
	}
	working, _, _ = server.sessions.GetArtifactV2Working("account-1", working.ID)
	_, err = service.AdvanceComposition(context.Background(), principal, artifactv2.AdvanceCompositionInput{RequestID: "composition", ArtifactID: working.ID, ExpectedWorkingRevision: working.Revision, ConstructionVersion: "concat-v2", Selections: []artifactv2.CompositionSelection{{PartID: part.ID, PartRevisionID: revision.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	working, _, _ = server.sessions.GetArtifactV2Working("account-1", working.ID)
	round, err := service.OpenIteration(context.Background(), principal, artifactv2.OpenIterationInput{RequestID: "round", ArtifactID: working.ID, ExpectedWorkingRevision: working.Revision, RequestedCandidates: 1, TargetPartIDs: []string{part.ID}})
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := server.sessions.GetArtifactV2Working("account-1", working.ID)
	body := `{"client_request_id":"stale","iteration_id":"` + round.ID + `","slot_id":"missing","expected_working_revision":999,"expected_iteration_revision":1}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/artifact-v2-api/artifact-v2/"+working.ID+"/select-candidate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(rec, withTestPrincipal(req))
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "read-only history") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	after, _, _ := server.sessions.GetArtifactV2Working("account-1", working.ID)
	if after.Revision != before.Revision || after.CompositionHead == nil || before.CompositionHead == nil || *after.CompositionHead != *before.CompositionHead {
		t.Fatalf("stale click changed head: before=%+v after=%+v", before, after)
	}
}
