package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/security"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func setupScopedAuthTestServer(t *testing.T) (*Server, string, *security.Service, func()) {
	t.Helper()
	root := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}

	authStore := pebblestore.NewClientAuthStore(store)
	secSvc := security.NewService(authStore, events)
	attachStatus, err := secSvc.EnsureAttachAuth()
	if err != nil {
		t.Fatal(err)
	}
	attachToken, err := secSvc.RevealAttachToken()
	if err != nil {
		t.Fatal(err)
	}
	_ = attachStatus

	identityStore := pebblestore.NewIdentityStore(store)
	sessionStore := pebblestore.NewIdentitySessionStore(store)
	identitySvc := identity.NewService(identityStore)
	identitySessions := identity.NewSessionService(identityStore, sessionStore)

	// Bootstrap default user & account
	_, err = identitySvc.BootstrapFirstIdentity("test-admin")
	if err != nil {
		t.Fatal(err)
	}

	sessionRecordStore := pebblestore.NewSessionStore(store)
	sessionsSvc := sessionruntime.NewService(sessionRecordStore, nil)

	server := NewServer(
		auth.NewService(pebblestore.NewAuthStore(store), events),
		nil, nil, nil,
		sessionsSvc,
		nil, nil,
		secSvc,
		nil, nil, nil,
		events,
		stream.NewHub(events),
	)
	server.SetIdentityService(identitySvc)
	server.SetIdentitySessionService(identitySessions)

	cleanup := func() {
		_ = store.Close()
	}
	return server, attachToken, secSvc, cleanup
}

func TestScopedAuthTokenLifecycleAndPermissions(t *testing.T) {
	server, attachToken, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()

	handler := server.Handler()

	// 1. Create a scoped token with scope ["automations:trigger"] using master attach token
	createBody := `{"name":"CI Trigger Token","scopes":["automations:trigger"]}`
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5555/v3/auth/tokens", bytes.NewBufferString(createBody))
	req.Header.Set("X-Swarm-Token", attachToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v3/auth/tokens status=%d body=%s", rec.Code, rec.Body.String())
	}

	var created struct {
		OK     bool                        `json:"ok"`
		Token  string                      `json:"token"`
		Record pebblestore.ScopedTokenRecord `json:"record"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created token response: %v", err)
	}
	if !created.OK || created.Token == "" || created.Record.ID == "" {
		t.Fatalf("invalid created token response: %#v", created)
	}
	scopedToken := created.Token
	scopedID := created.Record.ID

	// 2. List scoped tokens using attach token
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v3/auth/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+attachToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v3/auth/tokens status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed struct {
		OK     bool                          `json:"ok"`
		Tokens []pebblestore.ScopedTokenRecord `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal listed tokens: %v", err)
	}
	if len(listed.Tokens) != 1 || listed.Tokens[0].ID != scopedID {
		t.Fatalf("unexpected listed tokens: %#v", listed.Tokens)
	}

	// 3. Permission Enforcement with scoped token:
	// A) Attempt to access /v3/auth/tokens (requires admin) -> Must return 403 Forbidden
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v3/auth/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+scopedToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for non-admin token on /v3/auth/tokens, got %d: %s", rec.Code, rec.Body.String())
	}

	// B) Attempt to GET /v3/sessions (requires sessions:read) -> Must return 403 Forbidden
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v3/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+scopedToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for trigger-only token on /v3/sessions, got %d: %s", rec.Code, rec.Body.String())
	}

	// C) Attempt to call /v3/automations/v2 (requires automations:read) -> Must return 403 Forbidden
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v3/automations/v2?workspace_id=test", nil)
	req.Header.Set("Authorization", "Bearer "+scopedToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for trigger-only token on GET /v3/automations/v2, got %d: %s", rec.Code, rec.Body.String())
	}

	// D) Call /v3/automations/v2/trigger (requires automations:trigger) -> Allowed past auth/scope check!
	// (Returns 400 Bad Request because workspace doesn't exist, which proves auth & scope passed!)
	triggerBody := `{"workspace_id":"ws_missing","worker_id":"w_test"}`
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5555/v3/automations/v2/trigger", bytes.NewBufferString(triggerBody))
	req.Header.Set("Authorization", "Bearer "+scopedToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Fatalf("scoped token should be admitted to trigger, got status=%d: %s", rec.Code, rec.Body.String())
	}

	// 4. Revoke the scoped token
	req = httptest.NewRequest(http.MethodDelete, "http://127.0.0.1:5555/v3/auth/tokens/"+scopedID, nil)
	req.Header.Set("X-Swarm-Token", attachToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /v3/auth/tokens/%s status=%d body=%s", scopedID, rec.Code, rec.Body.String())
	}

	// 5. Using the revoked token now returns 401 Unauthorized
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5555/v3/automations/v2/trigger", bytes.NewBufferString(triggerBody))
	req.Header.Set("Authorization", "Bearer "+scopedToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for revoked token, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLocalTransportZeroConfIdentity(t *testing.T) {
	server, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()

	localHandler := server.LocalTransportHandler()

	// Calling GET /v3/sessions over local transport with NO auth header
	// automatically gets zero-conf identity with full permissions!
	req := httptest.NewRequest(http.MethodGet, "http://swarm-local-transport/v3/sessions", nil)
	rec := httptest.NewRecorder()
	localHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("zero-conf unix socket GET /v3/sessions failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Calling GET /v3/auth/tokens over local transport with NO auth header
	// automatically succeeds with admin access!
	req = httptest.NewRequest(http.MethodGet, "http://swarm-local-transport/v3/auth/tokens", nil)
	rec = httptest.NewRecorder()
	localHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("zero-conf unix socket GET /v3/auth/tokens failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAttachCredentialInheritance(t *testing.T) {
	server, attachToken, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()

	handler := server.Handler()

	// Calling GET /v3/sessions with master attach token automatically inherits product actor
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v3/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+attachToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("attach token GET /v3/sessions failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Calling GET /v3/auth/tokens with master attach token
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v3/auth/tokens", nil)
	req.Header.Set("X-Swarm-Token", attachToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("attach token GET /v3/auth/tokens failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestScopedTokenGranularScopesAndPurge(t *testing.T) {
	server, attachToken, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()

	handler := server.Handler()

	// 1. Create a sessions:read token
	createBody := `{"name":"Read-Only Sessions Token","scopes":["sessions:read"]}`
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5555/v3/auth/tokens", bytes.NewBufferString(createBody))
	req.Header.Set("X-Swarm-Token", attachToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v3/auth/tokens status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		Token  string                      `json:"token"`
		Record pebblestore.ScopedTokenRecord `json:"record"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// A) sessions:read token CAN read sessions
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v3/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+created.Token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions:read token should read /v3/sessions, got %d: %s", rec.Code, rec.Body.String())
	}

	// B) sessions:read token CANNOT create session (requires sessions:write) -> 403 Forbidden
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5555/v3/sessions", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer "+created.Token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("sessions:read token must not create session, got %d: %s", rec.Code, rec.Body.String())
	}

	// C) Purge token
	req = httptest.NewRequest(http.MethodDelete, "http://127.0.0.1:5555/v3/auth/tokens/"+created.Record.ID+"?purge=true", nil)
	req.Header.Set("X-Swarm-Token", attachToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("purge token failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// D) Purged token is immediately invalid -> 401 Unauthorized
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v3/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+created.Token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("purged token must be 401, got %d: %s", rec.Code, rec.Body.String())
	}
}
