package sessionreview

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type stubGit struct {
	output string
	err    error
}

func (s stubGit) Run(context.Context, string, ...string) (string, error) { return s.output, s.err }

func reviewSession(updatedAt int64) pebblestore.SessionSnapshot {
	return pebblestore.SessionSnapshot{ID: "session-1", Title: "Review me", UpdatedAt: updatedAt, WorktreeEnabled: true, WorktreeRootPath: "/tmp/worktree", WorktreeBranch: "agent/change", WorktreeBaseBranch: "dev"}
}

func TestClassifySnapshotRetainsDirtyWorktree(t *testing.T) {
	got := ClassifySnapshot(context.Background(), stubGit{}, reviewSession(1), gitstatus.Snapshot{HasGit: true, HeadOID: "abc", Clean: false, DirtyCount: 2}, time.Now(), time.Hour)
	if got.Classification != "retained" || got.Reason != "uncommitted_work" || !got.CommitEligible {
		t.Fatalf("classification = %#v", got)
	}
}

func TestClassifySnapshotRetainsMissingCommit(t *testing.T) {
	got := ClassifySnapshot(context.Background(), stubGit{output: "+ abc"}, reviewSession(1), gitstatus.Snapshot{HasGit: true, HeadOID: "abc", Clean: true}, time.Now(), time.Hour)
	if got.Classification != "retained" || got.Reason != "commits_missing_from_target" || got.MissingCommits != 1 || !got.IntegrateEligible {
		t.Fatalf("classification = %#v", got)
	}
}

func TestClassifySnapshotTreatsIntegratedCommitAsArchiveReadyAfterGrace(t *testing.T) {
	now := time.UnixMilli(20_000)
	got := ClassifySnapshot(context.Background(), stubGit{}, reviewSession(9_500), gitstatus.Snapshot{HasGit: true, HeadOID: "abc", Clean: true}, now, time.Second)
	if got.Classification != "done" || !got.ArchiveReady {
		t.Fatalf("classification = %#v", got)
	}
}

func TestClassifySnapshotTreatsPatchEquivalentCommitAsDoneWithGrace(t *testing.T) {
	now := time.UnixMilli(10_000)
	got := ClassifySnapshot(context.Background(), stubGit{output: "- abc"}, reviewSession(9_500), gitstatus.Snapshot{HasGit: true, HeadOID: "abc", Clean: true}, now, time.Second)
	if got.Classification != "done" || got.Reason != "clean_and_integrated" || got.Equivalent != 1 || got.ArchiveReady {
		t.Fatalf("classification = %#v", got)
	}
}

func TestClassifyAgainstTargetRecognizesConflictResolvedManualCherryPick(t *testing.T) {
	repo := initSessionReviewRepo(t)
	runSessionReviewGit(t, repo, "checkout", "-b", "source")
	writeSessionReviewFile(t, repo, "feature.txt", "source change\n")
	runSessionReviewGit(t, repo, "add", "feature.txt")
	runSessionReviewGit(t, repo, "commit", "-m", "add review feature")
	sourceCommit := sessionReviewGitOutput(t, repo, "rev-parse", "HEAD")

	runSessionReviewGit(t, repo, "checkout", "dev")
	writeSessionReviewFile(t, repo, "feature.txt", "target context\n")
	runSessionReviewGit(t, repo, "add", "feature.txt")
	runSessionReviewGit(t, repo, "commit", "-m", "prepare target context")
	cmd := exec.Command("git", "cherry-pick", sourceCommit)
	cmd.Dir = repo
	if err := cmd.Run(); err == nil {
		t.Fatal("expected the manual cherry-pick to conflict")
	}
	writeSessionReviewFile(t, repo, "feature.txt", "target context\nsource change\n")
	runSessionReviewGit(t, repo, "add", "feature.txt")
	runSessionReviewGit(t, repo, "cherry-pick", "--continue")

	cherry := sessionReviewGitOutput(t, repo, "cherry", "dev", sourceCommit)
	if !strings.HasPrefix(cherry, "+ ") {
		t.Fatalf("git cherry = %q, want false-positive missing source commit", cherry)
	}
	session := reviewSession(1)
	session.WorktreeRootPath = repo
	session.WorktreeBranch = "source"
	got := ClassifySnapshotAgainstTarget(context.Background(), ExecGitRunner{}, session, gitstatus.Snapshot{HasGit: true, HeadOID: sourceCommit, Clean: true}, time.Now(), time.Hour, "dev")
	if got.Classification != "done" || got.Reason != "clean_and_integrated" || got.MissingCommits != 0 || got.Equivalent != 1 {
		t.Fatalf("classification = %#v", got)
	}
}

