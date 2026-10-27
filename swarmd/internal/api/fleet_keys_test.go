package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/fleet"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Obviously fake key material: no provider is contacted; the fake adapter
// below decides verification.
const fleetKeyTestKey = "fake-fleet-test-key-0000000000aa"

// fleetKeyTestAdapter accepts only fleetKeyTestKey unless refuse is set, and
// echoes a partly masked key in its refusal the way real providers do.
type fleetKeyTestAdapter struct{ refuse bool }

func (fleetKeyTestAdapter) ID() string { return "openai" }

func (fleetKeyTestAdapter) Status(context.Context) (provideriface.Status, error) {
	return provideriface.Status{ID: "openai", Ready: true, AuthMethods: []provideriface.AuthMethod{{ID: "api", Label: "API key", CredentialType: "api"}}}, nil
}

func (a fleetKeyTestAdapter) VerifyCredential(_ context.Context, credential provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	key := credential.APIKey
	if !a.refuse && key == fleetKeyTestKey {
		return provideriface.AuthVerification{Connected: true, Method: "api"}, nil
	}
	masked := key
	if len(key) > 10 {
		masked = key[:6] + "****" + key[len(key)-4:]
	}
	return provideriface.AuthVerification{Connected: false, Method: "api", Message: "Incorrect API key provided: " + masked + ". Check the key."}, nil
}

// newFleetKeyTestSwarm is a fleet test Swarm that can save provider
// credentials through its canonical route, with one existing credential so
// a fleet key never takes the first-provider onboarding path.
func newFleetKeyTestSwarm(t *testing.T, name string, refuse bool) *fleetTestSwarm {
	t.Helper()
	f := newFleetTestSwarm(t, name)
	store := f.s.sessions.Store().Underlying()
	f.s.agentModelSettingsStore = pebblestore.NewAgentModelSettingsStore(store)
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	f.s.agents = agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	f.s.providers = registry.New(fleetKeyTestAdapter{refuse: refuse})
	if _, _, err := f.s.auth.UpsertCredential(auth.CredentialUpsertInput{AccountScopeID: f.account, ID: "own", Provider: "existing-test", Type: "api", APIKey: "fixture-existing-own", Active: true}); err != nil {
		t.Fatal(err)
	}
	return f
}

// keys calls a fleet key route on f as its owner, or with token.
func (f *fleetTestSwarm) keys(t *testing.T, method, path, body string, token *pebblestore.ScopedTokenRecord) (int, map[string]any, string) {
	t.Helper()
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(method, path, strings.NewReader(body)), f.user, f.account)
	if token != nil {
		req = requestWithScopedToken(req, token)
	}
	w := httptest.NewRecorder()
	f.s.handleFleetKeys(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if strings.Contains(w.Body.String(), fleetKeyTestKey) {
		t.Fatalf("%s %s answered with the key value", method, path)
	}
	return w.Code, out, w.Body.String()
}

