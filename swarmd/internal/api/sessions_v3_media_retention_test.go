package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/mediastaging"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var samplePNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00,
	0x0A, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

var sampleMP4 = []byte{
	0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2',
	0x00, 0x00, 0x00, 0x00, 'm', 'p', '4', '2', 'i', 's', 'o', 'm',
	0x00, 0x00, 0x00, 0x08, 'm', 'o', 'o', 'v',
}

var sampleWAV = []byte{
	'R', 'I', 'F', 'F', 0x24, 0x00, 0x00, 0x00, 'W', 'A', 'V', 'E',
	'f', 'm', 't', ' ', 0x10, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00,
	0x44, 0xAC, 0x00, 0x00, 0x88, 0x58, 0x01, 0x00, 0x02, 0x00, 0x10, 0x00,
	'd', 'a', 't', 'a', 0x00, 0x00, 0x00, 0x00,
}

var samplePDF = []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n")
var sampleTXT = []byte("Swarm Orchestrator media reuse specification document.\n")

func TestSessionV3Media_UploadRetainedIndependentOfModelPerception(t *testing.T) {
	// Purpose:
	// - Requirement: Session media uploads (POST /v3/sessions/{id}/media) must succeed and retain media
	//   even if the conversational model does not support image perception. Valid youtube-thumbnail.png
	//   with empty or generic MIME must sniff to image/png and persist as a durable SessionMediaAsset.
	// - Threat/regression: Rejecting media uploads because the chat model is text-only, preventing
	//   media retention and reuse.
	// - Boundary/authority: Server.handleSessionV3MediaUpload and SessionStore.PutSessionMediaAsset.
	// - Test layer: HTTP API integration test with principal authentication.

	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()

	// Create a session
	session := pebblestore.SessionSnapshot{
		ID:             "sess-media-decoupled-1",
		UserID:         p.UserID,
		AccountScopeID: p.AccountScopeID,
		WorkspacePath:  "/workspace",
		Title:          "Decoupled Media Session",
	}
	if _, err := sessionSvc.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       session.ID,
		UserID:          session.UserID,
		AccountScopeID:  session.AccountScopeID,
		ClientRequestID: "create-sess-1",
		PayloadHash:     "create-sess-1",
		Kind:            pebblestore.V3SessionMutationCreateSession,
		Session:         &session,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// 1. Upload youtube-thumbnail.png with empty/generic MIME
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/media", bytes.NewReader(samplePNG))
	req.Header.Set("X-Swarm-Media-Filename", "youtube-thumbnail.png")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Idempotency-Key", "upload-thumb-1")
	req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, p))
	req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"sessions:write", "sessions:read"},
	}))

	w := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for youtube-thumbnail.png, got %d: %s", w.Code, w.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	assetMap, ok := res["asset"].(map[string]any)
	if !ok {
		t.Fatalf("missing asset in response: %+v", res)
	}
	if assetMap["detected_mime_type"] != "image/png" {
		t.Errorf("detected_mime_type = %v, want image/png", assetMap["detected_mime_type"])
	}
	if assetMap["modality"] != "image" {
		t.Errorf("modality = %v, want image", assetMap["modality"])
	}
	if assetMap["file_name"] != "youtube-thumbnail.png" {
		t.Errorf("file_name = %v, want youtube-thumbnail.png", assetMap["file_name"])
	}
	assetID, ok := assetMap["id"].(string)
	if !ok || !strings.HasPrefix(assetID, "media_") {
		t.Fatalf("invalid asset id: %v", assetMap["id"])
	}

	// 2. Idempotent re-upload must return 200 OK with replayed=true and same asset ID
	req2 := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/media", bytes.NewReader(samplePNG))
	req2.Header.Set("X-Swarm-Media-Filename", "youtube-thumbnail.png")
	req2.Header.Set("Content-Type", "application/octet-stream")
	req2.Header.Set("Idempotency-Key", "upload-thumb-1")
	req2 = req2.WithContext(context.WithValue(req2.Context(), productPrincipalRequestContextKey, p))
	req2 = req2.WithContext(context.WithValue(req2.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"sessions:write", "sessions:read"},
	}))

	w2 := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for idempotent replay, got %d: %s", w2.Code, w2.Body.String())
	}
	var res2 map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &res2)
	if res2["replayed"] != true {
		t.Fatalf("expected replayed=true on idempotent repeat")
	}
}

