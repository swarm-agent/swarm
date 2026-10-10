package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Requirement: a scoped token below admin (a sessions SDK key, an automation
// token, a client-app key for an AI-built UI) must never reach owner surfaces
// whose handlers check only for a principal: provider credentials and ChatGPT
// sign-in (which provider account, and so whose history, every agent runs
// on), the credential vault (export hands out every key and token, import can
// swap the active credential), attach-token rotation, and Workspace Actions
// (programs run on the host outside the sandbox and approval). Threats
// (credential and sign-in routes reproduced live before this guard; vault and
// actions found in review): a sessions-only key exporting every credential,
// attaching its holder's own provider account, or running a host program.
// Owners: ownerOnlyScope wrapping those registrations in server_routes.go.
// Requests through Handler() with real temporary auth and token stores are
// the narrowest layer that runs the route registration, withAuth and the
// guard together; owner flows are covered by the handlers' own tests.
func TestOwnerOnlyRoutesNeedAdminScopedToken(t *testing.T) {
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	mint := func(scopes ...string) string {
		t.Helper()
		token, _, err := sec.CreateScopedToken("key", scopes, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	routes := []struct{ method, path, body string }{
		{"GET", "/v1/auth/credentials", ""},
		{"POST", "/v1/auth/credentials", `{"provider":"openai","type":"api","api_key":"sk-attacker","active":true}`},
		{"POST", "/v1/auth/credentials/active", `{"provider":"openai","id":"x"}`},
		{"POST", "/v1/auth/credentials/verify", `{"provider":"openai","id":"x"}`},
		{"POST", "/v1/auth/credentials/delete", `{"provider":"openai","id":"x"}`},
		{"POST", "/v1/auth/codex", `{}`},
		{"POST", "/v1/auth/codex/oauth/start", `{"provider":"codex","method":"device","active":true}`},
		{"GET", "/v1/auth/codex/oauth/status?session_id=x", ""},
		{"POST", "/v1/auth/codex/oauth/complete", `{"session_id":"x","callback_input":"y"}`},
		{"POST", "/v1/onboarding/provider/credential", `{"provider":"openai","type":"api","api_key":"sk-attacker","active":true}`},
		{"GET", "/v1/vault", ""},
		{"POST", "/v1/vault/export", `{"password":"attacker-password"}`},
		{"POST", "/v1/vault/import", `{"password":"attacker-password","bundle":"x"}`},
		{"POST", "/v1/vault/enable", `{"password":"attacker-password"}`},
		{"POST", "/v1/vault/lock", `{}`},
		{"POST", "/v1/vault/disable", `{"password":"attacker-password"}`},
		{"POST", "/v1/auth/attach/rotate", `{}`},
		{"GET", "/v1/workspace/actions", ""},
		{"POST", "/v1/workspace/actions", `{"name":"x","entrypoint":"run.sh"}`},
		{"POST", "/v1/workspace/actions/run", `{"action_id":"x"}`},
		{"GET", "/v1/workspace/actions/runs", ""},
		{"POST", "/v1/workspace/actions/runs/cancel", `{"run_id":"x"}`},
	}
	do := func(token string, route struct{ method, path, body string }) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(route.method, "http://127.0.0.1:7781"+route.path, strings.NewReader(route.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	for _, token := range []string{mint("sessions:read", "sessions:write"), mint("automations:read", "automations:write", "settings:write")} {
		for _, route := range routes {
			if w := do(token, route); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `required scope \"admin\"`) {
				t.Fatalf("%s %s with a non-admin key: %d %s", route.method, route.path, w.Code, w.Body.String())
			}
		}
	}
	if list, err := s.auth.ListCredentialsForAccount(actor.AccountScopeID, "", "", 10); err != nil || len(list.Records) != 0 {
		t.Fatalf("refused requests stored a credential: %+v %v", list, err)
	}
	// An admin key passes the guard; the handler then decides.
	admin := mint("admin")
	if w := do(admin, routes[0]); w.Code == http.StatusForbidden {
		t.Fatalf("admin key refused: %d %s", w.Code, w.Body.String())
	}
}
