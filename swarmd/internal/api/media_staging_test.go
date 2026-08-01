package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/mediastaging"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var mediaStagingAPIPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0, 'I', 'H', 'D', 'R'}

func newMediaStagingAPIServer(t *testing.T) (*Server, *mediastaging.Service) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "media-staging-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := mediastaging.NewService(pebblestore.NewMediaStagingStore(store))
	server := &Server{}
	server.SetMediaStagingService(service)
	return server, service
}

func stageMediaRequest(t *testing.T, handler http.Handler, principalAccount, key, contentType string, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, MediaStagingCollectionPath, bytes.NewReader(payload))
	if principalAccount != "" {
		request = requestWithTestPrincipalForAccount(request, "user-"+principalAccount, principalAccount)
	}
	if key != "" {
		request.Header.Set(mediaStagingIdempotencyHeader, key)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeStagingID(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Staging struct {
			ID string `json:"id"`
		} `json:"staging"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
	if body.Staging.ID == "" {
		t.Fatalf("response has no staging ID: %s", response.Body.String())
	}
	return body.Staging.ID
}

func TestMediaStagingHTTPAuthenticationReplayConflictAndRoutes(t *testing.T) {
	server, _ := newMediaStagingAPIServer(t)
	handler := server.apiMux()

	unauthorized := stageMediaRequest(t, handler, "", "upload", "image/png", mediaStagingAPIPNG)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	created := stageMediaRequest(t, handler, "account-a", "upload", "image/png", mediaStagingAPIPNG)
	if created.Code != http.StatusCreated {
		t.Fatalf("created status=%d body=%s", created.Code, created.Body.String())
	}
	stagingID := decodeStagingID(t, created)

	replayed := stageMediaRequest(t, handler, "account-a", "upload", "image/png", mediaStagingAPIPNG)
	if replayed.Code != http.StatusOK || replayed.Header().Get("Idempotent-Replayed") != "true" || decodeStagingID(t, replayed) != stagingID {
		t.Fatalf("replay status=%d headers=%v body=%s", replayed.Code, replayed.Header(), replayed.Body.String())
	}
	conflict := stageMediaRequest(t, handler, "account-a", "upload", "image/png", append(append([]byte(nil), mediaStagingAPIPNG...), 1))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	otherAccount := stageMediaRequest(t, handler, "account-b", "upload", "image/png", mediaStagingAPIPNG)
	if otherAccount.Code != http.StatusCreated || decodeStagingID(t, otherAccount) == stagingID {
		t.Fatalf("account-scoped idempotency status=%d body=%s", otherAccount.Code, otherAccount.Body.String())
	}

	wrongMethod := httptest.NewRecorder()
	handler.ServeHTTP(wrongMethod, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, MediaStagingCollectionPath, nil)))
	if wrongMethod.Code != http.StatusMethodNotAllowed || wrongMethod.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("collection method status=%d allow=%q", wrongMethod.Code, wrongMethod.Header().Get("Allow"))
	}
	malformedPath := httptest.NewRecorder()
	handler.ServeHTTP(malformedPath, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, MediaStagingCollectionPath+"/"+stagingID+"/extra", nil)))
	if malformedPath.Code != http.StatusNotFound {
		t.Fatalf("malformed path status=%d body=%s", malformedPath.Code, malformedPath.Body.String())
	}
}

func TestMediaStagingHTTPMetadataIsolationStatusAndDelete(t *testing.T) {
	server, service := newMediaStagingAPIServer(t)
	handler := server.apiMux()
	created := stageMediaRequest(t, handler, "account-a", "status", "image/png", mediaStagingAPIPNG)
	stagingID := decodeStagingID(t, created)
	itemPath := MediaStagingCollectionPath + "/" + stagingID

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, itemPath, nil), "user-a", "account-a"))
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), "account-a") || strings.Contains(get.Body.String(), "digest_sha256") || strings.Contains(get.Body.String(), "bound_session") || !strings.Contains(get.Body.String(), `"consumable":true`) {
		t.Fatalf("unsafe metadata status=%d body=%s", get.Code, get.Body.String())
	}
	crossAccount := httptest.NewRecorder()
	handler.ServeHTTP(crossAccount, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, itemPath, nil), "user-b", "account-b"))
	if crossAccount.Code != http.StatusNotFound || strings.Contains(crossAccount.Body.String(), stagingID) {
		t.Fatalf("cross-account status=%d body=%s", crossAccount.Code, crossAccount.Body.String())
	}
	crossAccountDelete := httptest.NewRecorder()
	handler.ServeHTTP(crossAccountDelete, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodDelete, itemPath, nil), "user-b", "account-b"))
	if crossAccountDelete.Code != http.StatusNotFound || strings.Contains(crossAccountDelete.Body.String(), stagingID) {
		t.Fatalf("cross-account delete status=%d body=%s", crossAccountDelete.Code, crossAccountDelete.Body.String())
	}

	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodDelete, itemPath, nil), "user-a", "account-a"))
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"status":"deleted"`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, _, err := service.Read("account-a", stagingID, time.Now().UnixMilli()); err == nil {
		t.Fatal("deleted staging bytes remained consumable")
	}
	replayDelete := httptest.NewRecorder()
	handler.ServeHTTP(replayDelete, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodDelete, itemPath, nil), "user-a", "account-a"))
	if replayDelete.Code != http.StatusOK || !strings.Contains(replayDelete.Body.String(), `"replayed":true`) {
		t.Fatalf("delete replay status=%d body=%s", replayDelete.Code, replayDelete.Body.String())
	}
}

