package claudecode

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
	endpoint := NewSessionMCP(sec, location.Root())
	if addr := location.Addr(); addr != "" {
		if err := endpoint.SetAddress(addr); err != nil {
			panic(err)
		}
	}
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
	return endpoint.SetAddress(addr)
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

// Purpose: the endpoint storage root may arrive after construction. WaitReady
// must wait for both the root and the address (in either order); the root is
// absolute, set once, and never moved, so credential bindings and CLI configs
// stay at one location. Unit-level: only SessionMCP state is involved.
func TestSessionMCPSetRootOnceAndWaitReadyNeedsRootAndAddress(t *testing.T) {
	endpoint := NewSessionMCP(nil, "")
	wait := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		return endpoint.WaitReady(ctx)
	}
	if err := wait(); err == nil {
		t.Fatal("ready with neither root nor address")
	}
	if _, err := endpoint.storageRoot(); err == nil {
		t.Fatal("storage root reported before it was set")
	}
	if err := endpoint.SetRoot("relative/root"); err == nil {
		t.Fatal("relative root accepted")
	}
	root := t.TempDir()
	if err := endpoint.SetRoot(root); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err == nil {
		t.Fatal("ready with a root but no address")
	}
	if err := endpoint.SetAddress("127.0.0.1:7781"); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Fatalf("not ready with root and address: %v", err)
	}
	if err := endpoint.SetRoot(root); err != nil {
		t.Fatalf("same root rejected: %v", err)
	}
	if err := endpoint.SetRoot(t.TempDir()); err == nil {
		t.Fatal("root moved after it was set")
	}
	if got, err := endpoint.storageRoot(); err != nil || got != root {
		t.Fatalf("root=%q err=%v", got, err)
	}
}
