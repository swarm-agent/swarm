package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Purpose: execute initializeCacheScript against isolated directories to prove
// pnpm's versioned warm content is copied once and not overwritten on reuse.
// The shell layer is the narrowest test of actual copy/layout semantics; only
// pnpm's path discovery is substituted, not package installation or timing.
func TestProjectCachePnpmSeed(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock unavailable")
	}
	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	store := filepath.Join(root, "warm", "v10")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{cache, store, bin} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(store, "content"), []byte("warm"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "pnpm"), []byte("#!/bin/sh\nprintf '%s\\n' \"$TEST_STORE\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(initializeCacheScript, projectCachePath, cache)
	script = strings.ReplaceAll(script, "/opt/go/cache", filepath.Join(root, "absent-go-cache"))
	run := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", script, "seed", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TEST_STORE="+store, "GOMODCACHE=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("seed: %v %s", err, out)
		}
	}
	run()
	target := filepath.Join(cache, "pnpm", "v10", "content")
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "warm" {
		t.Fatalf("seed: %q %v", data, err)
	}
	if err := os.WriteFile(target, []byte("job content"), 0644); err != nil {
		t.Fatal(err)
	}
	run()
	data, err = os.ReadFile(target)
	if err != nil || string(data) != "job content" {
		t.Fatalf("reseeded: %q %v", data, err)
	}
}
