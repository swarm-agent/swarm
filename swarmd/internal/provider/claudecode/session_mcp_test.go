package claudecode

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
func testEndpoint(t *testing.T) *SessionMCP {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return NewSessionMCP(security.NewService(pebblestore.NewClientAuthStore(store), nil), pebblestore.NewAuthStore(store), t.TempDir())
}
func testSessionServer(t *testing.T, endpoint *SessionMCP, id string) *testMCPServer {
	t.Helper()
	server := httptest.NewServer(endpoint)
	t.Cleanup(server.Close)
	if err := endpoint.SetAddress(server.Listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	_, path, err := endpoint.prepare(testPrincipal(), id)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCPServers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	return &testMCPServer{Server: server, token: strings.TrimPrefix(config.MCPServers["swarm"].Headers["Authorization"], "Bearer ")}
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
