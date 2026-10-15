package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Purpose: per-job scopeContainerName and runArgsImage must create independent
// kernel memory cgroups. This Docker-only failure-injection test allocates real
// memory in one sandbox while a second command is in flight, then verifies the
// second command and the first job's bind-mounted uncommitted file survive.
// It is not a workload benchmark; argv unit tests cannot prove OOM isolation.
func TestWorkerDockerOOMIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker unavailable")
	}
	if err := exec.CommandContext(ctx, docker, "info").Run(); err != nil {
		t.Skip("Docker unavailable")
	}
	if err := exec.CommandContext(ctx, docker, "image", "inspect", DefaultImage).Run(); err != nil {
		t.Fatal("sandbox image required")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t, ModeRequired, readyRunner(), Config{Memory: "1g"})
	// Failure injection bypasses the production minimum only for this probe.
	m.cfg.Memory = "128m"
	// No network is required for the OOM probe.
	m.cfg.Network = "none"
	a := Scope{Root: root, JobCheckout: filepath.Join(root, "a")}
	b := Scope{Root: root, JobCheckout: filepath.Join(root, "b")}
	names := []string{scopeContainerName(a), scopeContainerName(b)}
	for _, name := range names {
		args := m.RunArgs(name, root, "oom-test", []string{root}, SecretSetup{})
		if out, err := exec.CommandContext(ctx, docker, args...).CombinedOutput(); err != nil {
			t.Fatalf("create: %v %s", err, out)
		}
		t.Cleanup(func() {
			c, done := context.WithTimeout(context.Background(), 20*time.Second)
			defer done()
			_ = exec.CommandContext(c, docker, "rm", "-f", "-v", name).Run()
		})
	}
	// The reader remains running until input arrives after the other job OOMs.
	reader := exec.CommandContext(ctx, docker, "exec", "-i", names[1], "sh", "-c", "echo ready; cat")
	input, err := reader.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := reader.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, 6)
	if _, err := io.ReadFull(output, ready); err != nil || string(ready) != "ready\n" {
		t.Fatalf("second not ready: %q %v", ready, err)
	}
	attack := fmt.Sprintf("echo preserved > %s/uncommitted; python3 -c 'a=bytearray(512*1024*1024)'", root)
	out, err := exec.CommandContext(ctx, docker, "exec", names[0], "sh", "-c", attack).CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 137 {
		t.Fatalf("expected OOM/SIGKILL exit 137, got %v: %s", err, out)
	}
	if _, err := io.WriteString(input, "still-alive\n"); err != nil {
		t.Fatal(err)
	}
	input.Close()
	remaining, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Wait(); err != nil || !bytes.Contains(remaining, []byte("still-alive")) {
		t.Fatalf("second job affected: %q %v", remaining, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "uncommitted"))
	if err != nil || strings.TrimSpace(string(data)) != "preserved" {
		t.Fatalf("work lost: %s %v", data, err)
	}
}
