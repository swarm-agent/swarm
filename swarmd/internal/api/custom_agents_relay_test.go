package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"swarm/packages/swarmd/internal/remote"
	"swarm/packages/swarmd/internal/security"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type customRelaySecrets struct {
	mu   sync.Mutex
	data []byte
}

func (s *customRelaySecrets) PutJSON(_ string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	s.data, err = json.Marshal(v)
	return err
}
func (s *customRelaySecrets) GetJSON(_ string, v any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data) == 0 {
		return false, nil
	}
	return true, json.Unmarshal(s.data, v)
}
func (s *customRelaySecrets) Delete(string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = nil
	return nil
}

type customRelayIssuer struct{ server *Server }

func (i customRelayIssuer) Mint(name string, scopes []string) (string, string, error) {
	p := testPrincipal()
	raw, record, err := i.server.security.CreateInternalToken(name, scopes, p.AccountScopeID, p.UserID)
	return raw, record.ID, err
}
func (i customRelayIssuer) Revoke(id string) error {
	_, err := i.server.security.RevokeScopedToken(testPrincipal().AccountScopeID, id)
	return err
}

// Purpose: remote.Service must dispatch define through MCP and its real inner
// route with a minted device credential, including pre-upgrade devices. A local
// WebSocket relay and real token store prove the scope boundary end to end;
// read clients must be refused without creating a profile.
func TestCustomAgentRelayDeviceDefinition(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			c, _ := newMCPPresetTestCall(t)
			c.server.security = security.NewService(store.NewClientAuthStore(c.server.sessions.Store().Underlying()), nil)
			results := make(chan error, 1)
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					results <- err
					return
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
				if err = conn.WriteJSON(map[string]any{"type": "challenge", "nonce": "test-nonce"}); err != nil {
					results <- err
					return
				}
				var auth map[string]any
				if err = conn.ReadJSON(&auth); err != nil {
					results <- err
					return
				}
				if err = conn.WriteJSON(map[string]any{"type": "ready"}); err != nil {
					results <- err
					return
				}
				for _, write := range []bool{false, true} {
					name := "read-denied"
					scopes := []string{"swarm:read"}
					if write {
						name = "social-maker"
						scopes = append(scopes, "swarm:write")
					}
					body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "swarm_define_agent", "arguments": map[string]any{"name": name, "instructions": "Make media", "tools": []string{"read", "manage_media"}}}}
					if err = conn.WriteJSON(map[string]any{"type": "mcp.request", "id": "call", "client": map[string]any{"client_id": "client", "scopes": scopes}, "body": body}); err != nil {
						results <- err
						return
					}
					var response struct {
						Body struct {
							Error  any `json:"error"`
							Result struct {
								IsError bool `json:"isError"`
							} `json:"result"`
						} `json:"body"`
					}
					if err = conn.ReadJSON(&response); err != nil {
						results <- err
						return
					}
					if response.Body.Error != nil || response.Body.Result.IsError == write {
						results <- fmt.Errorf("unexpected relay response: %+v", response)
						return
					}
				}
				results <- nil
			}))
			defer relay.Close()
			secrets := &customRelaySecrets{}
			issuer := customRelayIssuer{c.server}
			svc := remote.NewService(secrets, issuer)
			svc.SetControlHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				token, err := c.server.security.ValidateScopedToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
				if err != nil || token == nil {
					http.Error(w, "invalid token", 401)
					return
				}
				c.server.handleControlMCP(w, requestWithScopedToken(withTestPrincipal(r), token))
			}))
			if _, err := svc.Init(remote.InitInput{RelayURL: relay.URL, DeviceName: "test-device", AllowWrite: true}); err != nil {
				t.Fatal(err)
			}
			var old string
			if legacy {
				var cfg remote.Config
				_, _ = secrets.GetJSON("", &cfg)
				if err := issuer.Revoke(cfg.TokenID); err != nil {
					t.Fatal(err)
				}
				var err error
				cfg.Token, cfg.TokenID, err = issuer.Mint("swarm-remote old", []string{"sessions:read", "sessions:write", "agents:read", "automations:read", "automations:write", "settings:write"})
				if err != nil {
					t.Fatal(err)
				}
				old = cfg.Token
				cfg.TokenScopeVersion = 0
				if err := secrets.PutJSON("", cfg); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := svc.SetEnabled(true); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); svc.Run(ctx) }()
			select {
			case err := <-results:
				if err != nil {
					t.Error(err)
				}
			case <-ctx.Done():
				t.Error("relay timed out")
			}
			cancel()
			<-done
			p, found, err := c.server.agents.GetProfileForAccount(testPrincipal().AccountScopeID, "social-maker")
			if err != nil || !found || !p.Enabled {
				t.Fatalf("definition missing: %v", err)
			}
			if _, found, err := c.server.agents.GetProfileForAccount(testPrincipal().AccountScopeID, "read-denied"); err != nil || found {
				t.Fatalf("read client mutated profiles: %v", err)
			}
			if legacy {
				token, _ := c.server.security.ValidateScopedToken(old)
				if token != nil {
					t.Fatal("legacy token still valid")
				}
			}
		})
	}
}
