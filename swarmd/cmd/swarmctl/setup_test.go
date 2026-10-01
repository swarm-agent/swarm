package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// Purpose: runSetup must dispatch only explicit setup intent to the existing
// onboarding, account-model and workspace APIs. A private Unix fixture is the
// narrow CLI/HTTP boundary, not a daemon or provider simulation/qualification.
// It detects wrong routes, accidental authority headers, defaults and secret output.
func TestSetupDispatchesCanonicalOperations(t *testing.T) {
	cases := []struct {
		name, method, route, input string
		args                       []string
		want                       map[string]any
	}{
		{"identity", "POST", "/v1/onboarding", "", []string{"--username", "owner", "--name", "headless"}, map[string]any{"username": "owner", "swarm_name": "headless"}},
		{"credential", "POST", "/v1/onboarding/provider/credential", "fixture-secret\n", []string{"--provider", "google", "--api-key-stdin"}, map[string]any{"provider": "google", "type": "api", "api_key": "fixture-secret", "active": true}},
		{"workspace", "POST", "/v1/workspace/add", "", []string{"--path", "/project", "--name", "project"}, map[string]any{"path": "/project", "name": "project", "make_current": true}},
		{"model", "PATCH", "/v1/agent-model-settings", "", []string{"--role", "action", "--provider", "google", "--model", "chosen-model", "--thinking", "low"}, map[string]any{"swarm": map[string]any{"action": map[string]any{"provider": "google", "model": "chosen-model", "thinking": "low", "service_tier": "", "context_mode": ""}}}},
		{"model", "PATCH", "/v1/agent-model-settings", "", []string{"--role", "router", "--provider", "google", "--model", "chosen-model", "--thinking", "low"}, map[string]any{"system_agents": map[string]any{"router": map[string]any{"provider": "google", "model": "chosen-model", "thinking": "low", "service_tier": "", "context_mode": ""}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name+tc.route, func(t *testing.T) {
			calls := 0
			socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.route {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("X-Swarm-Token") != "" || r.Header.Get("Cookie") != "" {
					t.Error("setup exported authentication material")
				}
				var got map[string]any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(tc.want)
				if !bytes.Equal(gotJSON, wantJSON) {
					t.Error("setup payload differs from explicit intent")
				}
				w.Header().Set("Set-Cookie", "secret=fixture-secret")
				fmt.Fprint(w, `{"ok":true,"api_key":"fixture-secret"}`)
			})
			args := append([]string{tc.name, "--socket", socket}, tc.args...)
			var output bytes.Buffer
			if err := runSetup(args, strings.NewReader(tc.input), &output); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || output.String() != "Setup operation saved.\n" {
				t.Fatalf("calls=%d unsafe or missing success output", calls)
			}
		})
	}
}

// Purpose: runSetup must reject missing intent and unsafe input before a request
// can mutate identity/settings. Real parsing plus a counting transport fixture
// proves both the negative result and zero backend calls without ambient state.
func TestSetupInvalidInputsDoNotMutate(t *testing.T) {
	calls := 0
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, `{}`) })
	for _, args := range [][]string{
		{"identity", "--username", "owner"},
		{"credential", "--provider", "google"},
		{"credential", "--provider", "google", "--api-key", "fixture-secret"},
		{"model", "--role", "action"},
		{"model", "--role", "unknown", "--provider", "google", "--model", "chosen", "--thinking", "low"},
		{"model", "--role", "action", "--provider", "google", "--model", "chosen"},
		{"workspace", "--path", "relative"},
		{"status", "unexpected"},
		{"status", "--addr", "http://example.test"},
	} {
		args = append(args[:1], append([]string{"--socket", socket}, args[1:]...)...)
		var output bytes.Buffer
		err := runSetup(args, strings.NewReader(""), &output)
		if err == nil {
			t.Errorf("accepted invalid operation %s", args[0])
		}
		if strings.Contains(fmt.Sprint(err)+output.String(), "fixture-secret") {
			t.Error("flag error leaked input")
		}
	}
	for _, input := range []string{"", strings.Repeat("k", 16385)} {
		err := runSetup([]string{"credential", "--socket", socket, "--provider", "google", "--api-key-stdin"}, strings.NewReader(input), io.Discard)
		if err == nil {
			t.Error("accepted empty or oversized key")
		}
	}
	if calls != 0 {
		t.Fatalf("invalid input made %d requests", calls)
	}
}

