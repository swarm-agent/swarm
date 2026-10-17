package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: runnerCLI.command must refresh joined runners' unit without changing
// credentials or installer-owned sandbox policy. This CLI/filesystem boundary
// proves upgrade and idempotency without privileged host writes or systemd.
func TestRunnerUnit(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	var calls []string
	writes := 0
	c := runnerCLI{config: filepath.Join(root, "config"), units: filepath.Join(root, "units"), out: &out,
		root: func(args []string) error {
			if strings.Join(args, " ") != "runner unit" {
				t.Fatal(args)
			}
			return nil
		},
		run: func(name string, args ...string) error {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil
		},
		write: func(path, content string, mode os.FileMode) error {
			writes++
			return os.WriteFile(path, []byte(content), mode)
		},
	}
	for _, dir := range []string{c.config, filepath.Join(c.units, runnerUnit+".d")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	unit := filepath.Join(c.units, runnerUnit)
	sandbox := filepath.Join(c.units, runnerUnit+".d", "sandbox.conf")
	for path, content := range map[string]string{unit: "old unit", sandbox: "installer policy"} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.command([]string{"unit"}); err == nil || !strings.Contains(err.Error(), "not joined") {
		t.Fatalf("expected not joined: %v", err)
	}
	if writes != 0 || len(calls) != 0 {
		t.Fatal("unjoined runner mutated")
	}
	credential := filepath.Join(c.config, "credential")
	if err := os.WriteFile(credential, []byte("test credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.command([]string{"unit"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Environment=SWARM_RUNNER_SLOTS=1", "EnvironmentFile=-/etc/swarm-runner/slots.env", "ExecStart=/opt/swarm-runner/current/bin/swarmd runner --slots ${SWARM_RUNNER_SLOTS}"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s", want)
		}
	}
	if writes != 1 || out.String() != "swarm-runner.service changed\n" {
		t.Fatalf("writes=%d output=%q", writes, out.String())
	}
	out.Reset()
	if err := c.command([]string{"unit"}); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || out.String() != "swarm-runner.service unchanged\n" {
		t.Fatalf("writes=%d output=%q", writes, out.String())
	}
	if fmt.Sprint(calls) != "[systemctl daemon-reload systemctl daemon-reload]" {
		t.Fatal(calls)
	}
	for path, want := range map[string]string{credential: "test credential", sandbox: "installer policy"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("modified %s: %v", path, err)
		}
	}
	c.root = func([]string) error { return fmt.Errorf("root denied") }
	if err := c.command([]string{"unit"}); err == nil || err.Error() != "root denied" {
		t.Fatalf("root guard: %v", err)
	}
	if writes != 1 || len(calls) != 2 {
		t.Fatal("root denial mutated state")
	}
}
