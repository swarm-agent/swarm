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

// Requirement: the shell delegates provisioning to swarmsetup and preserves its
// failure diagnostic without printing success or reaching runtime verification.
// A complete fake artifact tests both install modes without privileged mutation.
func TestInstallerPropagatesProvisioningFailure(t *testing.T) {
	for _, mode := range []string{"--no-service"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			artifact := filepath.Join(root, "artifact")
			for _, rel := range []string{"linux-amd64/root/swarm", "linux-amd64/root/swarmdev", "linux-amd64/root/rebuild", "linux-amd64/root/swarmsetup", "linux-amd64/root/swarmtui", "linux-amd64/swarmd/swarmd", "linux-amd64/swarmd/swarmctl", "linux-amd64/swarmd/swarm-fff-search", "linux-amd64/swarmd/libfff_c.so", "web/index.html", "build-info.txt", "LICENSE", "THIRD_PARTY_NOTICES.md"} {
				path := filepath.Join(artifact, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				body := "fixture"
				if strings.HasSuffix(rel, "root/swarmsetup") {
					body = "#!/bin/sh\necho 'injected account provisioning failure' >&2\nexit 42\n"
				}
				if err := os.WriteFile(path, []byte(body), 0755); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "../../install.sh", "--yes", mode, "--artifact-root", artifact)
			cmd.Env = append(os.Environ(), "TMPDIR="+root)
			out, err := cmd.CombinedOutput()
			if err == nil || ctx.Err() != nil {
				t.Fatalf("expected bounded failure: %v %s", err, out)
			}
			text := string(out)
			if !strings.Contains(text, "injected account provisioning failure") || strings.Contains(text, "verifying launcher") || strings.Contains(text, "\nok\n") {
				t.Fatalf("lost failure or false success: %s", out)
			}
		})
	}
}
