package run

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"
)

// Purpose: WorkerExecutionService.Dispatch must materialize a reviewer session
// detached at a captured head, enqueue only after sandbox registration, preserve
// context on replay, and remove dirty output on terminal acknowledgement. A
// deterministic local engine fixture executes real Git through the routing
// boundary; it does NOT prove mount enforcement (the Docker test does that).
func TestReviewerAdmissionAndTerminalCleanup(t *testing.T) {
	for _, tc := range []struct{ name, report, status, want string }{
		{"ok", "VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: test", sessions.RunIntentCompleted, "ok"},
		{"block", "VERDICT: BLOCK\nSUMMARY: broken\nFINDINGS: 1. high file:1\nTESTS RUN: failed", sessions.RunIntentCompleted, "block"},
		{"garbage", "garbage", sessions.RunIntentCompleted, "block"},
		{"missing", "", sessions.RunIntentCompleted, "block"},
		{"failed", "VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: test", sessions.RunIntentFailed, "block"},
		{"stopped", "VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: test", sessions.RunIntentCancelled, "block"},
	} {
		t.Run(tc.name, func(t *testing.T) { testReviewerTerminal(t, tc.report, tc.status, tc.want) })
	}
}

func testReviewerTerminal(t *testing.T, report, status, want string) {
	_, ss, s, workspace := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()
	create := func(role string) store.WorkerRecord {
		t.Helper()
		w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: role, Role: role}, nil)
		if err != nil {
			t.Fatal(err)
		}
		w, err = s.Activate("account", "owner", w.ID, w.Revision, nil)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	coder := create(store.WorkerRoleCoder)
	job, err := s.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{WorkerID: coder.ID, WorkspaceID: workspace, RequestSource: "direct", Input: map[string]any{"prompt": "source brief"}, IdempotencyKey: "source"})
	if err != nil {
		t.Fatal(err)
	}
	source, _, _ := ss.GetSession(job.SessionID)
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(source.WorktreeRootPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "change")
	head := git(source.WorktreeRootPath, "rev-parse", "HEAD")
	// This fixture implements only the engine protocol needed by admission's
	// routed Git. It is deliberately not an enforcement or provider benchmark.
	scratch := t.TempDir()
	engine := filepath.Join(scratch, "engine")
	script := `#!/bin/sh
case "$1" in
 version) echo fixture;;
 image) echo sha256:fixture;;
 info) echo '{}';;
 network) echo fixture;;
 container) exit 1;;
 run|rm|start) exit 0;;
 volume) exit 0;;
 exec)
 shift
 while [ "$#" -gt 0 ]; do
 case "$1" in
 -w) cd "$2" || exit 1; shift 2;;
 -e) export "$2"; shift 2;;
 -i) shift;;
 *) shift; break;;
 esac
 done
 exec "$@";;
 *) exit 1;;
esac
`
	if err := os.WriteFile(engine, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeRequired, Engine: engine, UID: 10001, GID: 10001, StateDir: scratch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bucket := filepath.Dir(source.WorktreeRootPath)
	m.SetLayout(sandbox.Layout{Projects: func() ([]string, error) { return []string{source.WorkspacePath}, nil }, WorktreesRoot: filepath.Dir(bucket), Bucket: func(string) (string, error) { return filepath.Base(bucket), nil }})
	sandbox.SetDefault(m)
	defer sandbox.SetDefault(nil)
	reviewer := create(store.WorkerRoleReviewer)
	req := store.WorkerRunAdmission{WorkerID: reviewer.ID, WorkspaceID: workspace, RequestSource: "direct", Input: map[string]any{"prompt": "review independently", "review_job": "worker/" + coder.ID + "/" + job.ID}, IdempotencyKey: "review"}
	r, err := s.Dispatch(context.Background(), "account", "owner", req)
	if err != nil {
		t.Fatal(err)
	}
	review, found, err := ss.GetSession(r.SessionID)
	if err != nil || !found {
		t.Fatal(err)
	}
	if git(review.WorktreeRootPath, "rev-parse", "HEAD") != head {
		t.Fatal("wrong head")
	}
	if out, err := exec.Command("git", "-C", review.WorktreeRootPath, "symbolic-ref", "--quiet", "HEAD").CombinedOutput(); err == nil {
		t.Fatal("review is not detached", string(out))
	}
	env := reviewEnvironment(review.Metadata)
	if env["SWARM_REVIEW_HEAD"] != head || env["SWARM_REVIEW_BASE"] != "dev" || env["SWARM_REVIEW_JOB"] != req.Input["review_job"] {
		t.Fatal(env)
	}
	scope, err := m.CommandScope(review.WorktreeRootPath)
	if err != nil || scope.ReviewCheckout != review.WorktreeRootPath {
		t.Fatal(scope, err)
	}
	// A later source commit must neither change retry identity nor captured head.

	replay, err := s.Dispatch(context.Background(), "account", "owner", req)
	if err != nil || replay.ID != r.ID {
		t.Fatal(replay, err)
	}
	if err := os.WriteFile(filepath.Join(review.WorktreeRootPath, "output"), []byte("build"), 0600); err != nil {
		t.Fatal(err)
	}
	intent, found, err := ss.GetSessionRunIntent(r.SessionID, r.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	intent.Status = status
	if report != "" {
		_, err = ss.ApplySessionMutation(sessions.SessionMutationInput{SessionID: r.SessionID, AccountScopeID: "account", UserID: "owner", Kind: sessions.SessionMutationAppendMessage, ClientRequestID: "report", IdempotencyKey: "report", PayloadHash: "report", RequestHash: "report", EventType: "session.message.appended", Message: &store.MessageSnapshot{ID: "report", Role: "assistant", Content: report}, NowUnixMs: time.Now().UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = ss.ApplySessionMutation(sessions.SessionMutationInput{SessionID: r.SessionID, AccountScopeID: "account", UserID: "owner", Kind: sessions.SessionMutationRecordRunIntent, ClientRequestID: "review-complete", IdempotencyKey: "review-complete", PayloadHash: "review-complete", RequestHash: "review-complete", EventType: "session.run_intent.updated", RunIntent: &intent, NowUnixMs: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	// Inject a cleanup failure by presenting a mismatched captured head. The
	// registration must remain restrictive until the successful retry.
	if err := s.host.trees.RemoveReviewWorkspace(review.WorkspacePath, review.WorktreeRootPath, r.SessionID, "wrong-head"); err == nil {
		t.Fatal("cleanup failure not injected")
	}
	if scope, err := m.CommandScope(review.WorktreeRootPath); err != nil || scope.ReviewCheckout == "" {
		t.Fatal("failed cleanup downgraded scope", scope, err)
	}
	if err := s.observeRun(r); err != nil {
		t.Fatal(err)
	}
	evidence, err := ws.GetJobReview("account", coder.ID, job.ID)
	if err != nil || evidence == nil || evidence.Verdict != want || evidence.Head != head {
		t.Fatal(evidence, err)
	}
	view, err := s.RunReview(job)
	if err != nil || view == nil || view.JobReview == nil || view.Stale {
		t.Fatal(view, err)
	}
	git(source.WorktreeRootPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "later")
	view, err = s.RunReview(job)
	if err != nil || view == nil || view.JobReview == nil || !view.Stale {
		t.Fatal(view, err)
	}
	if _, err := os.Stat(review.WorktreeRootPath); !os.IsNotExist(err) {
		t.Fatal("checkout retained", err)
	}
	final, _, err := ws.GetWorkerRun("account", reviewer.ID, r.ID)
	if err != nil || (status == sessions.RunIntentCompleted && final.Status != "succeeded") || len(final.Deliverables) != 0 {
		t.Fatal(final, err)
	}
}
