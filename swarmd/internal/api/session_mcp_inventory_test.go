package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: tools/list on the mounted endpoint must be the real V3 resolved
// system-agent catalog, not Swarm Control's different inventory or built-ins.
// Resolve through resolveSessionV3ProviderTools and query the real HTTP handler;
// this is the narrowest cross-layer proof for Orchestrator vs Coder authority.
func TestSessionMCPResolvedSystemAgentInventories(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "inventory"), nil)
	defer f.close()
	id := f.createSession("Inventory")
	row, found, err := f.sessions.GetSession(id)
	if err != nil || !found {
		t.Fatalf("session: %v", err)
	}
	f.server.runner = runruntime.NewService(f.sessions, f.server.model, f.server.providers, tool.NewRuntime(1), f.permissions, f.server.agents, nil, nil)
	credentialStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "credentials"))
	if err != nil {
		t.Fatal(err)
	}
	defer credentialStore.Close()
	f.server.security = security.NewService(pebblestore.NewClientAuthStore(credentialStore), nil)
	endpoint := testSessionMCPEndpoint(t, f.server.security)
	f.server.SetSessionMCP(endpoint)
	p := identity.Principal{AccountScopeID: row.AccountScopeID, UserID: row.UserID}
	for _, name := range []string{"swarm", "coder"} {
		t.Run(name, func(t *testing.T) {
			profile, err := f.server.agents.ResolveSystemAgent(map[string]string{"swarm": "swarm", "coder": "system-coder"}[name], pebblestore.AgentProfile{})
			if err != nil {
				t.Fatal(err)
			}
			definitions, err := f.server.v3SessionExecutor.resolveSessionV3ProviderTools(p.AccountScopeID, profile)
			if err != nil {
				t.Fatal(err)
			}
			detach, err := endpoint.WithToolExecutor(context.Background(), p, provideriface.Request{SessionID: id, Tools: definitions})
			if err != nil {
				t.Fatal(err)
			}
			defer detach()
			token, err := endpoint.SessionToken(p, id)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			f.server.LoopbackAPIHandler().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("tools/list: %d %s", w.Code, w.Body)
			}
			var response struct {
				Result struct {
					Tools []struct {
						Name string `json:"name"`
					} `json:"tools"`
				} `json:"result"`
			}
			if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatalf("%s: %v", w.Body, err)
			}
			if len(response.Result.Tools) != len(definitions) {
				t.Fatalf("listed=%d resolved=%d", len(response.Result.Tools), len(definitions))
			}
			names := map[string]bool{}
			for _, d := range response.Result.Tools {
				names[d.Name] = true
			}
			for _, d := range definitions {
				if !names[d.Name] {
					t.Fatalf("missing resolved tool %s", d.Name)
				}
			}
			if name == "swarm" && !names["manage-worktree"] {
				t.Fatalf("not the Orchestrator catalog: %v", names)
			}
			if name == "coder" {
				for _, required := range []string{"bash", "read", "edit", "search", "task_progress"} {
					if !names[required] {
						t.Fatalf("coder missing %s: %v", required, names)
					}
				}
				if names["manage-worktree"] {
					t.Fatal("coder acquired orchestrator management tools")
				}
			}
		})
	}
}
