package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement (brief item 6, review finding): swarm_get_job on the job of a
// reviewer an automation step allowed to commit security fixes shows its own
// fix commits and head, on its agent/review-<run> branch, with the reviewed
// head as base. A reviewer's delivery assessment is report-only and never
// reports a head, so the result must not depend on it. A fix checkout that
// cannot be read (rewritten below the reviewed head) shows result.fix_error
// and no head. A reviewer without fixes is never read this way.
// Threat: the reviewer job's result showing no head or commits, or a head
// that hides lost fixes, so the AI or user reading it cannot see what the
// reviewer changed.
// Owners: controlMCPGetJob -> controlMCPCall.jobView ->
// fillReviewFixResult -> Server.reviewFixCheckout, through the daemon's real
// worker run route (GET /v3/workers/{id}/runs/{run}), worker/session store and
// real Git. No model provider or sandbox is involved.
func TestGetJobShowsAFixingReviewersCommitsAndHead(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	ctx := context.Background()
	reviewer, err := f.server.sessions.CreateWorker(ctx, p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Security reviewer", IdempotencyKey: "getjob-fix-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "main")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "base")
	base := outboxPinGit(t, folder, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(folder, "upload.go"), []byte("package upload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, folder, "add", ".")
	outboxPinGit(t, folder, "commit", "-q", "-m", "coder work")
	reviewed := outboxPinGit(t, folder, "rev-parse", "HEAD")
	store := f.server.sessions.Store()
	reviewedJob := "worker/" + f.workerID + "/run-coder"

	// reviewerJob records a reviewer job as the worker execution service
	// does: its run, and its session on agent/review-<run> in its own clone.
	reviewerJob := func(runID string, fixes, commit bool) (string, string) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), runID)
		outboxPinGit(t, filepath.Dir(dir), "clone", "-q", folder, dir)
		outboxPinGit(t, dir, "checkout", "-q", "-b", "agent/review-"+runID, reviewed)
		if commit {
			if err := os.WriteFile(filepath.Join(dir, "upload.go"), []byte("package upload\n\n// escaped\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			outboxPinGit(t, dir, "commit", "-q", "-am", "escape the upload name")
		}
		if err := store.CreateSession(pebblestore.SessionSnapshot{ID: "sess-" + runID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: folder, Title: "Reviewer",
			WorktreeEnabled: true, WorktreeRootPath: dir, WorktreeBranch: "agent/review-" + runID, WorktreeBaseBranch: reviewed, CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		input := map[string]any{"prompt": "Review.", "review_job": reviewedJob, "captured_review_target": map[string]any{"head": reviewed, "base": base, "job": reviewedJob}}
		if fixes {
			input["review_fixes"] = jobautomation.FixesSecurity
		}
		if _, err := store.WorkerStore().RecordWorkerRun(p.AccountScopeID, pebblestore.WorkerRunRecord{ID: runID, WorkerID: reviewer.ID, UserID: p.UserID, Role: pebblestore.WorkerRoleReviewer,
			SessionID: "sess-" + runID, Status: "succeeded", RequestSource: jobautomation.RequestSourceAutomation, Input: input, CreatedAt: 1, StartedAt: 1, CompletedAt: 2}); err != nil {
			t.Fatal(err)
		}
		return controlMCPJobID(controlMCPJobWorker, reviewer.ID, runID), dir
	}
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read", "automations:read"}})
	call := &controlMCPCall{request: req, next: f.server.internalRouteHandler(), server: f.server}
	getJob := func(jobID string) map[string]any {
		t.Helper()
		out, err := controlMCPGetJob(call, map[string]any{"job_id": jobID})
		if err != nil {
			t.Fatalf("swarm_get_job %s: %v", jobID, err)
		}
		return controlMCPMap(controlMCPMap(out)["result"])
	}

	// A fix commit: the result shows it and the reviewer's head.
	fixJob, fixDir := reviewerJob("runfix1", true, true)
	fixHead := outboxPinGit(t, fixDir, "rev-parse", "HEAD")
	result := getJob(fixJob)
	commits, _ := result["commits"].([]string)
	if result["head"] != fixHead || result["branch"] != "agent/review-runfix1" || result["base_commit"] != reviewed ||
		len(commits) != 1 || commits[0] != fixHead+" escape the upload name" || result["fix_error"] != nil {
		t.Fatalf("fixing reviewer job result = %+v", result)
	}

	// No fix commits: the reviewed head, no commits.
	cleanJob, _ := reviewerJob("runfix2", true, false)
	result = getJob(cleanJob)
	if commits, _ := result["commits"].([]string); result["head"] != reviewed || len(commits) != 0 || result["fix_error"] != nil {
		t.Fatalf("fixing reviewer without commits result = %+v", result)
	}

	// A branch rewritten below the reviewed head: fix_error, no head.
	lostJob, lostDir := reviewerJob("runfix3", true, true)
	outboxPinGit(t, lostDir, "reset", "-q", "--hard", base)
	result = getJob(lostJob)
	if result["head"] != nil || !strings.Contains(controlMCPString(result, "fix_error"), "does not descend from the reviewed head") {
		t.Fatalf("rewritten fix branch result = %+v", result)
	}

	// A reviewer without fixes: not read from its checkout.
	roJob, _ := reviewerJob("runro", false, true)
	result = getJob(roJob)
	if result["fix_error"] != nil || result["branch"] == "agent/review-runro" || result["base_commit"] == reviewed {
		t.Fatalf("read-only reviewer job result = %+v", result)
	}
}
