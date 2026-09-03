package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: Artifact V3 HTTP projects exact complete revisions and selection
// uses the service's head/turn CAS. Regression threat: route translation to
// collection/variant state or a stale click moving head. The handler is the
// narrowest layer proving authentication, exact request fields, and status.
func TestArtifactV3HTTPDetailRevisionPreviewAndSelection(t *testing.T) {
	server, service := newArtifactV3APITestServer(t)
	service.artifact = artifactV3APITestArtifact()

	for _, route := range []string{
		"/v3/sessions/artifact-v3-api/artifacts-v3",
		"/v3/sessions/artifact-v3-api/artifacts-v3/artifact-1",
		"/v3/sessions/artifact-v3-api/artifacts-v3/artifact-1/revisions",
		"/v3/sessions/artifact-v3-api/artifacts-v3/artifact-1/revisions/" + strings.Repeat("a", 40),
	} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, withTestPrincipal(httptest.NewRequest(http.MethodGet, route, nil)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", route, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "collection_id") || strings.Contains(recorder.Body.String(), "variant_id") {
			t.Fatalf("legacy identity leaked from %s: %s", route, recorder.Body.String())
		}
	}

	preview := httptest.NewRecorder()
	server.Handler().ServeHTTP(preview, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/sessions/artifact-v3-api/artifacts-v3/artifact-1/preview?revision=rev-root", nil)))
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "Artifact V3") || preview.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("preview status=%d headers=%v body=%s", preview.Code, preview.Header(), preview.Body.String())
	}

	body := `{"client_request_id":"select-1","candidate_id":"candidate-1","expected_head_ref":"rev-root","expected_turn_revision":2}`
	selected := httptest.NewRecorder()
	server.Handler().ServeHTTP(selected, withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions/artifact-v3-api/artifacts-v3/artifact-1/turns/turn-1/select", strings.NewReader(body))))
	if selected.Code != http.StatusOK || service.lastSelect.ExpectedHeadRef != "rev-root" || service.lastSelect.ExpectedTurnRevision != 2 {
		t.Fatalf("selection status=%d request=%+v body=%s", selected.Code, service.lastSelect, selected.Body.String())
	}
}

// Requirement: owner/session authorization and stale-head rejection disclose no
// foreign Artifact state and do not mutate service state.
func TestArtifactV3HTTPRejectsForeignSessionAndStaleSelection(t *testing.T) {
	server, service := newArtifactV3APITestServer(t)
	service.artifact = artifactV3APITestArtifact()
	service.selectErr = pebblestore.ErrArtifactV3Conflict

	foreign := httptest.NewRecorder()
	server.Handler().ServeHTTP(foreign, withAccountPrincipal(httptest.NewRequest(http.MethodGet, "/v3/sessions/artifact-v3-api/artifacts-v3", nil), "other-account", "user-1"))
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	if service.calls != 0 {
		t.Fatalf("foreign request reached Artifact V3 service: %d", service.calls)
	}

	body := `{"client_request_id":"stale","candidate_id":"candidate-1","expected_head_ref":"stale-head","expected_turn_revision":2}`
	stale := httptest.NewRecorder()
	server.Handler().ServeHTTP(stale, withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions/artifact-v3-api/artifacts-v3/artifact-1/turns/turn-1/select", strings.NewReader(body))))
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "artifact_v3_conflict") {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}
	if service.selectionCount != 0 {
		t.Fatalf("stale service advanced head: %d", service.selectionCount)
	}
}

