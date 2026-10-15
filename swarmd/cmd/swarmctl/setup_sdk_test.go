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
// listing ever exposing a credential. runSetupSDKTokens is the authority; the
// fixture returns records carrying a hash and hint that must not be printed.
func TestSetupSDKTokensListsIDsWithoutSecrets(t *testing.T) {
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v3/auth/tokens" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"ok":true,"tokens":[
			{"id":"tok-gw","name":"gateway","token_hash":"HASH-SECRET","token_hint":"swk_HINT","agent_name":"frontdesk","messages_per_minute":30,"sessions_per_hour":100,"max_concurrent_runs":4,"created_at":1,"expires_at":2},
			{"id":"tok-op","name":"hub","token_hash":"HASH-SECRET-2","token_hint":"swk_HINT2","agent_name":"frontdesk","agent_operator":true,"created_at":1,"expires_at":2,"revoked":true},
			{"id":"tok-sdk","name":"headless-sdk","token_hash":"HASH-SECRET-3","scopes":["sessions:read"],"created_at":1,"expires_at":2}]}`)
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
		} `json:"tokens"`
	}
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(listed.Tokens)
	want := "[{tok-gw gateway:frontdesk 4 false} {tok-op operator:frontdesk 0 true} {tok-sdk scoped 0 false}]"
	if got != want {
		t.Fatalf("listed %s, want %s", got, want)
	}
}