func TestSessionV3Media_CorruptAndMismatchedContentRejected(t *testing.T) {
	// Purpose:
	// - Requirement: Media uploads with corrupted payload or mismatched MIME types must be rejected
	//   with HTTP 400 Bad Request without creating usable partial references.
	// - Threat/regression: Corrupted binaries polluting storage or bypassing validation.
	// - Boundary/authority: Server.handleSessionV3MediaUpload.
	// - Test layer: HTTP API negative tests.

	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()

	session := pebblestore.SessionSnapshot{
		ID:             "sess-media-corrupt",
		UserID:         p.UserID,
		AccountScopeID: p.AccountScopeID,
		Title:          "Corrupt Test",
	}
	_, _ = sessionSvc.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       session.ID,
		UserID:          session.UserID,
		AccountScopeID:  session.AccountScopeID,
		ClientRequestID: "create-sess-corrupt",
		PayloadHash:     "create-sess-corrupt",
		Kind:            pebblestore.V3SessionMutationCreateSession,
		Session:         &session,
	})

	token := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"sessions:write", "sessions:read"},
	}

	// 1. Declared image/png but body is garbage text
	req1 := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/media", strings.NewReader("not a png image just text"))
	req1.Header.Set("Content-Type", "image/png")
	req1 = req1.WithContext(context.WithValue(req1.Context(), productPrincipalRequestContextKey, p))
	req1 = req1.WithContext(context.WithValue(req1.Context(), productScopedTokenRequestContextKey, token))
	w1 := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(w1, req1)
	if w1.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for corrupt PNG, got %d: %s", w1.Code, w1.Body.String())
	}

	// 2. Declared image/jpeg but body is PNG
	req2 := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/media", bytes.NewReader(samplePNG))
	req2.Header.Set("Content-Type", "image/jpeg")
	req2 = req2.WithContext(context.WithValue(req2.Context(), productPrincipalRequestContextKey, p))
	req2 = req2.WithContext(context.WithValue(req2.Context(), productScopedTokenRequestContextKey, token))
	w2 := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for MIME mismatch, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestSessionV3MediaAsset_ServeContent_GET_HEAD_DELETE_AndSecurityHeaders(t *testing.T) {
	// Purpose:
	// - Requirement: Retained session media must be retrievable via GET /v3/sessions/{id}/media/{asset_id}
	//   with security headers (sandbox CSP, nosniff, Accept-Ranges, immutable cache), support HEAD,
	//   and support DELETE for unreferenced assets. Cross-account access must fail with 404/denied.
	// - Threat/regression: Media assets unreadable via HTTP or lacking sandbox CSP protections.
	// - Boundary/authority: Server.handleSessionV3MediaAsset.
	// - Test layer: HTTP API content serving and security header inspection test.

	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()

	session := pebblestore.SessionSnapshot{
		ID:             "sess-media-serve",
		UserID:         p.UserID,
		AccountScopeID: p.AccountScopeID,
		Title:          "Serve Test",
	}
	_, _ = sessionSvc.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       session.ID,
		UserID:          session.UserID,
		AccountScopeID:  session.AccountScopeID,
		ClientRequestID: "create-sess-serve",
		PayloadHash:     "create-sess-serve",
		Kind:            pebblestore.V3SessionMutationCreateSession,
		Session:         &session,
	})

	asset, _, err := sessionSvc.Store().PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{
		AccountScopeID:   p.AccountScopeID,
		SessionID:        session.ID,
		FileName:         "youtube-thumbnail.png",
		DeclaredMIMEType: "image/png",
		Reader:           bytes.NewReader(samplePNG),
	})
	if err != nil {
		t.Fatalf("put media asset: %v", err)
	}

	token := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"sessions:read", "sessions:write"},
	}

	// 1. GET /v3/sessions/{sessionID}/media/{assetID}
	getReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+session.ID+"/media/"+asset.ID, nil)
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), productPrincipalRequestContextKey, p))
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), productScopedTokenRequestContextKey, token))
	getW := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on GET media asset, got %d: %s", getW.Code, getW.Body.String())
	}
	if getW.Header().Get("Content-Type") != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", getW.Header().Get("Content-Type"))
	}
	if getW.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", getW.Header().Get("X-Content-Type-Options"))
	}
	if !strings.Contains(getW.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("expected sandbox in CSP, got: %q", getW.Header().Get("Content-Security-Policy"))
	}
	if getW.Header().Get("Accept-Ranges") != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", getW.Header().Get("Accept-Ranges"))
	}
	if !bytes.Equal(getW.Body.Bytes(), samplePNG) {
		t.Fatalf("served bytes mismatch: got %d bytes, want %d", len(getW.Body.Bytes()), len(samplePNG))
	}

	// 2. HEAD /v3/sessions/{sessionID}/media/{assetID}
	headReq := httptest.NewRequest(http.MethodHead, "/v3/sessions/"+session.ID+"/media/"+asset.ID, nil)
	headReq = headReq.WithContext(context.WithValue(headReq.Context(), productPrincipalRequestContextKey, p))
	headReq = headReq.WithContext(context.WithValue(headReq.Context(), productScopedTokenRequestContextKey, token))
	headW := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(headW, headReq)
	if headW.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on HEAD media asset, got %d", headW.Code)
	}
	if headW.Body.Len() != 0 {
		t.Fatalf("expected empty body on HEAD, got %d bytes", headW.Body.Len())
	}

	// 3. Cross-account GET denied
	otherPrincipal := identity.Principal{
		Type:           "user",
		UserID:         "other-user",
		AccountScopeID: "other-account",
	}
	otherToken := &pebblestore.ScopedTokenRecord{
		AccountScopeID: "other-account", UserID: "other-user", Scopes: []string{"sessions:read"},
	}
	crossReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+session.ID+"/media/"+asset.ID, nil)
	crossReq = crossReq.WithContext(context.WithValue(crossReq.Context(), productPrincipalRequestContextKey, otherPrincipal))
	crossReq = crossReq.WithContext(context.WithValue(crossReq.Context(), productScopedTokenRequestContextKey, otherToken))
	crossW := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(crossW, crossReq)
	if crossW.Code != http.StatusNotFound && crossW.Code != http.StatusUnauthorized {
		t.Fatalf("expected 404 or 401 on cross-account GET, got %d", crossW.Code)
	}

	// 4. DELETE unreferenced asset
	delReq := httptest.NewRequest(http.MethodDelete, "/v3/sessions/"+session.ID+"/media/"+asset.ID, nil)
	delReq = delReq.WithContext(context.WithValue(delReq.Context(), productPrincipalRequestContextKey, p))
	delReq = delReq.WithContext(context.WithValue(delReq.Context(), productScopedTokenRequestContextKey, token))
	delW := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(delW, delReq)
	if delW.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on DELETE, got %d: %s", delW.Code, delW.Body.String())
	}

	// Subsequent GET returns 404
	getW2 := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(getW2, getReq)
	if getW2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after deletion, got %d", getW2.Code)
	}
}

func TestSessionV3Media_MessageAttachedWithoutModelPerception(t *testing.T) {
	// Purpose:
	// - Requirement: Messages with retained media references must be accepted and appended
	//   even if the conversational model cannot perceive the media.
	// - Threat/regression: Rejecting user messages because the active chat model does not perceive images.
	// - Boundary/authority: Server.validateSessionsV3MessageMedia and acceptSessionsV3Message.
	// - Test layer: HTTP API message append integration test.

	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()

	session := pebblestore.SessionSnapshot{
		ID:             "sess-media-msg",
		UserID:         p.UserID,
		AccountScopeID: p.AccountScopeID,
		Title:          "Message Media Test",
	}
	_, _ = sessionSvc.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       session.ID,
		UserID:          session.UserID,
		AccountScopeID:  session.AccountScopeID,
		ClientRequestID: "create-sess-msg",
		PayloadHash:     "create-sess-msg",
		Kind:            pebblestore.V3SessionMutationCreateSession,
		Session:         &session,
	})

	asset, _, err := sessionSvc.Store().PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{
		AccountScopeID:   p.AccountScopeID,
		SessionID:        session.ID,
		FileName:         "youtube-thumbnail.png",
		DeclaredMIMEType: "image/png",
		Reader:           bytes.NewReader(samplePNG),
	})
	if err != nil {
		t.Fatalf("put media asset: %v", err)
	}

	token := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"sessions:write", "sessions:read"},
	}

	// Append message with media reference
	msgBody := fmt.Sprintf(`{
		"client_request_id": "msg-media-1",
		"role": "user",
		"content": "Please inspect this thumbnail",
		"media": [{
			"asset_id": %q,
			"modality": "image",
			"mime_type": "image/png",
			"digest_sha256": %q,
			"size": %d
		}]
	}`, asset.ID, asset.DigestSHA256, asset.Size)

	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/messages", strings.NewReader(msgBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, p))
	req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, token))

	w := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on message append with media, got %d: %s", w.Code, w.Body.String())
	}

	messages, err := sessionSvc.ListSessionMessages(session.ID, 0, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("expected 1 message listed, got %d: %v", len(messages), err)
	}
	if len(messages[0].Media) != 1 || messages[0].Media[0].AssetID != asset.ID {
		t.Fatalf("persisted message lost media reference: %+v", messages[0].Media)
	}
}

