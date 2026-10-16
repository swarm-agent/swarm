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
