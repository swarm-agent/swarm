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
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run(root, "init", "-b", "dev")
	write(filepath.Join(root, "base"), "base")
	run(root, "add", ".")
	run(root, "commit", "-m", "base")
	base := run(root, "rev-parse", "HEAD")
	child := filepath.Join(t.TempDir(), "child")
	run(root, "worktree", "add", "-b", "agent/task", child)
	in := TaskDeliveryInput{SourcePath: child, TargetPath: root, Identity: pebblestore.TaskDeliveryAssessment{BaseOID: base, SourceBranch: "agent/task", TargetBranch: "dev"}}
	check := func(want string) pebblestore.TaskDeliveryAssessment {
		t.Helper()
		before := run(root, "show-ref")
		got := AssessTaskDelivery(context.Background(), in)
		if got.State != want {
			t.Fatalf("want %s: %+v", want, got)
		}
		if run(root, "show-ref") != before {
			t.Fatal("assessment mutated refs")
		}
		return got
	}
	check("empty")
	write(filepath.Join(child, "change"), "change")
	check("dirty")
	run(child, "add", ".")
	run(child, "commit", "-m", "task")
	head := run(child, "rev-parse", "HEAD")
	got := check("candidate_work")
	if got.CandidateCommits != 1 || len(got.Files) != 1 || got.Files[0] != "change" || got.SourceOID != head || got.TargetOID != base || len(got.AllowedActions) != 1 {
		t.Fatalf("wrong delta: %+v", got)
	}
	run(root, "commit", "--allow-empty", "-m", "advance")
	run(root, "cherry-pick", head)
	got = check("history_equivalent")
	if len(got.AllowedActions) != 0 {
		t.Fatal("equivalence offered integration")
	}
	run(root, "revert", "--no-edit", "HEAD")
	check("candidate_work") // Reverted history is not integrated; preflight owns conflicts.
	if _, err := os.Stat(filepath.Join(root, "change")); !os.IsNotExist(err) {
		t.Fatal("assessment restored reverted content")
	}
	run(root, "checkout", "--orphan", "rewritten")
	run(root, "add", ".")
	run(root, "commit", "-m", "rewritten base")
	in.Identity.TargetBranch = "rewritten"
	check("history_rewritten")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := AssessTaskDelivery(ctx, in); got.State != "unavailable" || len(got.AllowedActions) != 0 {
		t.Fatalf("cancelled read actionable: %+v", got)
	}
	in.Identity.TargetBranch = "dev"
	check("unavailable") // Moved target branch is rejected, not silently followed.
	run(root, "checkout", "dev")
	for i := 0; i < 513; i++ {
		write(filepath.Join(child, fmt.Sprintf("file-%03d", i)), "bounded")
	}
	run(child, "add", ".")
	run(child, "commit", "-m", "too many paths")
	got = check("ambiguous")
	if got.Reason != "Candidate path limit exceeded" || len(got.Files) != 0 || len(got.AllowedActions) != 0 {
		t.Fatalf("unbounded paths: %+v", got)
	}
	tree := run(child, "rev-parse", "HEAD^{tree}")
	parent := run(child, "rev-parse", "HEAD")
	for i := 0; i < 257; i++ {
		parent = run(child, "commit-tree", tree, "-p", parent, "-m", "bounded candidate")
	}
	run(child, "update-ref", "refs/heads/agent/task", parent)
	got = check("ambiguous")
	if got.Reason != "Candidate commit limit exceeded" || got.CandidateCommits != 0 || len(got.AllowedActions) != 0 {
		t.Fatalf("unbounded commits: %+v", got)
	}
}

