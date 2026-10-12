package main

// Purpose: `swarmctl host` is the owner's terminal way to list, approve and
// deny host change requests over the private daemon socket. runHost is the
// authority; a Unix-socket fixture proves it sends exactly the documented
// routes and bodies, never authentication material, and makes no request for
// a missing or unsafe ID. The daemon's own checks are covered in the API
// package.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestHostCommandsSendExactRoutes(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		route string
		query string
		body  map[string]any
	}{
		{"list", []string{"list"}, "GET /v1/host/changes", "", nil},
		{"list pending", []string{"list", "--status", "pending"}, "GET /v1/host/changes", "status=pending", nil},
		{"approve", []string{"approve", "hc_1_ab", "--reason", "my repo"}, "POST /v1/host/changes/hc_1_ab/approve", "", map[string]any{"reason": "my repo"}},
		{"deny", []string{"deny", "hc_1_ab"}, "POST /v1/host/changes/hc_1_ab/deny", "", map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if got := r.Method + " " + r.URL.Path; got != tc.route || r.URL.RawQuery != tc.query {
					t.Errorf("unexpected request %s ?%s", got, r.URL.RawQuery)
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("X-Swarm-Token") != "" || r.Header.Get("Cookie") != "" {
					t.Error("host command exported authentication material")
				}
				if tc.body != nil {
					var got map[string]any
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					gotJSON, _ := json.Marshal(got)
					wantJSON, _ := json.Marshal(tc.body)
					if !bytes.Equal(gotJSON, wantJSON) {
						t.Errorf("body %s, want %s", gotJSON, wantJSON)
					}
				}
				fmt.Fprint(w, `{"ok":true,"change":{"id":"hc_1_ab","status":"applied"}}`)
			})
			var output bytes.Buffer
			if err := runHost(append(tc.args, "--socket", socket), &output); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !strings.Contains(output.String(), `"applied"`) {
				t.Fatalf("calls=%d output=%s", calls, output.String())
			}
		})
	}
}

func TestHostCommandsRefuseMissingOrUnsafeID(t *testing.T) {
	calls := 0
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, `{}`) })
	for _, args := range [][]string{
		{"approve"},
		{"deny"},
		{"approve", "../secrets"},
		{"deny", "x?y"},
		{"list", "extra"},
		{"bogus"},
	} {
		var output bytes.Buffer
		if err := runHost(append(args, "--socket", socket), &output); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid host commands reached the daemon %d times", calls)
	}
}
