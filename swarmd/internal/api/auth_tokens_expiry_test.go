package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: no key an API caller mints over POST /v3/auth/tokens may live
// forever. For client/SDK keys, monitoring keys and AI keys a missing, zero or
// negative expires_in_seconds is refused with 400 (never defaulted), and so is
// one over 365 days (including a value that would overflow a duration); a
// refused mint stores nothing; 1 hour and exactly 365 days are accepted with
// that expiry. Gateway and operator tokens (30 days) are covered by
// TestAgentBoundTokenMustExpireWithinThirtyDays. Threat: a leaked key that
// never stops working. Boundary: Server.handleAuthTokens and
// requestedLifetime, with security.CreateScopedTokenUnder behind it.
// Narrowest layer: the owner-only mint handler over a temporary store.
func TestAuthTokenMintRequiresBoundedExpiry(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	account := actor.AccountScopeID
	mint := func(body string) (int, string, pebblestore.ScopedTokenRecord) {
		w := httptest.NewRecorder()
		s.handleAuthTokens(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens", strings.NewReader(body)), actor.UserID, account))
		var out struct {
			Error  string                        `json:"error"`
			Record pebblestore.ScopedTokenRecord `json:"record"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Error, out.Record
	}
	before, err := s.security.ListScopedTokens(account)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{
		"client key": `"name":"k","scopes":["sessions:read","sessions:write"]`,
		"monitor":    `"name":"m","scopes":["signals:read"]`,
		"AI key":     `"name":"ai","ai_access":"read"`,
	}
	for kind, fields := range kinds {
		for _, expiry := range []string{"", `,"expires_in_seconds":0`, `,"expires_in_seconds":-60`, `,"expires_in_seconds":31536001`, `,"expires_in_seconds":9223372036854775807`} {
			code, msg, _ := mint("{" + fields + expiry + "}")
			if code != http.StatusBadRequest || !strings.Contains(msg, "expires_in_seconds") {
				t.Fatalf("%s with %q: %d %q, want 400 naming expires_in_seconds", kind, expiry, code, msg)
			}
		}
	}
	if after, _ := s.security.ListScopedTokens(account); len(after) != len(before) {
		t.Fatalf("a refused mint stored a token: %d -> %d", len(before), len(after))
	}
	for kind, fields := range kinds {
		for _, seconds := range []int64{3600, 31536000} {
			start := time.Now()
			code, msg, record := mint("{" + fields + `,"expires_in_seconds":` + jsonInt(seconds) + "}")
			want := start.Add(time.Duration(seconds) * time.Second).UnixMilli()
			if code != http.StatusOK || record.ExpiresAt < want || record.ExpiresAt > want+60_000 {
				t.Fatalf("%s for %ds: %d %q expires_at=%d want ~%d", kind, seconds, code, msg, record.ExpiresAt, want)
			}
		}
	}
}

// Purpose: keys already stored without an expiry keep working but are
// visible: GET /v3/auth/tokens sets never_expires on exactly those records.
// Boundary: Server.handleAuthTokens list branch and security.NeverExpires.
// Narrowest layer: the list handler over a temporary store holding one
// never-expiring record (minted through the resource-bound path, as a legacy
// or trigger-worker key is stored) and one expiring key.
func TestAuthTokenListMarksNeverExpiringKeys(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	account := actor.AccountScopeID
	_, forever, err := s.security.CreateScopedToken("legacy", []string{"sessions:read"}, account, actor.UserID, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, expiring, err := s.security.CreateScopedTokenUnder(nil, "current", []string{"sessions:read"}, account, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleAuthTokens(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, "/v3/auth/tokens", nil), actor.UserID, account))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Tokens []map[string]any `json:"tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	seen := map[string]any{}
	for _, token := range out.Tokens {
		seen[token["id"].(string)] = token["never_expires"]
		if token["id"] == forever.ID && token["name"] != "legacy" {
			t.Fatalf("listing lost record fields: %v", token)
		}
	}
	if seen[forever.ID] != true {
		t.Fatalf("never-expiring key not marked: %v", seen[forever.ID])
	}
	if v, present := seen[expiring.ID]; !present || v != nil {
		t.Fatalf("expiring key marked never_expires=%v (listed=%v)", v, present)
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
