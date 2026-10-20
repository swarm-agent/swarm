package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// Purpose: a frontend can discover Swarm on the AI gateway before it has a
// key, and follow the activity feed with its key, without either path widening
// what keys or keyless callers reach.
// Threat: discovery leaking who owns the machine or anything secret; the feed
// exception letting an AI key (whose levels are enforced per tool in /mcp)
// reach raw session, settings or write routes; a keyless tailnet caller
// reaching anything but /mcp and discovery.
// Boundary: isHelloRequest/serveHello and aiKeyFeedRoute in ContainerSDKHandler
// and TailnetGatewayHandler.
func TestFrontendGatewayDiscoveryAndFeed(t *testing.T) {
	f := newGatewayTestFixture(t)
	tailnet := f.server.TailnetGatewayHandler()

	keyless := func(h http.Handler, method, path string, header map[string]string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "127.0.0.1:50000"
		for k, v := range header {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// Discovery answers without a key on both gateways, with exactly the
	// documented fields and no identity.
	for name, h := range map[string]http.Handler{"sdk": f.handler, "tailnet": tailnet} {
		code, out := keyless(h, http.MethodGet, "/v3/hello", nil)
		if code != http.StatusOK {
			t.Fatalf("%s hello: %d %v", name, code, out)
		}
		var keys []string
		for k := range out {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if got := strings.Join(keys, ","); got != "api,auth,commit,control,features,owner,product,version" {
			t.Fatalf("%s hello fields: %s", name, got)
		}
		if out["product"] != "swarm" || out["owner"] != true {
			t.Fatalf("%s hello: %v", name, out)
		}
		body, _ := json.Marshal(out)
		for _, leak := range []string{f.user, f.account, "swk_x"} {
			if leak != "" && strings.Contains(string(body), leak) {
				t.Fatalf("%s hello leaks %q: %s", name, leak, body)
			}
		}
		if code, _ := keyless(h, http.MethodPost, "/v3/hello", nil); code == http.StatusOK {
			t.Fatalf("%s: POST /v3/hello answered without a key", name)
		}
	}

	// Keyless callers still reach nothing else: not the feed, not sessions.
	for _, path := range []string{"/v3/activity", "/v3/activity/stream", "/v3/sessions"} {
		if code, _ := keyless(f.handler, http.MethodGet, path, nil); code != http.StatusUnauthorized {
			t.Fatalf("keyless sdk GET %s: %d, want 401", path, code)
		}
		if code, _ := keyless(tailnet, http.MethodGet, path, map[string]string{TailscaleAppCapsHeader: `{"swarmagent.dev/cap/swarm":[{"level":"write"}]}`}); code != http.StatusForbidden {
			t.Fatalf("keyless tailnet GET %s: %d, want 403", path, code)
		}
	}

	// AI keys of every level read the feed and still reach no other raw route.
	for _, level := range []string{"read", "write", "full"} {
		key := f.mint(t, `{"name":"ai-`+level+`","ai_access":"`+level+`","expires_in_seconds":3600}`)
		if code, out := f.do(t, key, http.MethodGet, "/v3/activity?cursor=0", ""); code != http.StatusOK {
			t.Fatalf("%s AI key reading the feed: %d %v", level, code, out)
		}
		for _, tc := range []struct{ method, path, body string }{
			{http.MethodPost, "/v3/activity", `{}`},
			{http.MethodGet, "/v3/sessions", ""},
			{http.MethodPost, "/v3/sessions", `{"title":"x"}`},
			{http.MethodGet, "/v3/projects", ""},
			{http.MethodGet, "/v3/box/summary", ""},
			{http.MethodGet, "/v1/agent-model-settings", ""},
		} {
			if code, out := f.do(t, key, tc.method, tc.path, tc.body); code != http.StatusForbidden {
				t.Fatalf("%s AI key %s %s: %d %v, want 403", level, tc.method, tc.path, code, out)
			}
		}
	}

	// A client app key (session routes only) does not gain the feed.
	app := f.mint(t, `{"name":"app","scopes":["sessions:read","sessions:write","client:app"],"expires_in_seconds":3600}`)
	if code, out := f.do(t, app, http.MethodGet, "/v3/activity?cursor=0", ""); code != http.StatusForbidden {
		t.Fatalf("client app key reading the feed: %d %v, want 403", code, out)
	}
}
