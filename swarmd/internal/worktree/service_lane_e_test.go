package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLaneE_E2E055ApplyRejectsBranchMovementAndPreservesChild covers
// E2E-055/REQ-INT-001/REQ-CLN-002 at the apply-time revalidation boundary.
func TestLaneE_E2E055ApplyRejectsBranchMovementAndPreservesChild(t *testing.T) {
	repo := initRollbackTestRepository(t)
	base, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	childPath := filepath.Join(t.TempDir(), "child")
	if _, err := runGit(repo, "worktree", "add", "-b", "agent/lane-e-child", childPath, base); err != nil {
		t.Fatalf("add child worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(childPath, "child.txt"), []byte("child\n"), 0o644); err != nil {
		t.Fatalf("write child: %v", err)
	}
	if _, err := runGit(childPath, "add", "child.txt"); err != nil {
		t.Fatalf("stage child: %v", err)
	}
	if _, err := runGit(childPath, "commit", "-m", "lane e child"); err != nil {
		t.Fatalf("commit child: %v", err)
	}
	childHead, err := runGit(childPath, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve child HEAD: %v", err)
	}

	svc := &Service{}
	plan, err := svc.PrepareTaskIntegration(repo, "dev", base, []TaskIntegrationChild{{SessionID: "lane-e-child", BaseCommit: base, HeadCommit: childHead}})
	if err != nil {
		t.Fatalf("prepare integration: %v", err)
	}
	if _, err := runGit(repo, "switch", "-c", "moved-after-plan"); err != nil {
		t.Fatalf("move parent branch: %v", err)
	}
	result, err := svc.ApplyTaskIntegration(repo, plan)
	if err == nil || !strings.Contains(err.Error(), "stale parent branch: expected dev, found moved-after-plan") {
		t.Fatalf("apply after branch movement result=%+v err=%v", result, err)
	}
	parentHead, _ := runGit(repo, "rev-parse", "HEAD")
	if parentHead != base {
		t.Fatalf("parent HEAD changed: got %s want %s", parentHead, base)
	}
	if _, err := os.Stat(filepath.Join(repo, "child.txt")); !os.IsNotExist(err) {
		t.Fatalf("child effect reached parent: %v", err)
	}
	preservedHead, err := runGit(childPath, "rev-parse", "HEAD")
	if err != nil || preservedHead != childHead {
		t.Fatalf("child lineage not preserved: head=%q err=%v want=%s", preservedHead, err, childHead)
	}
}

// TestLaneE_E2E056ApplyRejectsHeadMovementAndPreservesChild covers
// E2E-056/REQ-INT-001: parent HEAD is revalidated immediately before apply.
func TestLaneE_E2E056ApplyRejectsHeadMovementAndPreservesChild(t *testing.T) {
	repo := initRollbackTestRepository(t)
	base, _ := runGit(repo, "rev-parse", "HEAD")
	childPath := filepath.Join(t.TempDir(), "child")
	if _, err := runGit(repo, "worktree", "add", "-b", "agent/lane-e-head-child", childPath, base); err != nil {
		t.Fatalf("add child worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(childPath, "child.txt"), []byte("child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(childPath, "add", "child.txt")
	if _, err := runGit(childPath, "commit", "-m", "lane e child"); err != nil {
		t.Fatal(err)
	}
	childHead, _ := runGit(childPath, "rev-parse", "HEAD")
	svc := &Service{}
	plan, err := svc.PrepareTaskIntegration(repo, "dev", base, []TaskIntegrationChild{{SessionID: "lane-e-head-child", BaseCommit: base, HeadCommit: childHead}})
	if err != nil {
		t.Fatalf("prepare integration: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "parent.txt"), []byte("parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "add", "parent.txt")
	if _, err := runGit(repo, "commit", "-m", "move parent HEAD"); err != nil {
		t.Fatal(err)
	}
	movedHead, _ := runGit(repo, "rev-parse", "HEAD")

	_, err = svc.ApplyTaskIntegration(repo, plan)
	if err == nil || !strings.Contains(err.Error(), "stale parent HEAD: expected "+base+", found "+movedHead) {
		t.Fatalf("apply after HEAD movement error = %v", err)
	}
	currentHead, _ := runGit(repo, "rev-parse", "HEAD")
	if currentHead != movedHead {
		t.Fatalf("parent movement was overwritten: got %s want %s", currentHead, movedHead)
	}
	preservedHead, err := runGit(childPath, "rev-parse", "HEAD")
	if err != nil || preservedHead != childHead {
		t.Fatalf("child lineage not preserved: head=%q err=%v want=%s", preservedHead, err, childHead)
	}
}
