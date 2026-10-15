package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/environments/provider"
	"testing"
	"time"
)

// Purpose: real Docker proves cross-container, cross-checkout Go test reuse and
// post-trim usability, which argument tests cannot prove. This fixture is not
// a benchmark of the repository's internal/worktree package.
func TestProjectCacheDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker CLI unavailable; cross-container reuse, trim and timing not verified")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skip("Docker daemon unavailable")
	}
	image := os.Getenv("SWARM_SANDBOX_TEST_IMAGE")
	if image == "" {
		image = DefaultImage
	}
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", image).Run(); err != nil {
		t.Skip("sandbox image not installed; no image is pulled by this test")
	}
	root := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker failed: %v\n%s", err, out)
		}
		return string(out)
	}
	m := newTestManager(t, ModeRequired, readyRunner(), Config{})
	// Use the real runner only after readiness was checked above.
	m.runner = &provider.OSCommandRunner{}
	m.cfg.Image = image
	m.runtime = ""
	m.owner = func(string) (int, bool) { return os.Getuid(), true }
	if err := m.initializeProjectCache(ctx, root); err != nil {
		t.Fatal(err)
	}
	volume := ProjectCacheVolume(root)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		_ = exec.CommandContext(c, "docker", "volume", "rm", volume).Run()
	})
	for _, name := range []string{"one", "two"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for file, body := range map[string]string{"go.mod": "module cacheproof\n\ngo 1.22\n", "cache_test.go": "package cacheproof\nimport \"testing\"\nfunc TestValue(t *testing.T) { if 2+2 != 4 { t.Fatal(\"bad value\") } }\n"} {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	args := func(dir, command string) []string {
		return []string{"run", "--rm", "--network", "none", "--user", fmt.Sprintf("%d:%d", os.Getuid(), m.cfg.GID), "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--mount", "type=volume,src=" + volume + ",dst=" + projectCachePath, "--mount", "type=bind,src=" + root + ",dst=" + root, "-w", dir, "-e", "GOCACHE=" + projectCachePath + "/go-build", "-e", "GOMODCACHE=" + projectCachePath + "/go-mod", "-e", "GOFLAGS=-buildvcs=false -trimpath", "-e", "GOMAXPROCS=4", "--entrypoint", "sh", image, "-c", command}
	}
	if err := exec.CommandContext(ctx, "docker", append(args(root, "command -v go"))...).Run(); err != nil {
		t.Skip("sandbox image has no Go toolchain")
	}
	run(args(filepath.Join(root, "one"), "go test -p 4 ./...")...)
	if out := run(args(filepath.Join(root, "two"), "go test -p 4 ./...")...); !strings.Contains(out, "(cached)") {
		t.Fatalf("second checkout not cached: %s", out)
	}
	// The production trim uses the same Go cache removal operation, preserving
	// dependency stores and allowing the next test to rebuild successfully.
	trimArgs := args(filepath.Join(root, "two"), trimCacheScript)
	trimArgs = append(trimArgs, "swarm-cache-trim", "1")
	run(trimArgs...)
	if out := run(args(filepath.Join(root, "two"), "go test -p 4 ./...")...); strings.Contains(out, "(cached)") {
		t.Fatalf("cap did not evict test result: %s", out)
	}
	if ProjectCacheVolume(root) == ProjectCacheVolume(root+"-other") {
		t.Fatal("projects share volume")
	}
}
