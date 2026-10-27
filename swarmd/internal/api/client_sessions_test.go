package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Purpose: a client session is an owner session for the person's own client
// app. It is minted only over the owner socket, lists with its name, acts as
// the owner on the loopback API through X-Swarm-Token, and stops the moment
// it is revoked. Threat: a leaked token minting more; a revoked client still
// in. Boundary: Server.handleClientSessions and identity.SessionService.
// Validate through Server.withAuth. This HTTP layer also proves that the supplied
// device name survives storage and the validated actor identifies that exact
// client session, rather than confusing two clients with the same owner.
func TestClientSessionsMintListActAndRevoke(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	owner := func(r *http.Request) *http.Request {
		return requestWithTestPrincipalForAccount(r, actor.UserID, actor.AccountScopeID)
	}

	// Minting needs the owner socket.
	w := httptest.NewRecorder()
	s.handleClientSessions(w, owner(httptest.NewRequest(http.MethodPost, clientSessionsPath, strings.NewReader(`{"name":"console","device":"test-device"}`))))
	if w.Code != http.StatusForbidden {
		t.Fatalf("mint off the socket: HTTP %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleClientSessions(w, markLocalTransportRequest(owner(httptest.NewRequest(http.MethodPost, clientSessionsPath, strings.NewReader(`{"name":"console","device":"test-device"}`)))))
	if w.Code != http.StatusCreated {
		t.Fatalf("mint: HTTP %d %s", w.Code, w.Body.String())
	}
	var minted struct {
		Token     string `json:"token"`
		SessionID string `json:"session_id"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &minted); err != nil {
		t.Fatal(err)
	}
	if minted.Token == "" || minted.SessionID == "" || minted.Name != "console" {
		t.Fatalf("mint answered %s", w.Body.String())
	}

	validated, err := s.identitySessions.Validate(minted.Token)
	if err != nil || validated.ClientSessionID != minted.SessionID {
		t.Fatalf("client identity: id=%q err=%v", validated.ClientSessionID, err)
	}
	plain, err := s.identitySessions.IssueForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	plainActor, err := s.identitySessions.Validate(plain.Token)
	if err != nil || plainActor.ClientSessionID != "" {
		t.Fatalf("unnamed owner session acquired device identity: %q, %v", plainActor.ClientSessionID, err)
	}

	// The token is the owner on the loopback API.
	seen := ""
	handler := s.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromRequest(r)
		if ok {
			seen = p.UserID
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/v3/sync/bootstrap", nil)
		req.Header.Set("X-Swarm-Token", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := call(minted.Token); code != http.StatusNoContent || seen != actor.UserID {
		t.Fatalf("token on the API: HTTP %d as %q, want the owner %q", code, seen, actor.UserID)
	}

	// It lists by name.
	w = httptest.NewRecorder()
	s.handleClientSessions(w, owner(httptest.NewRequest(http.MethodGet, clientSessionsPath, nil)))
	var listed struct {
		Clients []struct {
			SessionID string `json:"session_id"`
			Name      string `json:"name"`
			Device    string `json:"device"`
			RevokedAt int64  `json:"revoked_at"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Clients) != 1 || listed.Clients[0].SessionID != minted.SessionID || listed.Clients[0].Name != "console" || listed.Clients[0].Device != "test-device" {
		t.Fatalf("list: HTTP %d %s (%v)", w.Code, w.Body.String(), err)
	}

	// Revoked: out at once, still listed as revoked.
	w = httptest.NewRecorder()
	s.handleClientSessions(w, owner(httptest.NewRequest(http.MethodDelete, clientSessionsPath+"/"+minted.SessionID, nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("revoke: HTTP %d %s", w.Code, w.Body.String())
	}
	seen = ""
	if code := call(minted.Token); code == http.StatusNoContent || seen != "" {
		t.Fatalf("revoked token still in: HTTP %d as %q", code, seen)
	}
	w = httptest.NewRecorder()
	s.handleClientSessions(w, owner(httptest.NewRequest(http.MethodGet, clientSessionsPath, nil)))
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Clients) != 1 || listed.Clients[0].RevokedAt == 0 {
		t.Fatalf("list after revoke: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleClientSessions(w, owner(httptest.NewRequest(http.MethodDelete, clientSessionsPath+"/nope", nil)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown: HTTP %d", w.Code)
	}
}
