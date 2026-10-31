package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/claudecode"
	"swarm/packages/swarmd/internal/provider/registry"
	"swarm/packages/swarmd/internal/security"
)

// testSessionMCPEndpoint is a session endpoint over a temporary storage root.
// The loopback address only names the URL written to the CLI config.
func testSessionMCPEndpoint(t *testing.T, sec *security.Service) *claudecode.SessionMCP {
	t.Helper()
	root := t.TempDir()
	return claudecode.NewSessionMCP(sec, func() string { return root }, func() string { return "127.0.0.1:1" })
}

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
	endpoint := testSessionMCPEndpoint(t, s.security)
	key, err := endpoint.SessionToken(identity.Principal{AccountScopeID: actor.AccountScopeID, UserID: actor.UserID}, "fixture-session")
	if err != nil {
		t.Fatal(err)
	}
	s.SetSessionMCP(endpoint)
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

// Purpose: the API server, not daemon wiring outside the provider scope,
// connects a registered Claude Code runner to the standing endpoint. The
// endpoint must stay unusable until the daemon has given the server its data
// directory and bound loopback API address, and work after both are set.
func TestSessionMCPConnectedFromRegisteredRunner(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	providers := registry.New()
	providers.RegisterRunner(claudecode.NewRunner(nil, nil))
	s.sessionMCP = nil
	s.SetDataDir("")
	s.SetProviderKeyPageAddr("")
	s.connectClaudeCodeSessionMCP(providers)
	if s.sessionMCP == nil {
		t.Fatal("registered Claude Code runner was not connected to the session endpoint")
	}
	p := identity.Principal{AccountScopeID: "fixture-account", UserID: "fixture-user"}
	if _, err := s.sessionMCP.SessionToken(p, "fixture-session"); err == nil {
		t.Fatal("session credential issued before the daemon configured storage and address")
	}
	s.SetDataDir(t.TempDir())
	s.SetProviderKeyPageAddr("0.0.0.0:7781")
	if _, err := s.sessionMCP.SessionToken(p, "fixture-session"); err == nil {
		t.Fatal("non-loopback API address accepted")
	}
	s.SetProviderKeyPageAddr("127.0.0.1:7781")
	if _, err := s.sessionMCP.SessionToken(p, "fixture-session"); err != nil {
		t.Fatalf("configured endpoint: %v", err)
	}
}
