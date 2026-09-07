package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// Purpose: PrepareTaskIntegration/ApplyTaskIntegration must preserve a resolved
// merge containing the exact target, rather than replay its conflicting ancestor.
// Real temporary Git repositories are the narrowest layer proving tree/history
// preservation and rejection of dirty, stale, forged, and unowned requests.
func TestTaskIntegrationResolvedMerge(t *testing.T) {
	repo := initSparseTaskRepository(t)
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := runGit(path, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(path, "README.md"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base := git(repo, "rev-parse", "HEAD")
	child := filepath.Join(t.TempDir(), "child")
	git(repo, "worktree", "add", "-b", "agent/resolved", child, base)
	write(child, "child\n")
	git(child, "add", "README.md")
	git(child, "commit", "-m", "child")
	write(repo, "parent\n")
	git(repo, "add", "README.md")
	git(repo, "commit", "-m", "parent")
	target := git(repo, "rev-parse", "HEAD")
	svc := &Service{}
	original := git(child, "rev-parse", "HEAD")
	if _, err := svc.PrepareTaskIntegration(repo, "dev", target, []TaskIntegrationChild{{SessionID: "resolved", BaseCommit: base, HeadCommit: original}}); err == nil {
		t.Fatal("unresolved divergent source accepted")
	}
	if _, err := runGit(child, "merge", "--no-edit", "dev"); err == nil {
		t.Fatal("fixture must conflict")
	}
	write(child, "parent and child resolved\n")
	git(child, "add", "README.md")
	git(child, "commit", "-m", "resolve")
	head := git(child, "rev-parse", "HEAD")
	children := []TaskIntegrationChild{{SessionID: "resolved", BaseCommit: base, HeadCommit: head}}
	plan, err := svc.PrepareTaskIntegration(repo, "dev", target, children)
	if err != nil {
		t.Fatal(err)
	}
	if plan.FastForwardHead != head {
		t.Fatalf("missing resolved head: %+v", plan)
	}
	assertUnchanged := func() {
		t.Helper()
		if got := git(repo, "rev-parse", "HEAD"); got != target {
			t.Fatal("target changed on rejection")
		}
		if got := git(repo, "status", "--porcelain"); got != "" {
			t.Fatalf("dirty: %s", got)
		}
	}
	forged := plan
	forged.FastForwardHead = original
	if _, err := svc.ApplyTaskIntegration(repo, forged); err == nil {
		t.Fatal("forged plan accepted")
	}
	assertUnchanged()
	scoped := append([]TaskIntegrationChild(nil), children...)
	scoped[0].OwnedScopes = []string{"small/**"}
	if _, err := svc.PrepareTaskIntegration(repo, "dev", target, scoped); err == nil {
		t.Fatal("unowned changes accepted")
	}
	assertUnchanged()
	if _, err := svc.PrepareTaskIntegration(repo, "dev", base, children); err == nil {
		t.Fatal("stale target accepted")
	}
	assertUnchanged()
	write(repo, "dirty\n")
	if _, err := svc.ApplyTaskIntegration(repo, plan); err == nil {
		t.Fatal("dirty target accepted")
	}
	if got, err := os.ReadFile(filepath.Join(repo, "README.md")); err != nil || string(got) != "dirty\n" {
		t.Fatal("dirty user bytes changed")
	}
	write(repo, "parent\n")
	assertUnchanged()
	result, err := svc.ApplyTaskIntegration(repo, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResultingParentHead != head || git(repo, "rev-parse", "HEAD") != head {
		t.Fatal("resolved history rewritten")
	}
	if got, err := os.ReadFile(filepath.Join(repo, "README.md")); err != nil || string(got) != "parent and child resolved\n" {
		t.Fatal("resolution lost")
	}
	if git(repo, "status", "--porcelain") != "" || git(child, "rev-parse", "HEAD") != head {
		t.Fatal("worktree changed unexpectedly")
	}
}