// Purpose: the aggregate writer used by every assessment Git command must stop
// excess stdout/stderr instead of allocating arbitrary output. A unit test is
// the narrowest deterministic layer for the exact byte boundary.
func TestTaskDeliveryOutputBudget(t *testing.T) {
	budget := &deliveryBudget{remaining: 3}
	first, second := &deliveryBuffer{budget: budget}, &deliveryBuffer{budget: budget}
	if _, err := first.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if n, err := second.Write([]byte("d")); err == nil || n != 0 || second.Len() != 0 {
		t.Fatal("aggregate output limit not enforced")
	}
}

// Purpose: real Git movements during inspection must invalidate actionable
// observations; singleflight must bind exact OIDs and the two-analysis cap must
// fail closed. Instance-local barriers make this deterministic without sleeps,
// fake Git responses, mutable global hooks or a live daemon.
func TestTaskDeliveryMovementAndCapacity(t *testing.T) {
	for _, lane := range []string{"source", "target"} {
		t.Run(lane, func(t *testing.T) {
			in, git := deliveryFixture(t)
			path := in.SourcePath
			if lane == "target" {
				path = in.TargetPath
			}
			got := assessDeliveryWithHook(context.Background(), in, func() { git(path, "commit", "--allow-empty", "-m", "concurrent move") })
			if got.State != "unavailable" || got.Freshness != "stale" || len(got.AllowedActions) != 0 {
				t.Fatalf("moved lane actionable: %+v", got)
			}
		})
	}
	t.Run("exact-tuples-and-capacity", func(t *testing.T) {
		in, git := deliveryFixture(t)
		entered, release := make(chan struct{}, 2), make(chan struct{})
		results := make(chan pebblestore.TaskDeliveryAssessment, 2)
		started := 0
		defer func() {
			close(release)
			for i := 0; i < started; i++ {
				select {
				case <-results:
				case <-time.After(4 * time.Second):
					t.Error("inspection failed to finish")
				}
			}
		}()
		start := func(input TaskDeliveryInput) {
			started++
			go func() {
				results <- assessDeliveryWithHook(context.Background(), input, func() { entered <- struct{}{}; <-release })
			}()
		}
		wait := func() {
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("inspection did not reach barrier")
			}
		}
		start(in)
		wait()
		// Same logical identity, different exact source OID must not join flight 1.
		git(in.SourcePath, "commit", "--allow-empty", "-m", "new source OID")
		start(in)
		wait()
		third := in
		third.Identity.TaskID = "another-task"
		got := AssessTaskDelivery(context.Background(), third)
		if got.ReasonCode != "capacity_exceeded" || len(got.AllowedActions) != 0 {
			t.Fatalf("capacity failed open: %+v", got)
		}
		// release via defer, then buffered results allow all goroutines to exit.
	})
}

