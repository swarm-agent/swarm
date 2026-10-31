package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Purpose: session MCP routing must classify an exact internal marker, not
// HasScope's wildcard/admin authority. Exercise withAuth, gateAIKey, SDK auth,
// and LoopbackAPIHandler's Swarm Control dispatch over the real token store;
// these HTTP boundaries are the narrowest proof that broad keys retain access.
// The existing confinement test proves genuine session keys remain refused.
func TestSessionMCPBroadKeysRetainOrdinaryAndControlAccess(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	s.SetSessionMCP(testSessionMCPEndpoint(t, s.security))
	for _, scope := range []string{"*", "admin", "session:*", ""} {
		t.Run("scope="+scope, func(t *testing.T) {
			var scopes []string
			if scope != "" {
				scopes = []string{scope}
			}
			token, _, err := s.security.CreateInternalToken("broad-key-fixture", scopes, actor.AccountScopeID, actor.UserID)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			req := httptest.NewRequest(http.MethodGet, "/v3/sessions", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			s.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, req)
			if !called || w.Code != http.StatusNoContent {
				t.Fatalf("withAuth: called=%v status=%d body=%s", called, w.Code, w.Body)
			}
			for _, transport := range []struct {
				name    string
				handler http.Handler
			}{{"loopback", s.LoopbackAPIHandler()}, {"SDK", s.ContainerSDKHandler()}} {
				t.Run(transport.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`))
					req.Header.Set("Authorization", "Bearer "+token)
					req.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					transport.handler.ServeHTTP(w, req)
					var response struct {
						Result struct {
							ServerInfo struct {
								Name string `json:"name"`
							} `json:"serverInfo"`
						} `json:"result"`
						Error json.RawMessage `json:"error"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
						t.Fatalf("decode: %v status=%d body=%s", err, w.Code, w.Body)
					}
					if w.Code != http.StatusOK || len(response.Error) > 0 || response.Result.ServerInfo.Name == "" {
						t.Fatalf("Swarm Control initialization: %d %s", w.Code, w.Body)
					}
				})
			}
		})
	}
}
