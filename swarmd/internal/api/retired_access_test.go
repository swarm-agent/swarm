package api

import (
	"net/http/httptest"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: the compatibility access endpoint must read account authority and
// refuse writes without mutating legacy metadata. HTTP handler plus real store
// is the smallest layer proving the retirement and its postconditions.
func TestRetiredSessionAccess(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: actor.UserID, AccountScopeID: actor.AccountScopeID}
	session := store.SessionSnapshot{ID: "retired-access", UserID: p.UserID, AccountScopeID: p.AccountScopeID}
	if err := s.sessions.Store().CreateSessionForAccount(session, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		r := requestWithTestPrincipalForAccount(httptest.NewRequest(method, "/v3/sessions/retired-access/access", strings.NewReader(`{"tasks":"free"}`)), p.UserID, p.AccountScopeID)
		w := httptest.NewRecorder()
		s.handleSessionV3Access(w, r, p, session.ID)
		if method == "GET" {
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"tasks":"auto"`) {
				t.Fatalf("read: %d %s", w.Code, w.Body.String())
			}
		} else if w.Code != 400 || !strings.Contains(w.Body.String(), "/v3/permissions") {
			t.Fatalf("write: %d %s", w.Code, w.Body.String())
		}
	}
	got, _, err := s.sessions.GetSession(session.ID)
	if err != nil || !store.SessionAccessFromMetadata(got.Metadata).Empty() {
		t.Fatalf("legacy state changed: %+v %v", got, err)
	}
	if _, err := controlMCPSetSessionAccess(nil, nil); err == nil || !strings.Contains(err.Error(), "/v3/permissions") {
		t.Fatalf("MCP retirement: %v", err)
	}
}