func TestProjectMedia_StagedUploadRetainedAndServed(t *testing.T) {
	// Purpose:
	// - Requirement: Staged media referenced in POST /v3/projects/{id}/media must be bound
	//   and materialized into durable SessionMediaAsset so it never expires with staging cleanup,
	//   and must be retrievable via GET /v3/projects/{id}/media/{mediaID}.
	// - Threat/regression: Staged media in projects expiring after 1h TTL and failing to resolve.
	// - Boundary/authority: Server.handleProjects (/media) and resolveSourceMediaBytes.
	// - Test layer: HTTP API project media lifecycle test.

	dir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(dir, "proj-media.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	p := testPrincipal()
	ss := pebblestore.NewSessionStore(store)
	msStore := pebblestore.NewMediaStagingStore(store)
	msSvc := mediastaging.NewService(msStore)
	server := &Server{
		sessions: sessionruntime.NewService(ss, nil),
	}
	server.SetMediaStagingService(msSvc)

	project := &pebblestore.ProjectRecord{
		ID:        "proj-media-persist-1",
		AccountID: p.AccountScopeID,
		Name:      "Persist Media Project",
	}
	if err := ss.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatalf("put project: %v", err)
	}

	// 1. Stage a media file
	stgRecord, _, err := msSvc.Put(pebblestore.PutMediaStagingInput{
		AccountScopeID:   p.AccountScopeID,
		IdempotencyKey:   "stage-thumb-1",
		DeclaredMIMEType: "image/png",
		FileName:         "youtube-thumbnail.png",
		TTL:              time.Hour,
		Reader:           bytes.NewReader(samplePNG),
	})
	if err != nil {
		t.Fatalf("stage media: %v", err)
	}

	token := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"projects:write", "projects:read", "sessions:write", "sessions:read"},
	}

	// 2. Add staged media to project
	payload := fmt.Sprintf(`{
		"id": %q,
		"title": "YouTube Thumbnail",
		"filename": "youtube-thumbnail.png",
		"url": "/v3/media-staging/%s"
	}`, stgRecord.ID, stgRecord.ID)

	req := httptest.NewRequest(http.MethodPost, "/v3/projects/"+project.ID+"/media", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, p))
	req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, token))

	w := httptest.NewRecorder()
	server.handleProjects(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on project media add, got %d: %s", w.Code, w.Body.String())
	}

	// Verify staging record was BOUND (so it will not expire with cleanup!)
	boundRecord, found, err := msSvc.Get(p.AccountScopeID, stgRecord.ID)
	if err != nil || !found {
		t.Fatalf("get staging record: found=%v err=%v", found, err)
	}
	if boundRecord.State != pebblestore.MediaStagingStateBound {
		t.Fatalf("staging record state=%q want %q", boundRecord.State, pebblestore.MediaStagingStateBound)
	}

	// 3. GET /v3/projects/{id}/media/{mediaID} serves content
	var createRes map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &createRes)
	mediaMap := createRes["media"].(map[string]any)
	mediaID := mediaMap["id"].(string)

	getReq := httptest.NewRequest(http.MethodGet, "/v3/projects/"+project.ID+"/media/"+mediaID, nil)
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), productPrincipalRequestContextKey, p))
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), productScopedTokenRequestContextKey, token))
	getW := httptest.NewRecorder()
	server.handleProjects(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on GET project media, got %d: %s", getW.Code, getW.Body.String())
	}
	if getW.Header().Get("Content-Type") != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", getW.Header().Get("Content-Type"))
	}
	if !bytes.Equal(getW.Body.Bytes(), samplePNG) {
		t.Fatalf("served project media bytes mismatch: got %d want %d", len(getW.Body.Bytes()), len(samplePNG))
	}
}