// Requirement: Artifact V3 projection events commit through the durable session
// mutation/outbox boundary and are replayable as native V3 events.
func TestArtifactV3ProjectionPublishesDurableReplayWithoutLegacyShape(t *testing.T) {
	server, _ := newArtifactV3APITestServer(t)
	artifact := artifactV3APITestArtifact()
	if err := server.PublishArtifactV3Projection(testPrincipal(), artifact, "artifact.v3.candidate.ready", "artifact-v3-event-1"); err != nil {
		t.Fatal(err)
	}
	events, err := server.sessions.ListSessionEvents("artifact-v3-api", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, event := range events {
		if event.EventType != "artifact.v3.candidate.ready" {
			continue
		}
		found = true
		if strings.Contains(string(event.Payload), "collection_id") || strings.Contains(string(event.Payload), "variant_id") || strings.Contains(string(event.Payload), "artifact.v2") {
			t.Fatalf("legacy state in durable payload: %s", event.Payload)
		}
		var projection ArtifactV3ProjectionPayload
		if err := json.Unmarshal(event.Payload, &projection); err != nil || projection.Artifact.ID != artifact.ID {
			t.Fatalf("projection=%+v err=%v", projection, err)
		}
		record := sessionruntime.RealtimeOutboxRecord{SessionID: "artifact-v3-api", Event: event}
		if replayed, ok := ArtifactV3ProjectionPayloadFromRecord(record); !ok || replayed.Artifact.Head.CommitOID != artifact.Head.CommitOID {
			t.Fatalf("replayed=%+v ok=%t", replayed, ok)
		}
	}
	if !found {
		t.Fatal("native Artifact V3 durable event not found")
	}
}

func newArtifactV3APITestServer(t *testing.T) (*Server, *artifactV3APIServiceFake) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "artifact-v3-api.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	_, _, err = sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "artifact-v3-api", AccountScopeID: "account-1", UserID: "user-1", Title: "Artifact V3", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(nil, nil, nil, nil, sessions, nil, nil, nil, nil, nil, nil, nil, nil)
	service := &artifactV3APIServiceFake{}
	server.SetArtifactV3Service(service)
	return server, service
}

