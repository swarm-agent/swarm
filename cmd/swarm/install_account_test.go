package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Requirement: install.sh forwards the exact selected identity mode to setup,
// not a conflicting service-account consent or shell-interpolated command. A
// fake complete artifact stops at setup, before host runtime/service mutation.
func TestInstallerForwardsAccountChoice(t *testing.T) {
	for _, mode := range []string{"--install-user", "--create-user", "--create-service-account"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			artifact := filepath.Join(root, "artifact")
			calls := filepath.Join(root, "args")
			for _, rel := range []string{"linux-amd64/root/swarm", "linux-amd64/root/swarmdev", "linux-amd64/root/rebuild", "linux-amd64/root/swarmsetup", "linux-amd64/root/swarmtui", "linux-amd64/swarmd/swarmd", "linux-amd64/swarmd/swarmctl", "linux-amd64/swarmd/swarm-fff-search", "linux-amd64/swarmd/libfff_c.so", "web/index.html", "build-info.txt", "LICENSE", "THIRD_PARTY_NOTICES.md"} {
				path := filepath.Join(artifact, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				body := "fixture"
				if strings.HasSuffix(rel, "root/swarmsetup") {
					body = "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$ACCOUNT_TEST_ARGS\"\nexit 42\n"
				}
				if err := os.WriteFile(path, []byte(body), 0755); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"../../install.sh", "--yes", "--no-service", "--artifact-root", artifact, mode}
			if mode != "--create-service-account" {
				args = append(args, "developer")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", args...)
			cmd.Env = append(os.Environ(), "TMPDIR="+root, "ACCOUNT_TEST_ARGS="+calls)
			if out, err := cmd.CombinedOutput(); err == nil || ctx.Err() != nil {
				t.Fatalf("expected bounded stop: %v %s", err, out)
			}
			got, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			want := "--artifact-root\n" + artifact + "\n" + mode + "\n"
			if mode != "--create-service-account" {
				want += "developer\n"
			}
			want += "--no-service\n"
			if string(got) != want {
				t.Fatalf("args=%q want=%q", got, want)
			}
		})
	}
}
