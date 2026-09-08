package worktree

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Purpose: AllocateTaskWorkspace must reject invalid scope patterns before any
// Git/worktree side effects. Threat: late glob rejection or newline injection
// into sparse-checkout stdin. A temporary real repository proves refs and
// worktree registrations stay unchanged and corrected exact paths materialize.
func TestTaskScopePreflightRejectsBeforeAllocation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := initSparseTaskRepository(t)
	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	base := TaskBase{RepoRoot: repo, ParentBranch: "dev", BaseCommit: head}
	refs, _ := runGit(repo, "show-ref")
	worktrees, _ := runGit(repo, "worktree", "list", "--porcelain")
	for _, scopes := range [][]string{{"small/owned*.txt"}, {"small/owned.txt\n/large"}, {".", "small/*.txt"}, {"small/{a,b}.txt"}} {
		allocation, err := (&Service{}).AllocateTaskWorkspace(repo, base, "invalid-child", scopes)
		if err == nil || !strings.Contains(err.Error(), "owned scope") {
			t.Fatalf("scopes=%q error=%v", scopes, err)
		}
		if !reflect.DeepEqual(allocation, Allocation{}) {
			t.Fatal("failed allocation returned state")
		}
		gotRefs, _ := runGit(repo, "show-ref")
		gotWorktrees, _ := runGit(repo, "worktree", "list", "--porcelain")
		if refs != gotRefs || worktrees != gotWorktrees {
			t.Fatal("invalid scope mutated Git state")
		}
	}
	allocation, err := (&Service{}).AllocateTaskWorkspace(repo, base, "exact-child", []string{"small/owned.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(allocation.WorkspacePath, "small", "owned.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(allocation.WorkspacePath, "large", "unrelated.bin")); err != nil {
		t.Fatalf("committed read source missing: %v", err)
	}
}
