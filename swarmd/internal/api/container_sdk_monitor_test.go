package api

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a headless machine is watched from outside it, so a monitor key
// (signals:read alone) must reach the machine summary and the signal feed on
// the container SDK listener, and an outside source with signals:write must be
// able to report an external signal there. Nothing else may widen: the monitor
// key still cannot read sessions or manage signal sinks, a sessions key and a
// client app key cannot read the summary or the feed, and an AI key stays on
// /mcp. Owner: ContainerSDKHandler with containerSDKMonitorRouteAllowed and the
// handlers' own scope checks, over real token and signal stores.
func TestContainerSDKMonitorRoutes(t *testing.T) {
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	feed := pebblestore.NewSignalStore(store)
	s.SetSignalStore(feed)
	s.SetSignalSinkStore(pebblestore.NewSignalSinkStore(store))
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	issue := func(scopes ...string) string {
		t.Helper()
		token, _, err := sec.CreateScopedToken("monitor-test", scopes, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	monitor := issue("signals:read")
	reporter := issue("signals:write")
	sessions := issue("sessions:read", "sessions:write")
	clientApp := issue("sessions:read", "sessions:write", clientAppScope)
	aiKey := issue("swarm:read", "sessions:read")

	handler := s.ContainerSDKHandler()
	call := func(method, path, token, body string) int {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:7783"+path, strings.NewReader(body))
		r.RemoteAddr = "192.0.2.1:40000"
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}

	for _, path := range []string{"/v3/box/summary", "/v3/signals"} {
		if code := call("GET", path, monitor, ""); code != 200 {
			t.Fatalf("monitor key GET %s = %d, want 200", path, code)
		}
		for name, token := range map[string]string{"sessions": sessions, "client app": clientApp, "ai": aiKey} {
			if code := call("GET", path, token, ""); code != 403 {
				t.Fatalf("%s key GET %s = %d, want 403", name, path, code)
			}
		}
	}
	report := `{"kind":"external.falco.alert","severity":"warning","summary":"shell in container","source":"falco"}`
	if code := call("POST", "/v3/signals", reporter, report); code != 200 {
		t.Fatalf("reporter POST /v3/signals = %d, want 200", code)
	}
	if code := call("POST", "/v3/signals", monitor, report); code != 403 {
		t.Fatalf("monitor key reported a signal: %d", code)
	}
	page, err := feed.ListAfter(0, 10, pebblestore.SignalFilter{Account: actor.AccountScopeID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Signals) != 1 || page.Signals[0].Kind != "external.falco.alert" {
		t.Fatalf("feed = %+v, want exactly the one reported signal", page.Signals)
	}

	for _, c := range []struct{ method, path string }{
		{"GET", "/v3/sessions"},
		{"GET", "/v3/signals/sinks"},
		{"POST", "/v3/signals/sinks"},
		{"POST", "/v3/box/summary"},
		{"GET", "/v1/onboarding"},
	} {
		if code := call(c.method, c.path, monitor, `{}`); code != 403 {
			t.Fatalf("monitor key %s %s = %d, want 403", c.method, c.path, code)
		}
	}
}
