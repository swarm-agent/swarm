package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Requirement: on a box with no shell a second provider can be added and the
// old one removed after first setup. The listing never prints key material,
// add-credential only takes Vertex (no secret), and remove sends exactly the
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
