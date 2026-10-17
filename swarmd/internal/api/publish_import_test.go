package api

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// Purpose: a worker job commits in its own clone of the folder, and the
// folder's publish command pushes from the folder. Delivery must bring the
// job's exact head into the folder first, or the command fails with "commit
// is not in the folder". Real Git repositories are the boundary.
func TestImportJobHeadBringsTheClonesCommitIntoTheFolder(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	folder, clone := t.TempDir(), t.TempDir()
	git(folder, "init", "-q", "-b", "dev")
	git(folder, "commit", "-q", "--allow-empty", "-m", "base")
	git(clone, "clone", "-q", "--no-local", folder, ".")
	git(clone, "commit", "-q", "--allow-empty", "-m", "the job's change")
	head := git(clone, "rev-parse", "HEAD")
	if err := exec.Command("git", "-C", folder, "cat-file", "-e", head+"^{commit}").Run(); err == nil {
		t.Fatal("fixture: the folder already has the job's commit")
	}

	if err := importJobHead(context.Background(), folder, "", head); err == nil || !strings.Contains(err.Error(), "not in") {
		t.Fatalf("missing commit without a known worktree = %v, want a clear refusal", err)
	}
	if err := importJobHead(context.Background(), folder, clone, head); err != nil {
		t.Fatal(err)
	}
	if got := git(folder, "rev-parse", head+"^{commit}"); got != head {
		t.Fatalf("folder has %s, want the job's %s", got, head)
	}
	if branch := git(folder, "rev-parse", "dev"); branch == head {
		t.Fatal("import moved the folder's branch")
	}
	// Already present: nothing to fetch, even when the clone is gone.
	if err := importJobHead(context.Background(), folder, t.TempDir()+"/gone", head); err != nil {
		t.Fatal(err)
	}
}
