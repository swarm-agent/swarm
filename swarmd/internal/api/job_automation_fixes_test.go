package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: an agent step's fixes ("security") is accepted only when its
// worker is a reviewer. swarm_set_automation (and then) and the API refuse it
// for a role name, a worker id or a worker name that is not a reviewer, and
// for a worker that does not resolve; an agent step's job start refuses it
// for a non-reviewer worker too, before anything is admitted.
// Threat: a coder (or an unknown worker guessed to be a reviewer) given the
// fixing reviewer's writable checkout, or a definition stored with fixes the
// daemon would later grant.
// Owners: jobautomation.NormalizeSteps, Server.jobAutomationFixesError (via
// controlMCPCall.automationSteps and handleJobAutomations) and
// jobAutomationAgents.Start, over the real worker store and routes.
func TestAgentStepFixesIsRefusedForNonReviewerWorkers(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	reviewer, err := f.server.sessions.CreateWorker(context.Background(), p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Security reviewer", IdempotencyKey: "fixes-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	step := func(worker string) []jobautomation.Step {
		steps := []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: worker, Brief: "Review.", Fixes: "security"}}
		if err := jobautomation.NormalizeSteps(steps); err != nil {
			t.Fatalf("normalize %s: %v", worker, err)
		}
		return steps
	}
	for _, worker := range []string{"reviewer", reviewer.ID, "Security reviewer"} {
		if err := f.server.jobAutomationFixesError(p.AccountScopeID, step(worker)); err != nil {
			t.Fatalf("fixes on reviewer %q refused: %v", worker, err)
		}
	}
	for _, worker := range []string{f.workerID, "no-such-worker"} {
		err := f.server.jobAutomationFixesError(p.AccountScopeID, step(worker))
		if err == nil || !jobautomation.IsValidationError(err) || !strings.Contains(err.Error(), "only for a reviewer worker") {
			t.Fatalf("fixes on %q = %v, want a refusal", worker, err)
		}
	}
	// A role name that is not a reviewer is refused by the shared validator.
	for _, raw := range []string{
		`[{"id":"review","type":"agent","worker":"coder","brief":"Review.","fixes":"security"}]`,
		`[{"id":"review","type":"agent","worker":"reviewer","brief":"Review.","fixes":"everything"}]`,
		`[{"id":"push","type":"run","command":"deliver/push.sh","fixes":"security"}]`,
	} {
		var steps any
		if err := json.Unmarshal([]byte(raw), &steps); err != nil {
			t.Fatal(err)
		}
		if _, err := controlMCPAutomationSteps(steps); err == nil {
			t.Fatalf("steps %s were accepted", raw)
		}
	}

	// swarm_set_automation refuses a coder worker id and stores nothing.
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "fixes.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f.server.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: jobautomation.NewStore(store)}))
	writer := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
		&pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "automations:read", "automations:write"}})
	call := &controlMCPCall{request: writer, next: f.server.internalRouteHandler(), server: f.server}
	folder := t.TempDir()
	set := func(worker string) error {
		_, err := controlMCPSetAutomation(call, map[string]any{"name": "review " + worker, "scope": map[string]any{"folder": folder},
			"steps": []any{map[string]any{"id": "review", "type": "agent", "worker": worker, "brief": "Review.", "fixes": "security"}}})
		return err
	}
	if err := set(f.workerID); err == nil || !strings.Contains(err.Error(), "only for a reviewer worker") {
		t.Fatalf("swarm_set_automation with fixes on a coder = %v", err)
	}
	if err := set(reviewer.ID); err != nil {
		t.Fatalf("swarm_set_automation with fixes on a reviewer = %v", err)
	}
	list, err := jobautomation.NewStore(store).List(p.AccountScopeID)
	if err != nil || len(list) != 1 || list[0].Steps[0].Fixes != jobautomation.FixesSecurity || list[0].Steps[0].Worker != reviewer.ID {
		t.Fatalf("stored automations = %+v %v", list, err)
	}

	// The API refuses the same with 400 and stores nothing more.
	post := func(body string) *httptest.ResponseRecorder {
		r := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, JobAutomationsPath, strings.NewReader(body))),
			&pebblestore.ScopedTokenRecord{Scopes: []string{"automations:write"}})
		w := httptest.NewRecorder()
		f.server.handleJobAutomations(w, r)
		return w
	}
	bad := `{"name":"coder fixes","scope":{"folder":"` + folder + `"},"steps":[{"id":"review","type":"agent","worker":"` + f.workerID + `","brief":"Review.","fixes":"security"}]}`
	if w := post(bad); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "only for a reviewer worker") {
		t.Fatalf("API create with fixes on a coder = %d %s", w.Code, w.Body)
	}
	if list, _ := jobautomation.NewStore(store).List(p.AccountScopeID); len(list) != 1 {
		t.Fatalf("a refused definition was stored: %d automations", len(list))
	}

	// The job start refuses fixes for a non-reviewer worker before admission.
	_, err = f.server.JobAutomationAgents().Start(context.Background(), jobautomation.AgentRequest{Account: p.AccountScopeID, Folder: folder, Worker: f.workerID,
		Brief: "Fix it.", Fixes: jobautomation.FixesSecurity, IdempotencyKey: "jdel_fix.review.0",
		Result: jobautomation.Result{V: 1, JobID: "worker/" + f.workerID + "/run-1", Kind: jobautomation.KindWorker, Status: jobautomation.StatusSucceeded}})
	var refused *jobautomation.RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "only for a reviewer worker") {
		t.Fatalf("start with fixes on a coder = %v", err)
	}
	runs, _, err := f.server.sessions.Store().WorkerStore().ListWorkerRuns(p.AccountScopeID, f.workerID, 10, "")
	if err != nil || len(runs) != 0 {
		t.Fatalf("a refused start admitted %d runs (%v)", len(runs), err)
	}
}

