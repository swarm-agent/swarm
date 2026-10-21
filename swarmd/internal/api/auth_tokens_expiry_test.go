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

// Purpose: POST /v3/auth/tokens makes every kind of key without a lifetime:
// it then works while used and stops after 90 days unused (lapses_at, reason
// "unused"); expires_in_seconds is only an optional fixed end date, the same
// for every kind, refused when negative or over 365 days (including a value
// that would overflow a duration), and a refused mint stores nothing. kind,
// when given, must match the other fields; monitor makes a status-only app
// key. Old requests without kind still work. Threat: keys in use dying on a
// date; keys of one kind posing as another. Boundary: Server.handleAuthTokens,
// requestedLifetime, checkRequestedKind. Narrowest layer: the owner-only mint
// handler over a temporary store.
func TestAuthTokenMintTakesAnOptionalFixedDate(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	account := actor.AccountScopeID
	type minted struct {
		pebblestore.ScopedTokenRecord
		KeyKind     string `json:"key_kind"`
		LapsesAt    int64  `json:"lapses_at"`
		LapseReason string `json:"lapse_reason"`
		FixedDate   bool   `json:"fixed_date"`
	}
	mint := func(body string) (int, string, minted) {
		w := httptest.NewRecorder()
		s.handleAuthTokens(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens", strings.NewReader(body)), actor.UserID, account))
		var out struct {
			Error  string `json:"error"`
			Record minted `json:"record"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Error, out.Record
	}
	before, err := s.security.ListScopedTokens(account)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{
		"app":     `"name":"k","scopes":["sessions:read","sessions:write"]`,
		"monitor": `"name":"m","kind":"app","monitor":true`,
		"ai":      `"name":"ai","ai_access":"read"`,
	}
	for kind, fields := range kinds {
		for _, expiry := range []string{`,"expires_in_seconds":-60`, `,"expires_in_seconds":31536001`, `,"expires_in_seconds":9223372036854775807`} {
			code, msg, _ := mint("{" + fields + expiry + "}")
			if code != http.StatusBadRequest || !strings.Contains(msg, "expires_in_seconds") {
				t.Fatalf("%s with %q: %d %q, want 400 naming expires_in_seconds", kind, expiry, code, msg)
			}
		}
	}
	for _, body := range []string{`{"name":"x","kind":"bot","scopes":["sessions:read"]}`, `{"name":"x","kind":"ai","scopes":["sessions:read"]}`, `{"name":"x","kind":"app","ai_access":"read"}`, `{"name":"x","kind":"robot"}`, `{"name":"x","monitor":true,"scopes":["sessions:read"]}`} {
		if code, msg, _ := mint(body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %q, want 400", body, code, msg)
		}
	}
	if after, _ := s.security.ListScopedTokens(account); len(after) != len(before) {
		t.Fatalf("a refused mint stored a token: %d -> %d", len(before), len(after))
	}
	for kind, fields := range kinds {
		start := time.Now()
		for _, expiry := range []string{"", `,"expires_in_seconds":0`} {
			code, msg, record := mint("{" + fields + expiry + "}")
			wantLapse := start.Add(90 * 24 * time.Hour).UnixMilli()
			if code != http.StatusOK || record.ExpiresAt != 0 || record.FixedDate || record.LapseReason != "unused" || record.LapsesAt < wantLapse || record.LapsesAt > wantLapse+60_000 {
				t.Fatalf("%s without a date: %d %q %+v", kind, code, msg, record)
			}
			wantKind := map[string]string{"app": "app", "monitor": "app", "ai": "ai"}[kind]
			if record.KeyKind != wantKind {
				t.Fatalf("%s minted as kind %q", kind, record.KeyKind)
			}
			if kind == "monitor" && strings.Join(record.Scopes, ",") != "signals:read" {
				t.Fatalf("monitor scopes %v", record.Scopes)
			}
		}
		for _, seconds := range []int64{3600, 31536000} {
			start := time.Now()
			code, msg, record := mint("{" + fields + `,"expires_in_seconds":` + jsonInt(seconds) + "}")
			want := start.Add(time.Duration(seconds) * time.Second).UnixMilli()
			if code != http.StatusOK || !record.FixedDate || record.ExpiresAt < want || record.ExpiresAt > want+60_000 {
				t.Fatalf("%s with a %ds date: %d %q %+v", kind, seconds, code, msg, record)
			}
		}
	}
}

// Purpose: GET /v3/auth/tokens says for every key what it is (key_kind),
// whether it works (state) and when it stops on its own (lapses_at,
// lapse_reason); never_expires marks only Swarm's internal keys. Boundary:
// Server.handleAuthTokens list branch, newScopedTokenListItem. Narrowest
// layer: the list handler over a temporary store.
func TestAuthTokenListSaysKindStateAndLapse(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	account := actor.AccountScopeID
	_, internal, err := s.security.CreateInternalToken("swarm-remote pc", []string{"sessions:read"}, account, actor.UserID)
	if err != nil {
		t.Fatal(err)
	}
	_, app, err := s.security.CreateScopedTokenUnder(nil, "current", []string{"sessions:read"}, account, actor.UserID, 0, "", "")
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
	seen := map[string]map[string]any{}
	for _, token := range out.Tokens {
		seen[token["id"].(string)] = token
	}
	if got := seen[internal.ID]; got["never_expires"] != true || got["key_kind"] != "internal" || got["lapses_at"] != nil {
		t.Fatalf("internal key listed as %v", got)
	}
	if got := seen[app.ID]; got["never_expires"] != nil || got["key_kind"] != "app" || got["state"] != "active" || got["lapse_reason"] != "unused" || got["name"] != "current" {
		t.Fatalf("app key listed as %v", got)
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