func (f *fleetTestSwarm) openaiCredentials(t *testing.T) []auth.CredentialStatus {
	t.Helper()
	list, err := f.s.auth.ListCredentialsForAccount(f.account, "openai", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	return list.Records
}

func (f *fleetTestSwarm) credentialCount(t *testing.T) int {
	t.Helper()
	list, err := f.s.auth.ListCredentialsForAccount(f.account, "", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	return list.Total
}

func fleetKeyTarget(t *testing.T, out map[string]any, machine string) map[string]any {
	t.Helper()
	key := out["key"].(map[string]any)
	if machine == "" {
		home, _ := key["home"].(map[string]any)
		return home
	}
	target, _ := key["machines"].(map[string]any)[machine].(map[string]any)
	return target
}

// submitFleetKeyPage is the person pasting key on the home's key page.
func submitFleetKeyPage(t *testing.T, home *fleetTestSwarm, link, key string) (int, string) {
	t.Helper()
	code := link[strings.LastIndex(link, "/")+1:]
	r := httptest.NewRequest(http.MethodPost, "http://"+providerKeyTestAddr+"/keys/"+code, strings.NewReader("api_key="+key))
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("Origin", "http://"+providerKeyTestAddr)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	home.s.handleProviderKeyPage(w, r)
	if strings.Contains(w.Body.String(), key) {
		t.Fatal("the key page echoed the key")
	}
	return w.Code, w.Body.String()
}

func linkFleetKeyMachine(t *testing.T, home, machine *fleetTestSwarm, name string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": machine.invite(t), "name": name})
	if code, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link %s: HTTP %d %v", name, code, out)
	}
}

// Purpose: the home is the fleet's key manager. A person enters a provider
// key once on the home's key page; the home verifies it, keeps it sealed,
// installs it on itself and on each chosen machine over the real pinned link,
// and records each machine's own verification outcome. Revoke removes only
// the credential that key installed, and the home forgets the value once it
// is gone everywhere.
// Threats: a key value in any answer, list or recorded error; a client (or a
// local caller) installing or removing keys on a machine outside the home's
// key manager, including through the home's raw proxy; one machine's failed
// verification affecting another; a revoke deleting unrelated credentials;
// an AI key without swarm:approve, or an agent's gateway token, managing keys.
// Boundary: Server.handleFleetKeys, saveFleetKey, installFleetCredential,
// removeFleetCredentials, handleFleetForward (fleetKeyProxyRefused) and
// fleetLinkHandler, across three in-process daemons with real stores, the
// real TLS link and a fake provider verifier. This is the narrowest layer
// that exercises the link, the key page and the canonical credential route
// together.
func TestFleetKeysInstallAndRevokeOverTheLink(t *testing.T) {
	home := newFleetKeyTestSwarm(t, "home", false)
	defer home.cleanup()
	b := newFleetKeyTestSwarm(t, "box-b", false)
	defer b.cleanup()
	c := newFleetKeyTestSwarm(t, "box-c", true)
	defer c.cleanup()
	home.s.SetProviderKeyPageAddr(providerKeyTestAddr)
	linkFleetKeyMachine(t, home, b, "b")
	linkFleetKeyMachine(t, home, c, "c")
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	defer func() {
		if strings.Contains(logs.String(), fleetKeyTestKey) {
			t.Error("the key reached the log")
		}
	}()

	// Requests are checked before any link is made.
	for _, body := range []string{
		`{"provider":"openai","machines":["nope"]}`,
		`{"provider":"openai"}`,
		`{"provider":"existing-test","machines":["b"]}`,
		`{"provider":"openai","machines":["b"],"api_key":"x"}`,
	} {
		if code, out, _ := home.keys(t, http.MethodPost, "/v3/fleet/keys/requests", body, nil); code != http.StatusBadRequest {
			t.Fatalf("request %s: HTTP %d %v", body, code, out)
		}
	}

	code, out, _ := home.keys(t, http.MethodPost, "/v3/fleet/keys/requests", `{"provider":"openai","label":"team","machines":["c","b"],"include_home":true}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("request: HTTP %d %v", code, out)
	}
	id, link := out["id"].(string), out["url"].(string)
	if !fleetKeyIDPattern.MatchString(id) || !strings.HasPrefix(link, "http://"+providerKeyTestAddr+"/keys/") {
		t.Fatalf("request answer: %v", out)
	}
	// Nothing is recorded before the person enters the key.
	if _, list, _ := home.keys(t, http.MethodGet, "/v3/fleet/keys", "", nil); len(list["keys"].([]any)) != 0 {
		t.Fatalf("key recorded before entry: %v", list)
	}

	// The person enters it once. b verifies and saves it; c's provider
	// refuses it, which is recorded without affecting b or the home.
	status, page := submitFleetKeyPage(t, home, link, fleetKeyTestKey)
	if status != http.StatusOK || !strings.Contains(page, "Installed on this home, b") || !strings.Contains(page, "Not installed on c") {
		t.Fatalf("key page: HTTP %d %s", status, page)
	}
	if status, _ := submitFleetKeyPage(t, home, link, fleetKeyTestKey); status != http.StatusNotFound {
		t.Fatalf("the link worked twice: HTTP %d", status)
	}
	onB := b.openaiCredentials(t)
	if len(onB) != 1 || !onB[0].Active || onB[0].Connection == nil || !onB[0].Connection.Connected ||
		!slices.Contains(onB[0].Tags, "fleet-key:"+id) || !strings.Contains(onB[0].Label, "managed by home home") || !strings.Contains(onB[0].Label, id) {
		t.Fatalf("b's credential: %+v", onB)
	}
	if onC := c.openaiCredentials(t); len(onC) != 0 {
		t.Fatalf("c saved a key its provider refused: %+v", onC)
	}
	if onHome := home.openaiCredentials(t); len(onHome) != 1 || !slices.Contains(onHome[0].Tags, "fleet-key:"+id) {
		t.Fatalf("home's own credential: %+v", onHome)
	}
	value, kept, err := home.s.fleetTokens.GetFleetKeyValue(home.account, id)
	if err != nil || !kept || string(value) != fleetKeyTestKey {
		t.Fatalf("home does not keep the sealed key: kept=%v err=%v", kept, err)
	}

	code, list, raw := home.keys(t, http.MethodGet, "/v3/fleet/keys", "", nil)
	keys, _ := list["keys"].([]any)
	if code != http.StatusOK || len(keys) != 1 {
		t.Fatalf("list: HTTP %d %v", code, list)
	}
	key := keys[0].(map[string]any)
	machines := key["machines"].(map[string]any)
	bState, cState := machines["b"].(map[string]any), machines["c"].(map[string]any)
	if key["id"] != id || key["provider"] != "openai" || key["label"] != "team" || key["last4"] != "00aa" || len(key["fingerprint"].(string)) != 8 ||
		bState["state"] != "installed" || bState["verified"] != true || cState["state"] != "failed" ||
		!strings.Contains(cState["error"].(string), "provider verification failed") || key["home"].(map[string]any)["state"] != "installed" {
		t.Fatalf("recorded outcome: %s", raw)
	}
	for _, fragment := range []string{"fake-f", "****"} {
		if strings.Contains(raw, fragment) {
			t.Fatalf("recorded error carries key material %q: %s", fragment, raw)
		}
	}

	// The machine side answers only its home's key manager over the link:
	// not a local owner, not an AI key, not the home's raw proxy.
	install := `{"id":"` + id + `","provider":"openai","key":"` + fleetKeyTestKey + `"}`
	before := b.credentialCount(t)
	for _, token := range []*pebblestore.ScopedTokenRecord{nil, {ID: "tok_local", AccountScopeID: b.account, UserID: b.user, Scopes: []string{"admin"}}} {
		if code, out, _ := b.keys(t, http.MethodPost, "/v3/fleet/keys/install", install, token); code != http.StatusForbidden {
			t.Fatalf("local install: HTTP %d %v", code, out)
		}
		if code, out, _ := b.keys(t, http.MethodPost, "/v3/fleet/keys/"+id+"/remove", "{}", token); code != http.StatusForbidden {
			t.Fatalf("local remove: HTTP %d %v", code, out)
		}
	}
	for _, path := range []string{
		"/v3/fleet/machines/b/api/v3/fleet/keys/install",
		"/v3/fleet/machines/b/api//v3/fleet/keys/install",
		"/v3/fleet/machines/b/api/v3/fleet/./keys/install",
		"/v3/fleet/machines/b/api/v3/fleet/keys/" + id + "/remove",
		"/v3/fleet/machines/b/api/v3/fleet/keys",
	} {
		if code, out := home.call(t, http.MethodPost, path, install, false); code != http.StatusForbidden {
			t.Fatalf("through the proxy %s: HTTP %d %v", path, code, out)
		}
	}
	if b.credentialCount(t) != before || len(b.openaiCredentials(t)) != 1 {
		t.Fatal("a refused install or remove changed b's credentials")
	}
	// Over the link, a home may only install and remove.
	machine, _, _ := home.s.fleet.GetMachine("b")
	token, _, _ := home.s.fleetTokens.GetFleetLinkToken(home.account, "b")
	cert, _, _ := home.s.fleetCertificate()
	if err := fleet.NewClient(cert, machine.Address, b.fp, token).JSON(t.Context(), http.MethodGet, "/v3/fleet/keys", nil, nil); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("reading b's own fleet keys over the link: %v", err)
	}

	// c's provider accepts the key now: install the kept key there.
	c.s.providers = registry.New(fleetKeyTestAdapter{})
	code, out, _ = home.keys(t, http.MethodPost, "/v3/fleet/keys/"+id+"/install", `{"machines":["c"]}`, nil)
	if code != http.StatusOK || fleetKeyTarget(t, out, "c")["state"] != "installed" || len(c.openaiCredentials(t)) != 1 {
		t.Fatalf("install on c: HTTP %d %v", code, out)
	}
	// A second install replaces the same credential.
	if code, out, _ = home.keys(t, http.MethodPost, "/v3/fleet/keys/"+id+"/install", `{"machines":["c"]}`, nil); code != http.StatusOK || len(c.openaiCredentials(t)) != 1 {
		t.Fatalf("reinstall on c: HTTP %d %v %+v", code, out, c.openaiCredentials(t))
	}

	// Revoke from b removes only that key's credential there.
	code, out, _ = home.keys(t, http.MethodPost, "/v3/fleet/keys/"+id+"/revoke", `{"machines":["b"]}`, nil)
	if code != http.StatusOK || fleetKeyTarget(t, out, "b")["state"] != "removed" || fleetKeyTarget(t, out, "c")["state"] != "installed" || out["value_deleted"] != false {
		t.Fatalf("revoke b: HTTP %d %v", code, out)
	}
	if len(b.openaiCredentials(t)) != 0 || b.credentialCount(t) != 1 || len(c.openaiCredentials(t)) != 1 {
		t.Fatal("revoking from b removed the wrong credentials")
	}
	if _, kept, _ := home.s.fleetTokens.GetFleetKeyValue(home.account, id); !kept {
		t.Fatal("the home forgot a key still installed elsewhere")
	}

	// Revoke everywhere: the home forgets the value; it cannot be installed again.
	code, out, _ = home.keys(t, http.MethodPost, "/v3/fleet/keys/"+id+"/revoke", "", nil)
	if code != http.StatusOK || out["value_deleted"] != true || fleetKeyTarget(t, out, "")["state"] != "removed" || fleetKeyTarget(t, out, "c")["state"] != "removed" {
		t.Fatalf("revoke all: HTTP %d %v", code, out)
	}
	if len(c.openaiCredentials(t)) != 0 || c.credentialCount(t) != 1 || len(home.openaiCredentials(t)) != 0 || home.credentialCount(t) != 1 {
		t.Fatal("revoking everywhere left the key or removed other credentials")
	}
	if _, kept, _ := home.s.fleetTokens.GetFleetKeyValue(home.account, id); kept {
		t.Fatal("the home still keeps a key removed everywhere")
	}
	if code, out, _ := home.keys(t, http.MethodPost, "/v3/fleet/keys/"+id+"/install", `{"machines":["b"]}`, nil); code != http.StatusConflict {
		t.Fatalf("install of a revoked key: HTTP %d %v", code, out)
	}
	if len(b.openaiCredentials(t)) != 0 {
		t.Fatal("a revoked key was installed again")
	}

	// The MCP list is the same record, never the value.
	call := &controlMCPCall{
		request: requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp", nil), home.user, home.account), &pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read"}}),
		next:    http.HandlerFunc(home.s.handleFleetKeys), server: home.s,
	}
	listed, err := controlMCPListFleetKeys(call, map[string]any{"id": id})
	encoded, _ := json.Marshal(listed)
	if err != nil || !strings.Contains(string(encoded), `"revoked_at"`) || strings.Contains(string(encoded), fleetKeyTestKey) {
		t.Fatalf("MCP list: %s %v", encoded, err)
	}
}

// Purpose: reads need sessions:read; requesting, installing and revoking
// fleet keys need an owner session or an AI key with swarm:approve; agents'
// gateway tokens never see or manage them; the MCP route allowlist never
// reaches the machine-side routes.
// Threat: an AI key with write access, or an agent, sending the person's key
// to machines or removing it; a client calling the machine side through MCP.
// Boundary: Server.handleFleetKeys (fleetKeyWriteAuthority) and
// controlMCPRouteAllowed; a direct handler call is the narrowest layer.
func TestFleetKeysScope(t *testing.T) {
	home := newFleetKeyTestSwarm(t, "home", false)
	defer home.cleanup()
	b := newFleetKeyTestSwarm(t, "box-b", false)
	defer b.cleanup()
	home.s.SetProviderKeyPageAddr(providerKeyTestAddr)
	linkFleetKeyMachine(t, home, b, "b")
	token := func(scopes []string, agent string) *pebblestore.ScopedTokenRecord {
		return &pebblestore.ScopedTokenRecord{ID: "tok_test", Name: "test-key", AccountScopeID: home.account, UserID: home.user, Scopes: scopes, AgentName: agent}
	}
	request := `{"provider":"openai","machines":["b"]}`
	for _, tc := range []struct {
		name         string
		token        *pebblestore.ScopedTokenRecord
		read, create int
	}{
		{"read key", token([]string{"sessions:read", "swarm:read"}, ""), http.StatusOK, http.StatusForbidden},
		{"write key", token([]string{"sessions:read", "sessions:write", "swarm:write"}, ""), http.StatusOK, http.StatusForbidden},
		{"no read scope", token([]string{"workers:trigger"}, ""), http.StatusForbidden, http.StatusForbidden},
		{"gateway token", token([]string{"admin"}, "coder"), http.StatusForbidden, http.StatusForbidden},
		{"approve key", token([]string{"sessions:read", "swarm:approve"}, ""), http.StatusOK, http.StatusCreated},
	} {
		if code, out, _ := home.keys(t, http.MethodGet, "/v3/fleet/keys", "", tc.token); code != tc.read {
			t.Fatalf("%s read: HTTP %d %v", tc.name, code, out)
		}
		if code, out, _ := home.keys(t, http.MethodPost, "/v3/fleet/keys/requests", request, tc.token); code != tc.create {
			t.Fatalf("%s request: HTTP %d %v", tc.name, code, out)
		}
		for _, path := range []string{"/v3/fleet/keys/fk_0000000000000000/install", "/v3/fleet/keys/fk_0000000000000000/revoke"} {
			if code, out, _ := home.keys(t, http.MethodPost, path, `{"machines":["b"]}`, tc.token); tc.create == http.StatusForbidden && code != http.StatusForbidden {
				t.Fatalf("%s %s: HTTP %d %v", tc.name, path, code, out)
			}
		}
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/v3/fleet/keys/install"},
		{http.MethodPost, "/v3/fleet/keys/fk_0000000000000000/remove"},
		{http.MethodPost, "/v3/fleet/keys/fk_0000000000000000/install"},
		{http.MethodPost, "/v3/fleet/machines/b/api/v3/fleet/keys/install"},
	} {
		if controlMCPRouteAllowed(route.method, route.path) {
			t.Fatalf("Swarm Control may call %s %s", route.method, route.path)
		}
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v3/fleet/keys"},
		{http.MethodPost, "/v3/fleet/keys/requests"},
		{http.MethodPost, "/v3/fleet/keys/fk_0000000000000000/revoke"},
	} {
		if !controlMCPRouteAllowed(route.method, route.path) {
			t.Fatalf("Swarm Control cannot call %s %s", route.method, route.path)
		}
	}
}
