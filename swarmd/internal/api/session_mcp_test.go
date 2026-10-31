package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/provider/claudecode"
)

// Purpose: the daemon mounts session MCP only on its loopback API listener.
// Session keys must never gain ordinary HTTP or Swarm Control authority through
// another transport. Test real auth middleware with a canonical scoped token;
// absence of an active executor is 503 only on the mounted endpoint, and other
// routes/transports reject the credential before reaching their handlers.
func TestSessionMCPLoopbackMountAndTransportConfinement(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.security.CreateSessionToken(actor.AccountScopeID, actor.UserID, "fixture-session")
	if err != nil {
		t.Fatal(err)
	}
	// No credential preparation is needed to test routing/authentication.
	s.SetSessionMCP(claudecode.NewSessionMCP(s.security, nil, t.TempDir()))
	for _, tc := range []struct {
		name    string
		handler http.Handler
		path    string
		want    int
	}{
		{"loopback MCP", s.LoopbackAPIHandler(), "/mcp", 503},
		{"ordinary HTTP", s.LoopbackAPIHandler(), "/v3/sessions", 403},
		{"auth-exempt route", s.Handler(), "/health", 403},
		{"private MCP", s.LocalTransportHandler(), "/mcp", 403},
		{"unmounted MCP", s.Handler(), "/mcp", 403},
		{"SDK MCP", s.ContainerSDKHandler(), "/mcp", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			req.Header.Set("Authorization", "Bearer "+key)
			w := httptest.NewRecorder()
			tc.handler.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if strings.Contains(w.Body.String(), "\"tools\"") {
				t.Fatalf("unauthorized inventory: %s", w.Body)
			}
		})
	}
}