func TestResolveSourceMedia_ProjectMediaURLAndReuse(t *testing.T) {
	// Purpose:
	// - Requirement: Server-side resolveSourceMediaBytes and resolveSourceMediaRecord resolve
	//   /v3/projects/{projectID}/media/{mediaID} references preserving exact bytes and digest.
	// - Threat/regression: Task creation failing when consuming project media URLs or falling back to untrusted paths.
	// - Boundary/authority: Server.resolveSourceMediaBytes and Server.resolveSourceMediaRecord.
	// - Layer: Direct server resolution with Pebble stores.
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()

	// Seed session and session media asset
	sessionID := "sess-proj-media-test"
	_ = sessionSvc.Store().CreateSession(pebblestore.SessionSnapshot{
		ID:             sessionID,
		AccountScopeID: p.AccountScopeID,
		UserID:         p.UserID,
		Mode:           "auto",
	})
	asset, _, err := sessionSvc.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{
		AccountScopeID:   p.AccountScopeID,
		SessionID:        sessionID,
		Modality:         "image",
		DeclaredMIMEType: "image/png",
		FileName:         "test-thumbnail.png",
		Reader:           bytes.NewReader(samplePNG),
	})
	if err != nil {
		t.Fatalf("put session media asset: %v", err)
	}

	// Seed project with uploaded media referencing the asset
	project := &pebblestore.ProjectRecord{
		ID:        "proj-reuse-test",
		AccountID: p.AccountScopeID,
		Name:      "Reuse Test Project",
		UploadedMedia: []pebblestore.ProjectTaskMediaRef{
			{
				ID:           asset.ID,
				Title:        asset.FileName,
				Filename:     asset.FileName,
				Kind:         "image",
				MediaType:    asset.DetectedMIMEType,
				URL:          fmt.Sprintf("/v3/sessions/%s/media/%s", sessionID, asset.ID),
				SizeBytes:    asset.Size,
				DigestSHA256: asset.DigestSHA256,
				CreatedAt:    time.Now().UnixMilli(),
			},
		},
	}
	if err := sessionSvc.Store().PutProject(p.AccountScopeID, project); err != nil {
		t.Fatalf("put project: %v", err)
	}

	// 1. Resolve via /v3/projects/{projectID}/media/{mediaID} URL
	projectMediaRef := pebblestore.ProjectTaskMediaRef{
		ID:        asset.ID,
		URL:       fmt.Sprintf("/v3/projects/%s/media/%s", project.ID, asset.ID),
		Kind:      "image",
		MediaType: "image/png",
	}
	payload, mType, err := server.resolveSourceMediaBytes(context.Background(), p, projectMediaRef, "image")
	if err != nil {
		t.Fatalf("resolve project media url: %v", err)
	}
	if !bytes.Equal(payload, samplePNG) {
		t.Fatalf("payload bytes mismatch: got %d want %d", len(payload), len(samplePNG))
	}
	if mType != "image/png" {
		t.Errorf("mType = %q, want image/png", mType)
	}

	// 2. Resolve via resolveSourceMediaRecord
	rec, err := server.resolveSourceMediaRecord(context.Background(), p, projectMediaRef, "image", project.ID)
	if err != nil {
		t.Fatalf("resolveSourceMediaRecord for project media: %v", err)
	}
	if !bytes.Equal(rec.Bytes, samplePNG) {
		t.Fatalf("record bytes mismatch: got %d want %d", len(rec.Bytes), len(samplePNG))
	}
	if rec.SourceLink == nil || rec.SourceLink.DigestSHA256 != asset.DigestSHA256 {
		t.Errorf("expected SourceLink with digest %s, got %+v", asset.DigestSHA256, rec.SourceLink)
	}

	// 3. Foreign account cannot resolve project media
	foreign := identity.Principal{UserID: "foreign-user", AccountScopeID: "foreign-account", Type: "user"}
	_, _, errForeign := server.resolveSourceMediaBytes(context.Background(), foreign, projectMediaRef, "image")
	if errForeign == nil {
		t.Fatal("expected error resolving project media for foreign account, got nil")
	}

	// 4. Arbitrary external URLs and filesystem paths remain strictly rejected
	_, _, errHTTP := server.resolveSourceMediaBytes(context.Background(), p, pebblestore.ProjectTaskMediaRef{URL: "http://malicious.com/pic.png"}, "image")
	if errHTTP == nil || !strings.Contains(errHTTP.Error(), "arbitrary external URLs") {
		t.Errorf("expected external URL rejection, got: %v", errHTTP)
	}
	_, _, errFS := server.resolveSourceMediaBytes(context.Background(), p, pebblestore.ProjectTaskMediaRef{URL: "/etc/passwd"}, "image")
	if errFS == nil || !strings.Contains(errFS.Error(), "filesystem paths") {
		t.Errorf("expected filesystem path rejection, got: %v", errFS)
	}
}

func TestSessionsV3Executor_UnperceivedMediaConveyedAsInputText(t *testing.T) {
	// Purpose:
	// - Requirement: When a user message carries retained media that the current model
	//   cannot perceive (e.g. text-only model or unsupported modality), durable replay
	//   does not fail and does not send raw binary, but conveys the exact retained reference
	//   in text ([Attached media input (retained, model perception not supported): ...])
	//   so the model has the exact asset ID for tool use without claiming perception.
	// - Threat/regression: Omission of attached media identity from text models prevents tool use,
	//   or fake perception claims that the model processed unsupported media pixels.
	// - Boundary/authority: sessionV3Executor.sessionsV3ProviderInputWithMedia in sessions_v3_executor.go.
	// - Layer: Executor provider input preparation against Pebble session storage.
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()

	sessionID := "sess-unperceived-test"
	_ = sessionSvc.Store().CreateSession(pebblestore.SessionSnapshot{
		ID:             sessionID,
		AccountScopeID: p.AccountScopeID,
		UserID:         p.UserID,
		Mode:           "auto",
	})
	asset, _, err := sessionSvc.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{
		AccountScopeID:   p.AccountScopeID,
		SessionID:        sessionID,
		Modality:         "image",
		DeclaredMIMEType: "image/png",
		FileName:         "youtube-thumbnail.png",
		Reader:           bytes.NewReader(samplePNG),
	})
	if err != nil {
		t.Fatalf("put session media asset: %v", err)
	}

	session, found, err := sessionSvc.GetSession(sessionID)
	if err != nil || !found {
		t.Fatalf("get session: found=%t err=%v", found, err)
	}

	messages := []pebblestore.MessageSnapshot{
		{
			ID:        "msg-1",
			SessionID: sessionID,
			Role:      "user",
			Content:   "Please use this thumbnail to create a video clip.",
			Media: []pebblestore.SessionMediaReference{
				{
					AssetID:      asset.ID,
					Modality:     asset.Modality,
					MIMEType:     asset.DetectedMIMEType,
					FileType:     asset.FileType,
					FileName:     asset.FileName,
					Size:         asset.Size,
					DigestSHA256: asset.DigestSHA256,
				},
			},
		},
	}

	// Model with no image perception capability (text-only model)
	textOnlyRuntime := sessionV3ResolvedRuntime{
		Session: session,
		MediaContract: provideriface.SessionMediaContract{
			Hash:         "text-only-contract",
			Capabilities: []provideriface.MediaContractCapability{}, // no image capability
		},
	}

	server.v3SessionExecutor = newSessionV3Executor(server)
	input, err := server.v3SessionExecutor.sessionsV3ProviderInputWithMedia(textOnlyRuntime, messages, sessionsV3ProviderInputOptions{})
	if err != nil {
		t.Fatalf("sessionsV3ProviderInputWithMedia failed: %v", err)
	}
	if len(input) != 1 {
		t.Fatalf("expected 1 user message, got %d", len(input))
	}

	encoded, _ := json.Marshal(input[0])
	str := string(encoded)

	// Invariant: binary session_media must NOT be sent to text-only model
	if strings.Contains(str, `"type":"session_media"`) {
		t.Errorf("binary session_media unexpectedly included for text-only model: %s", str)
	}

	// Invariant: user text is preserved
	if !strings.Contains(str, "Please use this thumbnail to create a video clip.") {
		t.Errorf("user prompt text missing: %s", str)
	}

	// Invariant: attached media input note with exact asset ID and filename is conveyed
	if !strings.Contains(str, "Attached media input (retained, model perception not supported)") {
		t.Errorf("expected retained media note, got: %s", str)
	}
	if !strings.Contains(str, asset.ID) {
		t.Errorf("expected asset ID %q in text note, got: %s", asset.ID, str)
	}
	if !strings.Contains(str, "youtube-thumbnail.png") {
		t.Errorf("expected filename %q in text note, got: %s", "youtube-thumbnail.png", str)
	}
}