// Requirement: a reviewer's fixes are measured with Git in the folder after
// both commits are brought in from their checkouts: added plus removed lines
// from the delivery head to the reviewer's head, a binary file counting as
// more than the limit.
// Threat: a large rewrite or a binary blob slipping under the 80-line limit
// because it was counted as nothing.
// Owners: JobHeadChangedLines (importJobHead, git diff --numstat) and
// countNumstatLines, over real Git checkouts.
func TestJobHeadChangedLinesCountsAddedAndRemovedLines(t *testing.T) {
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(folder, "upload.go"), []byte("package upload\n\nfunc Name(s string) string { return s }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, folder, "add", ".")
	outboxPinGit(t, folder, "commit", "-q", "-m", "base")
	coder := filepath.Join(t.TempDir(), "coder")
	outboxPinGit(t, filepath.Dir(coder), "clone", "-q", folder, coder)
	outboxPinGit(t, coder, "commit", "-q", "--allow-empty", "-m", "coder work")
	jobHead := outboxPinGit(t, coder, "rev-parse", "HEAD")
	reviewer := filepath.Join(t.TempDir(), "reviewer")
	outboxPinGit(t, filepath.Dir(reviewer), "clone", "-q", coder, reviewer)
	// One line replaced (1 removed, 1 added) and two added: 4 lines.
	if err := os.WriteFile(filepath.Join(reviewer, "upload.go"), []byte("package upload\n\nimport \"html\"\n\nfunc Name(s string) string { return html.EscapeString(s) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, reviewer, "commit", "-q", "-am", "escape the name")
	fixHead := outboxPinGit(t, reviewer, "rev-parse", "HEAD")

	ctx := context.Background()
	lines, err := JobHeadChangedLines(ctx, folder, jobautomation.HeadRef{Commit: jobHead, Worktree: coder}, jobautomation.HeadRef{Commit: fixHead, Worktree: reviewer})
	if err != nil || lines != 4 {
		t.Fatalf("changed lines = %d %v, want 4", lines, err)
	}
	if err := os.WriteFile(filepath.Join(reviewer, "blob.bin"), []byte{0, 1, 2, 0, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, reviewer, "add", ".")
	outboxPinGit(t, reviewer, "commit", "-q", "-m", "binary")
	binaryHead := outboxPinGit(t, reviewer, "rev-parse", "HEAD")
	lines, err = JobHeadChangedLines(ctx, folder, jobautomation.HeadRef{Commit: jobHead, Worktree: coder}, jobautomation.HeadRef{Commit: binaryHead, Worktree: reviewer})
	if err != nil || lines <= jobautomation.MaxReviewerFixLines {
		t.Fatalf("a binary change counted %d lines (%v), want over %d", lines, err, jobautomation.MaxReviewerFixLines)
	}
	if _, err := JobHeadChangedLines(ctx, folder, jobautomation.HeadRef{Commit: "HEAD~1"}, jobautomation.HeadRef{Commit: fixHead, Worktree: reviewer}); err == nil {
		t.Fatal("a ref name was accepted as a commit id")
	}
	if _, err := countNumstatLines("12\tnot-a-number\n"); err == nil {
		t.Fatal("a malformed numstat line was counted")
	}
}

// Requirement: swarm_get_job (on the reviewed job) and the delivery views
// show the moved head and the reviewer's applied fix commits; fix commits a
// BLOCK verdict or the line limit kept out are not shown as the delivery's.
// Owner: jobDeliveryView.
func TestDeliveryViewShowsAppliedReviewerFixCommits(t *testing.T) {
	head := strings.Repeat("b", 40)
	d := jobautomation.Delivery{ID: "jdel_1", JobID: "worker/w/r", Head: head, State: jobautomation.StateSucceeded, Steps: []jobautomation.StepRecord{
		{ID: "review", Type: jobautomation.StepAgent, State: jobautomation.StateSucceeded, Outcome: map[string]any{"fixes_applied": true,
			"fix_commits": []any{map[string]any{"sha": head, "subject": "escape the name"}}, "moved_head": map[string]any{"from": strings.Repeat("a", 40), "to": head}}},
		{ID: "second", Type: jobautomation.StepAgent, State: jobautomation.StateSucceeded, Outcome: map[string]any{"fixes_applied": false,
			"fix_commits": []any{map[string]any{"sha": strings.Repeat("c", 40), "subject": "not applied"}}}},
	}}
	view := jobDeliveryView(d)
	commits, _ := view["fix_commits"].([]any)
	if view["head"] != head || len(commits) != 1 || commits[0].(map[string]any)["subject"] != "escape the name" {
		t.Fatalf("delivery view = %+v", view)
	}
	steps, _ := view["steps"].([]map[string]any)
	if len(steps) != 2 || steps[0]["outcome"].(map[string]any)["moved_head"] == nil {
		t.Fatalf("delivery steps = %+v", steps)
	}
}
