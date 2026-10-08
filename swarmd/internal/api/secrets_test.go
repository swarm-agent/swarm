package api

// Purpose: the secrets API is the machine owner's only way to create a slot,
// set its sealed value, and grant it to a workspace. These tests pin the
// boundary: an owner principal can create/set/grant and list; the value is
// never returned by any route; and a scoped token (what every agent and AI key
// carries) is refused on every method. Handler level over a real store, since
// the boundary is the HTTP contract plus ScopedTokenFromRequest.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func newSecretsTestServer(t *testing.T) (*Server, identity.Principal) {
	t.Helper()
	dir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(dir, "main.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	secretStore, err := pebblestore.Open(filepath.Join(dir, "secrets.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close(); _ = secretStore.Close() })
	s := &Server{
		secretSlots:  pebblestore.NewSecretSlotStore(store),
		secretValues: pebblestore.NewAuthStoreWithSecretStore(store, secretStore),
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u", AccountScopeID: "acct", AccountScopeSource: identity.AccountScopeSourceServerState}
	return s, principal
}

func ownerReq(method, path, body string, principal identity.Principal) *http.Request {
	return requestWithTestPrincipalForAccount(httptest.NewRequest(method, "http://local"+path, strings.NewReader(body)), principal.UserID, principal.AccountScopeID)
}

func TestSecretsOwnerLifecycleAndValueNeverReturned(t *testing.T) {
	s, principal := newSecretsTestServer(t)
	do := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleSecrets(rec, r)
		return rec
	}

	if rec := do(ownerReq(http.MethodPost, "/v1/secrets", `{"name":"STRIPE_KEY","description":"billing","hosts":["api.stripe.com"]}`, principal)); rec.Code != http.StatusOK {
		t.Fatalf("create slot: %d %s", rec.Code, rec.Body)
	}
	if rec := do(ownerReq(http.MethodPut, "/v1/secrets/STRIPE_KEY/value", "sk_live_SECRET\n", principal)); rec.Code != http.StatusOK {
		t.Fatalf("set value: %d %s", rec.Code, rec.Body)
	}
	if rec := do(ownerReq(http.MethodPost, "/v1/secrets/STRIPE_KEY/grants", `{"workspace_path":"/projects/app","expires_seconds":3600}`, principal)); rec.Code != http.StatusOK {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body)
	}

	// No route returns the value.
	for _, path := range []string{"/v1/secrets", "/v1/secrets/STRIPE_KEY", "/v1/secrets/STRIPE_KEY/grants", "/v1/secrets/uses"} {
		rec := do(ownerReq(http.MethodGet, path, "", principal))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "sk_live_SECRET") {
			t.Fatalf("value leaked from %s: %s", path, rec.Body)
		}
	}

	// The slot reports it has a value, and the grant is live for its workspace.
	var listed struct {
		Secrets []pebblestore.SecretSlot `json:"secrets"`
	}
	rec := do(ownerReq(http.MethodGet, "/v1/secrets", "", principal))
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Secrets) != 1 || !listed.Secrets[0].HasValue || listed.Secrets[0].Hosts[0] != "api.stripe.com" {
		t.Fatalf("unexpected slot listing: %+v", listed.Secrets)
	}
	active, err := s.secretSlots.ActiveGrantsForWorkspace("/projects/app")
	if err != nil || len(active) != 1 || active[0].Slot.Name != "STRIPE_KEY" {
		t.Fatalf("grant not active for workspace: %+v %v", active, err)
	}
}

func TestSecretsRefuseScopedTokens(t *testing.T) {
	s, _ := newSecretsTestServer(t)
	scoped := &pebblestore.ScopedTokenRecord{}
	cases := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/secrets", ""},
		{http.MethodPost, "/v1/secrets", `{"name":"K","hosts":["x.example.com"]}`},
		{http.MethodPut, "/v1/secrets/K/value", "secret"},
		{http.MethodPost, "/v1/secrets/K/grants", `{"workspace_path":"/p","expires_seconds":60}`},
		{http.MethodGet, "/v1/secrets/uses", ""},
		{http.MethodDelete, "/v1/secrets/K", ""},
	}
	for _, c := range cases {
		r := requestWithScopedToken(httptest.NewRequest(c.method, "http://local"+c.path, strings.NewReader(c.body)), scoped)
		rec := httptest.NewRecorder()
		s.handleSecrets(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s with scoped token: got %d, want 403", c.method, c.path, rec.Code)
		}
	}
	// Nothing was created.
	if slots, _ := s.secretSlots.ListSlots("acct"); len(slots) != 0 {
		t.Fatalf("scoped token created slots: %+v", slots)
	}
}