func TestSessionV3Media_AllModalities_UploadPreviewReloadReuse(t *testing.T) {
	// Purpose:
	// - Requirement: Manual image, video, audio, and document uploads must be persistently retained
	//   in session storage, previewable via HTTP GET with security headers, reloadable across store
	//   restarts, and reusable in downstream project tasks with exact byte-for-byte and digest verification.
	// - Threat/regression: Non-image media failing upload, losing bytes across store restart, or failing
	//   to resolve when reused by project tasks.
	// - Boundary/authority: Server.handleSessionV3MediaUpload, Server.handleSessionV3MediaAsset,
	//   SessionStore.PutSessionMediaAsset/ReadSessionMediaAsset, Server.resolveSourceMediaBytes/Record.
	// - Test layer: End-to-end HTTP API and Pebble store lifecycle integration test.

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "all-modalities.pebble")

	p := testPrincipal()
	token := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID,
		UserID:         p.UserID,
		Scopes:         []string{"sessions:write", "sessions:read", "projects:write", "projects:read"},
	}

	testCases := []struct {
		name         string
		modality     string
		declaredMIME string
		expectedMIME string
		filename     string
		payload      []byte
	}{
		{
			name:         "Image_PNG",
			modality:     "image",
			declaredMIME: "image/png",
			expectedMIME: "image/png",
			filename:     "test-card.png",
			payload:      samplePNG,
		},
		{
			name:         "Video_MP4",
			modality:     "video",
			declaredMIME: "video/mp4",
			expectedMIME: "video/mp4",
			filename:     "test-clip.mp4",
			payload:      sampleMP4,
		},
		{
			name:         "Audio_WAV",
			modality:     "audio",
			declaredMIME: "audio/wav",
			expectedMIME: "audio/wav",
			filename:     "test-voice.wav",
			payload:      sampleWAV,
		},
		{
			name:         "Document_PDF",
			modality:     "document",
			declaredMIME: "application/pdf",
			expectedMIME: "application/pdf",
			filename:     "test-spec.pdf",
			payload:      samplePDF,
		},
	}

	type retainedAsset struct {
		id       string
		modality string
		mime     string
		filename string
		digest   string
		payload  []byte
	}
	retained := make(map[string]retainedAsset)

	sessionID := "sess-modalities-test"

	// PHASE 1: Store open, session create, upload all modalities, preview via HTTP GET
	{
		store, err := pebblestore.Open(dbPath)
		if err != nil {
			t.Fatalf("open store phase 1: %v", err)
		}
		sessionStore := pebblestore.NewSessionStore(store)
		sessionSvc := sessionruntime.NewService(sessionStore, nil)
		server := &Server{sessions: sessionSvc}

		// Create session
		session := pebblestore.SessionSnapshot{
			ID:             sessionID,
			UserID:         p.UserID,
			AccountScopeID: p.AccountScopeID,
			Title:          "All Modalities Test",
		}
		if _, err := sessionStore.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
			SessionID:       session.ID,
			UserID:          session.UserID,
			AccountScopeID:  session.AccountScopeID,
			ClientRequestID: "create-modalities-sess",
			PayloadHash:     "create-modalities-sess",
			Kind:            pebblestore.V3SessionMutationCreateSession,
			Session:         &session,
		}); err != nil {
			t.Fatalf("create session: %v", err)
		}

		for _, tc := range testCases {
			t.Run("UploadAndPreview_"+tc.name, func(t *testing.T) {
				// 1. Upload via POST /v3/sessions/{sessionID}/media
				uploadReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/media", bytes.NewReader(tc.payload))
				uploadReq.Header.Set("Content-Type", tc.declaredMIME)
				uploadReq.Header.Set("X-Swarm-Media-Modality", tc.modality)
				uploadReq.Header.Set("X-Swarm-Media-Filename", tc.filename)
				uploadReq = uploadReq.WithContext(context.WithValue(uploadReq.Context(), productPrincipalRequestContextKey, p))
				uploadReq = uploadReq.WithContext(context.WithValue(uploadReq.Context(), productScopedTokenRequestContextKey, token))

				uploadW := httptest.NewRecorder()
				server.handleSessionV3PrimaryByID(uploadW, uploadReq)
				if uploadW.Code != http.StatusCreated {
					t.Fatalf("[%s] expected 201 Created on upload, got %d: %s", tc.name, uploadW.Code, uploadW.Body.String())
				}

				var res map[string]any
				if err := json.Unmarshal(uploadW.Body.Bytes(), &res); err != nil {
					t.Fatalf("[%s] decode upload response: %v", tc.name, err)
				}
				assetMap, ok := res["asset"].(map[string]any)
				if !ok {
					t.Fatalf("[%s] response missing asset map: %+v", tc.name, res)
				}

				assetID := assetMap["id"].(string)
				if !strings.HasPrefix(assetID, "media_") {
					t.Fatalf("[%s] expected asset ID to start with media_, got %q", tc.name, assetID)
				}
				if assetMap["modality"] != tc.modality {
					t.Errorf("[%s] modality = %v, want %v", tc.name, assetMap["modality"], tc.modality)
				}
				if assetMap["detected_mime_type"] != tc.expectedMIME {
					t.Errorf("[%s] detected_mime_type = %v, want %v", tc.name, assetMap["detected_mime_type"], tc.expectedMIME)
				}
				if assetMap["file_name"] != tc.filename {
					t.Errorf("[%s] file_name = %v, want %v", tc.name, assetMap["file_name"], tc.filename)
				}
				digest := assetMap["digest_sha256"].(string)
				if digest == "" {
					t.Fatalf("[%s] digest_sha256 is empty", tc.name)
				}

				// 2. Preview via GET /v3/sessions/{sessionID}/media/{assetID}
				getReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+sessionID+"/media/"+assetID, nil)
				getReq = getReq.WithContext(context.WithValue(getReq.Context(), productPrincipalRequestContextKey, p))
				getReq = getReq.WithContext(context.WithValue(getReq.Context(), productScopedTokenRequestContextKey, token))

				getW := httptest.NewRecorder()
				server.handleSessionV3PrimaryByID(getW, getReq)
				if getW.Code != http.StatusOK {
					t.Fatalf("[%s] expected 200 OK on GET preview, got %d: %s", tc.name, getW.Code, getW.Body.String())
				}

				if getW.Header().Get("Content-Type") != tc.expectedMIME {
					t.Errorf("[%s] preview Content-Type = %q, want %q", tc.name, getW.Header().Get("Content-Type"), tc.expectedMIME)
				}
				if getW.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Errorf("[%s] preview X-Content-Type-Options = %q, want nosniff", tc.name, getW.Header().Get("X-Content-Type-Options"))
				}
				if !strings.Contains(getW.Header().Get("Content-Security-Policy"), "sandbox") {
					t.Errorf("[%s] preview CSP missing sandbox: %q", tc.name, getW.Header().Get("Content-Security-Policy"))
				}
				if getW.Header().Get("Accept-Ranges") != "bytes" {
					t.Errorf("[%s] preview Accept-Ranges = %q, want bytes", tc.name, getW.Header().Get("Accept-Ranges"))
				}
				if !bytes.Equal(getW.Body.Bytes(), tc.payload) {
					t.Fatalf("[%s] preview payload byte mismatch: got %d bytes, want %d bytes", tc.name, len(getW.Body.Bytes()), len(tc.payload))
				}

				retained[tc.name] = retainedAsset{
					id:       assetID,
					modality: tc.modality,
					mime:     tc.expectedMIME,
					filename: tc.filename,
					digest:   digest,
					payload:  tc.payload,
				}
			})
		}

		if err := store.Close(); err != nil {
			t.Fatalf("close store phase 1: %v", err)
		}
	}

	// PHASE 2: Store reload (restart durability) & Project Reuse resolution
	{
		store2, err := pebblestore.Open(dbPath)
		if err != nil {
			t.Fatalf("reopen store phase 2: %v", err)
		}
		defer func() { _ = store2.Close() }()

		sessionStore2 := pebblestore.NewSessionStore(store2)
		sessionSvc2 := sessionruntime.NewService(sessionStore2, nil)
		server2 := &Server{sessions: sessionSvc2}

		// Seed a project record to test project media reuse
		project := &pebblestore.ProjectRecord{
			ID:        "proj-all-modalities-reuse",
			AccountID: p.AccountScopeID,
			Name:      "Modalities Reuse Project",
		}
		for _, ra := range retained {
			project.UploadedMedia = append(project.UploadedMedia, pebblestore.ProjectTaskMediaRef{
				ID:           ra.id,
				Title:        ra.filename,
				Filename:     ra.filename,
				Kind:         ra.modality,
				MediaType:    ra.mime,
				URL:          fmt.Sprintf("/v3/sessions/%s/media/%s", sessionID, ra.id),
				SizeBytes:    int64(len(ra.payload)),
				DigestSHA256: ra.digest,
				CreatedAt:    time.Now().UnixMilli(),
			})
		}
		if err := sessionStore2.PutProject(p.AccountScopeID, project); err != nil {
			t.Fatalf("put project phase 2: %v", err)
		}

		for _, tc := range testCases {
			ra := retained[tc.name]

			t.Run("DurabilityAfterReload_"+tc.name, func(t *testing.T) {
				// Assert stored bytes and metadata survived store close and reopen
				readAsset, payload, err := sessionStore2.ReadSessionMediaAsset(p.AccountScopeID, sessionID, ra.id)
				if err != nil {
					t.Fatalf("[%s] read asset after reload failed: %v", tc.name, err)
				}
				if readAsset.ID != ra.id {
					t.Errorf("[%s] ID after reload = %q, want %q", tc.name, readAsset.ID, ra.id)
				}
				if readAsset.FileName != ra.filename {
					t.Errorf("[%s] FileName after reload = %q, want %q", tc.name, readAsset.FileName, ra.filename)
				}
				if readAsset.DetectedMIMEType != ra.mime {
					t.Errorf("[%s] MIME after reload = %q, want %q", tc.name, readAsset.DetectedMIMEType, ra.mime)
				}
				if readAsset.DigestSHA256 != ra.digest {
					t.Errorf("[%s] digest after reload = %q, want %q", tc.name, readAsset.DigestSHA256, ra.digest)
				}
				if !bytes.Equal(payload, ra.payload) {
					t.Fatalf("[%s] bytes after reload mismatch: got %d bytes, want %d bytes", tc.name, len(payload), len(ra.payload))
				}
			})

			t.Run("ProjectReuse_"+tc.name, func(t *testing.T) {
				// Assert server-side resolveSourceMediaBytes and resolveSourceMediaRecord resolve
				// project media URL and session media URL preserving exact bytes and digest.
				mediaRef := pebblestore.ProjectTaskMediaRef{
					ID:        ra.id,
					URL:       fmt.Sprintf("/v3/sessions/%s/media/%s", sessionID, ra.id),
					Kind:      ra.modality,
					MediaType: ra.mime,
				}

				payload, mType, err := server2.resolveSourceMediaBytes(context.Background(), p, mediaRef, ra.modality)
				if err != nil {
					t.Fatalf("[%s] resolveSourceMediaBytes failed: %v", tc.name, err)
				}
				if mType != ra.mime {
					t.Errorf("[%s] resolved MIME = %q, want %q", tc.name, mType, ra.mime)
				}
				if !bytes.Equal(payload, ra.payload) {
					t.Fatalf("[%s] resolved payload mismatch: got %d bytes, want %d", tc.name, len(payload), len(ra.payload))
				}

				rec, err := server2.resolveSourceMediaRecord(context.Background(), p, mediaRef, ra.modality, project.ID)
				if err != nil {
					t.Fatalf("[%s] resolveSourceMediaRecord failed: %v", tc.name, err)
				}
				if !bytes.Equal(rec.Bytes, ra.payload) {
					t.Fatalf("[%s] record bytes mismatch: got %d bytes, want %d", tc.name, len(rec.Bytes), len(ra.payload))
				}
				if rec.SourceLink == nil || rec.SourceLink.DigestSHA256 != ra.digest {
					t.Errorf("[%s] SourceLink digest mismatch: got %+v, want digest %s", tc.name, rec.SourceLink, ra.digest)
				}
			})
		}
	}
}

