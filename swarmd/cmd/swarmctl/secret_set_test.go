package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Purpose: runSecret/setSecret must create missing slots only with an explicit
// allowlist, refuse host changes without any mutation, and keep piped values and
// server bodies out of output/errors. A fixture daemon socket is the narrowest
// layer that proves the CLI's requests and refusal postconditions together.
func TestSecretSet(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing bool
		hosts    string
		wantErr  string
		reject   bool
	}{
		{name: "create", hosts: "api.example.com"},
		{name: "missing hosts", wantErr: "a new secret needs --hosts: the websites it may be sent to"},
		{name: "mismatch", existing: true, hosts: "other.example.com", wantErr: "--hosts differs"},
		{name: "matching", existing: true, hosts: "api.example.com"},
		{name: "omitted", existing: true},
		{name: "server rejection", existing: true, reject: true, wantErr: "HTTP 400"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const value = "fixture-private-value"
			var requests []string
			socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				switch r.Method + " " + r.URL.Path {
				case "GET /v1/secrets":
					if tc.existing {
						io.WriteString(w, `{"secrets":[{"name":"KEY","hosts":["api.example.com"]}]}`)
					} else {
						io.WriteString(w, `{"secrets":[]}`)
					}
				case "POST /v1/secrets":
					var got struct {
						Name, Description string
						Hosts             []string
					}
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					if got.Name != "KEY" || got.Description != "test key" || len(got.Hosts) != 1 || got.Hosts[0] != "api.example.com" {
						t.Errorf("unexpected create metadata: %+v", got)
					}
					io.WriteString(w, value)
				case "PUT /v1/secrets/KEY/value":
					got, err := io.ReadAll(r.Body)
					if err != nil || string(got) != value {
						t.Error("value request did not match piped input")
					}
					if tc.reject {
						w.WriteHeader(http.StatusBadRequest)
					}
					io.WriteString(w, value)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
			})
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if _, err := io.WriteString(writer, value+"\n"); err != nil {
				t.Fatal(err)
			}
			writer.Close()
			args := []string{"set", "--socket", socket, "--name", "KEY", "--description", "test key"}
			if tc.hosts != "" {
				args = append(args, "--hosts", tc.hosts)
			}
			var output bytes.Buffer
			err = runSecret(args, input, &output)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %s", err, tc.wantErr)
				}
				if output.Len() != 0 {
					t.Fatal("failure printed output")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if output.String() != "KEY set (sent only to api.example.com)\n" {
					t.Fatalf("unexpected output: %q", output.String())
				}
			}
			if strings.Contains(output.String(), value) || (err != nil && strings.Contains(err.Error(), value)) {
				t.Fatal("value leaked")
			}
			want := "GET /v1/secrets"
			if tc.wantErr == "" || tc.reject {
				if !tc.existing {
					want += ",POST /v1/secrets"
				}
				want += ",PUT /v1/secrets/KEY/value"
			}
			if strings.Join(requests, ",") != want {
				t.Fatalf("requests = %v, want %s", requests, want)
			}
		})
	}
}