// Purpose: setupRequest must never expose provider errors/secret-bearing bodies
// or follow redirects. The real Unix HTTP client proves failure and zero redirect
// requests; this is a deterministic transport security test, not live setup.
func TestSetupErrorsAndRedirectsDoNotLeak(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadRequest, http.StatusTemporaryRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "http://example.test/stolen")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":"fixture-secret"}`)
			})
			var output bytes.Buffer
			err := runSetup([]string{"credential", "--socket", socket, "--provider", "google", "--api-key-stdin"}, strings.NewReader("fixture-secret"), &output)
			if err == nil || strings.Contains(err.Error()+output.String(), "fixture-secret") || output.Len() != 0 || calls != 1 {
				t.Fatal("unsafe credential failure behavior")
			}
		})
	}
}

// Purpose: completion is a separate, resumable final step. runSetup must not
// mark onboarding complete before identity, credentials, workspace and all model
// roles exist. Count writes and verify the sole completion payload at this layer.
func TestSetupCompletionPrerequisites(t *testing.T) {
	for _, missing := range []string{"identity", "credential", "workspace", "model", ""} {
		t.Run("missing-"+missing, func(t *testing.T) {
			writes := 0
			socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					writes++
					var payload map[string]bool
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload) != 1 || !payload["desktop_onboarding_complete"] || r.URL.Path != "/v1/onboarding" {
						t.Error("invalid completion write")
					}
					fmt.Fprint(w, `{"ok":true}`)
					return
				}
				if r.URL.Path == "/v1/onboarding" {
					credentials, workspaces := 1, 1
					if missing == "credential" {
						credentials = 0
					}
					if missing == "workspace" {
						workspaces = 0
					}
					fmt.Fprintf(w, `{"identity":{"bootstrapped":%t},"heuristics":{"credential_count":%d,"saved_workspace_count":%d}}`, missing != "identity", credentials, workspaces)
					return
				}
				if r.URL.Path != "/v1/agent-model-settings" {
					t.Error("unexpected prerequisite read")
				}
				assignment := map[string]string{"provider": "google", "model": "chosen", "thinking": "low"}
				system := map[string]any{}
				for _, role := range []string{"compact", "finder", "coder", "designer", "router"} {
					system[role] = assignment
				}
				if missing == "model" {
					delete(system, "router")
				}
				json.NewEncoder(w).Encode(map[string]any{"agent_model_settings": map[string]any{"swarm": map[string]any{"action": assignment, "plan": assignment}, "system_agents": system}})
			})
			err := runSetup([]string{"complete", "--socket", socket}, strings.NewReader(""), io.Discard)
			if missing != "" && (err == nil || writes != 0) {
				t.Fatal("premature completion")
			}
			if missing == "" && (err != nil || writes != 1) {
				t.Fatalf("completion failed: %v writes=%d", err, writes)
			}
		})
	}
}

// Purpose: newSetupClient must fail closed without a private socket, regardless
// of proxy configuration. No TCP fallback, host daemon or credentials are used.
func TestSetupMissingSocketFailsClosed(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://example.test:3128")
	var output bytes.Buffer
	err := runSetup([]string{"status", "--socket", filepath.Join(t.TempDir(), "missing.sock")}, strings.NewReader(""), &output)
	if err == nil || output.Len() != 0 {
		t.Fatal("missing socket did not fail closed")
	}
	if _, err := newSetupClient("relative.sock"); err == nil {
		t.Fatal("accepted relative socket")
	}
}
