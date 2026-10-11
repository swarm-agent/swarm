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
	if got := check("empty"); !got.SourceDirty || got.Freshness != "observed" || len(got.AllowedActions) != 0 {
		t.Fatalf("uncommitted work must not fabricate commits: %+v", got)
	}
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
	if len(got.AllowedActions) != 1 || got.AllowedActions[0] != "recover_integrate" {
		t.Fatal("equivalence must offer explicit delta verification, never original integration")
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

// Purpose: base-scoped evidence routes merge/rewritten history only to explicit
// delta recovery, and admits advanced targets to normal integration preflight.
// Recovery validates the first-parent boundary; neither action is read-side delivery.
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
				want = "history_rewritten"
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
				// A tracked change blocks writes; untracked files are covered by
				// TestTaskDeliveryUntrackedTargetFiles.
				if err := os.WriteFile(filepath.Join(in.TargetPath, "base"), []byte("uncommitted"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "candidate_work"
			}
			refs := git(in.SourcePath, "show-ref")
			got := AssessTaskDelivery(context.Background(), in)
			if got.State != want || (len(got.AllowedActions) > 0) != (scenario != "dirty-target" && (want == "candidate_work" || want == "history_rewritten" || want == "history_equivalent")) || refs != git(in.SourcePath, "show-ref") {
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

// Purpose: AssessTaskDelivery must report committed ancestry independently from
// source/target dirtiness while withholding write actions. Real isolated Git is
// the narrowest layer proving ancestry, unchanged refs and preserved dirty bytes.
func TestTaskDeliveryDirtyAncestry(t *testing.T) {
	for _, integrated := range []bool{false, true} {
		for _, lane := range []string{"source", "target"} {
			t.Run(fmt.Sprintf("integrated=%t/%s", integrated, lane), func(t *testing.T) {
				in, git := deliveryFixture(t)
				want := "candidate_work"
				if integrated {
					git(in.TargetPath, "merge", "--ff-only", "agent/task")
					want = "integrated"
				}
				path := in.SourcePath
				if lane == "target" {
					path = in.TargetPath
				}
				file := filepath.Join(path, "base")
				if err := os.WriteFile(file, []byte("uncommitted"), 0600); err != nil {
					t.Fatal(err)
				}
				refs := git(in.SourcePath, "show-ref")
				got := AssessTaskDelivery(context.Background(), in)
				if got.State != want || got.Freshness != "observed" || got.SourceDirty != (lane == "source") || got.TargetDirty != (lane == "target") || len(got.AllowedActions) != 0 {
					t.Fatalf("dirtiness obscured ancestry or authorized writes: %+v", got)
				}
				dirtyFiles := got.SourceDirtyFiles
				if lane == "target" {
					dirtyFiles = got.TargetDirtyFiles
				}
				if len(dirtyFiles) != 1 || dirtyFiles[0] != "base (modified)" {
					t.Fatalf("blocking change not named: %+v", got)
				}
				if !integrated && got.CandidateCommits != 1 {
					t.Fatalf("lost committed candidate: %+v", got)
				}
				data, err := os.ReadFile(file)
				if err != nil || string(data) != "uncommitted" || refs != git(in.SourcePath, "show-ref") {
					t.Fatal("assessment mutated Git state")
				}
			})
		}
	}
}

// Purpose: a cleanly committed task must stay integrable when the target
// checkout only holds untracked files the task does not write (an app's own
// state files), while uncommitted task work, untracked files the task would
// overwrite and recovery-only states still block, each naming the exact paths.
// Threat: "uncommitted changes" falsely hides integrate for committed work, or
// a relaxed check lets integration clobber an untracked target file.
// Owner: deliveryInspector.finish in AssessTaskDelivery. Real isolated Git is
// the narrowest layer proving status classification against actual paths.
func TestTaskDeliveryUntrackedTargetFiles(t *testing.T) {
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("unrelated-untracked-target", func(t *testing.T) {
		in, git := deliveryFixture(t)
		write(filepath.Join(in.TargetPath, "app-state.json"), `{"draft":true}`)
		write(filepath.Join(in.TargetPath, ".app", "cache", "index.db"), "cache")
		refs := git(in.SourcePath, "show-ref")
		got := AssessTaskDelivery(context.Background(), in)
		if got.State != "candidate_work" || got.Freshness != "observed" || got.SourceDirty || got.TargetDirty || len(got.TargetDirtyFiles) != 0 || len(got.AllowedActions) != 1 || got.AllowedActions[0] != "integrate" {
			t.Fatalf("unrelated untracked target files blocked integration: %+v", got)
		}
		if refs != git(in.SourcePath, "show-ref") {
			t.Fatal("assessment mutated refs")
		}
	})
	for name, path := range map[string]string{"same-path": "feature", "file-below-task-path": filepath.Join("feature", "notes")} {
		t.Run("colliding-untracked-target/"+name, func(t *testing.T) {
			in, _ := deliveryFixture(t)
			write(filepath.Join(in.TargetPath, path), "local")
			got := AssessTaskDelivery(context.Background(), in)
			want := filepath.ToSlash(path) + " (untracked, and the task writes this path)"
			if got.State != "candidate_work" || !got.TargetDirty || len(got.AllowedActions) != 0 || len(got.TargetDirtyFiles) != 1 || got.TargetDirtyFiles[0] != want {
				t.Fatalf("colliding untracked file not blocking: %+v", got)
			}
		})
	}
	t.Run("untracked-task-worktree", func(t *testing.T) {
		in, _ := deliveryFixture(t)
		write(filepath.Join(in.SourcePath, "notes.txt"), "forgotten")
		got := AssessTaskDelivery(context.Background(), in)
		if !got.SourceDirty || got.TargetDirty || len(got.AllowedActions) != 0 || len(got.SourceDirtyFiles) != 1 || got.SourceDirtyFiles[0] != "notes.txt (untracked)" {
			t.Fatalf("uncommitted task work not blocking: %+v", got)
		}
	})
	t.Run("recovery-keeps-clean-target", func(t *testing.T) {
		in, git := deliveryFixture(t)
		tree := git(in.TargetPath, "rev-parse", "HEAD^{tree}")
		git(in.TargetPath, "update-ref", "refs/heads/dev", git(in.TargetPath, "commit-tree", tree, "-m", "rewrite"))
		write(filepath.Join(in.TargetPath, "app-state.json"), "{}")
		got := AssessTaskDelivery(context.Background(), in)
		if got.State != "history_rewritten" || !got.TargetDirty || len(got.AllowedActions) != 0 || len(got.TargetDirtyFiles) != 1 || got.TargetDirtyFiles[0] != "app-state.json (untracked)" {
			t.Fatalf("recovery admitted an untracked target: %+v", got)
		}
	})
}

// Purpose: PrepareTaskIntegration (and Apply, which re-prepares) must accept a
// parent checkout whose only changes are untracked files the children do not
// write, keep those files byte-for-byte, and refuse tracked changes or
// untracked files the change would overwrite, naming them. Real Git proves the
// merge result and preserved bytes; no integration step is mocked.
func TestPrepareTaskIntegrationUntrackedParentFiles(t *testing.T) {
	setup := func(t *testing.T) (TaskDeliveryInput, func(string, ...string) string, []TaskIntegrationChild) {
		in, git := deliveryFixture(t)
		head := git(in.SourcePath, "rev-parse", "HEAD")
		return in, git, []TaskIntegrationChild{{SessionID: "task", BaseCommit: in.Identity.BaseOID, HeadCommit: head, PreserveAncestry: true}}
	}
	t.Run("unrelated-untracked-kept", func(t *testing.T) {
		in, git, children := setup(t)
		state := filepath.Join(in.TargetPath, "app-state.json")
		if err := os.WriteFile(state, []byte(`{"keep":1}`), 0600); err != nil {
			t.Fatal(err)
		}
		svc := &Service{}
		parentHead := git(in.TargetPath, "rev-parse", "HEAD")
		plan, err := svc.PrepareTaskIntegration(in.TargetPath, "dev", parentHead, children)
		if err != nil {
			t.Fatalf("prepare refused unrelated untracked file: %v", err)
		}
		result, err := svc.ApplyTaskIntegration(in.TargetPath, plan)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		git(in.TargetPath, "merge-base", "--is-ancestor", children[0].HeadCommit, result.ResultingParentHead)
		if data, err := os.ReadFile(state); err != nil || string(data) != `{"keep":1}` {
			t.Fatalf("untracked file changed: %q %v", data, err)
		}
	})
	t.Run("colliding-untracked-refused", func(t *testing.T) {
		in, git, children := setup(t)
		if err := os.WriteFile(filepath.Join(in.TargetPath, "feature"), []byte("local"), 0600); err != nil {
			t.Fatal(err)
		}
		parentHead := git(in.TargetPath, "rev-parse", "HEAD")
		_, err := (&Service{}).PrepareTaskIntegration(in.TargetPath, "dev", parentHead, children)
		if err == nil || !strings.Contains(err.Error(), "would be overwritten by the task: feature;") {
			t.Fatalf("colliding untracked file err=%v", err)
		}
		if git(in.TargetPath, "rev-parse", "HEAD") != parentHead {
			t.Fatal("refused prepare moved the parent")
		}
	})
	t.Run("tracked-change-refused", func(t *testing.T) {
		in, git, children := setup(t)
		if err := os.WriteFile(filepath.Join(in.TargetPath, "base"), []byte("edited"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := (&Service{}).PrepareTaskIntegration(in.TargetPath, "dev", git(in.TargetPath, "rev-parse", "HEAD"), children)
		if err == nil || !strings.Contains(err.Error(), "parent worktree is dirty") || !strings.Contains(err.Error(), "base") {
			t.Fatalf("tracked change err=%v", err)
		}
	})
}
