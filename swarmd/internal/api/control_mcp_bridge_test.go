package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Requirement: the stdio bridge (`swarmctl mcp`, `swarm mcp`) relies on this
// daemon contract and nothing more. Over the owner socket, and only there,
// the owner may mint a short-lived full AI key without any credential; that
// key reaches Swarm Control at the full level, cannot mint further keys or
// reach other routes, and a purge removes it so it stops working at once.
// Threats: a caller without socket ownership minting a key over the loopback
// API, the bridge's key acting as an owner credential outside /mcp, or a
// "deleted" key that still authenticates. Owners: withAuth (local transport
// and scoped-token branches), gateAIKey, handleAuthTokens/aiKeyScopes,
// handleAuthTokenSubpath purge, handleControlMCP. In-process HTTP through the
// real LocalTransportHandler and Handler with temporary stores is the
// narrowest layer joining these; the bridge's own framing is covered in
// swarmd/cmd/swarmctl (TestMCPBridge*). Not proven: a live daemon socket.
func TestControlMCPLocalBridgeKey(t *testing.T) {
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	socket, loopback := s.LocalTransportHandler(), s.Handler()
	send := func(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://swarm-local-transport"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:40000"
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	const mint = `{"name":"AI client on this machine (swarm mcp)","ai_access":"full","expires_in_seconds":1800}`
	countKeys := func() int {
		records, err := sec.ListScopedTokens(actor.AccountScopeID)
		if err != nil {
			t.Fatal(err)
		}
		return len(records)
	}

	if w := send(loopback, "POST", "/v3/auth/tokens", "", mint); w.Code != http.StatusUnauthorized || countKeys() != 0 {
		t.Fatalf("loopback API without socket ownership minted a key: %d %s", w.Code, w.Body.String())
	}
	w := send(socket, "POST", "/v3/auth/tokens", "", mint)
	var issued struct {
		Token  string `json:"token"`
		Record struct {
			ID        string   `json:"id"`
			Scopes    []string `json:"scopes"`
			ExpiresAt int64    `json:"expires_at"`
		} `json:"record"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &issued) != nil || !strings.HasPrefix(issued.Token, "swk_") || issued.Record.ID == "" || issued.Record.ExpiresAt == 0 {
		t.Fatalf("owner socket could not mint the bridge key: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(strings.Join(issued.Record.Scopes, ","), "swarm:manage") {
		t.Fatalf("full access must carry the full Swarm Control level: %v", issued.Record.Scopes)
	}

	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	w = send(socket, "POST", "/mcp?tools=all", issued.Token, list)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"swarm_request_provider_key"`) || !strings.Contains(w.Body.String(), `"swarm_resolve_permission"`) {
		t.Fatalf("bridge key must see the full tool set: %d %s", w.Code, w.Body.String())
	}
	// By default the same key lists the worker and job tools only.
	w = send(socket, "POST", "/mcp", issued.Token, list)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"swarm_give_job"`) || strings.Contains(w.Body.String(), `"swarm_resolve_permission"`) {
		t.Fatalf("default tool list must be the worker and job tools: %d %s", w.Code, w.Body.String())
	}
	if w := send(socket, "POST", "/mcp", "", list); w.Code != http.StatusUnauthorized {
		t.Fatalf("token-less socket calls to /mcp must stay refused: %d", w.Code)
	}
	before := countKeys()
	if w := send(socket, "POST", "/v3/auth/tokens", issued.Token, mint); w.Code != http.StatusForbidden || countKeys() != before {
		t.Fatalf("the bridge key must not mint keys: %d %s", w.Code, w.Body.String())
	}
	if w := send(socket, "GET", "/v3/auth/tokens", issued.Token, ""); w.Code != http.StatusForbidden {
		t.Fatalf("the bridge key must reach only /mcp: %d", w.Code)
	}

	if w := send(socket, "DELETE", "/v3/auth/tokens/"+issued.Record.ID+"?purge=true", "", ""); w.Code != http.StatusOK || countKeys() != 0 {
		t.Fatalf("purge failed: %d %s", w.Code, w.Body.String())
	}
	if w := send(socket, "POST", "/mcp", issued.Token, list); w.Code != http.StatusUnauthorized {
		t.Fatalf("a purged key must stop working: %d", w.Code)
	}
}
