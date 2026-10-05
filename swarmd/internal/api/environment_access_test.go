package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: HTTP environment routes must not be an alternate lease authority for
// Coder sessions. Real durable sessions plus direct admission are the narrowest
// layer proving role, principal, and forged-session rejection before providers.
func TestEnvironmentHTTPAgentAdmission(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: f.userID, AccountScopeID: f.accountID}
	for _, role := range []string{"swarm", "system-orchestrator", "coder", "clone", "system-clone"} {
		snap := pebblestore.SessionSnapshot{ID: "env-" + role, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Mode: "auto", Metadata: map[string]any{"agent_name": role, "agent_profile": pebblestore.AgentProfile{Name: role}}}
		if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: snap.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: snap.ID, IdempotencyKey: snap.ID, PayloadHash: snap.ID, RequestHash: snap.ID, Kind: sessionruntime.SessionMutationCreateSession, Session: &snap}); err != nil {
			t.Fatal(err)
		}
		for _, scoped := range []bool{false, true} {
			caller := p
			selected := snap.ID
			if scoped {
				caller.SessionID = snap.ID
				selected = ""
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/environments", nil)
			req = req.WithContext(identity.ContextWithPrincipal(req.Context(), caller))
			got, err := f.server.authorizeEnvironmentHTTPSession(req, selected, "manage_environments")
			allowed := role == "swarm" || role == "system-orchestrator"
			if (err == nil) != allowed || (allowed && got.ID != snap.ID) {
				t.Fatalf("role=%s scoped=%v got=%s err=%v", role, scoped, got.ID, err)
			}
			if !allowed {
				caller.SessionID = snap.ID
				req = req.WithContext(identity.ContextWithPrincipal(req.Context(), caller))
				if _, err := f.server.authorizeEnvironmentHTTPSession(req, "env-swarm", "manage_environments"); err == nil {
					t.Fatal("forged session accepted")
				}
				for _, handler := range []http.HandlerFunc{f.server.handleEnvironments, f.server.handleDeployments, f.server.handleConnections} {
					w := httptest.NewRecorder()
					handler(w, req)
					if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "environment access denied") {
						t.Fatalf("handler bypass: %d %s", w.Code, w.Body.String())
					}
				}
			}
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/environments", nil)
	foreign := p
	foreign.AccountScopeID = "foreign"
	req = req.WithContext(identity.ContextWithPrincipal(req.Context(), foreign))
	if _, err := f.server.authorizeEnvironmentHTTPSession(req, "env-swarm", "manage_environments"); err == nil {
		t.Fatal("foreign account accepted")
	}
	req = req.WithContext(identity.ContextWithPrincipal(req.Context(), p))
	if _, err := f.server.authorizeEnvironmentHTTPSession(req, "missing", "manage_environments"); err == nil {
		t.Fatal("missing session accepted")
	}
	if _, err := f.server.authorizeEnvironmentHTTPSession(req, "", "manage_environments"); err != nil {
		t.Fatalf("human management rejected: %v", err)
	}
}
