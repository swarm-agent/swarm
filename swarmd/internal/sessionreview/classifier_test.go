package sessionreview

import (
	"context"
	"errors"
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
