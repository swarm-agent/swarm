package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	"swarm/packages/swarmd/internal/remote"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Obviously fake key material. Nothing here is a real provider key and no
// provider is contacted: the fake adapter below decides verification.
const (
	providerKeyTestGood = "fake-test-key-good-000000000001"
	providerKeyTestBad  = "fake-test-key-bad-000000000009"
	providerKeyTestAddr = "127.0.0.1:7781"
)

type providerKeyTestAdapter struct {
	id       string
	takesKey bool
}

func (a providerKeyTestAdapter) ID() string { return a.id }

func (a providerKeyTestAdapter) Status(context.Context) (provideriface.Status, error) {
	status := provideriface.Status{ID: a.id, Ready: true}
	if a.takesKey {
		status.AuthMethods = []provideriface.AuthMethod{{ID: "api", Label: "API key", CredentialType: "api"}}
	} else {
		status.AuthMethods = []provideriface.AuthMethod{{ID: "oauth", Label: "Sign in", CredentialType: "oauth"}}
	}
	return status, nil
}

// VerifyCredential accepts only the good fake key and, like real providers,
// echoes a partly masked key in its refusal.
func (a providerKeyTestAdapter) VerifyCredential(_ context.Context, credential provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	key := credential.APIKey
	if key == providerKeyTestGood {
		return provideriface.AuthVerification{Connected: true, Method: "api"}, nil
	}
	masked := key
	if len(key) > 10 {
		masked = key[:6] + "****" + key[len(key)-4:]
	}
	return provideriface.AuthVerification{Connected: false, Method: "api", Message: "Incorrect API key provided: " + masked + ". Check the key."}, nil
}

type providerKeyFixture struct {
	s       *Server
	call    func(tool string, args map[string]any) (string, bool)
	account string
	now     *time.Time
	tokenID string
	revoke  func()
}

