package run

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// Purpose: a reviewer allowed to commit security fixes answers OK after
// fixing, so its OK approves the reviewed head only with its fix commits on
// top. recordReviewResult must bind the recorded review to those commits
// (FixHead), and the reviewed job's review projection (what swarm_get_job
// shows on the coder job before swarm_deliver_job) must then read as stale,
// never as a plain, current OK of the coder's unfixed head. This holds
// whatever the automation step later decides, including fixes over the
// 80-line limit. A fixing reviewer that committed nothing is a plain OK of
// the reviewed head. An OK whose fix checkout cannot be read is recorded as
// BLOCK ("reviewer fixes unreadable"): what it approved is unknown.
// Threat (review finding): the coder job shown as reviewed OK and not stale
// while the approval depended on fixes that were rejected (too large) or
// never applied, so swarm_deliver_job merges the unfixed head.
// Owners: WorkerExecutionService.recordReviewResult (withReviewFixHead,
// worktree.ReviewFixHead), WorkerStore.RecordJobReview and
// WorkerExecutionService.RunReview, over a real Pebble store, session service
// and real Git checkouts. The model provider is the only boundary replaced:
// the reviewer's final message is written into its session directly.
func TestFixingReviewerOKIsBoundToItsFixCommits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	coderDir := t.TempDir()
	git(coderDir, "init", "-q", "-b", "agent/worker-run-coder")
	if err := os.WriteFile(filepath.Join(coderDir, "upload.go"), []byte("package upload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(coderDir, "add", ".")
	git(coderDir, "commit", "-q", "-m", "coder work")
	head := git(coderDir, "rev-parse", "HEAD")

	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "review-fix-record.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	ws := sessions.Store().WorkerStore()
	coder, err := ws.CreateWorker("account-a", "user-a", pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleCoder, Name: "Coder", IdempotencyKey: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := ws.CreateWorker("account-a", "user-a", pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Reviewer", IdempotencyKey: "reviewer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pref := pebblestore.ModelPreference{Provider: "fixture-provider", Model: "reviewer-model", Thinking: "high"}
	coderSession, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "session-coder", UserID: "user-a", AccountScopeID: "account-a", WorkspacePath: coderDir, Preference: &pref,
		Metadata: map[string]any{"worker_role": pebblestore.WorkerRoleCoder, "worker_execution_run_id": "run-coder", "project_id": "project-a", "task_id": "task-coder"}})
	if err != nil {
		t.Fatal(err)
	}
	coderRun, err := ws.RecordWorkerRun("account-a", pebblestore.WorkerRunRecord{ID: "run-coder", WorkerID: coder.ID, UserID: "user-a", Role: pebblestore.WorkerRoleCoder, SessionID: coderSession.ID,
		Status: "succeeded", RequestSource: "direct", CreatedAt: 1, StartedAt: 1, CompletedAt: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.Store().PutProjectTask("account-a", &pebblestore.ProjectTaskRecord{ID: "task-coder", Title: "Coder job", ProjectID: "project-a", AccountID: "account-a", SessionID: coderSession.ID,
		Agent: "coder", Status: "needs_review", WorkerID: coder.ID, WorkerRunID: "run-coder", WorkspacePath: coderDir}); err != nil {
		t.Fatal(err)
	}
	coderJob := "worker/" + coder.ID + "/run-coder"
	service := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: &Service{sessions: sessions}, trees: &worktree.Service{}}}

	// review records one fixing reviewer job: its own agent/review-<run>
	// checkout at the reviewed head (edit commits on it), its session pinned
	// with worker_review_fixes, and its final OK report.
	at := int64(10)
	review := func(runID string, edit func(dir string)) pebblestore.JobReview {
		t.Helper()
		dir := filepath.Join(t.TempDir(), runID)
		git(filepath.Dir(dir), "clone", "-q", coderDir, dir)
		git(dir, "checkout", "-q", "-b", reviewFixBranch(runID), head)
		if edit != nil {
			edit(dir)
		}
		session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "session-" + runID, UserID: "user-a", AccountScopeID: "account-a", WorkspacePath: coderDir, Preference: &pref,
			Worktree: &sessionruntime.CreateSessionWorktree{RootPath: dir, BaseBranch: head, BranchName: reviewFixBranch(runID)},
			Metadata: map[string]any{"worker_role": pebblestore.WorkerRoleReviewer, "worker_execution_run_id": runID, "worker_review_fixes": ReviewFixesSecurity,
				"worker_review_target": map[string]any{"head": head, "base": strings.Repeat("a", 40), "job": coderJob}}})
		if err != nil {
			t.Fatal(err)
		}
		if !session.WorktreeEnabled || session.WorktreeRootPath != dir {
			t.Fatalf("reviewer session worktree = %v %q", session.WorktreeEnabled, session.WorktreeRootPath)
		}
		if _, _, _, err := sessions.AppendMessage(session.ID, "assistant", strings.Replace(reviewRecordMessage("OK", "1. FIXED high upload.go:2 the name was not escaped"), "none (review is read-only)", "go test ./upload", 1), nil); err != nil {
			t.Fatal(err)
		}
		at++
		run, err := ws.RecordWorkerRun("account-a", pebblestore.WorkerRunRecord{ID: runID, WorkerID: reviewer.ID, UserID: "user-a", Role: pebblestore.WorkerRoleReviewer, SessionID: session.ID,
			Status: "succeeded", RequestSource: "automation", Input: map[string]any{"review_job": coderJob, "review_fixes": ReviewFixesSecurity}, CreatedAt: at, StartedAt: at, CompletedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		if err := service.recordReviewResult(run); err != nil {
			t.Fatal(err)
		}
		recorded, err := ws.GetJobReviewByReviewer("account-a", coder.ID, "run-coder", "worker/"+reviewer.ID+"/"+runID)
		if err != nil || recorded == nil {
			t.Fatalf("recorded review = %v %v", recorded, err)
		}
		return *recorded
	}
	projection := func() map[string]any {
		t.Helper()
		got, err := service.RunReview(coderRun)
		if err != nil || got == nil || got.JobReview == nil {
			t.Fatalf("coder job review = %+v %v", got, err)
		}
		encoded, _ := json.Marshal(got)
		var out map[string]any
		_ = json.Unmarshal(encoded, &out)
		return out
	}

	// Control: a fixing reviewer that committed nothing is a plain OK of
	// the reviewed head, current while the coder's checkout is at it.
	clean := review("run-review-clean", nil)
	if clean.Verdict != "ok" || clean.Head != head || clean.FixHead != "" {
		t.Fatalf("fixing reviewer without commits = %+v", clean)
	}
	if view := projection(); view["verdict"] != "ok" || view["stale"] != false || view["fix_head"] != nil {
		t.Fatalf("coder job after a plain OK = %+v", view)
	}

	// OK with 81 lines of fix commits (over the automation's limit): the
	// record carries the fix head and the coder job's review is stale.
	fixed := review("run-review-large", func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte("package upload\n"+strings.Repeat("// line\n", 80)), 0o644); err != nil {
			t.Fatal(err)
		}
		git(dir, "add", ".")
		git(dir, "commit", "-q", "-m", "reviewer: rewrite")
	})
	if fixed.Verdict != "ok" || fixed.Head != head || fixed.FixHead == "" || fixed.FixHead == head {
		t.Fatalf("fixing reviewer with commits = %+v", fixed)
	}
	view := projection()
	if view["verdict"] != "ok" || view["fix_head"] != fixed.FixHead || view["stale"] != true || view["head"] != head {
		t.Fatalf("coder job after an OK that depends on fixes = %+v", view)
	}
	if git(coderDir, "rev-parse", "HEAD") != head {
		t.Fatal("the coder's checkout moved")
	}

	// OK whose fix checkout is not on its branch: recorded as BLOCK.
	unreadable := review("run-review-lost", func(dir string) { git(dir, "checkout", "-q", "--detach") })
	if unreadable.Verdict != "block" || !strings.Contains(unreadable.Reason, "reviewer fixes unreadable") || unreadable.FixHead != "" {
		t.Fatalf("fixing reviewer with an unreadable checkout = %+v", unreadable)
	}
	if view := projection(); view["verdict"] != "block" {
		t.Fatalf("coder job after an unreadable fixing OK = %+v", view)
	}
}
