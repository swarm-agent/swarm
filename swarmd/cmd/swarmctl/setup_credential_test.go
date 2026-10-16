package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Requirement: `setup credential --provider vertex --project P [--location L]`
// saves secret-free adc settings (default location global) through the
// first-provider onboarding route, reads no key, and shows the daemon's reason
// when it refuses; --project/--location are refused for other providers.
// Threat: a key read for vertex, or project flags silently ignored for an
// API-key provider, would mislead the operator; hiding "no metadata server"
// behind a bare HTTP 400 leaves the deployment undiagnosable.
// Authority: runSetup's credential branch and setupRequestShowingRejection.
// Layer: the CLI against a fixture daemon socket, the narrowest layer that
// sees the exact request payload.
func TestSetupCredentialVertexSendsProjectAndLocationOnly(t *testing.T) {
	var got map[string]any
	reject := false
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/onboarding/provider/credential" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		got = map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if reject {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"error":"Google credentials unavailable: no GCE metadata server reachable\u0007"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	var out bytes.Buffer
	if err := runSetup([]string{"credential", "--socket", socket, "--provider", "vertex", "--project", "example-project"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"provider": "vertex", "type": "adc", "google_project": "example-project", "google_location": "global", "active": true}
	if len(got) != len(want) {
		t.Fatalf("payload = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("payload[%s] = %v, want %v (payload %v)", key, got[key], value, got)
		}
	}

	if err := runSetup([]string{"credential", "--socket", socket, "--provider", "vertex", "--project", "example-project", "--location", "europe-west4"}, strings.NewReader(""), &out); err != nil || got["google_location"] != "europe-west4" {
		t.Fatalf("regional setup: err = %v, payload = %v", err, got)
	}

	reject = true
	err := runSetup([]string{"credential", "--socket", socket, "--provider", "vertex", "--project", "example-project"}, strings.NewReader(""), &out)
	if err == nil || !strings.Contains(err.Error(), "no GCE metadata server reachable") || strings.ContainsRune(err.Error(), '\a') {
		t.Fatalf("rejection err = %v, want the daemon's printable reason", err)
	}

	got = nil
	for _, args := range [][]string{
		{"credential", "--socket", socket, "--provider", "vertex"},
		{"credential", "--socket", socket, "--provider", "vertex", "--project", "example-project", "--api-key-stdin"},
		{"credential", "--socket", socket, "--provider", "google", "--project", "example-project", "--api-key-stdin"},
		{"credential", "--socket", socket, "--provider", "google", "--location", "global", "--api-key-stdin"},
	} {
		if err := runSetup(args, strings.NewReader("key"), &out); err == nil {
			t.Fatalf("runSetup(%v) succeeded, want refusal", args)
		}
	}
	if got != nil {
		t.Fatalf("a refused invocation reached the daemon: %v", got)
	}
}
