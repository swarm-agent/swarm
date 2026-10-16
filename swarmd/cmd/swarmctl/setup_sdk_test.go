package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupFixture serves handler on a private Unix socket, the daemon IPC that
// runSetup talks to, and returns the socket path.
func setupFixture(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	// Unix socket names are limited to ~108 bytes; avoid test-name prefixes.
	dir, err := os.MkdirTemp("", "setup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "api.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return socket
}

// Requirement: an operator can find a token's ID to revoke it, without the
// listing ever exposing a credential, and sees which keys never expire (the
// daemon's never_expires flag, for keys stored before expiry was required). runSetupSDKTokens is the authority; the
// fixture returns records carrying a hash and hint that must not be printed.
func TestSetupSDKTokensListsIDsWithoutSecrets(t *testing.T) {
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v3/auth/tokens" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"ok":true,"tokens":[
			{"id":"tok-gw","name":"gateway","token_hash":"HASH-SECRET","token_hint":"swk_HINT","agent_name":"frontdesk","messages_per_minute":30,"sessions_per_hour":100,"max_concurrent_runs":4,"created_at":1,"expires_at":2},
			{"id":"tok-op","name":"hub","token_hash":"HASH-SECRET-2","token_hint":"swk_HINT2","agent_name":"frontdesk","agent_operator":true,"created_at":1,"expires_at":2,"revoked":true},
			{"id":"tok-sdk","name":"headless-sdk","token_hash":"HASH-SECRET-3","scopes":["sessions:read"],"created_at":1,"expires_at":2},
			{"id":"tok-old","name":"old-sdk","token_hash":"HASH-SECRET-4","scopes":["sessions:read"],"created_at":1,"expires_at":0,"never_expires":true}]}`)
	})
	var out bytes.Buffer
	if err := runSetup([]string{"sdk-tokens", "--socket", socket}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "HASH") || strings.Contains(out.String(), "HINT") {
		t.Fatalf("listing exposed a token hash or hint: %s", out.String())
	}
	var listed struct {
		Tokens []struct {
			ID, Kind          string
			MaxConcurrentRuns int  `json:"max_concurrent_runs"`
			Revoked           bool `json:"revoked"`
			NeverExpires      bool `json:"never_expires"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(listed.Tokens)
	want := "[{tok-gw gateway:frontdesk 4 false false} {tok-op operator:frontdesk 0 true false} {tok-sdk scoped 0 false false} {tok-old scoped 0 false true}]"
	if got != want {
		t.Fatalf("listed %s, want %s", got, want)
	}
}

// Requirement: the daemon refuses a key without an expiry, so swarmctl setup
// sdk-token keeps its one-hour default but always sends it explicitly, for
// every kind (session, monitor, AI and gateway keys), and refuses a lifetime
// over its kind's CLI maximum before calling the daemon. Boundary:
// runSetupSDKToken payload construction. Narrowest layer: the CLI over a
// socket fixture that records each mint request.
func TestSetupSDKTokenAlwaysSendsAnExpiry(t *testing.T) {
	var bodies []map[string]any
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		fmt.Fprint(w, `{"ok":true,"token":"swk_x","record":{"id":"tok_1"}}`)
	})
	for _, extra := range [][]string{nil, {"--monitor"}, {"--ai-access", "read"}, {"--agent", "frontdesk"}} {
		var out bytes.Buffer
		if err := runSetup(append([]string{"sdk-token", "--socket", socket}, extra...), strings.NewReader(""), &out); err != nil {
			t.Fatalf("%v: %v", extra, err)
		}
	}
	if len(bodies) != 4 {
		t.Fatalf("sent %d mint requests, want 4", len(bodies))
	}
	for i, body := range bodies {
		if body["expires_in_seconds"] != float64(3600) {
			t.Fatalf("request %d sent expires_in_seconds=%v, want the explicit 3600 default: %v", i, body["expires_in_seconds"], body)
		}
	}
	for _, args := range [][]string{
		{"--expires-in-seconds", "86401"},
		{"--agent", "frontdesk", "--expires-in-seconds", "2592001"},
		{"--monitor", "--expires-in-seconds", "31536001"},
		{"--ai-access", "read", "--expires-in-seconds", "31536001"},
		{"--expires-in-seconds", "0"},
	} {
		var out bytes.Buffer
		if err := runSetup(append([]string{"sdk-token", "--socket", socket}, args...), strings.NewReader(""), &out); err == nil {
			t.Fatalf("%v was accepted", args)
		}
	}
	if len(bodies) != 4 {
		t.Fatalf("an over-limit lifetime reached the daemon: %v", bodies[4:])
	}
}

// Requirement: the per-end-user limits are set when a gateway token is minted
// (sdk-token --end-user-*) and changed in place later (sdk-token-limits),
// which sends only the limits named, so an edit never resets the others; the
// flags are refused on non-gateway keys. runSetupSDKToken and
// runSetupSDKTokenLimits are the authority; the fixture records each body.
func TestSetupSDKTokenEndUserLimits(t *testing.T) {
	var bodies []map[string]any
	var paths []string
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies, paths = append(bodies, body), append(paths, r.Method+" "+r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/limits") {
			fmt.Fprint(w, `{"ok":true,"record":{"id":"tok-gw","agent_name":"frontdesk","token_hash":"HASH-SECRET","end_user_messages_per_minute":7}}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"token":"swk_test","record":{"id":"tok-gw"}}`)
	})
	var out bytes.Buffer
	if err := runSetup([]string{"sdk-token", "--socket", socket, "--agent", "frontdesk", "--name", "gw", "--expires-in-seconds", "3600",
		"--end-user-messages-per-minute", "10", "--end-user-sessions-per-hour", "5", "--end-user-max-tokens-per-day", "20000"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if paths[0] != "POST /v3/auth/tokens" || bodies[0]["end_user_messages_per_minute"] != float64(10) || bodies[0]["end_user_sessions_per_hour"] != float64(5) || bodies[0]["end_user_max_tokens_per_day"] != float64(20000) {
		t.Fatalf("mint sent %s %v", paths[0], bodies[0])
	}
	out.Reset()
	if err := runSetup([]string{"sdk-token-limits", "--socket", socket, "--id", "tok-gw", "--end-user-messages-per-minute", "7"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if paths[1] != "POST /v3/auth/tokens/tok-gw/limits" || len(bodies[1]) != 1 || bodies[1]["end_user_messages_per_minute"] != float64(7) {
		t.Fatalf("limits edit sent %s %v (want only the named limit)", paths[1], bodies[1])
	}
	if strings.Contains(out.String(), "HASH") {
		t.Fatalf("limits output exposed the token hash: %s", out.String())
	}
	for _, args := range [][]string{
		{"sdk-token", "--socket", socket, "--name", "gw", "--expires-in-seconds", "3600", "--end-user-messages-per-minute", "10"},
		{"sdk-token", "--socket", socket, "--agent", "frontdesk", "--operator", "--name", "hub", "--expires-in-seconds", "3600", "--end-user-sessions-per-hour", "5"},
		{"sdk-token-limits", "--socket", socket, "--id", "tok-gw"},
		{"sdk-token-limits", "--socket", socket, "--end-user-messages-per-minute", "7"},
		{"sdk-token-limits", "--socket", socket, "--id", "tok-gw", "--end-user-max-tokens-per-day", "-1"},
	} {
		if err := runSetup(args, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if len(paths) != 2 {
		t.Fatalf("a refused command reached the daemon: %v", paths)
	}
}
