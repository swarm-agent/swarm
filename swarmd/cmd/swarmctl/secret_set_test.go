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
// canonical allowlist, accept equivalent host spellings, refuse host changes
// without any mutation, and keep piped values and value response bodies out of
// output/errors. A fixture daemon socket is the narrowest layer that proves the
// CLI's requests and refusal postconditions together.
func TestSecretSet(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing bool
		hosts    string
		wantErr  string
		reject   bool
	}{
		{name: "create", hosts: "api.example.com"},
		{name: "create canonical hosts", hosts: " API.Example.com.,api.example.com "},
		{name: "matching case", existing: true, hosts: "API.Example.com"},
		{name: "matching trailing dot", existing: true, hosts: " api.example.com. "},
		{name: "extra trailing dot is not equivalent", existing: true, hosts: "api.example.com..", wantErr: "--hosts differs"},
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
					if got.Name != "KEY" || got.Description != "test key" || strings.Join(normalizeSecretHosts(got.Hosts), ",") != "api.example.com" {
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

// Purpose: runSecret/setSecret must surface metadata rejection reasons before
// reading private input. The fixture socket and a recording reader prove both
// the request sequence (no value PUT) and that rejected setup never reads stdin.
func TestSecretSetMetadataRejection(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			const reason = "name must use uppercase letters"
			var requests []string
			socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				if r.Method == method {
					http.Error(w, reason, http.StatusBadRequest)
					return
				}
				if r.Method != http.MethodGet {
					t.Errorf("unexpected request: %s", r.Method)
				}
				io.WriteString(w, `{"secrets":[]}`)
			})
			input := &secretRecordingReader{}
			var output bytes.Buffer
			err := runSecret([]string{"set", "--socket", socket, "--name", "my_key", "--hosts", "api.example.com"}, input, &output)
			if err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("error = %v, want daemon reason", err)
			}
			if input.read || output.Len() != 0 {
				t.Fatal("rejected metadata read private input or printed output")
			}
			want := "GET /v1/secrets"
			if method == http.MethodPost {
				want += ",POST /v1/secrets"
			}
			if strings.Join(requests, ",") != want {
				t.Fatalf("requests = %v, want %s", requests, want)
			}
		})
	}
}

type secretRecordingReader struct{ read bool }

func (r *secretRecordingReader) Read(p []byte) (int, error) {
	r.read = true
	return 0, io.EOF
}

// Purpose: normalization used by setSecret must match the daemon's canonical
// allowlist representation on both sides; this helper-level test isolates sort,
// deduplication, empty entries, whitespace, case and trailing-dot handling.
func TestSecretHostNormalization(t *testing.T) {
	got := normalizeSecretHosts([]string{" Z.Example.com. ", "api.example.com", "API.EXAMPLE.COM.", " "})
	if strings.Join(got, ",") != "api.example.com,z.example.com" {
		t.Fatalf("hosts = %v", got)
	}
	if !sameSecretHosts(got, []string{"Z.Example.Com.", " API.Example.Com "}) {
		t.Fatal("equivalent allowlists differ")
	}
	if sameSecretHosts(got, []string{"other.example.com"}) {
		t.Fatal("different allowlists match")
	}
}
