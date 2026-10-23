package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Requirement: on a box with no shell a second provider can be added and the
// old one removed after first setup. The listing never prints key material,
// Vertex addition carries no secret, and remove sends exactly the
// provider and ID. runSetupCredentials over a socket fixture is the boundary.
func TestSetupCredentialsListAddRemove(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		paths, bodies = append(paths, r.Method+" "+r.URL.Path), append(bodies, body)
		switch r.URL.Path {
		case "/v1/auth/credentials":
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `{"records":[{"id":"cred_g","provider":"google","active":true,"auth_type":"api","last4":"WXYZ"}]}`)
				return
			}
			fmt.Fprint(w, `{"id":"cred_v"}`)
		default:
			fmt.Fprint(w, `{"ok":true}`)
		}
	})
	var out bytes.Buffer
	if err := runSetup([]string{"credentials", "--socket", socket}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":"cred_g"`) || strings.Contains(out.String(), "WXYZ") {
		t.Fatalf("listing = %s (must show the ID and never key fragments)", out.String())
	}
	out.Reset()
	if err := runSetup([]string{"add-credential", "--socket", socket, "--provider", "vertex", "--project", "example-project"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if paths[1] != "POST /v1/auth/credentials" || bodies[1]["type"] != "adc" || bodies[1]["google_project"] != "example-project" || bodies[1]["google_location"] != "global" {
		t.Fatalf("add sent %s %v", paths[1], bodies[1])
	}
	if err := runSetup([]string{"remove-credential", "--socket", socket, "--provider", "google", "--id", "cred_g"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if paths[2] != "POST /v1/auth/credentials/delete" || len(bodies[2]) != 2 || bodies[2]["provider"] != "google" || bodies[2]["id"] != "cred_g" {
		t.Fatalf("remove sent %s %v", paths[2], bodies[2])
	}
	for _, args := range [][]string{
		{"add-credential", "--socket", socket, "--provider", "google", "--project", "p"},
		{"add-credential", "--socket", socket, "--provider", "vertex"},
		{"remove-credential", "--socket", socket, "--provider", "google"},
		{"remove-credential", "--socket", socket, "--provider", "google", "--id", "../x y"},
	} {
		if err := runSetup(args, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if len(paths) != 3 {
		t.Fatalf("a refused command reached the daemon: %v", paths)
	}
}

// Requirement: `setup daily-limit` without --usd only shows the cap; turning
// it off takes an explicit --usd 0. (A bare call used to switch a live cap
// off while meaning to read it.)
func TestSetupDailyLimitWithoutUSDOnlyReads(t *testing.T) {
	var methods []string
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		fmt.Fprint(w, `{"limits":{"daily_cost_limit_usd":5,"enabled":true,"today_cost_usd":0.25}}`)
	})
	var out bytes.Buffer
	if err := runSetup([]string{"daily-limit", "--socket", socket}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 || methods[0] != http.MethodGet || !strings.Contains(out.String(), "$5.00") {
		t.Fatalf("bare daily-limit sent %v and printed %q", methods, out.String())
	}
	if err := runSetup([]string{"daily-limit", "--socket", socket, "--usd", "0"}, strings.NewReader(""), &bytes.Buffer{}); err != nil || methods[1] != http.MethodPost {
		t.Fatalf("explicit --usd 0: err=%v methods=%v", err, methods)
	}
}

// Requirement: runSetupCredentials must send additional API keys only in the
// owner credential request body, never argv, URLs or output. A Unix socket
// fixture is the narrowest layer exercising CLI parsing and actual transport;
// failures must be actionable without disclosing an echoed key.
func TestSetupAddCredentialAPIKey(t *testing.T) {
	const key = "fixture-api-key"
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			calls := 0
			socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.RequestURI() != "/v1/auth/credentials" || strings.Contains(fmt.Sprint(r.Header), key) {
					t.Errorf("unexpected request metadata")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(body, map[string]any{"provider": "exa", "type": "api", "api_key": key, "active": true}) {
					t.Error("unexpected request body")
				}
				if rejected {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "verification failed: " + key})
				} else {
					fmt.Fprint(w, `{"id":"cred_api"}`)
				}
			})
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if _, err := io.WriteString(writer, "  "+key+"\n"); err != nil {
				t.Fatal(err)
			}
			writer.Close()
			var out bytes.Buffer
			err = runSetup([]string{"add-credential", "--socket", socket, "--provider", "exa", "--api-key-stdin"}, reader, &out)
			if rejected {
				if err == nil || !strings.Contains(err.Error(), "verification failed") || strings.Contains(err.Error(), key) {
					t.Fatalf("unsafe or missing rejection: %v", err)
				}
			} else if err != nil || !strings.Contains(out.String(), "credential saved") {
				t.Fatalf("result: %v %s", err, &out)
			}
			if calls != 1 || strings.Contains(out.String(), key) {
				t.Fatal("wrong request count or secret output")
			}
		})
	}
}