func deliveryFixture(t *testing.T) (TaskDeliveryInput, func(string, ...string) string) {
	t.Helper()
	root := t.TempDir()
	git := func(path string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(root, "init", "-b", "dev")
	if err := os.WriteFile(filepath.Join(root, "base"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	git(root, "add", ".")
	git(root, "commit", "-m", "base")
	// Historical commits before the recorded fork cannot inflate the one task
	// candidate, including when the target later rewrites its entire ancestry.
	tree := git(root, "rev-parse", "HEAD^{tree}")
	parent := git(root, "rev-parse", "HEAD")
	for i := 0; i < 20; i++ {
		parent = git(root, "commit-tree", tree, "-p", parent, "-m", "old history")
	}
	git(root, "update-ref", "refs/heads/dev", parent)
	child := filepath.Join(t.TempDir(), "child")
	git(root, "worktree", "add", "-b", "agent/task", child)
	if err := os.WriteFile(filepath.Join(child, "feature"), []byte("feature"), 0600); err != nil {
		t.Fatal(err)
	}
	git(child, "add", ".")
	git(child, "commit", "-m", "task")
	return TaskDeliveryInput{SourcePath: child, TargetPath: root, Identity: pebblestore.TaskDeliveryAssessment{BaseOID: parent, SourceBranch: "agent/task", TargetBranch: "dev"}}, git
}

// Purpose: base-scoped evidence rejects merge/import ambiguity and rewritten
// history, but admits advanced targets to the integration service's preflight.
// Actual trees and refs, not status-only mocks, establish the postconditions.
func TestTaskDeliveryTopology(t *testing.T) {
	for _, scenario := range []string{"rewritten", "partial", "squash", "merge", "missing-target", "wrong-repository", "dirty-target", "no-net-change"} {
		t.Run(scenario, func(t *testing.T) {
			in, git := deliveryFixture(t)
			want := "ambiguous"
			switch scenario {
			case "rewritten":
				tree := git(in.TargetPath, "rev-parse", "HEAD^{tree}")
				newHead := git(in.TargetPath, "commit-tree", tree, "-m", "rewrite")
				git(in.TargetPath, "update-ref", "refs/heads/dev", newHead)
				want = "history_rewritten"
			case "partial":
				if err := os.WriteFile(filepath.Join(in.TargetPath, "feature"), []byte("partial feature"), 0600); err != nil {
					t.Fatal(err)
				}
				git(in.TargetPath, "add", ".")
				git(in.TargetPath, "commit", "-m", "partial")
				want = "candidate_work"
			case "no-net-change":
				git(in.SourcePath, "revert", "--no-edit", "HEAD")
				want = "empty"
			case "squash":
				if err := os.WriteFile(filepath.Join(in.SourcePath, "second"), []byte("second"), 0600); err != nil {
					t.Fatal(err)
				}
				git(in.SourcePath, "add", ".")
				git(in.SourcePath, "commit", "-m", "second task commit")
				tree := git(in.SourcePath, "rev-parse", "HEAD^{tree}")
				squash := git(in.TargetPath, "commit-tree", tree, "-p", in.Identity.BaseOID, "-m", "squashed task")
				git(in.TargetPath, "read-tree", "--reset", "-u", squash)
				git(in.TargetPath, "update-ref", "refs/heads/dev", squash)
				want = "history_equivalent" // Current tree only; never ancestry delivery.
			case "merge":
				tree := git(in.SourcePath, "rev-parse", "HEAD^{tree}")
				side := git(in.SourcePath, "commit-tree", tree, "-p", in.Identity.BaseOID, "-m", "imported")
				tip := git(in.SourcePath, "rev-parse", "HEAD")
				merge := git(in.SourcePath, "commit-tree", tree, "-p", tip, "-p", side, "-m", "merge")
				git(in.SourcePath, "update-ref", "refs/heads/agent/task", merge)
			case "missing-target":
				in.TargetPath = filepath.Join(t.TempDir(), "missing")
				want = "unavailable"
			case "wrong-repository":
				in.TargetPath = t.TempDir()
				git(in.TargetPath, "init", "-b", "dev")
				git(in.TargetPath, "commit", "--allow-empty", "-m", "foreign")
				want = "unavailable"
			case "dirty-target":
				if err := os.WriteFile(filepath.Join(in.TargetPath, "dirty"), []byte("uncommitted"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "dirty"
			}
			refs := git(in.SourcePath, "show-ref")
			got := AssessTaskDelivery(context.Background(), in)
			if got.State != want || (len(got.AllowedActions) > 0) != (want == "candidate_work") || refs != git(in.SourcePath, "show-ref") {
				t.Fatalf("unsafe topology result: %+v", got)
			}
			if scenario == "rewritten" && (got.CandidateCommits != 1 || len(got.Files) != 1 || got.Files[0] != "feature") {
				t.Fatalf("old history counted: %+v", got)
			}
			data, err := os.ReadFile(filepath.Join(in.SourcePath, "feature"))
			if scenario == "no-net-change" {
				if !os.IsNotExist(err) {
					t.Fatal("assessment restored reverted source")
				}
			} else if err != nil || string(data) != "feature" {
				t.Fatal("source content changed")
			}
		})
	}
}