func setupProviderKeyFixture(t *testing.T) providerKeyFixture {
	t.Helper()
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	t.Cleanup(cleanup)
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "agents"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	s.agents = agentruntime.NewService(pebblestore.NewAgentStore(db), events)
	if err := s.agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	s.providers = registry.New(providerKeyTestAdapter{id: "fakeai", takesKey: true}, providerKeyTestAdapter{id: "signinonly"})
	now := time.Unix(1_800_000_000, 0)
	s.providerKeys.now = func() time.Time { return now }
	s.SetProviderKeyPageAddr(providerKeyTestAddr)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	// A box past onboarding (one provider already signed in), so the
	// canonical non-first credential path runs; first-run onboarding
	// hydration needs model services this fixture does not build.
	if _, _, err := s.auth.UpsertCredential(auth.CredentialUpsertInput{Provider: "signinonly", AccountScopeID: actor.AccountScopeID, Type: "oauth", AccessToken: "fake-seed-access-token"}); err != nil {
		t.Fatal(err)
	}
	scopes, err := aiKeyScopes("full")
	if err != nil {
		t.Fatal(err)
	}
	token, record, err := sec.CreateScopedToken("ai", scopes, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	f := providerKeyFixture{s: s, call: controlMCPTestCaller(t, s, token), account: actor.AccountScopeID, now: &now, tokenID: record.ID}
	f.revoke = func() {
		if _, err := sec.RevokeScopedToken(actor.AccountScopeID, record.ID); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f providerKeyFixture) requestLink(t *testing.T, provider string) string {
	t.Helper()
	text, isErr := f.call("swarm_request_provider_key", map[string]any{"provider": provider, "label": "from test"})
	if isErr {
		t.Fatalf("request_provider_key: %s", text)
	}
	var out struct {
		URL  string `json:"url"`
		Tell string `json:"tell_the_person"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.URL, "http://"+providerKeyTestAddr+providerKeyPagePrefix) || !strings.Contains(out.Tell, out.URL) || !strings.Contains(out.Tell, "Do not paste the key into this chat") {
		t.Fatalf("link result = %s", text)
	}
	code := strings.TrimPrefix(out.URL, "http://"+providerKeyTestAddr+providerKeyPagePrefix)
	if !providerKeyCodePattern.MatchString(code) {
		t.Fatalf("code %q is not 43 base64url characters (256 bits)", code)
	}
	return code
}

func (f providerKeyFixture) providers(t *testing.T) map[string]map[string]any {
	t.Helper()
	text, isErr := f.call("swarm_list_providers", map[string]any{})
	if isErr {
		t.Fatalf("list_providers: %s", text)
	}
	for _, secret := range []string{providerKeyTestGood, providerKeyTestBad, "0001", "0009"} {
		if strings.Contains(text, secret) {
			t.Fatalf("list_providers leaked key material %q: %s", secret, text)
		}
	}
	var out struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, entry := range out.Providers {
		byID[entry["provider"].(string)] = entry
	}
	return byID
}

type providerKeyPost struct {
	origin, referer, fetchSite, contentType string
	form                                    url.Values
	body                                    string
}

func providerKeyHit(handler http.Handler, method, code string, post *providerKeyPost, mutate func(*http.Request)) *httptest.ResponseRecorder {
	var body string
	if post != nil {
		body = post.body
		if post.form != nil {
			body = post.form.Encode()
		}
	}
	r := httptest.NewRequest(method, "http://"+providerKeyTestAddr+providerKeyPagePrefix+code, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:51000"
	if post != nil {
		contentType := post.contentType
		if contentType == "" {
			contentType = "application/x-www-form-urlencoded"
		}
		r.Header.Set("Content-Type", contentType)
		if post.origin != "" {
			r.Header.Set("Origin", post.origin)
		}
		if post.referer != "" {
			r.Header.Set("Referer", post.referer)
		}
		if post.fetchSite != "" {
			r.Header.Set("Sec-Fetch-Site", post.fetchSite)
		}
	}
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func assertNoKeyInResponse(t *testing.T, w *httptest.ResponseRecorder, keys ...string) {
	t.Helper()
	text := w.Body.String()
	for name, values := range w.Header() {
		text += name + ":" + strings.Join(values, ",")
	}
	for _, key := range keys {
		if strings.Contains(text, key) || strings.Contains(text, key[len(key)-4:]) {
			t.Fatalf("response echoed key material: %s", text)
		}
	}
}

// Requirement: an AI on Swarm Control can list providers (read) and ask for a
// provider key link (manage, owner-only like other credential tools); read and
// write AI keys never see the link tool, a connection without admin cannot get
// a link, and relay clients are never offered it. Threat: a lower grant
// steering which provider account every agent runs on. Owners: controlMCPTools,
// remote.ToolScope/localOnlyTools, controlMCPRequestProviderKey. In-process MCP
// through ContainerSDKHandler with real token stores is the narrowest layer
// running the level filter, scope check and tool together.
func TestControlMCPProviderKeyToolScopes(t *testing.T) {
	if remote.ToolScope("swarm_list_providers") != remote.ScopeRead || remote.ToolScope("swarm_request_provider_key") != remote.ScopeManage {
		t.Fatalf("scopes: list %s, request %s", remote.ToolScope("swarm_list_providers"), remote.ToolScope("swarm_request_provider_key"))
	}
	registered := map[string]bool{}
	for _, tool := range controlMCPTools() {
		registered[tool.Name] = true
	}
	if !registered["swarm_list_providers"] || !registered["swarm_request_provider_key"] {
		t.Fatal("provider key tools are not registered")
	}
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	s.providers = registry.New(providerKeyTestAdapter{id: "fakeai", takesKey: true})
	s.SetProviderKeyPageAddr(providerKeyTestAddr)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	mint := func(scopes []string) string {
		t.Helper()
		token, _, err := sec.CreateScopedToken("k", scopes, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	for _, level := range []string{"read", "write"} {
		scopes, _ := aiKeyScopes(level)
		call := controlMCPTestCaller(t, s, mint(scopes))
		if text, isErr := call("swarm_request_provider_key", map[string]any{"provider": "fakeai"}); !isErr || !strings.Contains(text, "swarm:manage") {
			t.Fatalf("%s key got a link: %s", level, text)
		}
		if text, isErr := call("swarm_list_providers", map[string]any{}); isErr {
			t.Fatalf("%s key cannot list providers: %s", level, text)
		}
	}
	// A plain sessions token (no Swarm Control levels) lacks admin.
	call := controlMCPTestCaller(t, s, mint([]string{"sessions:read", "sessions:write"}))
	if text, isErr := call("swarm_request_provider_key", map[string]any{"provider": "fakeai"}); !isErr || !strings.Contains(text, "lacks the admin permission") {
		t.Fatalf("non-admin token got a link: %s", text)
	}
	if len(s.providerKeys.byCode) != 0 {
		t.Fatal("refused requests issued codes")
	}
	// The page is off when the API listener is not loopback.
	s.SetProviderKeyPageAddr("0.0.0.0:7781")
	full, _ := aiKeyScopes("full")
	if text, isErr := controlMCPTestCaller(t, s, mint(full))("swarm_request_provider_key", map[string]any{"provider": "fakeai"}); !isErr || !strings.Contains(text, "loopback API listener") {
		t.Fatalf("link without a loopback listener: %s", text)
	}
}

// Requirement: the key reaches Swarm only through its own loopback page and
// the canonical credential route. A code is 256-bit, single-use, bound to one
// provider and to the requesting account and key, and expires in 10 minutes;
// the POST needs a same-origin form with a bounded body; failures keep the key
// out of every response and tool result and store nothing; a saved key is
// active and verified and list_providers reports the outcome. Threats: a key
// passing through the AI, cross-site form posts, replayed or expired links, a
// link reused for another provider, a revoked AI key still saving
// credentials, key material echoed by a provider error. Owners:
// handleProviderKeyPage, submitProviderKey, saveProviderKey,
// providerKeyRegistry, controlMCPListProviders and POST /v1/auth/credentials.
// In-process requests through LoopbackAPIHandler and ContainerSDKHandler with
// real temporary stores and a fake verifying provider are the narrowest layer
// running page, registry, route chain and storage together; no provider is
// contacted.
func TestProviderKeyPageFlow(t *testing.T) {
	f := setupProviderKeyFixture(t)
	page := f.s.LoopbackAPIHandler()
	stored := func() []string {
		t.Helper()
		list, err := f.s.auth.ListCredentialsForAccount(f.account, "fakeai", "", 50)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, record := range list.Records {
			out = append(out, record.Provider)
		}
		return out
	}
	origin := "http://" + providerKeyTestAddr

	// Only providers that take an API key.
	for _, provider := range []string{"signinonly", "nosuch"} {
		if text, isErr := f.call("swarm_request_provider_key", map[string]any{"provider": provider}); !isErr || !strings.Contains(text, "fakeai") {
			t.Fatalf("link for %s: %s", provider, text)
		}
	}
	code := f.requestLink(t, "fakeai")
	if status := f.providers(t)["fakeai"]["key_request"].(map[string]any)["status"]; status != "waiting" {
		t.Fatalf("key_request status = %v", status)
	}

	// GET: a minimal self-contained form with strict headers.
	w := providerKeyHit(page, http.MethodGet, code, nil, nil)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `type="password"`) || !strings.Contains(body, `autocomplete="off"`) || !strings.Contains(body, "fakeai") ||
		strings.Contains(body, "<script") || strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Fatalf("form page: %d %s", w.Code, body)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "form-action 'self'") ||
		w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", w.Header())
	}

	// Cross-origin, originless, cross-site and oversized posts are refused
	// before the code is touched.
	good := url.Values{"api_key": {providerKeyTestGood}, "provider": {"signinonly"}}
	for name, post := range map[string]providerKeyPost{
		"foreign origin":   {origin: "http://evil.example", form: good},
		"null origin":      {origin: "null", form: good},
		"other port":       {origin: "http://127.0.0.1:9999", form: good},
		"no origin":        {form: good},
		"foreign referer":  {referer: "http://evil.example/keys/" + code, form: good},
		"cross-site fetch": {origin: origin, fetchSite: "cross-site", form: good},
	} {
		post := post
		if w := providerKeyHit(page, http.MethodPost, code, &post, nil); w.Code != http.StatusForbidden {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := providerKeyHit(page, http.MethodPost, code, &providerKeyPost{origin: origin, contentType: "application/json", body: `{"api_key":"` + providerKeyTestGood + `"}`}, nil); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("json post: %d", w.Code)
	}
	if w := providerKeyHit(page, http.MethodPost, code, &providerKeyPost{origin: origin, body: "api_key=" + strings.Repeat("a", providerKeyMaxBodyBytes+1)}, nil); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized post: %d", w.Code)
	}
	if got := stored(); len(got) != 0 {
		t.Fatalf("refused posts stored %v", got)
	}

	// A key the provider rejects: nothing stored, reason shown without the
	// key, link still usable.
	w = providerKeyHit(page, http.MethodPost, code, &providerKeyPost{origin: origin, form: url.Values{"api_key": {providerKeyTestBad}}}, nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Incorrect API key provided") || !strings.Contains(w.Body.String(), `type="password"`) {
		t.Fatalf("bad key: %d %s", w.Code, w.Body.String())
	}
	assertNoKeyInResponse(t, w, providerKeyTestBad)
	if got := stored(); len(got) != 0 {
		t.Fatalf("rejected key stored %v", got)
	}
	request := f.providers(t)["fakeai"]["key_request"].(map[string]any)
	if request["status"] != "failed" || !strings.Contains(request["reason"].(string), "Incorrect API key provided") {
		t.Fatalf("failed outcome = %v", request)
	}

	// The good key, with a Referer instead of Origin and a stray provider
	// field: saved for the bound provider only, active and verified.
	w = providerKeyHit(page, http.MethodPost, code, &providerKeyPost{referer: origin + providerKeyPagePrefix + code, fetchSite: "same-origin", form: good}, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Saved") || !strings.Contains(w.Body.String(), "verified") {
		t.Fatalf("good key: %d %s", w.Code, w.Body.String())
	}
	assertNoKeyInResponse(t, w, providerKeyTestGood)
	if got := stored(); len(got) != 1 || got[0] != "fakeai" {
		t.Fatalf("stored %v, want one fakeai credential", got)
	}
	entry := f.providers(t)["fakeai"]
	active, _ := entry["active"].(map[string]any)
	if entry["stored_credentials"] != float64(1) || active["verification"] != "verified" || entry["key_request"].(map[string]any)["status"] != "saved" {
		t.Fatalf("after save: %v", entry)
	}

	// Single use: the code is consumed.
	if w := providerKeyHit(page, http.MethodGet, code, nil, nil); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), `type="password"`) {
		t.Fatalf("reused code GET: %d", w.Code)
	}
	if w := providerKeyHit(page, http.MethodPost, code, &providerKeyPost{origin: origin, form: good}, nil); w.Code != http.StatusNotFound {
		t.Fatalf("reused code POST: %d", w.Code)
	}

	// Expiry: a new link is dead after 10 minutes.
	expiring := f.requestLink(t, "fakeai")
	*f.now = f.now.Add(providerKeyCodeTTL + time.Second)
	if w := providerKeyHit(page, http.MethodGet, expiring, nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("expired GET: %d", w.Code)
	}
	if w := providerKeyHit(page, http.MethodPost, expiring, &providerKeyPost{origin: origin, form: good}, nil); w.Code != http.StatusNotFound {
		t.Fatalf("expired POST: %d", w.Code)
	}
	if status := f.providers(t)["fakeai"]["key_request"].(map[string]any)["status"]; status != "expired" {
		t.Fatalf("expired status = %v", status)
	}

	// Unknown codes get the same neutral page.
	unknown := strings.Repeat("A", 43)
	if w := providerKeyHit(page, http.MethodGet, unknown, nil, nil); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), providerKeyInvalidLink) {
		t.Fatalf("unknown code: %d", w.Code)
	}

	// A link outlives nothing: once the AI key that asked is revoked, the
	// page saves nothing.
	revoked := f.requestLink(t, "fakeai")
	f.revoke()
	w = providerKeyHit(page, http.MethodPost, revoked, &providerKeyPost{origin: origin, form: good}, nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "revoked") {
		t.Fatalf("revoked requester: %d %s", w.Code, w.Body.String())
	}
	assertNoKeyInResponse(t, w, providerKeyTestGood)
	if got := stored(); len(got) != 1 {
		t.Fatalf("revoked requester stored a credential: %v", got)
	}
}

// Requirement: the key page exists only on the loopback API listener and only
// for direct loopback requests naming that listener. Threat: the page (and so
// a provider-credential write) reachable through the tailnet gateway, the
// scoped-token container listener, the relay, peer transport, the private
// socket or Desktop, or through a DNS-rebinding host name or a reverse proxy
// on loopback. Owners: LoopbackAPIHandler (only daemon.go's API listener uses
// it), handleProviderKeyPage and providerKeyLoopbackRequest. Serving a live
// code through every listener's handler in process is the narrowest layer
// that proves the route split.
func TestProviderKeyPageOnlyOnLoopbackListener(t *testing.T) {
	f := setupProviderKeyFixture(t)
	code := f.requestLink(t, "fakeai")
	isForm := func(w *httptest.ResponseRecorder) bool {
		return w.Code == http.StatusOK && strings.Contains(w.Body.String(), `name="api_key"`)
	}
	if w := providerKeyHit(f.s.LoopbackAPIHandler(), http.MethodGet, code, nil, nil); !isForm(w) {
		t.Fatalf("loopback listener does not serve the page: %d", w.Code)
	}
	for name, handler := range map[string]http.Handler{
		"peer transport (Handler)":       f.s.Handler(),
		"private socket":                 f.s.LocalTransportHandler(),
		"desktop":                        f.s.DesktopHandler(),
		"scoped-token container / relay": f.s.ContainerSDKHandler(),
		"tailnet gateway":                f.s.TailnetGatewayHandler(),
	} {
		for _, mutate := range []func(*http.Request){
			nil,
			func(r *http.Request) {
				r.Header.Set(TailscaleAppCapsHeader, `{"swarmagent.dev/cap/swarm":[{"level":"write"}]}`)
			},
		} {
			if w := providerKeyHit(handler, http.MethodGet, code, nil, mutate); isForm(w) {
				t.Fatalf("%s serves the key page", name)
			}
		}
	}
	for name, mutate := range map[string]func(*http.Request){
		"remote peer":     func(r *http.Request) { r.RemoteAddr = "100.64.0.7:41000" },
		"rebinding host":  func(r *http.Request) { r.Host = "attacker.example:7781" },
		"other port host": func(r *http.Request) { r.Host = "127.0.0.1:9999" },
		"proxied":         func(r *http.Request) { r.Header.Set("X-Forwarded-For", "100.64.0.7") },
		"tailscale serve": func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "someone@example.com") },
	} {
		if w := providerKeyHit(f.s.LoopbackAPIHandler(), http.MethodGet, code, nil, mutate); isForm(w) || w.Code != http.StatusNotFound {
			t.Fatalf("%s reached the page: %d", name, w.Code)
		}
	}
	// The refused requests did not consume or alter the code.
	if w := providerKeyHit(f.s.LoopbackAPIHandler(), http.MethodGet, code, nil, nil); !isForm(w) {
		t.Fatalf("code lost after refused requests: %d", w.Code)
	}
}

// Requirement: provider messages shown to the person or the AI never carry
// key material, whole or partly masked, while plain text and links survive.
// Owner: providerKeyRedact. A pure function test is the narrowest layer.
func TestProviderKeyRedact(t *testing.T) {
	key := "fake-test-key-abcdef0123456789"
	got := providerKeyRedact("Incorrect API key provided: "+key+" and fake-t****6789. See https://example.invalid/account/api-keys.", key)
	if strings.Contains(got, key) || strings.Contains(got, "6789") || !strings.Contains(got, "Incorrect API key provided") || !strings.Contains(got, "https://example.invalid/account/api-keys") {
		t.Fatalf("redacted = %q", got)
	}
}