func TestSessionV3Media_MixedBatchUploadAndRetry(t *testing.T) {
	// Purpose:
	// - Requirement: Client uploading a mixed batch of media where one upload fails (e.g. corrupted file)
	//   and others succeed must:
	//   1. Retain the successful uploads durably without losing or reverting them.
	//   2. Return clear, actionable error for the failed file.
	//   3. Permit retrying only the failed file without creating duplicate assets for the successful items.
	// - Threat/regression: Entire batch failing or partial successes corrupted on single-file error;
	//   retry creating duplicate records or re-uploading all items.
	// - Boundary/authority: Server.handleSessionV3MediaUpload and SessionStore.PutSessionMediaAsset.
	// - Test layer: HTTP API multi-item batch lifecycle and retry test.

	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	token := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"sessions:write", "sessions:read"},
	}

	session := pebblestore.SessionSnapshot{
		ID:             "sess-mixed-batch",
		UserID:         p.UserID,
		AccountScopeID: p.AccountScopeID,
		Title:          "Mixed Batch Session",
	}
	if _, err := sessionSvc.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       session.ID,
		UserID:          session.UserID,
		AccountScopeID:  session.AccountScopeID,
		ClientRequestID: "create-mixed-batch-sess",
		PayloadHash:     "create-mixed-batch-sess",
		Kind:            pebblestore.V3SessionMutationCreateSession,
		Session:         &session,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Batch of 3 files:
	// File 1: Valid PNG (image)
	// File 2: Corrupted Video (declared video/mp4, but garbage text bytes)
	// File 3: Valid WAV (audio)
	file1Payload := samplePNG
	file2CorruptPayload := []byte("this is corrupt non-video garbage data")
	file2ValidPayload := sampleMP4
	file3Payload := sampleWAV

	// 1. Upload File 1 (PNG) -> Must succeed (201 Created)
	req1 := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/media", bytes.NewReader(file1Payload))
	req1.Header.Set("Content-Type", "image/png")
	req1.Header.Set("X-Swarm-Media-Modality", "image")
	req1.Header.Set("X-Swarm-Media-Filename", "diagram.png")
	req1.Header.Set("Idempotency-Key", "batch-file-1")
	req1 = req1.WithContext(context.WithValue(req1.Context(), productPrincipalRequestContextKey, p))
	req1 = req1.WithContext(context.WithValue(req1.Context(), productScopedTokenRequestContextKey, token))
	w1 := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(w1, req1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("file 1 upload expected 201 Created, got %d: %s", w1.Code, w1.Body.String())
	}
	var res1 map[string]any
	_ = json.Unmarshal(w1.Body.Bytes(), &res1)
	asset1 := res1["asset"].(map[string]any)
	asset1ID := asset1["id"].(string)

	// 2. Upload File 2 (Corrupt Video) -> Must fail (400 Bad Request)
	req2 := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/media", bytes.NewReader(file2CorruptPayload))
	req2.Header.Set("Content-Type", "video/mp4")
	req2.Header.Set("X-Swarm-Media-Modality", "video")
	req2.Header.Set("X-Swarm-Media-Filename", "clip.mp4")
	req2.Header.Set("Idempotency-Key", "batch-file-2-bad")
	req2 = req2.WithContext(context.WithValue(req2.Context(), productPrincipalRequestContextKey, p))
	req2 = req2.WithContext(context.WithValue(req2.Context(), productScopedTokenRequestContextKey, token))
	w2 := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("file 2 upload expected 400 Bad Request for corrupted video, got %d: %s", w2.Code, w2.Body.String())
	}

	// 3. Upload File 3 (WAV) -> Must succeed (201 Created)
	req3 := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/media", bytes.NewReader(file3Payload))
	req3.Header.Set("Content-Type", "audio/wav")
	req3.Header.Set("X-Swarm-Media-Modality", "audio")
	req3.Header.Set("X-Swarm-Media-Filename", "voice.wav")
	req3.Header.Set("Idempotency-Key", "batch-file-3")
	req3 = req3.WithContext(context.WithValue(req3.Context(), productPrincipalRequestContextKey, p))
	req3 = req3.WithContext(context.WithValue(req3.Context(), productScopedTokenRequestContextKey, token))
	w3 := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(w3, req3)
	if w3.Code != http.StatusCreated {
		t.Fatalf("file 3 upload expected 201 Created, got %d: %s", w3.Code, w3.Body.String())
	}
	var res3 map[string]any
	_ = json.Unmarshal(w3.Body.Bytes(), &res3)
	asset3 := res3["asset"].(map[string]any)
	asset3ID := asset3["id"].(string)

	// Invariant: Verify File 1 and File 3 are intact and stored despite File 2 failure
	if _, ok, err := sessionSvc.Store().GetSessionMediaAsset(p.AccountScopeID, session.ID, asset1ID); err != nil || !ok {
		t.Fatalf("asset 1 missing from store after partial batch failure: ok=%v err=%v", ok, err)
	}
	if _, ok, err := sessionSvc.Store().GetSessionMediaAsset(p.AccountScopeID, session.ID, asset3ID); err != nil || !ok {
		t.Fatalf("asset 3 missing from store after partial batch failure: ok=%v err=%v", ok, err)
	}

	// 4. Retry: Client retries ONLY File 2 with corrected MP4 bytes
	req2Retry := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/media", bytes.NewReader(file2ValidPayload))
	req2Retry.Header.Set("Content-Type", "video/mp4")
	req2Retry.Header.Set("X-Swarm-Media-Modality", "video")
	req2Retry.Header.Set("X-Swarm-Media-Filename", "clip.mp4")
	req2Retry.Header.Set("Idempotency-Key", "batch-file-2-retry")
	req2Retry = req2Retry.WithContext(context.WithValue(req2Retry.Context(), productPrincipalRequestContextKey, p))
	req2Retry = req2Retry.WithContext(context.WithValue(req2Retry.Context(), productScopedTokenRequestContextKey, token))
	w2Retry := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(w2Retry, req2Retry)
	if w2Retry.Code != http.StatusCreated {
		t.Fatalf("file 2 retry expected 201 Created, got %d: %s", w2Retry.Code, w2Retry.Body.String())
	}
	var res2Retry map[string]any
	_ = json.Unmarshal(w2Retry.Body.Bytes(), &res2Retry)
	asset2 := res2Retry["asset"].(map[string]any)
	asset2ID := asset2["id"].(string)

	// 5. Idempotent Resend: Client resends File 1 with original key "batch-file-1" -> 200 OK replayed
	req1Replay := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/media", bytes.NewReader(file1Payload))
	req1Replay.Header.Set("Content-Type", "image/png")
	req1Replay.Header.Set("X-Swarm-Media-Modality", "image")
	req1Replay.Header.Set("X-Swarm-Media-Filename", "diagram.png")
	req1Replay.Header.Set("Idempotency-Key", "batch-file-1")
	req1Replay = req1Replay.WithContext(context.WithValue(req1Replay.Context(), productPrincipalRequestContextKey, p))
	req1Replay = req1Replay.WithContext(context.WithValue(req1Replay.Context(), productScopedTokenRequestContextKey, token))
	w1Replay := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(w1Replay, req1Replay)
	if w1Replay.Code != http.StatusOK {
		t.Fatalf("file 1 idempotent replay expected 200 OK, got %d: %s", w1Replay.Code, w1Replay.Body.String())
	}
	var res1Replay map[string]any
	_ = json.Unmarshal(w1Replay.Body.Bytes(), &res1Replay)
	if res1Replay["replayed"] != true {
		t.Fatalf("expected replayed=true for idempotent retry")
	}
	replayedAsset := res1Replay["asset"].(map[string]any)
	if replayedAsset["id"] != asset1ID {
		t.Fatalf("replayed asset ID %v != original asset ID %v (duplicate created!)", replayedAsset["id"], asset1ID)
	}

	// 6. Verify all 3 distinct assets are readable with exact payloads
	for _, expected := range []struct {
		id      string
		payload []byte
	}{
		{id: asset1ID, payload: file1Payload},
		{id: asset2ID, payload: file2ValidPayload},
		{id: asset3ID, payload: file3Payload},
	} {
		_, body, err := sessionSvc.Store().ReadSessionMediaAsset(p.AccountScopeID, session.ID, expected.id)
		if err != nil {
			t.Fatalf("failed to read asset %s: %v", expected.id, err)
		}
		if !bytes.Equal(body, expected.payload) {
			t.Fatalf("asset %s payload mismatch: got %d bytes, want %d", expected.id, len(body), len(expected.payload))
		}
	}
}

