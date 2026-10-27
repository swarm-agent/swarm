package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: handleAccountPermissions must expose stored values and device live
// state while rejecting cross-device and stale writes without changing state.
// Authenticated HTTP requests are the narrowest layer testing actor identity
// together with the persistence boundary, rather than trusting supplied ids.
func TestAccountPermissionsHTTP(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	mint := func(name string) (string, string) {
		w := httptest.NewRecorder()
		r := requestWithTestPrincipalForAccount(httptest.NewRequest("POST", clientSessionsPath, strings.NewReader(`{"name":"console","device":"`+name+`"}`)), actor.UserID, actor.AccountScopeID)
		s.handleClientSessions(w, markLocalTransportRequest(r))
		if w.Code != 201 {
			t.Fatal(w.Body.String())
		}
		var v struct {
			Token string `json:"token"`
			ID    string `json:"session_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v.Token, v.ID
	}
	token, id := mint("device-one")
	_, other := mint("device-two")
	h := s.withAuth(http.HandlerFunc(s.handleAccountPermissions))
	call := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/v3/permissions", strings.NewReader(body))
		r.Header.Set("X-Swarm-Token", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call("GET", "")
	var view permissionsView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	if view.Home.Tasks != "auto" || view.Home.WaitingTasks != 0 || len(view.Devices) != 2 {
		t.Fatalf("defaults: %+v", view)
	}
	for _, d := range view.Devices {
		if d.ID == id && (!d.Connected || !d.ThisDevice || d.Files != "off") {
			t.Fatalf("device: %+v", d)
		}
	}
	if strings.Contains(w.Body.String(), "browser_attached") {
		t.Fatal("unknown browser attachment claimed")
	}
	w = call("PATCH", `{"expected_revision":0,"client_request_id":"other","devices":{"`+other+`":{"files":"allowed"}}}`)
	if w.Code != 403 {
		t.Fatalf("cross-device: %d %s", w.Code, w.Body.String())
	}
	w = call("PATCH", `{"expected_revision":0,"client_request_id":"own","devices":{"`+id+`":{"files":"ask"}}}`)
	if w.Code != 200 {
		t.Fatalf("own: %d %s", w.Code, w.Body.String())
	}
	w = call("PATCH", `{"expected_revision":0,"client_request_id":"stale","tasks":"auto"}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"current"`) {
		t.Fatalf("stale: %d %s", w.Code, w.Body.String())
	}
	w = call("GET", "")
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	events, err := s.sessions.Store().ListV3SessionEvents("__account_permissions__:"+actor.AccountScopeID, 0, 10)
	if err != nil || len(events) != 1 || events[0].EventType != "permissions.updated" {
		t.Fatalf("realtime event: %+v %v", events, err)
	}
	if view.Revision != 1 || view.Home.Tasks != "auto" {
		t.Fatalf("failed writes changed state: %+v", view)
	}
	for _, d := range view.Devices {
		if d.ID == id && (d.Files != "ask" || d.Changed.By != "device-one" || d.Changed.At == 0) {
			t.Fatalf("stored rule: %+v", d)
		}
	}
}

// Purpose: the HTTP authority boundary must distinguish an approving AI key
// from a read key or agent gateway. Rejections must leave the record unchanged.
func TestAccountPermissionsKeyAuthority(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		scopes []string
		agent  string
		want   int
	}{
		{"read", []string{"swarm:read"}, "", 403},
		{"gateway", []string{"swarm:approve"}, "agent", 403},
		{"approve", []string{"swarm:approve"}, "", 200},
	} {
		r := requestWithTestPrincipalForAccount(httptest.NewRequest("PATCH", "/v3/permissions", strings.NewReader(`{"expected_revision":0,"client_request_id":"key","tasks":"approve"}`)), actor.UserID, actor.AccountScopeID)
		r = requestWithScopedToken(r, &store.ScopedTokenRecord{Name: tc.name, Scopes: tc.scopes, AgentName: tc.agent})
		w := httptest.NewRecorder()
		s.handleAccountPermissions(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
		}
		p, err := s.sessions.Store().GetAccountPermissions(actor.AccountScopeID)
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == 403 && (p.Revision != 0 || p.Tasks != "auto") {
			t.Fatalf("refusal mutated record: %+v", p)
		}
		if tc.want == 200 && (p.Revision != 1 || p.Tasks != "approve") {
			t.Fatalf("approval not stored: %+v", p)
		}
	}
}

// Purpose: the registered endpoint must join only account-owned fleet links
// and expose their recorded reachability, not probe or invent remote health.
func TestAccountPermissionsLinkedLiveState(t *testing.T) {
	home := newFleetTestSwarm(t, "home")
	defer home.cleanup()
	remote := newFleetTestSwarm(t, "remote")
	defer remote.cleanup()
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "box"})
	if code, out := home.call(t, "POST", "/v3/fleet/machines", string(body), false); code != 201 {
		t.Fatalf("link: %d %v", code, out)
	}
	r := requestWithTestPrincipalForAccount(httptest.NewRequest("GET", "/v3/permissions", nil), home.user, home.account)
	w := httptest.NewRecorder()
	home.s.handleAccountPermissions(w, r)
	var v permissionsView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || w.Code != 200 || len(v.Linked) != 1 {
		t.Fatalf("view: %d %s", w.Code, w.Body.String())
	}
	if !v.Linked[0].Reachable || v.Linked[0].LastContact == 0 || v.Linked[0].Work != "ask" {
		t.Fatalf("link state: %+v", v.Linked)
	}
	r = requestWithTestPrincipalForAccount(httptest.NewRequest("GET", "/v3/permissions", nil), home.user, "other-account")
	w = httptest.NewRecorder()
	home.s.handleAccountPermissions(w, r)
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || len(v.Linked) != 0 {
		t.Fatalf("account leak: %s", w.Body.String())
	}
}
