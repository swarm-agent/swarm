package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Requirement: a private MCP tunnel using a scoped upstream key must distinguish
// absent RFC 9728 metadata from broken OAuth metadata. ContainerSDKHandler owns
// both discovery and credential admission. Real token storage plus in-process
// HTTP is the narrowest test for the status contract and unchanged auth boundary;
// an actual tunnel-client run separately establishes live interoperability.
func TestContainerSDKAbsentOAuthMetadata(t *testing.T) {
	s, attach, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := sec.CreateScopedToken("private-mcp", []string{"swarm:read", "sessions:read"}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	handler := s.ContainerSDKHandler()
	call := func(method, path, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:7783"+path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := call(method, path, key)
			if w.Code != http.StatusNotFound || strings.Contains(w.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("%s %s must report absent metadata, got %d %s", method, path, w.Code, w.Header().Get("Content-Type"))
			}
			for _, token := range []string{"", "swk_invalid", attach} {
				if w := call(method, path, token); w.Code != http.StatusUnauthorized {
					t.Fatalf("discovery bypassed authentication: %s %s returned %d", method, path, w.Code)
				}
			}
		}
		if w := call(http.MethodPost, path, key); w.Code != http.StatusForbidden {
			t.Fatalf("discovery accepted a write: %s returned %d", path, w.Code)
		}
	}
	for _, path := range []string{"/v1/onboarding", "/v3/sessions", "/.well-known/oauth-protected-resource/other"} {
		if w := call(http.MethodGet, path, key); w.Code != http.StatusForbidden {
			t.Fatalf("AI key escaped its MCP boundary: %s returned %d", path, w.Code)
		}
	}
	if w := call(http.MethodPost, "/mcp", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("MCP lost authentication: got %d", w.Code)
	}
}
