package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: request_provider_key permits write/admin, never read, and advertises
// reachable links. controlMCPRequestProviderKey and levelAllows own this boundary;
// direct calls with real provider registration are the narrowest scope proof.
func TestProviderKeyWriteScopeAndTailnetOrigin(t *testing.T) {
	s, _ := newControlMCPProviderConnectServer(t)
	s.SetProviderKeyPageAddr(providerKeyTestAddr)
	for _, scope := range []string{"sessions:read", "sessions:write", "admin"} {
		c := controlMCPProviderTestCall(t, s, scope)
		c.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{AccountScopeID: testAccountScopeID, UserID: testUserID, Scopes: []string{scope}})
		result, err := controlMCPRequestProviderKey(c, map[string]any{"provider": "openai"})
		if scope == "sessions:read" {
			if err == nil || len(s.providerKeys.byCode) != 0 {
				t.Fatal("read minted capability")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(result.(map[string]any)["url"].(string), "http://"+providerKeyTestAddr) {
			t.Fatal(result)
		}
	}
	if !s.SetProviderKeyTailnetOrigin("https://one.example.ts.net:8444") {
		t.Fatal("origin refused")
	}
	c := controlMCPProviderTestCall(t, s, "sessions:write")
	c.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{AccountScopeID: testAccountScopeID, UserID: testUserID, Scopes: []string{"sessions:write"}})
	result, err := controlMCPRequestProviderKey(c, map[string]any{"provider": "openai"})
	if err != nil {
		t.Fatal(err)
	}
	got := result.(map[string]any)
	if !strings.HasPrefix(got["url"].(string), "https://one.example.ts.net:8444/keys/") || !strings.Contains(got["tell_the_person"].(string), "device in your tailnet") {
		t.Fatal(got)
	}
}

// Purpose: only a valid pending capability from local Serve with user identity
// and a same-tailnet origin reaches submitProviderKey. Gateway-level tests prove
// routing, CORS and non-consumption on rejection without a live network/provider.
// A deliberately unavailable identity backend makes accepted POST fail safely;
// no submitted key may reach the response or audit log.
func TestProviderKeyTailnetGatewayCapabilityAndCORS(t *testing.T) {
	s := &Server{}
	s.SetProviderKeyTailnetOrigin("https://one.example.ts.net:8444")
	code, _, err := s.providerKeys.issue(providerKeyRequest{provider: "openai", accountScopeID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	handler := s.TailnetGatewayHandler()
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	request := func(method, path, origin, remote, login string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://one.example.ts.net:8444"+path, strings.NewReader("api_key="+providerKeyTestGood))
		r.RemoteAddr = remote
		r.Header.Set("Tailscale-User-Login", login)
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if method == "OPTIONS" {
			r.Header.Set("Access-Control-Request-Method", "POST")
			r.Header.Set("Access-Control-Request-Headers", "content-type")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), providerKeyTestGood) || strings.Contains(logs.String(), providerKeyTestGood) {
			t.Fatal("secret leaked")
		}
		return w
	}
	path := "/keys/" + code
	for _, tc := range []struct {
		method, path, origin, remote, login string
		status                              int
	}{
		{"GET", path, "", "127.0.0.1:1234", "person@example.test", 200},
		{"GET", path, "", "100.64.0.1:1234", "person@example.test", 403},
		{"GET", path, "", "127.0.0.1:1234", "", 403},
		{"GET", "/keys/invalid", "", "127.0.0.1:1234", "person@example.test", 403},
		{"OPTIONS", path, "https://two.example.ts.net:8444", "127.0.0.1:1234", "person@example.test", 204},
	} {
		w := request(tc.method, tc.path, tc.origin, tc.remote, tc.login)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.method, w.Code, w.Body.String())
		}
	}
	for _, origin := range []string{"https://two.other.ts.net:8444", "https://two.example.ts.net", "http://two.example.ts.net:8444", "https://two.example.ts.net.evil.test:8444", "null", "https://two.example.ts.net:8444/path"} {
		for _, method := range []string{"POST", "OPTIONS"} {
			w := request(method, path, origin, "127.0.0.1:1234", "person@example.test")
			if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("accepted %s %s", method, origin)
			}
			entry, ok := s.providerKeys.lookup(code)
			if !ok || entry.attempts != 0 {
				t.Fatal("rejection mutated capability")
			}
		}
	}
	w := request("POST", path, "https://two.example.ts.net:8444", "127.0.0.1:1234", "person@example.test")
	if w.Code != 400 || w.Header().Get("Access-Control-Allow-Origin") != "https://two.example.ts.net:8444" {
		t.Fatalf("POST not admitted: %d", w.Code)
	}
	entry, ok := s.providerKeys.lookup(code)
	if !ok || entry.attempts != 1 {
		t.Fatal("POST did not reach submit")
	}
	if !strings.Contains(logs.String(), "person@example.test") || !strings.Contains(logs.String(), "outcome=failed") {
		t.Fatal("missing audit")
	}
	claimed, _, ok := s.providerKeys.begin(code)
	if !ok {
		t.Fatal("claim")
	}
	s.providerKeys.finish(claimed, true, "verified")
	for _, method := range []string{"GET", "POST", "OPTIONS"} {
		if w := request(method, path, "https://two.example.ts.net:8444", "127.0.0.1:1234", "person@example.test"); w.Code != http.StatusNotFound || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("used capability accepted")
		}
		if w := request(method, "/keys/"+strings.Repeat("Z", 43), "https://two.example.ts.net:8444", "127.0.0.1:1234", "person@example.test"); w.Code != 404 {
			t.Fatal("unknown capability accepted")
		}
	}
	if w := request("GET", "/v1/providers", "", "127.0.0.1:1234", "person@example.test"); w.Code == 200 {
		t.Fatal("non-MCP route admitted")
	}
}

