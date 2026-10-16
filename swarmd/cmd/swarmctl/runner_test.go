package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: runnerCLI is the narrow privileged-install boundary. Exercise real
// HTTP decoding and filesystem modes with a fake hub and command recorder;
// rejection must not create credentials or start services. No host changes.
func TestRunnerCommands(t *testing.T) {
	var calls []string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v3/runners/join-tokens":
			fmt.Fprint(w, `{"token":"one-time","expires_in_seconds":900}`)
		case "/v3/runners/join":
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if b["token"] != "one-time" {
				w.WriteHeader(401)
				return
			}
			w.WriteHeader(201)
			fmt.Fprint(w, `{"credential":"long-lived","name":"worker"}`)
		case "/v3/runners":
			fmt.Fprint(w, `{"runners":[{"name":"worker","state":"busy","engine":"gvisor","last_seen":1000,"current_landing":"landing"}]}`)
		case "/v3/runners/worker":
			fmt.Fprint(w, `{"revoked":true}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer hub.Close()
	var out bytes.Buffer
	dir := t.TempDir()
	var commands []string
	c := runnerCLI{in: strings.NewReader("one-time\n"), out: &out, config: filepath.Join(dir, "etc"), units: filepath.Join(dir, "units"), client: hub.Client(), hostname: func() (string, error) { return "worker", nil }, root: func([]string) error { return nil }, run: func(name string, args ...string) error {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil
	}, write: func(path, body string, mode os.FileMode) error {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			return err
		}
		return os.Chmod(path, mode)
	}}
	c.request = func(method, path string, payload, out any) error {
		b, _ := json.Marshal(payload)
		req, _ := http.NewRequest(method, hub.URL+path, bytes.NewReader(b))
		resp, err := hub.Client().Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if out != nil {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	}
	if err := c.command([]string{"token"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "one-time\n" {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := c.command([]string{"list"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"worker", "busy", "gvisor", "1970-01-01T00:00:01Z", "landing"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	if err := c.command([]string{"join", hub.URL}); err != nil {
		t.Fatal(err)
	}
	for file, mode := range map[string]os.FileMode{filepath.Join(c.config, "credential"): 0600, filepath.Join(c.config, "runner.env"): 0600, filepath.Join(c.units, runnerUnit): 0644} {
		st, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != mode {
			t.Fatalf("%s: %v", file, st.Mode())
		}
	}
	cred, _ := os.ReadFile(filepath.Join(c.config, "credential"))
	if string(cred) != "long-lived\n" {
		t.Fatal("credential mismatch")
	}
	unit, _ := os.ReadFile(filepath.Join(c.units, runnerUnit))
	for _, want := range []string{"RuntimeDirectory=swarm-runner", "TimeoutStopSec=30min", "--state-file /run/swarm-runner/state", "User=swarm-runner", "SupplementaryGroups=docker", "Restart=always", "--credential-file /etc/swarm-runner/credential"} {
		if !strings.Contains(string(unit), want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Join(commands, ";") != "chown root:swarm-runner "+c.config+";chmod 0750 "+c.config+";chown swarm-runner:swarm-runner "+filepath.Join(c.config, "credential")+";systemctl daemon-reload;systemctl enable --now swarm-runner.service" {
		t.Fatal(commands)
	}
	n := len(calls)
	if err := c.command([]string{"join", hub.URL}); err == nil {
		t.Fatal("overwrote credential")
	}
	if len(calls) != n {
		t.Fatal("spent token on overwrite")
	}
	if err := c.command([]string{"remove", "worker"}); err != nil {
		t.Fatal(err)
	}
	if calls[len(calls)-1] != "DELETE /v3/runners/worker" {
		t.Fatal(calls)
	}
	c.config = filepath.Join(dir, "rejected")
	c.in = strings.NewReader("bad")
	commands = nil
	if err := c.command([]string{"join", hub.URL}); err == nil {
		t.Fatal("accepted bad token")
	}
	if _, err := os.Stat(c.config); !os.IsNotExist(err) {
		t.Fatal("rejected join wrote files")
	}
	if len(commands) != 0 {
		t.Fatal(commands)
	}
	c.in = strings.NewReader("one-time")
	if err := c.command([]string{"join", hub.URL + "/bad"}); err == nil {
		t.Fatal("accepted path")
	}
	c.config = filepath.Join(dir, "etc")
	c.stateFile = filepath.Join(dir, "state")
	if err := os.WriteFile(c.stateFile, []byte("busy landing-status 2026-01-01T00:00:00Z\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := c.command([]string{"status"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(commands, ";"), "systemctl show swarm-runner.service --property=ActiveState,SubState,StatusText") {
		t.Fatal(commands)
	}
	for _, want := range []string{hub.URL, "Name: worker", "busy landing-status 2026-01-01T00:00:00Z"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	c.stateFile = filepath.Join(dir, "missing")
	if err := c.command([]string{"status"}); err == nil {
		t.Fatal("missing state reported as success")
	}
}
