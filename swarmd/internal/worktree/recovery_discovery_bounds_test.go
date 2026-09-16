package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Purpose: DiscoverRecoveryPaths must enumerate more than 100 registrations
// without reading lane contents or changing Git state. Real temporary Git
// registrations exercise the subprocess/parser boundary that previously failed.
func TestRecoveryDiscoveryBeyondHundred(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) []byte {
		t.Helper()
		out, err := recoveryGit(repo, nil, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git("init", "-b", "dev")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base")
	for i := 0; i < 101; i++ {
		git("worktree", "add", "--detach", "--no-checkout", filepath.Join(root, fmt.Sprintf("lane-%03d", i)), "HEAD")
	}
	before := string(git("worktree", "list", "--porcelain", "-z"))
	paths, err := DiscoverRecoveryPaths(repo)
	if err != nil || len(paths) != 102 {
		t.Fatalf("paths=%d err=%v", len(paths), err)
	}
	if after := string(git("worktree", "list", "--porcelain", "-z")); after != before {
		t.Fatal("discovery changed registration")
	}
	// A symlink repository selector must still fail before Git inspection.
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverRecoveryPaths(alias); err == nil {
		t.Fatal("accepted symlink repository")
	}
}