func TestClassifyAgainstTargetDoesNotReconcileDifferentCommitWithSameSubject(t *testing.T) {
	repo := initSessionReviewRepo(t)
	runSessionReviewGit(t, repo, "checkout", "-b", "source")
	writeSessionReviewFile(t, repo, "source.txt", "source\n")
	runSessionReviewGit(t, repo, "add", "source.txt")
	runSessionReviewGit(t, repo, "commit", "-m", "shared subject")
	sourceCommit := sessionReviewGitOutput(t, repo, "rev-parse", "HEAD")

	runSessionReviewGit(t, repo, "checkout", "dev")
	writeSessionReviewFile(t, repo, "other.txt", "different\n")
	runSessionReviewGit(t, repo, "add", "other.txt")
	runSessionReviewGit(t, repo, "commit", "-m", "shared subject")

	session := reviewSession(1)
	session.WorktreeRootPath = repo
	session.WorktreeBranch = "source"
	got := ClassifySnapshotAgainstTarget(context.Background(), ExecGitRunner{}, session, gitstatus.Snapshot{HasGit: true, HeadOID: sourceCommit, Clean: true}, time.Now(), time.Hour, "dev")
	if got.Classification != "retained" || got.Reason != "commits_missing_from_target" || got.MissingCommits != 1 || got.Equivalent != 0 {
		t.Fatalf("classification = %#v", got)
	}
}

func initSessionReviewRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runSessionReviewGit(t, repo, "init", "-b", "dev")
	runSessionReviewGit(t, repo, "config", "user.name", "Test User")
	runSessionReviewGit(t, repo, "config", "user.email", "test@example.invalid")
	writeSessionReviewFile(t, repo, "base.txt", "base\n")
	runSessionReviewGit(t, repo, "add", "base.txt")
	runSessionReviewGit(t, repo, "commit", "-m", "base")
	return repo
}

func writeSessionReviewFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runSessionReviewGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func sessionReviewGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestClassifySnapshotFailsClosedWhenTargetUnavailable(t *testing.T) {
	got := ClassifySnapshot(context.Background(), stubGit{err: errors.New("missing ref")}, reviewSession(1), gitstatus.Snapshot{HasGit: true, HeadOID: "abc", Clean: true}, time.Now(), time.Hour)
	if got.Classification != "retained" || got.Reason != "target_branch_unavailable" {
		t.Fatalf("classification = %#v", got)
	}
}

func TestClassifySnapshotUsesExplicitCheckoutTargetWhenStoredBaseIsMissing(t *testing.T) {
	session := reviewSession(1)
	session.WorktreeBaseBranch = ""
	got := classifySnapshotAgainstTarget(context.Background(), stubGit{}, session, gitstatus.Snapshot{HasGit: true, HeadOID: "abc", Clean: true}, time.Now(), time.Hour, "main")
	if got.Classification != "done" || got.TargetBranch != "main" || got.WorktreePath != "/tmp/worktree" {
		t.Fatalf("classification = %#v", got)
	}
}

func TestClassifyCurrentCheckoutOffersCommitForDirtyRegularSession(t *testing.T) {
	session := reviewSession(1)
	session.WorktreeEnabled = false
	session.WorktreeRootPath = ""
	session.WorktreeBranch = ""
	got := ClassifyCurrentCheckout(session, gitstatus.Snapshot{WorkspacePath: "/workspace", HasGit: true, HeadOID: "abc", Branch: "dev", Clean: false, DirtyCount: 3}, time.Now(), time.Hour)
	if got.Classification != "retained" || got.Reason != "current_checkout_uncommitted_work" || !got.CurrentCheckout || !got.CommitEligible || got.WorktreePath != "/workspace" || got.WorktreeBranch != "dev" {
		t.Fatalf("classification = %#v", got)
	}
}

func TestClassifyCurrentCheckoutRequiresSeparateArchiveAfterCleanRecheck(t *testing.T) {
	now := time.UnixMilli(20_000)
	session := reviewSession(9_500)
	session.WorktreeEnabled = false
	got := ClassifyCurrentCheckout(session, gitstatus.Snapshot{WorkspacePath: "/workspace", HasGit: true, HeadOID: "abc", Branch: "dev", Clean: true}, now, time.Second)
	if got.Classification != "done" || got.Reason != "current_checkout_clean" || !got.ArchiveReady || got.CommitEligible {
		t.Fatalf("classification = %#v", got)
	}
}
