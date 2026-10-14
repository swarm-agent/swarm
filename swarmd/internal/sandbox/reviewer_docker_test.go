package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Purpose: scopeRunArgs must enforce immutable shared Git state in the actual
// container mount namespace, not by prompt or Git wrapper. This Docker-gated
// integration test exercises commits, ref writes and direct metadata writes,
// verifies observational Git and writable build outputs, and checks the host
// repository postconditions. Hermetic argv tests cannot prove kernel mounts.
func TestReviewerDockerReadOnlyGit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker unavailable")
	}
	if out, err := exec.CommandContext(ctx, docker, "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("Docker unavailable: %s", out)
	}
	image := os.Getenv("SWARM_REVIEW_TEST_IMAGE")
	if image == "" {
		image = DefaultImage
	}
	if out, err := exec.CommandContext(ctx, docker, "image", "inspect", image).CombinedOutput(); err != nil {
		t.Fatalf("required Git/Go sandbox image %s unavailable: %s", image, out)
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "source")
	checkout := filepath.Join(dir, "checkout")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		c := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("config", "user.name", "Review Test")
	git("config", "user.email", "review@example.invalid")
	for name, content := range map[string]string{"go.mod": "module reviewtest\n\ngo 1.20\n", "review_test.go": "package reviewtest\nimport \"testing\"\nfunc TestBuild(t *testing.T) {}\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-m", "fixture")
	head := git("rev-parse", "HEAD")
	git("worktree", "add", "--detach", checkout, head)
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		t.Skip("run as a non-root user to match sandbox policy")
	}
	m := &Manager{cfg: Config{UID: uid, GID: gid, Image: image, Network: "none", Memory: "1g", PidsLimit: 128}}
	scope := Scope{Root: root, Mounts: []string{dir}, ReviewCheckout: checkout}
	name := scopeContainerName(scope)
	// Cleanup is bounded and names only resources created by this test.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		exec.CommandContext(cleanup, docker, "rm", "-f", name).Run()
		exec.CommandContext(cleanup, docker, "volume", "rm", name+"-home").Run()
	}()
	args := m.scopeRunArgs(name, root, "review-proof", scope.Mounts, SecretSetup{}, EnvironmentImage{Image: image, Path: sandboxPath}, checkout)
	if out, err := exec.CommandContext(ctx, docker, args...).CombinedOutput(); err != nil {
		t.Fatalf("review container: %v %s", err, out)
	}
	run := func(command string, success bool) string {
		t.Helper()
		argv := []string{"exec", "-w", checkout, "-e", "HOME=/tmp", "-e", "GOCACHE=/tmp/go-build", "-e", "GOPATH=/tmp/go", "-e", "GOTOOLCHAIN=local", "-e", "GIT_CONFIG_COUNT=1", "-e", "GIT_CONFIG_KEY_0=safe.directory", "-e", "GIT_CONFIG_VALUE_0=" + checkout, name}
		argv = append(argv, withSandboxUmask("sh", "-c", command)...)
		out, err := exec.CommandContext(ctx, docker, argv...).CombinedOutput()
		if (err == nil) != success {
			t.Fatalf("%s: success=%v err=%v output=%s", command, success, err, out)
		}
		return string(out)
	}
	run("git diff HEAD...HEAD", true)
	run("git log -1 --format=%H", true)
	run("go test -count=1 -timeout=30s ./...", true)
	// Ensure write attempts have work to do, rather than failing on identity or
	// a no-op commit. The checkout is writable, staging/shared metadata are not.
	run("printf '\n// build output\n' >> review_test.go", true)
	for _, command := range []string{"git -c user.name=Review -c user.email=review@example.invalid commit -am forbidden", "git update-ref refs/heads/x HEAD", "touch .git/x", "touch \"$(git rev-parse --git-common-dir)/x\"", "touch \"$(git rev-parse --git-dir)/x\""} {
		out := run(command, false)
		if !strings.Contains(strings.ToLower(out), "read-only") && !strings.Contains(strings.ToLower(out), "not a directory") {
			t.Fatalf("failure was not mount enforcement: %s: %s", command, out)
		}
	}
	if git("rev-parse", "HEAD") != head {
		t.Fatal("host HEAD changed")
	}
	if git("status", "--porcelain") != "" {
		t.Fatal("source checkout changed")
	}
	c := exec.CommandContext(ctx, "git", "-C", root, "show-ref", "--verify", "refs/heads/x")
	if c.Run() == nil {
		t.Fatal("forbidden ref created")
	}
	for _, path := range []string{filepath.Join(root, ".git", "x"), filepath.Join(root, ".git", "worktrees", "checkout", "x")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("metadata changed: %s: %v", path, err)
		}
	}
}
