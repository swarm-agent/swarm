package runner

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"
)

// Purpose: archiveCommands must materialize the source tree and a single local
// commit for repository-aware checks, while landingEnv preserves real IDs.
// Execute the exact preparation argv on an isolated fixture: this proves tree
// and Git behavior without claiming to prove container isolation.
func TestRunnerArchivePreparation(t *testing.T) {
	project := t.TempDir()
	checkout := filepath.Join(project, "checkout")
	f, err := os.Create(filepath.Join(project, "source.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"hello.txt": "hello\n", ".gitignore": "ignored.txt\n", "ignored.txt": "tracked archive content\n"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(args []string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = project
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + project, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for _, args := range archiveCommands(project, checkout) {
		run(args)
	}
	if got := run([]string{"git", "-C", checkout, "rev-list", "--count", "HEAD"}); got != "1" {
		t.Fatalf("commit count %q", got)
	}
	if got := run([]string{"git", "-C", checkout, "show", "HEAD:ignored.txt"}); got != "tracked archive content" {
		t.Fatalf("missing tree file: %q", got)
	}
	if got := run([]string{"git", "-C", checkout, "status", "--porcelain"}); got != "" {
		t.Fatalf("dirty tree: %q", got)
	}
	l := store.RunnerLanding{HeadCommit: strings.Repeat("a", 40), BaseCommit: strings.Repeat("b", 40), Env: map[string]string{"SWARM_LANDING_HEAD": "wrong", "CUSTOM": "value"}}
	env := landingEnv(l)
	if env["SWARM_LANDING_HEAD"] != l.HeadCommit || env["SWARM_LANDING_BASE"] != l.BaseCommit || env["CUSTOM"] != "value" || l.Env["SWARM_LANDING_HEAD"] != "wrong" {
		t.Fatalf("incorrect check environment: %v", env)
	}
}