func TestMediaStagingHTTPBoundsExpiredAndBoundFailClosed(t *testing.T) {
	server, service := newMediaStagingAPIServer(t)
	handler := server.apiMux()

	for name, test := range map[string]struct {
		response *httptest.ResponseRecorder
		want     int
	}{
		"missing key": {stageMediaRequest(t, handler, "account", "", "image/png", mediaStagingAPIPNG), http.StatusBadRequest},
		"missing MIME": {stageMediaRequest(t, handler, "account", "mime", "", mediaStagingAPIPNG), http.StatusUnsupportedMediaType},
		"spoofed MIME": {stageMediaRequest(t, handler, "account", "spoof", "image/jpeg", mediaStagingAPIPNG), http.StatusUnsupportedMediaType},
	} {
		if test.response.Code != test.want {
			t.Fatalf("%s status=%d want=%d body=%s", name, test.response.Code, test.want, test.response.Body.String())
		}
	}

	badTTLRequest := httptest.NewRequest(http.MethodPost, MediaStagingCollectionPath, bytes.NewReader(mediaStagingAPIPNG))
	badTTLRequest = requestWithTestPrincipalForAccount(badTTLRequest, "user", "account")
	badTTLRequest.Header.Set(mediaStagingIdempotencyHeader, "ttl")
	badTTLRequest.Header.Set("Content-Type", "image/png")
	badTTLRequest.Header.Set(mediaStagingTTLHeader, "86401")
	badTTL := httptest.NewRecorder()
	handler.ServeHTTP(badTTL, badTTLRequest)
	if badTTL.Code != http.StatusBadRequest {
		t.Fatalf("TTL status=%d body=%s", badTTL.Code, badTTL.Body.String())
	}

	oversizedRequest := httptest.NewRequest(http.MethodPost, MediaStagingCollectionPath, bytes.NewReader(mediaStagingAPIPNG))
	oversizedRequest = requestWithTestPrincipalForAccount(oversizedRequest, "user", "account")
	oversizedRequest.Header.Set(mediaStagingIdempotencyHeader, "oversized")
	oversizedRequest.Header.Set("Content-Type", "image/png")
	oversizedRequest.ContentLength = pebblestore.MediaStagingDefaultMaxBytes + 1
	oversized := httptest.NewRecorder()
	handler.ServeHTTP(oversized, oversizedRequest)
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d body=%s", oversized.Code, oversized.Body.String())
	}

	expired, _, err := service.Put(pebblestore.PutMediaStagingInput{AccountScopeID: "account", IdempotencyKey: "expired", DeclaredMIMEType: "image/png", TTL: time.Second, NowUnixMs: 1, Reader: bytes.NewReader(mediaStagingAPIPNG)})
	if err != nil {
		t.Fatalf("put expired: %v", err)
	}
	expiredGet := httptest.NewRecorder()
	handler.ServeHTTP(expiredGet, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, MediaStagingCollectionPath+"/"+expired.ID, nil), "user", "account"))
	if expiredGet.Code != http.StatusOK || !strings.Contains(expiredGet.Body.String(), `"status":"expired"`) || !strings.Contains(expiredGet.Body.String(), `"consumable":false`) {
		t.Fatalf("expired status=%d body=%s", expiredGet.Code, expiredGet.Body.String())
	}

	bound, _, err := service.Put(pebblestore.PutMediaStagingInput{AccountScopeID: "account", IdempotencyKey: "bound", DeclaredMIMEType: "image/png", Reader: bytes.NewReader(mediaStagingAPIPNG)})
	if err != nil {
		t.Fatalf("put bound: %v", err)
	}
	if _, _, err := service.Bind(pebblestore.BindMediaStagingInput{AccountScopeID: "account", SessionID: "session", Bindings: []pebblestore.MediaStagingBinding{{StagingID: bound.ID, AuthorityAssetID: "asset", DigestSHA256: bound.DigestSHA256}}}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	boundDelete := httptest.NewRecorder()
	handler.ServeHTTP(boundDelete, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodDelete, MediaStagingCollectionPath+"/"+bound.ID, nil), "user", "account"))
	if boundDelete.Code != http.StatusConflict || !strings.Contains(boundDelete.Body.String(), "cannot be abandoned") {
		t.Fatalf("bound delete status=%d body=%s", boundDelete.Code, boundDelete.Body.String())
	}
}