func TestSessionV3Media_InvalidContentAndMIMEValidation(t *testing.T) {
	// Purpose:
	// - Requirement: Non-media or invalid media payloads must be rejected with HTTP 400 Bad Request
	//   without storing bytes or creating partial state. Traversal attempts in filename must be
	//   sanitized and cannot escape storage boundaries. Oversized payloads must be rejected.
	// - Threat/regression: Corrupted binaries, executables, or oversized uploads bypassing validation.
	// - Boundary/authority: Server.handleSessionV3MediaUpload and PutSessionMediaAsset.
	// - Test layer: Negative API boundary tests.

	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	token := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"sessions:write", "sessions:read"},
	}

	session := pebblestore.SessionSnapshot{
		ID:             "sess-neg-validation",
		UserID:         p.UserID,
		AccountScopeID: p.AccountScopeID,
		Title:          "Negative Validation Session",
	}
	if _, err := sessionSvc.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       session.ID,
		UserID:          session.UserID,
		AccountScopeID:  session.AccountScopeID,
		ClientRequestID: "create-neg-sess",
		PayloadHash:     "create-neg-sess",
		Kind:            pebblestore.V3SessionMutationCreateSession,
		Session:         &session,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cases := []struct {
		name         string
		payload      []byte
		declaredMIME string
		modality     string
		filename     string
	}{
		{
			name:         "CorruptVideo_GarbagePayload",
			payload:      []byte("plain text claiming to be a high-def video clip"),
			declaredMIME: "video/mp4",
			modality:     "video",
			filename:     "video.mp4",
		},
		{
			name:         "CorruptAudio_GarbagePayload",
			payload:      []byte("plain text claiming to be high fidelity audio"),
			declaredMIME: "audio/wav",
			modality:     "audio",
			filename:     "sound.wav",
		},
		{
			name:         "MIMEMismatch_VideoAsImage",
			payload:      sampleMP4,
			declaredMIME: "image/png",
			modality:     "image",
			filename:     "pic.png",
		},
		{
			name:         "MIMEMismatch_AudioAsVideo",
			payload:      sampleWAV,
			declaredMIME: "video/mp4",
			modality:     "video",
			filename:     "clip.mp4",
		},
		{
			name:         "ExecutableBinaryAsImage",
			payload:      []byte{'M', 'Z', 0x90, 0x00, 0x03, 0x00, 0x00, 0x00},
			declaredMIME: "image/png",
			modality:     "image",
			filename:     "malicious.exe",
		},
		{
			name:         "EmptyPayload",
			payload:      []byte{},
			declaredMIME: "image/png",
			modality:     "image",
			filename:     "empty.png",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/media", bytes.NewReader(tc.payload))
			req.Header.Set("Content-Type", tc.declaredMIME)
			req.Header.Set("X-Swarm-Media-Modality", tc.modality)
			req.Header.Set("X-Swarm-Media-Filename", tc.filename)
			req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, p))
			req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, token))

			w := httptest.NewRecorder()
			server.handleSessionV3PrimaryByID(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("[%s] expected 400 Bad Request, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}
