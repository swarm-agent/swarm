package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Requirement: the AI that runs a box sees and controls every key through
// Swarm Control: swarm_list_keys lists each key's kind, state, last use and
// where from, and when it stops on its own, marks the calling key and never
// lists Swarm's internal keys or key material; swarm_create_key makes app and
// bot keys (never AI keys) with no lifetime unless a fixed date is asked;
// swarm_revoke_key stops any key but Swarm's internal ones. Threats: an AI that
// cannot see or kill a leaked key; key material or hashes leaking into a tool
// result; the AI revoking Swarm's own transport key. Owners:
// controlMCPListKeys, controlMCPCreateKey, controlMCPRevokeKey over the real
// mint handler and security service on a temporary store.
func TestControlMCPKeysListCreateRevoke(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	account, user := actor.AccountScopeID, actor.UserID
	fullScopes, _ := aiKeyScopes("full")
	_, caller, err := s.security.CreateScopedTokenUnder(nil, "local-ai-roy", fullScopes, account, user, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, internal, err := s.security.CreateInternalToken("swarm-remote pc", []string{"sessions:read"}, account, user)
	if err != nil {
		t.Fatal(err)
	}
	call := func(name string, args map[string]any) (map[string]any, string, bool) {
		t.Helper()
		req := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp", nil), user, account), &caller)
		c := &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}
		raw, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
		result, rpcErr := c.callTool(raw)
		if rpcErr != nil {
			t.Fatalf("%s: %v", name, rpcErr)
		}
		content, _ := result["content"].([]map[string]any)
		text := ""
		if len(content) > 0 {
			text, _ = content[0]["text"].(string)
		}
		isError, _ := result["isError"].(bool)
		out := map[string]any{}
		if !isError {
			if err := json.Unmarshal([]byte(text), &out); err != nil {
				t.Fatalf("%s result %q: %v", name, text, err)
			}
		}
		return out, text, isError
	}

	app, text, isErr := call("swarm_create_key", map[string]any{"kind": "app", "name": "my-ui"})
	if isErr || !strings.HasPrefix(app["key"].(string), "swk_") || !strings.Contains(app["rule"].(string), "90 days unused") {
		t.Fatalf("app key: %s", text)
	}
	appRecord := app["record"].(map[string]any)
	if appRecord["key_kind"] != "app" || appRecord["lapse_reason"] != "unused" || appRecord["fixed_date"] != nil {
		t.Fatalf("app key record: %v", appRecord)
	}
	monitor, text, isErr := call("swarm_create_key", map[string]any{"kind": "app", "name": "watch", "access": "monitor", "days": 30})
	if isErr || monitor["record"].(map[string]any)["fixed_date"] != true {
		t.Fatalf("monitor key with a date: %s", text)
	}
	for _, args := range []map[string]any{
		{"kind": "ai", "name": "x"},
		{"kind": "bot", "name": "x"},
		{"kind": "app", "name": "x", "access": "everything"},
		{"kind": "app", "name": "x", "days": 400},
	} {
		if _, text, isErr := call("swarm_create_key", args); !isErr {
			t.Fatalf("%v was accepted: %s", args, text)
		}
	}

	listed, text, isErr := call("swarm_list_keys", nil)
	if isErr {
		t.Fatalf("list: %s", text)
	}
	if strings.Contains(text, "token_hash") || strings.Contains(text, "swk_") {
		t.Fatalf("list leaks key material: %s", text)
	}
	byID := map[string]map[string]any{}
	for _, raw := range listed["keys"].([]any) {
		key := raw.(map[string]any)
		byID[key["id"].(string)] = key
	}
	if _, shown := byID[internal.ID]; shown {
		t.Fatal("Swarm's internal key was listed")
	}
	self := byID[caller.ID]
	if self["this_key"] != true || self["kind"] != "ai" || strings.Join(toStrings(self["options"]), ",") != "full" || self["state"] != "active" {
		t.Fatalf("calling key listed as %v", self)
	}
	if !strings.Contains(self["stops_because"].(string), "90 days unused") || self["last_used_from"] != nil {
		t.Fatalf("calling key lapse: %v", self)
	}
	appID := appRecord["id"].(string)
	if byID[appID]["kind"] != "app" || len(byID) != 3 {
		t.Fatalf("listed %v", byID)
	}

	if _, text, isErr := call("swarm_revoke_key", map[string]any{"key_id": internal.ID}); !isErr || !strings.Contains(text, "Swarm's own keys") {
		t.Fatalf("revoking the internal key: %s", text)
	}
	if _, text, isErr := call("swarm_revoke_key", map[string]any{"key_id": "tok_missing"}); !isErr || !strings.Contains(text, "swarm_list_keys") {
		t.Fatalf("revoking an unknown key: %s", text)
	}
	revoked, text, isErr := call("swarm_revoke_key", map[string]any{"key_id": appID})
	if isErr || revoked["revoked"] != true || revoked["key"].(map[string]any)["state"] != "revoked" {
		t.Fatalf("revoke: %s", text)
	}
	stored, err := s.security.ListScopedTokens(account)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range stored {
		if r.ID == appID && !r.Revoked {
			t.Fatal("the key was not revoked in the store")
		}
	}
	listed, _, _ = call("swarm_list_keys", nil)
	for _, raw := range listed["keys"].([]any) {
		if raw.(map[string]any)["id"] == appID {
			t.Fatal("a revoked key is still listed by default")
		}
	}
	listed, _, _ = call("swarm_list_keys", map[string]any{"include_revoked": true})
	found := false
	for _, raw := range listed["keys"].([]any) {
		found = found || raw.(map[string]any)["id"] == appID
	}
	if !found {
		t.Fatal("include_revoked did not list the revoked key")
	}
}

func toStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

// Requirement: a key's "used from" names the place, not the connection: the
// local socket and local processes are "this machine", a person's device
// through Tailscale Serve is its login, anything else its network (IPv4 /24,
// IPv6 /48). Forwarding headers count only from loopback, so a remote caller
// cannot pose as another place. Owner: keySource.
func TestKeySourceNamesThePlaceAKeyIsUsedFrom(t *testing.T) {
	req := func(remote string, headers map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/v3/sessions", nil)
		r.RemoteAddr = remote
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		return r
	}
	local := req("", nil).WithContext(context.WithValue(context.Background(), localTransportAuthEnabledKey, true))
	cases := []struct {
		r    *http.Request
		want string
	}{
		{local, "this machine"},
		{req("127.0.0.1:5555", nil), "this machine"},
		{req("127.0.0.1:5555", map[string]string{"Tailscale-User-Login": "roy@example.com"}), "tailnet roy@example.com"},
		{req("[::1]:5555", map[string]string{"X-Forwarded-For": "203.0.113.9, 10.0.0.1"}), "203.0.113.0/24"},
		{req("198.51.100.7:4000", map[string]string{"X-Forwarded-For": "203.0.113.9", "Tailscale-User-Login": "spoof"}), "198.51.100.0/24"},
		{req("[2001:db8:1:2::5]:4000", nil), "2001:db8:1::/48"},
		{req("not-an-address", nil), ""},
	}
	for i, c := range cases {
		if got := keySource(c.r); got != c.want {
			t.Fatalf("case %d: %q, want %q", i, got, c.want)
		}
	}
}

// Requirement: the box summary (swarm_status, the monitor SDK) shows keys
// about to stop working and keys used from a new place today, and each raises
// attention, so the AI hears about them without asking. Owner: boxKeys.
func TestBoxSummaryRaisesKeysStoppingSoonAndNewPlaces(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	account := actor.AccountScopeID
	now := time.Now()
	reasons := []string{}
	raise := func(_, reason string) { reasons = append(reasons, reason) }
	if keys := s.boxKeys(account, now, raise); len(reasons) != 0 || len(keys["stopping_soon"].([]map[string]any)) != 0 {
		t.Fatalf("a box with no keys raised %v %v", reasons, keys)
	}
	_, record, err := s.security.CreateScopedTokenUnder(nil, "laptop", []string{"sessions:read"}, account, actor.UserID, 3*24*time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, fresh, err := s.security.CreateScopedTokenUnder(nil, "bot-server", []string{"sessions:read"}, account, actor.UserID, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{"this machine", "203.0.113.0/24"} {
		if _, err := s.security.ValidateScopedTokenFrom(raw, from); err != nil {
			t.Fatal(err)
		}
	}
	keys := s.boxKeys(account, now, raise)
	stopping := keys["stopping_soon"].([]map[string]any)
	newPlaces := keys["new_places"].([]map[string]any)
	if len(stopping) != 1 || stopping[0]["id"] != record.ID || stopping[0]["because"] != "date" {
		t.Fatalf("stopping_soon = %v", stopping)
	}
	if len(newPlaces) != 1 || newPlaces[0]["id"] != fresh.ID || newPlaces[0]["from"] != "203.0.113.0/24" {
		t.Fatalf("new_places = %v", newPlaces)
	}
	if keys["active"].(map[string]int)["app"] != 2 || len(reasons) != 2 {
		t.Fatalf("active %v reasons %v", keys["active"], reasons)
	}
}
