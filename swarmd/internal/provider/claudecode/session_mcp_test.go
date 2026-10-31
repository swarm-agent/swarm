package claudecode

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type testMCPServer struct {
	*httptest.Server
	token string
}

func (s *testMCPServer) URL() string { return s.Server.URL + "/mcp" }
func testPrincipal() identity.Principal {
	return identity.Principal{AccountScopeID: "fixture-account", UserID: "fixture-user"}
}

// testLocation is the daemon-provided storage root and loopback API address
// a SessionMCP reads when a turn starts.
type testLocation struct {
	mu   sync.Mutex
	root string
	addr string
}

func (l *testLocation) Root() string { return l.root }
func (l *testLocation) Addr() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.addr
}

var testLocations sync.Map // *SessionMCP -> *testLocation

func newTestSessionMCP(sec *security.Service, location *testLocation) *SessionMCP {
	endpoint := NewSessionMCP(sec, location.Root, location.Addr)
	testLocations.Store(endpoint, location)
	return endpoint
}

// setTestAddress plays the daemon binding its loopback API listener.
func setTestAddress(endpoint *SessionMCP, addr string) error {
	value, _ := testLocations.Load(endpoint)
	location := value.(*testLocation)
	location.mu.Lock()
	location.addr = addr
	location.mu.Unlock()
	return nil
}

// reopenTestEndpoint recreates the endpoint (a daemon restart) over the same
// credential service, storage root and address.
func reopenTestEndpoint(old *SessionMCP, sec *security.Service) *SessionMCP {
	value, _ := testLocations.Load(old)
	return newTestSessionMCP(sec, value.(*testLocation))
}

func testEndpoint(t *testing.T) *SessionMCP {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return newTestSessionMCP(security.NewService(pebblestore.NewClientAuthStore(store), nil), &testLocation{root: t.TempDir()})
}

func testSessionServer(t *testing.T, endpoint *SessionMCP, id string) *testMCPServer {
	t.Helper()
	server := httptest.NewServer(endpoint)
	t.Cleanup(server.Close)
	if err := setTestAddress(endpoint, server.Listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	config, err := endpoint.prepare(testPrincipal(), id)
	if err != nil {
		t.Fatal(err)
	}
	if token := configToken(mustReadFile(t, config.configPath)); token != config.token || token == "" {
		t.Fatal("config does not carry the session credential")
	}
	return &testMCPServer{Server: server, token: config.token}
}

func testToolsServer(t *testing.T, ctx context.Context, tools []provideriface.ToolDefinition, invoker provideriface.ToolInvoker) *testMCPServer {
	t.Helper()
	endpoint := testEndpoint(t)
	plan := turnPlan{input: &turnInput{}, restart: &restartSignal{}, correlat: newToolUseCorrelator()}
	detach, err := endpoint.attach(ctx, testPrincipal(), provideriface.Request{SessionID: "fixture", Tools: tools, ToolInvoker: invoker}, &plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(detach)
	return testSessionServer(t, endpoint, "fixture")
}
