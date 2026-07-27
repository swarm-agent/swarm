package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/webpush"
)

func TestWebPushRoutesRequireAuthenticatedPrincipal(t *testing.T) {
	server := newWebPushTestServer(t)
	recorder := httptest.NewRecorder()
	server.handleWebPush(recorder, httptest.NewRequest(http.MethodGet, webPushRoutePrefix, nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestWebPushRoutesRejectMalformedSubscription(t *testing.T) {
	server := newWebPushTestServer(t)
	request := httptest.NewRequest(http.MethodPost, webPushRoutePrefix+"/subscriptions", strings.NewReader(`{"endpoint":"http://push.example.test","keys":{"auth":"bad","p256dh":"bad"}}`))
	request = request.WithContext(identity.ContextWithPrincipal(request.Context(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-a", AccountScopeID: "account-a"}))
	recorder := httptest.NewRecorder()
	server.handleWebPush(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func newWebPushTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "secret.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository, err := webpush.NewPebbleRepository(store)
	if err != nil {
		t.Fatal(err)
	}
	service, err := webpush.NewService(repository, "https://github.com/swarm-agent/swarm", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublicKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &Server{webPush: service}
}
