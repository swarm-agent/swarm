package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/config"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: RunnerGatewayHandler must expose only runner work, never owner or
// Swarm Control authority, regardless of capability headers. Real token storage
// and the HTTP boundary prove authentication and dispatch without a live daemon.
func TestRunnerGatewayBoundary(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth := pebblestore.NewClientAuthStore(db)
	sec := security.NewService(auth, nil)
	join, err := sec.CreateRunnerJoinToken("account", "owner", "runner")
	if err != nil {
		t.Fatal(err)
	}
	token, rec, err := sec.JoinRunner(join)
	if err != nil {
		t.Fatal(err)
	}
	ai, _, err := sec.CreateScopedToken("ai", []string{"swarm:write"}, "account", "owner", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sec.CreateScopedToken("owner", []string{"*"}, "account", "owner", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{security: sec}
	h := s.RunnerGatewayHandler()
	call := func(method, path, credential, body string, want int, text string) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		r.Header.Set("Tailscale-App-Capabilities", `{"swarmagent.dev/cap/swarm":[{"level":"write"}]}`)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want || !strings.Contains(w.Body.String(), text) {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
	}
	routes := []struct{ method, path string }{{"POST", "/v3/runners/claim"}, {"PUT", "/v3/runners/status"}, {"PUT", "/v3/runners/landings/job/heartbeat"}, {"PUT", "/v3/runners/landings/job/output"}, {"PUT", "/v3/runners/landings/job/result"}, {"GET", "/v3/runners/landings/job/payload"}, {"GET", "/v3/runners/landings/job/bundle"}}
	for _, route := range routes {
		call(route.method, route.path, token, "{}", 503, "runner store unavailable")
		for _, key := range []string{ai, owner, "owner-attach-token"} {
			call(route.method, route.path, key, "{}", 403, "runner credential required")
		}
		call(route.method, route.path, "", "{}", 401, "")
	}
	for _, route := range []struct{ method, path string }{{"POST", "/v3/runners/join-tokens"}, {"GET", "/v3/runners"}, {"DELETE", "/v3/runners/runner"}, {"GET", "/mcp"}, {"POST", "/v3/sessions"}, {"GET", "/healthz"}, {"GET", "/v3/runners/claim"}, {"PUT", "/v3/runners/landings/../result"}} {
		for _, key := range []string{token, ai, owner, ""} {
			call(route.method, route.path, key, "{}", 404, "")
		}
	}
	for _, key := range []string{ai, owner} {
		call("POST", "/v3/runners/join", key, "{}", 403, "runner credential required")
	}
	fresh, err := sec.CreateRunnerJoinToken("account", "owner", "fresh")
	if err != nil {
		t.Fatal(err)
	}
	call("POST", "/v3/runners/join", "", `{"token":"`+fresh+`"}`, 201, "credential")
	call("POST", "/v3/runners/join", "", `{"token":"`+fresh+`"}`, 401, "")
	s.runnerStore = db
	call("GET", "/v3/runners/landings/job/bundle", token, "", http.StatusGone, "retired")
	call("PUT", "/v3/runners/status", token, `{"state":"idle"}`, 200, "")
	rows, err := db.RunnerStatuses("account")
	if err != nil || len(rows) != 1 || rows[0].Name != "runner" {
		t.Fatalf("statuses: %+v %v", rows, err)
	}
	if _, err := auth.RevokeScopedToken(rec.AccountScopeID, rec.ID); err != nil {
		t.Fatal(err)
	}
	call("PUT", "/v3/runners/status", token, `{"state":"unhealthy"}`, 401, "")
	rows, err = db.RunnerStatuses("account")
	if err != nil || len(rows) != 1 || rows[0].State != "idle" {
		t.Fatalf("unauthorized mutation: %+v %v", rows, err)
	}
}

// Purpose: config.Parse is the startup authority; reject invalid and colliding
// gateway ports before listeners start. Kept here to respect the file scope.
func TestRunnerGatewayConfig(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	dir := t.TempDir()
	cfg := startupconfig.Default(filepath.Join(dir, "swarm.conf"))
	cfg.Port = 7781
	cfg.DesktopPort = 7782
	cfg.PeerTransportPort = 7785
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIGURATION_DIRECTORY", dir)
	for _, port := range []string{"-1", "65536", "7781", "7782", "7785"} {
		if _, err := config.Parse([]string{"--runner-gateway-port=" + port}); err == nil || !strings.Contains(err.Error(), "runner-gateway-port") {
			t.Fatalf("accepted %s", port)
		}
	}
	for _, port := range []string{"0", "1", "7784", "65535"} {
		if _, err := config.Parse([]string{"--runner-gateway-port=" + port}); err != nil {
			t.Fatalf("%s: %v", port, err)
		}
	}
	for _, args := range [][]string{{"--desktop-port=0", "--container-sdk-port=7784"}, {"--secrets-gateway=on", "--secrets-gateway-addr=127.0.0.1:7784"}} {
		if _, err := config.Parse(append(args, "--runner-gateway-port=7784")); err == nil || !strings.Contains(err.Error(), "runner-gateway-port") {
			t.Fatal("accepted collision")
		}
	}
}