// Purpose: a tailnet write capability (an ephemeral token, not a stored bearer)
// must actually save only after verification through saveProviderKey and the
// canonical credential handler. A fake adapter with a real store is the narrowest
// deterministic proof; failed verification must leave no stored credential.
func TestProviderKeyTailnetVerifyAndSave(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	store := s.sessions.Store().Underlying()
	s.agentModelSettingsStore = pebblestore.NewAgentModelSettingsStore(store)
	events, eventErr := pebblestore.NewEventLog(store)
	if eventErr != nil {
		t.Fatal(eventErr)
	}
	s.agents = agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	s.providers = registry.New(providerKeyTestAdapter{id: "openai", takesKey: true})
	s.SetProviderKeyTailnetOrigin("https://one.example.ts.net:8444")
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.auth.UpsertCredential(auth.CredentialUpsertInput{AccountScopeID: actor.AccountScopeID, Provider: "existing-test", Type: "api", APIKey: "fixture-existing", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := s.providerKeys.issue(providerKeyRequest{provider: "openai", accountScopeID: actor.AccountScopeID, userID: actor.UserID, tailnet: true, token: pebblestore.ScopedTokenRecord{ID: "tailnet:test", AccountScopeID: actor.AccountScopeID, UserID: actor.UserID, Scopes: []string{"sessions:write"}}})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	for _, key := range []string{providerKeyTestBad, providerKeyTestGood} {
		r := httptest.NewRequest("POST", "https://one.example.ts.net:8444/keys/"+code, strings.NewReader("api_key="+key))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Tailscale-User-Login", "person@example.test")
		r.Header.Set("Origin", "https://two.example.ts.net:8444")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.TailnetGatewayHandler().ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), key) || strings.Contains(logs.String(), key) {
			t.Fatal("secret leaked")
		}
		list, err := s.auth.ListCredentialsForAccount(actor.AccountScopeID, "openai", "", 200)
		if err != nil {
			t.Fatal(err)
		}
		if key == providerKeyTestBad {
			if w.Code != 400 || len(list.Records) != 0 {
				t.Fatalf("failure persisted: %d %s", w.Code, w.Body.String())
			}
		} else {
			if w.Code != 200 || len(list.Records) != 1 || !list.Records[0].Active || list.Records[0].Connection == nil || !list.Records[0].Connection.Connected {
				t.Fatalf("save failed: %d %s", w.Code, w.Body.String())
			}
			if _, ok := s.providerKeys.lookup(code); ok {
				t.Fatal("saved code still usable")
			}
		}
	}
}

// Purpose: the gateway shares providerKeyRegistry's TTL and attempt budget;
// clock-driven registry checks are the narrowest deterministic protection
// against replay after expiry or repeated failed verification.
func TestProviderKeyTailnetExpiryAndAttempts(t *testing.T) {
	s := &Server{}
	s.SetProviderKeyTailnetOrigin("https://one.example.ts.net:8444")
	now := time.Now()
	s.providerKeys.now = func() time.Time { return now }
	code, _, _ := s.providerKeys.issue(providerKeyRequest{provider: "test", accountScopeID: "test"})
	now = now.Add(providerKeyCodeTTL)
	if _, ok := s.providerKeys.lookup(code); ok {
		t.Fatal("expired code usable")
	}
	code, _, _ = s.providerKeys.issue(providerKeyRequest{provider: "test", accountScopeID: "test"})
	for i := 0; i < providerKeyMaxAttempts; i++ {
		entry, _, ok := s.providerKeys.begin(code)
		if !ok {
			t.Fatal("early exhaustion")
		}
		retry := s.providerKeys.finish(entry, false, "refused")
		if retry != (i < providerKeyMaxAttempts-1) {
			t.Fatal("wrong attempt budget")
		}
	}
	if _, ok := s.providerKeys.lookup(code); ok {
		t.Fatal("exhausted code usable")
	}
}
