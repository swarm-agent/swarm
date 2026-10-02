package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: AssessTaskDelivery must attribute only base..source work, never old
// rewritten history, and tree equality must not prove ancestry. Real isolated
// Git is the narrowest layer proving OIDs, contents, limits and no read mutations.
func TestTaskDeliveryAssessment(t *testing.T) {
	root := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(t.TempDir(), "empty"), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil { t.Fatalf("git %v: %v: %s", args, err, out) }
		return strings.TrimSpace(string(out))
	}
	write := func(path, text string) { t.Helper(); if err := os.WriteFile(path, []byte(text), 0600); err != nil { t.Fatal(err) } }
	run(root, "init", "-b", "dev")
	write(filepath.Join(root, "base"), "base")
	run(root, "add", "."); run(root, "commit", "-m", "base")
	base := run(root, "rev-parse", "HEAD")
	child := filepath.Join(t.TempDir(), "child")
	run(root, "worktree", "add", "-b", "agent/task", child)
	in := TaskDeliveryInput{SourcePath: child, TargetPath: root, Identity: pebblestore.TaskDeliveryAssessment{BaseOID: base, SourceBranch: "agent/task", TargetBranch: "dev"}}
	check := func(want string) pebblestore.TaskDeliveryAssessment {
		t.Helper()
		before := run(root, "show-ref")
		got := AssessTaskDelivery(context.Background(), in)
		if got.State != want { t.Fatalf("want %s: %+v", want, got) }
		if run(root, "show-ref") != before { t.Fatal("assessment mutated refs") }
		return got
	}
	check("empty")
	write(filepath.Join(child, "change"), "change")
	check("dirty")
	run(child, "add", "."); run(child, "commit", "-m", "task")
	head := run(child, "rev-parse", "HEAD")
	got := check("candidate_work")
	if got.CandidateCommits != 1 || len(got.Files) != 1 || got.Files[0] != "change" || got.SourceOID != head || got.TargetOID != base || len(got.AllowedActions) != 1 { t.Fatalf("wrong delta: %+v", got) }
	run(root, "commit", "--allow-empty", "-m", "advance")
	run(root, "cherry-pick", head)
	got = check("history_equivalent")
	if len(got.AllowedActions) != 0 { t.Fatal("equivalence offered integration") }
	run(root, "revert", "--no-edit", "HEAD")
	check("ambiguous")
	if _, err := os.Stat(filepath.Join(root, "change")); !os.IsNotExist(err) { t.Fatal("assessment restored reverted content") }
	run(root, "checkout", "--orphan", "rewritten")
	run(root, "add", "."); run(root, "commit", "-m", "rewritten base")
	in.Identity.TargetBranch = "rewritten"
	check("history_rewritten")
	ctx, cancel := context.WithCancel(context.Background()); cancel()
	if got := AssessTaskDelivery(ctx, in); got.State != "unavailable" || len(got.AllowedActions) != 0 { t.Fatalf("cancelled read actionable: %+v", got) }
	in.Identity.TargetBranch = "dev"
	check("unavailable") // Moved target branch is rejected, not silently followed.
	run(root, "checkout", "dev")
	for i := 0; i < 513; i++ { write(filepath.Join(child, fmt.Sprintf("file-%03d", i)), "bounded") }
	run(child, "add", "."); run(child, "commit", "-m", "too many paths")
	got = check("ambiguous")
	if got.Reason != "Candidate path limit exceeded" || len(got.Files) != 0 || len(got.AllowedActions) != 0 { t.Fatalf("unbounded paths: %+v", got) }
	tree := run(child, "rev-parse", "HEAD^{tree}")
	parent := run(child, "rev-parse", "HEAD")
	for i := 0; i < 257; i++ { parent = run(child, "commit-tree", tree, "-p", parent, "-m", "bounded candidate") }
	run(child, "update-ref", "refs/heads/agent/task", parent)
	got = check("ambiguous")
	if got.Reason != "Candidate commit limit exceeded" || got.CandidateCommits != 0 || len(got.AllowedActions) != 0 { t.Fatalf("unbounded commits: %+v", got) }
}

// Purpose: the aggregate writer used by every assessment Git command must stop
// excess stdout/stderr instead of allocating arbitrary output. A unit test is
// the narrowest deterministic layer for the exact byte boundary.
func TestTaskDeliveryOutputBudget(t *testing.T) {
	budget := &deliveryBudget{remaining: 3}
	first, second := &deliveryBuffer{budget: budget}, &deliveryBuffer{budget: budget}
	if _, err := first.Write([]byte("abc")); err != nil { t.Fatal(err) }
	if n, err := second.Write([]byte("d")); err == nil || n != 0 || second.Len() != 0 { t.Fatal("aggregate output limit not enforced") }
}
