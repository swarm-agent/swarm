package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDetectCurrentBranch(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	if output, err := exec.Command("git", "init", "-b", "dev", repoPath).CombinedOutput(); err != nil {
		t.Fatalf("init git workspace: %v: %s", err, output)
	}
	if got := DetectCurrentBranch(repoPath); got != "dev" {
		t.Fatalf("branch = %q, want dev", got)
	}

	if output, err := exec.Command("git", "-C", repoPath, "config", "user.email", "test@example.invalid").CombinedOutput(); err != nil {
		t.Fatalf("configure git email: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", repoPath, "config", "user.name", "Swarm Test").CombinedOutput(); err != nil {
		t.Fatalf("configure git name: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "file.txt"), []byte("test\n"), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	if output, err := exec.Command("git", "-C", repoPath, "add", "file.txt").CombinedOutput(); err != nil {
		t.Fatalf("stage test file: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", repoPath, "commit", "-m", "test").CombinedOutput(); err != nil {
		t.Fatalf("commit test file: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", repoPath, "checkout", "--detach").CombinedOutput(); err != nil {
		t.Fatalf("detach HEAD: %v: %s", err, output)
	}
	if got := DetectCurrentBranch(repoPath); got != "" {
		t.Fatalf("detached branch = %q, want empty", got)
	}
	if got := DetectCurrentBranch(t.TempDir()); got != "" {
		t.Fatalf("non-git branch = %q, want empty", got)
	}
}
