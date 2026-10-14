package sandbox

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	sibling := filepath.Join(dir, "sibling")
	git("worktree", "add", "--detach", sibling, head)
	snapshot := func(base string) map[string][32]byte {
		t.Helper()
		files := map[string][32]byte{}
		if err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			var data []byte
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				data = []byte(target)
			} else {
				var err error
				data, err = os.ReadFile(path)
				if err != nil {
					return err
				}
			}
			rel, _ := filepath.Rel(base, path)
			files[rel] = sha256.Sum256(data)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return files
	}
	beforeSource, beforeSibling := snapshot(root), snapshot(sibling)
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		t.Skip("run as a non-root user to match sandbox policy")
	}
	m, err := NewManager(ctx, Config{Mode: ModeRequired, Engine: docker, UID: uid, GID: gid, Image: image, Network: "none", Memory: "1g", PidsLimit: 128, StateDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{root}, nil }, WorktreesRoot: filepath.Dir(dir), Bucket: func(string) (string, error) { return filepath.Base(dir), nil }})
	if err := m.RegisterReviewCheckout(checkout); err != nil {
		t.Fatal(err)
	}
	session := "docker-review-" + filepath.Base(dir)
	defer reviewSessions.Delete(session)
	if err := m.BindReviewSession(session, checkout); err != nil {
		t.Fatal(err)
	}
	ctx, err = WithReviewSession(ctx, session, checkout)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Root: root, Mounts: []string{dir}, ReviewCheckout: checkout}
	name := scopeContainerName(scope)
	// Cleanup is bounded and names only resources created by this test.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		exec.CommandContext(cleanup, docker, "rm", "-f", name).Run()
		exec.CommandContext(cleanup, docker, "volume", "rm", name+"-home").Run()
	}()
	run := func(command string, success bool) string {
		t.Helper()
		result, err := m.Exec(ctx, scope, ExecRequest{Command: []string{"sh", "-c", command}, WorkingDir: checkout, Env: map[string]string{"GOCACHE": "/home/sandbox/go-build", "GOPATH": "/home/sandbox/go", "GOTOOLCHAIN": "local"}, Timeout: time.Minute, MaxTimeout: time.Minute, MaxOutput: 8192})
		if err != nil {
			t.Fatalf("execution infrastructure: %v", err)
		}
		out := result.CombinedOutput()
		if (result.ExitCode == 0) != success {
			t.Fatalf("%s: success=%v err=%v output=%s", command, success, err, out)
		}
		return string(out)
	}
	if _, err := m.Exec(ctx, scope, ExecRequest{Command: []string{"sh", "-c", "touch forbidden"}, WorkingDir: root}); err == nil {
		t.Fatal("source cwd override accepted")
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
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	run("ln -s "+quote(root)+" outside", true)
	for _, command := range []string{
		"cd " + quote(root) + " && printf attack >> go.mod",
		"printf attack >> outside/go.mod",
		"printf attack >> " + quote(filepath.Join(sibling, "go.mod")),
		"git -C " + quote(root) + " update-ref refs/heads/x HEAD",
		"git --git-dir=" + quote(filepath.Join(root, ".git")) + " --work-tree=" + quote(root) + " update-ref refs/heads/x HEAD",
		"git --work-tree=" + quote(root) + " checkout HEAD -- go.mod",
		"GIT_DIR=" + quote(filepath.Join(root, ".git")) + " git update-ref refs/heads/x HEAD",
	} {
		run(command, false)
	}
	if !reflect.DeepEqual(beforeSource, snapshot(root)) || !reflect.DeepEqual(beforeSibling, snapshot(sibling)) {
		t.Fatal("source, sibling or Git metadata changed")
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", sibling, "status", "--porcelain").CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("sibling changed: %v %s", err, out)
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
	if err := m.CleanupReview(ctx, checkout, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"container", "inspect", name}, {"volume", "inspect", name + "-home"}} {
		if out, err := exec.CommandContext(ctx, docker, args...).CombinedOutput(); err == nil {
			t.Fatalf("review resource survived: %v %s", args, out)
		}
	}
	for _, path := range []string{filepath.Join(root, ".git", "x"), filepath.Join(root, ".git", "worktrees", "checkout", "x")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("metadata changed: %s: %v", path, err)
		}
	}
}
