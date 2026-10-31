package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	endpoint := claudecode.NewSessionMCP(sec, root)
	if err := endpoint.SetAddress("127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	return endpoint
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
// storage root must be captured before recovery, while the address remains
// unavailable until the loopback listener is bound.
func TestSessionMCPConnectedFromRegisteredRunner(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	providers := registry.New()
	providers.RegisterRunner(claudecode.NewRunner(nil, nil))
	root := t.TempDir()
	s = NewCoreServer(nil, nil, nil, nil, nil, nil, nil, s.security, providers, nil, nil, nil, nil, WithDataDir(root))
	t.Cleanup(s.CancelInFlightRuns)
	if s.sessionMCP == nil {
		t.Fatal("registered Claude Code runner was not connected to the session endpoint")
	}
	p := identity.Principal{AccountScopeID: "fixture-account", UserID: "fixture-user"}
	if _, err := s.sessionMCP.SessionToken(p, "fixture-session"); err == nil {
		t.Fatal("session credential issued before the daemon configured storage and address")
	}
	// Later server configuration must not change the endpoint's captured root.
	s.SetDataDir(t.TempDir())
	s.SetProviderKeyPageAddr("0.0.0.0:7781")
	if _, err := s.sessionMCP.SessionToken(p, "fixture-session"); err == nil {
		t.Fatal("non-loopback API address accepted")
	}
	s.SetProviderKeyPageAddr("127.0.0.1:7781")
	if _, err := s.sessionMCP.SessionToken(p, "fixture-session"); err != nil {
		t.Fatalf("configured endpoint: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(root, "claudecode", "sessions")); err != nil || len(entries) != 1 {
		t.Fatalf("endpoint did not retain construction-time root: %v %v", entries, err)
	}
}

// Purpose: the daemon configures the data directory through Server.SetDataDir
// after NewCoreServer (daemon wiring is outside this change's scope), so a run
// recovered during construction may reach the endpoint before its storage
// root exists. SetDataDir must hand the root to the endpoint, and WaitReady
// must hold such a turn until both the root and the loopback address are set,
// instead of failing or writing state anywhere else. Uses the real API server
// option/setter path and the real endpoint; no daemon is needed.
func TestSessionMCPRootFromSetDataDirAndWaitReadyNeedsBoth(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	providers := registry.New()
	providers.RegisterRunner(claudecode.NewRunner(nil, nil))
	s = NewCoreServer(nil, nil, nil, nil, nil, nil, nil, s.security, providers, nil, nil, nil, nil)
	t.Cleanup(s.CancelInFlightRuns)
	if s.sessionMCP == nil {
		t.Fatal("registered Claude Code runner was not connected to the session endpoint")
	}
	p := identity.Principal{AccountScopeID: "fixture-account", UserID: "fixture-user"}
	waitBriefly := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		return s.sessionMCP.WaitReady(ctx)
	}
	s.SetProviderKeyPageAddr("127.0.0.1:7781")
	if err := waitBriefly(); err == nil {
		t.Fatal("endpoint ready with an address but no storage root")
	}
	if _, err := s.sessionMCP.SessionToken(p, "fixture-session"); err == nil {
		t.Fatal("session credential issued without a storage root")
	}
	root := t.TempDir()
	s.SetDataDir(root)
	if err := waitBriefly(); err != nil {
		t.Fatalf("endpoint not ready after SetDataDir and bind: %v", err)
	}
	if _, err := s.sessionMCP.SessionToken(p, "fixture-session"); err != nil {
		t.Fatalf("configured endpoint: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(root, "claudecode", "sessions")); err != nil || len(entries) != 1 {
		t.Fatalf("session state not under SetDataDir root: %v %v", entries, err)
	}
	// A second data directory must not move existing credential bindings.
	other := t.TempDir()
	s.SetDataDir(other)
	if _, err := s.sessionMCP.SessionToken(p, "fixture-session"); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatalf("endpoint root moved to a later data directory: %v", entries)
	}
}