// Requirement: runSetupCredentials rejects unsafe stdin and incompatible flags
// before contacting the daemon. This CLI/socket layer proves both rejection and
// absence of mutation; os.DevNull exercises the same character-device guard as
// terminal stdin without requiring an ambient interactive terminal.
func TestSetupAddCredentialRejectsUnsafeInput(t *testing.T) {
	calls := 0
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, `{}`) })
	terminal, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	for _, tc := range []struct {
		name  string
		flags []string
		input io.Reader
		want  string
	}{
		{"terminal", []string{"--provider", "exa", "--api-key-stdin"}, terminal, "redirect"},
		{"empty", []string{"--provider", "exa", "--api-key-stdin"}, strings.NewReader(" \n"), "nonempty"},
		{"oversize", []string{"--provider", "exa", "--api-key-stdin"}, strings.NewReader(strings.Repeat("x", 16385)), "16384"},
		{"vertex-key", []string{"--provider", "vertex", "--project", "p", "--api-key-stdin"}, strings.NewReader("key"), "reads no key"},
		{"api-project", []string{"--provider", "exa", "--project", "", "--api-key-stdin"}, strings.NewReader("key"), "only"},
		{"api-location", []string{"--provider", "exa", "--location", "global", "--api-key-stdin"}, strings.NewReader("key"), "only"},
		{"missing-flag", []string{"--provider", "exa"}, strings.NewReader("key"), "required"},
		{"argv", []string{"--provider", "exa", "--api-key", "fixture-secret"}, strings.NewReader(""), "invalid setup flags"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runSetup(append([]string{"add-credential", "--socket", socket}, tc.flags...), tc.input, &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "fixture-secret") || out.Len() != 0 {
				t.Fatalf("unexpected rejection: %v %s", err, &out)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("rejected inputs reached daemon %d times", calls)
	}
}

// Requirement: setupRequestShowingRejection must not expose provider-echoed
// partial API keys or unbounded messages. The CLI/socket boundary exercises the
// real additional-key rejection path without a live provider or vault.
func TestSetupCredentialRedactsPartialKey(t *testing.T) {
	for _, command := range []string{"add-credential"} {
		t.Run(command, func(t *testing.T) {
			const key = "fixture-secret-key"
			const masked = "sk-example****1234"
			socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "verification failed: " + masked + " " + key + " " + strings.Repeat("reason ", 100) + "tail-marker"})
			})
			var out bytes.Buffer
			err := runSetup([]string{command, "--socket", socket, "--provider", "exa", "--api-key-stdin"}, strings.NewReader(key), &out)
			if err == nil {
				t.Fatal("expected rejection")
			}
			const prefix = "setup request rejected (HTTP 400): "
			message := err.Error()
			if !strings.HasPrefix(message, prefix+"verification failed: [redacted] [redacted]") || strings.Contains(message, "1234") || strings.Contains(message, key) || strings.Contains(message, "tail-marker") || len(strings.TrimPrefix(message, prefix)) > 300 || out.Len() != 0 {
				t.Fatalf("unsafe rejection: %q, output %q", message, out.String())
			}
		})
	}
}