func artifactV3APITestArtifact() ArtifactV3Artifact {
	commit := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)
	revision := ArtifactV3Revision{RevisionRef: "rev-root", CommitOID: commit, TreeOID: tree, ManifestBlobOID: strings.Repeat("c", 40), Manifest: pebblestore.ArtifactV3Manifest{SchemaVersion: pebblestore.ArtifactV3ManifestVersion, Entrypoint: "index.html", Parts: []pebblestore.ArtifactV3Part{{ID: "hero", Label: "Hero", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#hero"}}}}, FileCount: 2, TreeBytes: 100}
	build := &ArtifactV3BuildEvidence{ID: "build-1", Status: "succeeded", CommitOID: commit, TreeOID: tree}
	validation := &ArtifactV3ValidationEvidence{ID: "validation-1", Status: "valid", CommitOID: commit, TreeOID: tree, EvidenceDigests: []string{strings.Repeat("d", 64)}}
	revision.Build, revision.Validation = build, validation
	candidateRevision := revision
	candidateRevision.RevisionRef, candidateRevision.CommitOID = "rev-candidate", strings.Repeat("e", 40)
	return ArtifactV3Artifact{ID: "artifact-1", OwnerSessionID: "artifact-v3-api", IntentReference: "native project", Status: "ready", Revision: 2, PartCount: 1, Parts: revision.Manifest.Parts, Head: &revision, CurrentRevision: &revision, Revisions: []ArtifactV3Revision{revision}, Turns: []ArtifactV3Turn{{TurnID: "turn-1", Revision: 2, Status: "awaiting_selection", Intent: "Improve hero", BaseCommitOID: commit, BaseRevision: &revision, Candidates: []ArtifactV3Candidate{{CandidateID: "candidate-1", Status: "ready", Revision: &candidateRevision, Build: build, Validation: validation}}, CreatedAt: 1, UpdatedAt: 2}}, UpdatedAt: 2}
}

type artifactV3APIServiceFake struct {
	artifact       ArtifactV3Artifact
	lastSelect     ArtifactV3SelectCandidateRequest
	selectErr      error
	calls          int
	selectionCount int
}

func (f *artifactV3APIServiceFake) ListArtifacts(_ context.Context, _ ArtifactV3Principal, sessionID string, _ int) ([]ArtifactV3Artifact, error) {
	f.calls++
	if sessionID != f.artifact.OwnerSessionID {
		return nil, pebblestore.ErrArtifactV3Unauthorized
	}
	return []ArtifactV3Artifact{f.artifact}, nil
}
func (f *artifactV3APIServiceFake) GetArtifact(_ context.Context, _ ArtifactV3Principal, sessionID, artifactID string) (ArtifactV3Artifact, error) {
	f.calls++
	if sessionID != f.artifact.OwnerSessionID || artifactID != f.artifact.ID {
		return ArtifactV3Artifact{}, pebblestore.ErrArtifactV3NotFound
	}
	return f.artifact, nil
}
func (f *artifactV3APIServiceFake) ListRevisions(_ context.Context, _ ArtifactV3Principal, sessionID, artifactID, _ string, _ int) (ArtifactV3RevisionPage, error) {
	f.calls++
	if sessionID != f.artifact.OwnerSessionID || artifactID != f.artifact.ID {
		return ArtifactV3RevisionPage{}, pebblestore.ErrArtifactV3NotFound
	}
	return ArtifactV3RevisionPage{Revisions: f.artifact.Revisions}, nil
}
func (f *artifactV3APIServiceFake) GetRevision(_ context.Context, _ ArtifactV3Principal, sessionID, artifactID, commit string) (ArtifactV3Revision, error) {
	f.calls++
	if sessionID != f.artifact.OwnerSessionID || artifactID != f.artifact.ID || commit != f.artifact.Head.CommitOID {
		return ArtifactV3Revision{}, pebblestore.ErrArtifactV3NotFound
	}
	return *f.artifact.Head, nil
}
func (f *artifactV3APIServiceFake) OpenPreview(_ context.Context, _ ArtifactV3Principal, sessionID, artifactID, revision string) (ArtifactV3Preview, error) {
	f.calls++
	if sessionID != f.artifact.OwnerSessionID || artifactID != f.artifact.ID || revision != "rev-root" {
		return ArtifactV3Preview{}, pebblestore.ErrArtifactV3NotFound
	}
	return ArtifactV3Preview{RevisionRef: revision, CommitOID: f.artifact.Head.CommitOID, MediaType: "text/html; charset=utf-8", Body: []byte("<!doctype html><title>Artifact V3</title>"), ETag: `"artifact-v3-root"`}, nil
}
func (f *artifactV3APIServiceFake) OpenTurn(_ context.Context, _ ArtifactV3Principal, req ArtifactV3OpenTurnRequest) (ArtifactV3Turn, error) {
	f.calls++
	if req.ArtifactID != f.artifact.ID {
		return ArtifactV3Turn{}, pebblestore.ErrArtifactV3NotFound
	}
	return f.artifact.Turns[0], nil
}
func (f *artifactV3APIServiceFake) SelectCandidate(_ context.Context, _ ArtifactV3Principal, req ArtifactV3SelectCandidateRequest) (ArtifactV3SelectionResult, error) {
	f.calls++
	f.lastSelect = req
	if f.selectErr != nil {
		return ArtifactV3SelectionResult{}, f.selectErr
	}
	if req.ExpectedHeadRef != f.artifact.Head.RevisionRef || req.ExpectedTurnRevision != f.artifact.Turns[0].Revision {
		return ArtifactV3SelectionResult{}, pebblestore.ErrArtifactV3Conflict
	}
	candidate := f.artifact.Turns[0].Candidates[0]
	if candidate.Revision == nil {
		return ArtifactV3SelectionResult{}, errors.New("missing candidate")
	}
	f.selectionCount++
	turn := f.artifact.Turns[0]
	turn.Status, turn.SelectedCandidateID = "selected", candidate.CandidateID
	return ArtifactV3SelectionResult{Head: *candidate.Revision, Turn: turn}, nil
}

func withAccountPrincipal(r *http.Request, accountScopeID, userID string) *http.Request {
	principal := testPrincipal()
	principal.AccountScopeID = accountScopeID
	principal.UserID = userID
	return r.WithContext(identity.ContextWithPrincipal(r.Context(), principal))
}
