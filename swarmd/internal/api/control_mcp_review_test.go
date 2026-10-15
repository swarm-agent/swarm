package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// controlMCPDirectCall is a Swarm Control call carrying a verified principal
// and a scoped token with exactly scopes, dispatching into next.
func controlMCPDirectCall(ctx context.Context, p identity.Principal, next http.Handler, server *Server, scopes ...string) *controlMCPCall {
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r = r.WithContext(context.WithValue(context.WithValue(identity.ContextWithPrincipal(ctx, p), productPrincipalRequestContextKey, p),
		productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: scopes}))
	return &controlMCPCall{request: r, next: next, server: server}
}

func controlMCPEncode(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// Requirement: an AI with a manage-level key can move an orchestrator task
// out of pending_approval the way the Desktop's Approve and Reject buttons do,
// through the same route with the task's own session/plan guards, and only
// for a task actually waiting for approval. Threats: a read-only connection
// launching work, approval of a task in another status (for example a
// needs_review task, which must go through integrate_task), or a decision
// that leaves no trace in the task record. Owners: controlMCPApproveTask, the
// route allowlist and POST /v3/projects/{id}/tasks/{tid}/approve|reject. The
// matrix fixture (real temporary stores, project and source binding) through
// the API mux is the narrowest layer that runs the guards and the stored
// transition together; no provider runs.
func TestControlMCPApprovesPendingTask(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer func() {
		f.server.BeginShutdown()
		f.server.CancelInFlightRuns()
		if !f.server.WaitForInFlightRuns(5 * time.Second) {
			t.Error("fixture runs did not stop before store cleanup")
			return
		}
		f.db.Close()
	}()
	projectID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	seed := func(taskID, status string) {
		t.Helper()
		sess := pebblestore.SessionSnapshot{ID: "session-" + taskID, UserID: f.userID, AccountScopeID: f.accountID, CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()}
		f.bindReviewSource(t, projectID, taskID, &sess)
		if err := f.seedSession(sess); err != nil {
			t.Fatal(err)
		}
		f.seedProjectTask(&pebblestore.ProjectTaskRecord{ID: taskID, ProjectID: projectID, AccountID: f.accountID, Title: "Direct coder task", Description: "Implement fix", Agent: "coder", Status: status, SessionID: sess.ID, BaseCommit: "base-commit", CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()})
	}
	seed("task-approve", "pending_approval")
	seed("task-reject", "pending_approval")
	seed("task-review", "needs_review")
	stored := func(taskID string) string {
		t.Helper()
		task, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, projectID, taskID)
		if err != nil || !found {
			t.Fatalf("task %s: %v", taskID, err)
		}
		return task.Status
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	write := controlMCPDirectCall(ctx, p, f.server.apiMux(), f.server, "sessions:read", "sessions:write")
	read := controlMCPDirectCall(ctx, p, f.server.apiMux(), f.server, "sessions:read")

	for _, tc := range []struct {
		name string
		call *controlMCPCall
		args map[string]any
		want string
	}{
		{"read grant", read, map[string]any{"project_id": projectID, "task_id": "task-approve", "action": "approve"}, "HTTP 403"},
		{"needs_review task", write, map[string]any{"project_id": projectID, "task_id": "task-review", "action": "approve"}, "integrate_task"},
		{"unknown task", write, map[string]any{"project_id": projectID, "task_id": "task-missing", "action": "approve"}, "swarm_list_tasks"},
		{"bad action", write, map[string]any{"project_id": projectID, "task_id": "task-approve", "action": "accept"}, "approve or reject"},
	} {
		if _, err := controlMCPApproveTask(tc.call, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want %q, got %v", tc.name, tc.want, err)
		}
	}
	if stored("task-approve") != "pending_approval" || stored("task-review") != "needs_review" {
		t.Fatal("refused decisions changed a task")
	}
	approved, err := controlMCPApproveTask(write, map[string]any{"project_id": projectID, "task_id": "task-approve", "action": "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if out := approved.(map[string]any); out["status"] != "approved" || stored("task-approve") == "pending_approval" {
		t.Fatalf("approve not applied: %+v, stored %s", out, stored("task-approve"))
	}
	if _, err := controlMCPApproveTask(write, map[string]any{"project_id": projectID, "task_id": "task-reject", "action": "reject"}); err != nil {
		t.Fatal(err)
	}
	if got := stored("task-reject"); got != "rejected" {
		t.Fatalf("reject stored %q", got)
	}
}

// Requirement: a plan that finished in waiting_review can be closed over
// Swarm Control with the Desktop's checkpoint accept, which picks the
// checkpoint actually waiting for review; a plan with nothing to review is
// refused unchanged and a read-only key cannot accept. Threat: plans stranded
// in waiting_review, or a review accepted on a checkpoint still running.
// Owners: controlMCPAcceptReview and the plan-mode checkpoint accept route
// (PlanLifecycleService.AcceptCheckpoint). The routed session server with real
// stores is the narrowest layer that runs the route and the plan save.
func TestControlMCPAcceptsPlanReview(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	review := createSessionsV3PrimaryTestSession(t, server, "mcp-review-create", "review")
	running := createSessionsV3PrimaryTestSession(t, server, "mcp-running-create", "running")
	doc := sessionsV3PlanModeTestDocument("plan-review", sessionruntime.PlanExecutionPolicyModeAutomatic, sessionruntime.PlanExecutionShapeCheckpointed, "cp-2", []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Status: sessionruntime.PlanCheckpointStatusCompleted, Review: &pebblestore.SessionPlanCheckpointReview{Status: sessionruntime.PlanCheckpointReviewStatusApproved}},
		{ID: "cp-2", Status: sessionruntime.PlanCheckpointStatusNeedsReview},
	})
	doc.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateWaitingReview, LastCheckpointID: "cp-2", LastOutcome: sessionruntime.PlanCheckpointStatusNeedsReview}
	sessionsV3PlanModeSeedPlan(t, sessionSvc, review.ID, "plan-review", doc, "approved")
	busy := sessionsV3PlanModeTestDocument("plan-busy", sessionruntime.PlanExecutionPolicyModeAutomatic, sessionruntime.PlanExecutionShapeCheckpointed, "cp-1", []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: sessionruntime.PlanCheckpointStatusInProgress}})
	busy.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress}
	sessionsV3PlanModeSeedPlan(t, sessionSvc, running.ID, "plan-busy", busy, "approved")

	base := withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)).Context()
	write := controlMCPDirectCall(base, testPrincipal(), server.apiMux(), server, "sessions:read", "sessions:write")
	read := controlMCPDirectCall(base, testPrincipal(), server.apiMux(), server, "sessions:read")

	if _, err := controlMCPAcceptReview(read, map[string]any{"session_id": review.ID}); err == nil || !strings.Contains(err.Error(), "sessions:write") {
		t.Fatalf("read grant accepted a review: %v", err)
	}
	if _, err := controlMCPAcceptReview(write, map[string]any{"session_id": running.ID}); err == nil || !strings.Contains(err.Error(), "waits for review") {
		t.Fatalf("running plan accepted: %v", err)
	}
	if plan := sessionsV3PlanModeGetPlan(t, sessionSvc, running.ID, "plan-busy"); plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusInProgress {
		t.Fatal("refused accept changed the running plan")
	}
	if plan := sessionsV3PlanModeGetPlan(t, sessionSvc, review.ID, "plan-review"); plan.Document.Checkpoints[1].Review != nil && plan.Document.Checkpoints[1].Review.Status == sessionruntime.PlanCheckpointReviewStatusApproved {
		t.Fatal("read grant changed the reviewed plan")
	}
	result, err := controlMCPAcceptReview(write, map[string]any{"session_id": review.ID, "notes": "looks right"})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	plan := sessionsV3PlanModeGetPlan(t, sessionSvc, review.ID, "plan-review")
	if out["accepted_checkpoint_id"] != "cp-2" || plan.Document.ExecutionState == nil || plan.Document.ExecutionState.Status != sessionruntime.PlanExecutionStateCompleted ||
		plan.Document.Checkpoints[1].Review == nil || plan.Document.Checkpoints[1].Review.Notes != "looks right" || !strings.Contains(out["summary"].(string), "completed") {
		t.Fatalf("review not closed: %+v %+v", out, plan.Document.ExecutionState)
	}
}

// Requirement: an AI can see what agents produced without a shell: the
// changes of a session's own worktree against the base it started from
// (commits, committed, uncommitted and untracked files, diff) and the text of
// one file, resolved only inside that worktree or a registered workspace.
// Threats: reading outside the folder through .., absolute paths or a symlink,
// reading .git internals, .env secrets, Swarm's protected storage or binary
// blobs, reading another account's session, or an unbounded result. Owners:
// controlMCPGetChanges, controlMCPReadFile, controlMCPOpenInside (os.Root) and
// tool.RefuseProtectedPath. Real Git worktrees with temporary stores through
// the API mux is the narrowest layer covering session ownership, worktree
// resolution and Git together.
func TestControlMCPReadsWhatAgentsProduced(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer func() { f.db.Close() }()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(dir, name, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-b", "main")
	write(repo, "README.md", "base\n")
	git(repo, "add", ".")
	git(repo, "commit", "-m", "base")
	base := git(repo, "rev-parse", "HEAD")
	if _, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(f.accountID, repo, "Bot"); err != nil {
		t.Fatal(err)
	}
	ss := pebblestore.NewSessionStore(f.db)
	el, err := pebblestore.NewEventLog(f.db)
	if err != nil {
		t.Fatal(err)
	}
	f.server.sessions = sessionruntime.NewService(ss, el)
	f.server.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(f.db))
	worktrees := worktreeruntime.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
	f.server.worktrees = worktrees
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	alloc, err := worktrees.AllocateProjectTaskFollowup(p, repo, "draft-run", "agent/worker-draft", base, "main")
	if err != nil {
		t.Fatal(err)
	}
	tree := alloc.WorkspacePath
	write(tree, "drafts/draft.md", "a draft\n")
	git(tree, "add", ".")
	git(tree, "commit", "-m", "draft")
	write(tree, "README.md", "base\nedited\n")
	write(tree, "notes.txt", "untracked\n")
	write(tree, ".env", "SECRET=x\n")
	write(tree, "blob.bin", "a\x00b")
	write(root, "outside.txt", "outside\n")
	if err := os.Symlink(filepath.Join(root, "outside.txt"), filepath.Join(tree, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	write(tree, "big.txt", strings.Repeat("x", controlMCPMaxFileBytes+10))
	snapshot := pebblestore.SessionSnapshot{ID: "draft-run", UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: tree, WorktreeEnabled: true, WorktreeRootPath: tree, WorktreeBranch: alloc.BranchName, WorktreeBaseBranch: "main", Mode: "auto", Metadata: map[string]any{"base_commit": base, "swarm_v3_source_workspace_path": repo, "swarm_v3_worktree_owner_session_id": "draft-run", "swarm_v3_runtime_workspace_path": tree}}
	if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: snapshot.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: "create-draft", IdempotencyKey: "create-draft", PayloadHash: "create-draft", RequestHash: "create-draft", Kind: sessionruntime.SessionMutationCreateSession, Session: &snapshot, WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: tree, SourcePath: repo, OwnerSessionID: snapshot.ID, Branch: alloc.BranchName, AllocatedRuntimeRoot: true}}); err != nil {
		t.Fatal(err)
	}
	c := controlMCPDirectCall(ctx, p, f.server.apiMux(), f.server, "sessions:read")
	noRead := controlMCPDirectCall(ctx, p, f.server.apiMux(), f.server, "automations:read")
	foreign := controlMCPDirectCall(ctx, identity.Principal{Type: "user", UserID: "other-user", AccountScopeID: "other-account"}, f.server.apiMux(), f.server, "sessions:read")

	changes, err := controlMCPGetChanges(c, map[string]any{"session_id": "draft-run"})
	if err != nil {
		t.Fatal(err)
	}
	text := controlMCPEncode(t, changes)
	for _, want := range []string{`"base":"` + base + `"`, `draft"]`, `{"path":"drafts/draft.md","status":"added"}`, `{"path":"README.md","status":"modified"}`, `{"path":"notes.txt","status":"untracked"}`, `+a draft`, `+edited`} {
		if !strings.Contains(text, want) {
			t.Fatalf("changes miss %s: %s", want, text)
		}
	}
	if scoped, err := controlMCPGetChanges(c, map[string]any{"session_id": "draft-run", "path": "drafts", "diff_chars": 0.0}); err != nil || strings.Contains(controlMCPEncode(t, scoped), "README.md") || strings.Contains(controlMCPEncode(t, scoped), `"diff"`) {
		t.Fatalf("path or diff_chars ignored: %v %v", scoped, err)
	}
	if cut, err := controlMCPGetChanges(c, map[string]any{"session_id": "draft-run", "diff_chars": 40.0}); err != nil || cut.(map[string]any)["diff_truncated"] != true || len(cut.(map[string]any)["diff"].(string)) > 40 {
		t.Fatalf("diff not bounded: %v %v", cut, err)
	}

	read, err := controlMCPReadFile(c, map[string]any{"session_id": "draft-run", "path": "drafts/draft.md"})
	if err != nil || read.(map[string]any)["content"] != "a draft\n" {
		t.Fatalf("read worktree file: %v %v", read, err)
	}
	if listing, err := controlMCPReadFile(c, map[string]any{"session_id": "draft-run", "path": "."}); err != nil || strings.Contains(controlMCPEncode(t, listing), `".git"`) || !strings.Contains(controlMCPEncode(t, listing), `"name":"drafts","type":"dir"`) {
		t.Fatalf("listing: %v %v", listing, err)
	}
	if big, err := controlMCPReadFile(c, map[string]any{"session_id": "draft-run", "path": "big.txt"}); err != nil || big.(map[string]any)["truncated"] != true || len(big.(map[string]any)["content"].(string)) != controlMCPMaxFileBytes {
		t.Fatalf("large file not capped: %v", err)
	}
	if fromRepo, err := controlMCPReadFile(c, map[string]any{"workspace_path": repo, "path": "README.md"}); err != nil || fromRepo.(map[string]any)["content"] != "base\n" {
		t.Fatalf("read registered workspace: %v %v", fromRepo, err)
	}
	for _, tc := range []struct {
		name string
		call *controlMCPCall
		args map[string]any
		want string
	}{
		{"parent", c, map[string]any{"session_id": "draft-run", "path": "../outside.txt"}, "leaves the folder"},
		{"absolute", c, map[string]any{"session_id": "draft-run", "path": filepath.Join(root, "outside.txt")}, "must be relative"},
		{"symlink escape", c, map[string]any{"session_id": "draft-run", "path": "escape.txt"}, "not readable here"},
		{"git internals", c, map[string]any{"session_id": "draft-run", "path": ".git/config"}, ".git"},
		{"env file", c, map[string]any{"session_id": "draft-run", "path": ".env"}, "secrets"},
		{"binary", c, map[string]any{"session_id": "draft-run", "path": "blob.bin"}, "binary"},
		{"unregistered workspace", c, map[string]any{"workspace_path": root, "path": "outside.txt"}, "not a registered workspace"},
		{"both roots", c, map[string]any{"session_id": "draft-run", "workspace_path": repo, "path": "README.md"}, "exactly one"},
		{"no read scope", noRead, map[string]any{"session_id": "draft-run", "path": "README.md"}, "sessions:read"},
		{"foreign session", foreign, map[string]any{"session_id": "draft-run", "path": "README.md"}, `session "draft-run" was not found`},
	} {
		if out, err := controlMCPReadFile(tc.call, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want %q, got %v %v", tc.name, tc.want, out, err)
		}
	}
	if _, err := controlMCPGetChanges(foreign, map[string]any{"session_id": "draft-run"}); err == nil || !strings.Contains(err.Error(), "swarm_list_sessions") {
		t.Fatalf("foreign changes: %v", err)
	}
	// Swarm's protected storage stays unreadable even inside a worktree.
	tool.SetProtectedPaths([]string{filepath.Join(tree, "drafts")}, nil)
	defer tool.SetProtectedPaths(nil, nil)
	if _, err := controlMCPReadFile(c, map[string]any{"session_id": "draft-run", "path": "drafts/draft.md"}); err == nil || !strings.Contains(err.Error(), "private storage") {
		t.Fatalf("protected path read: %v", err)
	}
}

// Requirement: pending requests read as data, not escaped strings: tool
// arguments are decoded into a bounded object (raw text only when they are not
// JSON), and the ask-user tool is named ask_user like its requirement, marked
// as a question. Owner: controlMCPPermissionSummary; pure function layer.
func TestControlMCPPermissionSummaryDecodesArguments(t *testing.T) {
	got := controlMCPPermissionSummary(map[string]any{"id": "perm_1", "tool_name": "ask-user", "requirement": "ask_user", "tool_arguments": `{"questions":[{"id":"q1","question":"` + strings.Repeat("q", 2000) + `"}]}`})
	args, ok := got["tool_arguments"].(map[string]any)
	if !ok || got["tool_name"] != "ask_user" || got["kind"] != "question" {
		t.Fatalf("summary: %+v", got)
	}
	question := controlMCPMap(controlMCPList(args["questions"])[0])["question"].(string)
	if !strings.Contains(question, "[truncated") {
		t.Fatal("long argument string not bounded")
	}
	raw := controlMCPPermissionSummary(map[string]any{"tool_name": "bash", "tool_arguments": "not json " + strings.Repeat("x", 1000)})
	if text, ok := raw["tool_arguments"].(string); !ok || raw["kind"] != "approval" || !strings.Contains(text, "[truncated") {
		t.Fatalf("raw arguments: %+v", raw)
	}
}

// Requirement: "settled" means exactly: the run ended (finished, failed or
// stopped, from the session lifecycle) or it waits on a decision; stop_run
// reports the run's final status and says a stopped plan is paused, not
// cancelled. Owners: controlMCPSettledState, controlMCPRunOutcome and
// controlMCPStopReport; pure function layer (the outbox-woken wait itself is
// TestControlMCPWaitForSession).
func TestControlMCPSettledAndStopReport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		run    string
		detail map[string]any
		want   string
	}{
		{"decision", "", map[string]any{"active_run_intent": map[string]any{"run_id": "r"}, "pending_permissions": []any{map[string]any{"id": "p"}}}, `{"reason":"waiting_on_decision","settled":true}`},
		{"running", "", map[string]any{"active_run_intent": map[string]any{"run_id": "r"}}, `{"settled":false}`},
		{"finished", "", map[string]any{"session": map[string]any{"lifecycle": map[string]any{"phase": "done", "stop_reason": "stop"}}}, `"outcome":"finished"`},
		{"failed", "", map[string]any{"session": map[string]any{"lifecycle": map[string]any{"error": "provider down"}}}, `"outcome":"failed"`},
		{"stopped", "", map[string]any{"session": map[string]any{"lifecycle": map[string]any{"phase": "cancelled"}}}, `"outcome":"stopped"`},
		// V3 details carry no lifecycle: the run's recorded status decides, also
		// for a run stopped before an executor picked it up.
		{"stopped before start", "cancelled", map[string]any{"session": map[string]any{}}, `"outcome":"stopped"`},
		{"run failed", "failed", map[string]any{"session": map[string]any{}}, `"outcome":"failed"`},
		{"run completed", "completed", map[string]any{"session": map[string]any{}}, `"outcome":"finished"`},
		{"run status beats lifecycle", "cancelled", map[string]any{"session": map[string]any{"lifecycle": map[string]any{"phase": "done"}}}, `"outcome":"stopped"`},
	} {
		if got := controlMCPEncode(t, controlMCPSettledState(tc.detail, tc.run)); !strings.Contains(got, tc.want) {
			t.Fatalf("%s: %s", tc.name, got)
		}
	}
	report := controlMCPStopReport("s1", "run_1", map[string]any{"settled": true, "reason": "run_ended", "outcome": "stopped"}, map[string]any{
		"session": map[string]any{"id": "s1"},
		"plan":    map[string]any{"status": "paused", "checkpoints": []map[string]any{{"id": "cp1", "status": "completed"}, {"id": "cp2", "status": "paused"}}},
	})
	if report["run_status"] != "stopped" || !strings.Contains(report["summary"].(string), "paused, not cancelled (1 of 2") {
		t.Fatalf("stop report: %+v", report)
	}
	if winding := controlMCPStopReport("s1", "run_1", map[string]any{"settled": false, "timed_out": true}, map[string]any{}); winding["run_status"] != "stopping" {
		t.Fatalf("unfinished stop: %+v", winding)
	}
}

// Requirement: usage explains a zero cost (subscription sign-in providers and
// unpriced models) and never shows an unset limit as a 0 cap; provider status
// shows a working sign-in as signed in, not "not verified". Owners:
// controlMCPBilling, controlMCPCostNote, controlMCPLimits and
// controlMCPCredentialStatus; pure function layer.
func TestControlMCPUsageAndProviderStatus(t *testing.T) {
	subscription := map[string]bool{"codex": true}
	for want, model := range map[string]map[string]any{
		"subscription": {"provider": "codex", "cost_usd": 0.0, "total_tokens": 45000.0},
		"unpriced":     {"provider": "local", "cost_usd": 0.0, "total_tokens": 10.0},
		"metered":      {"provider": "anthropic", "cost_usd": 0.5, "total_tokens": 10.0, "input_price_per_million": 3.0},
	} {
		if got := controlMCPBilling(model, subscription); got != want {
			t.Fatalf("billing %s: got %s", want, got)
		}
	}
	if note := controlMCPCostNote(map[string]any{}, map[string]bool{"subscription": true}); !strings.Contains(note, "subscription") {
		t.Fatalf("cost note: %q", note)
	}
	limits := controlMCPLimits(map[string]any{"enabled": false, "daily_cost_limit_usd": 0.0, "today_cost_usd": 0.0})
	if limits["daily_cost_limit_usd"] != "none" || limits["daily_tokens_limit"] != "none" || limits["note"] == nil {
		t.Fatalf("limits: %+v", limits)
	}
	if set := controlMCPLimits(map[string]any{"enabled": true, "daily_cost_limit_usd": 5.0, "daily_tokens_limit": 1000.0}); set["daily_cost_limit_usd"] != 5.0 || set["daily_tokens_limit"] != 1000.0 {
		t.Fatalf("set limits: %+v", set)
	}
	if got := controlMCPCredentialStatus("oauth", nil, true); got["status"] != "signed in" || got["auth_type"] != "sign_in" || got["verification"] != nil {
		t.Fatalf("sign-in: %+v", got)
	}
	if got := controlMCPCredentialStatus("oauth", nil, false); !strings.Contains(got["status"].(string), "not ready") {
		t.Fatalf("broken sign-in: %+v", got)
	}
	if got := controlMCPCredentialStatus("api", nil, true); got["status"] != "saved, not verified yet" {
		t.Fatalf("unverified key: %+v", got)
	}
	if got := controlMCPCredentialStatus("api", &auth.ConnectionStatus{Connected: true}, true); got["status"] != "verified" {
		t.Fatalf("verified key: %+v", got)
	}
}

// Requirement: by default the server instructions tell one story (a model
// provider, a project with its folders, then jobs for the coder, designer and
// finder, then results, follow-ups and merging) and
// name only default tools; the advanced lines appear only for a client that
// asked for every tool. Every line names only tools the caller's level can
// call and every tool it names exists. Threats: an AI steered to sessions or
// the orchestrator first, a read-only client told to give or merge jobs, or
// instructions drifting from the tool list. Owner: controlMCPCall.instructions
// and allTools; pure layer over the level check and the request URL.
func TestControlMCPInstructionsFollowLevel(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range controlMCPTools() {
		names[tool.Name] = true
	}
	for _, set := range [][]controlMCPInstruction{controlMCPCoreInstructionLines, controlMCPInstructionLines} {
		for _, line := range set {
			for _, name := range line.tools {
				name = strings.TrimPrefix(name, "!")
				if !names[name] {
					t.Fatalf("instruction names unknown tool %s", name)
				}
			}
		}
	}
	for _, line := range controlMCPCoreInstructionLines {
		for _, name := range line.tools {
			if name = strings.TrimPrefix(name, "!"); !controlMCPCoreTools[name] {
				t.Fatalf("default instructions name advanced tool %s", name)
			}
		}
	}
	principal := identity.Principal{Type: "user", UserID: "u", AccountScopeID: "a"}
	instructions := func(all bool, scopes ...string) string {
		call := controlMCPDirectCall(context.Background(), principal, nil, nil, scopes...)
		if all {
			call.request.URL.RawQuery = "tools=all"
		}
		return call.instructions()
	}
	defaults := instructions(false, "sessions:read", "sessions:write")
	for _, shown := range []string{"deploy AI workers", "coder", "designer", "finder", "swarm_list_providers", "never ask for a key in chat", "swarm_create_project", "swarm_give_job", "swarm_get_job", "swarm_message_job", "swarm_answer_job", "swarm_deliver_job", "Swarm never pushes otherwise", "swarm_status", "swarm_activity", "swarm_list_secrets", "untrusted"} {
		if !strings.Contains(defaults, shown) {
			t.Fatalf("default instructions miss %s: %s", shown, defaults)
		}
	}
	for _, hidden := range []string{"system-orchestrator", "run_task", "start_session", "resolve_permission", "swarm_add_workspace", "checkpoint", "run_plan", "accept_review"} {
		if strings.Contains(defaults, hidden) {
			t.Fatalf("default instructions mention advanced %s: %s", hidden, defaults)
		}
	}
	// The story's order: a provider, a project with its folders, then a job,
	// then its result, then merging.
	order := []string{"swarm_list_providers", "swarm_create_project", "swarm_give_job", "swarm_get_job", "swarm_deliver_job"}
	for i := 1; i < len(order); i++ {
		if strings.Index(defaults, order[i-1]) > strings.Index(defaults, order[i]) {
			t.Fatalf("story order: %s before %s: %s", order[i], order[i-1], defaults)
		}
	}
	readOnly := instructions(false, "sessions:read", "swarm:read")
	for _, hidden := range []string{"swarm_create_project", "swarm_give_job", "swarm_message_job", "swarm_answer_job", "swarm_deliver_job", "swarm_request_media", "swarm_schedule_job", "swarm_set_agent_model", "swarm_request_provider_key"} {
		if strings.Contains(readOnly, hidden) {
			t.Fatalf("read-only instructions mention %s: %s", hidden, readOnly)
		}
	}
	if !strings.Contains(readOnly, "swarm_status") || !strings.Contains(readOnly, "untrusted") {
		t.Fatalf("read-only instructions: %s", readOnly)
	}
	// A key that can call swarm_connect_chatgpt is told to offer ChatGPT sign-in
	// first and API keys only as the alternative; one that cannot gets the key
	// line alone. Exactly one provider line is shown.
	for _, tc := range []struct {
		text    string
		chatGPT bool
	}{{defaults, true}, {readOnly, false}, {instructions(false, "sessions:read", "sessions:write", "swarm:read", "swarm:write"), true}} {
		if strings.Count(tc.text, "Workers need a model provider") != 1 || !strings.Contains(tc.text, "never ask for a key in chat") ||
			strings.Contains(tc.text, "swarm_connect_chatgpt") != tc.chatGPT || strings.Contains(tc.text, "first offer the person to sign in with their ChatGPT plan") != tc.chatGPT {
			t.Fatalf("provider instructions (ChatGPT %v): %s", tc.chatGPT, tc.text)
		}
	}
	// A write key may give and merge jobs.
	writeOnly := instructions(false, "sessions:read", "sessions:write", "swarm:read", "swarm:write")
	if !strings.Contains(writeOnly, "swarm_give_job") || !strings.Contains(writeOnly, "swarm_deliver_job") || !strings.Contains(writeOnly, "swarm_create_project") || !strings.Contains(writeOnly, "swarm_set_agent_model") || strings.Contains(writeOnly, "swarm_request_provider_key") {
		t.Fatalf("write-only instructions: %s", writeOnly)
	}
	full := instructions(true, "sessions:read", "sessions:write")
	for _, shown := range []string{"swarm_give_job", "resolve_permission", "approve_task", "accept_review", "refuses some commands outright", "never become pending requests",
		"create_project", "run_task", "system-orchestrator", "Never hand the person an empty repository"} {
		if !strings.Contains(full, shown) {
			t.Fatalf("all-tools instructions miss %s: %s", shown, full)
		}
	}
	// With every tool, a write grant creates projects (projects come first)
	// and is warned about empty repositories, but is never told to approve.
	writeAll := instructions(true, "sessions:read", "sessions:write", "swarm:read", "swarm:write")
	if !strings.Contains(writeAll, "swarm_create_project") || !strings.Contains(writeAll, "empty repository") || strings.Contains(writeAll, "approve_task") {
		t.Fatalf("write-only all-tools instructions: %s", writeAll)
	}
}

// Requirement: list_providers wait_seconds returns as soon as the person's key
// link leaves waiting (saved, failed or expired), woken by the registry rather
// than polling, and returns at the deadline otherwise. Owners:
// controlMCPCall.waitForKeyRequest and providerKeyRegistry.changes/finish;
// the in-memory registry is the narrowest layer.
func TestControlMCPWaitsForProviderKey(t *testing.T) {
	s := &Server{}
	s.SetProviderKeyPageAddr("127.0.0.1:7781")
	c := controlMCPDirectCall(context.Background(), identity.Principal{Type: "user", UserID: "u", AccountScopeID: "a"}, nil, s, "admin")
	code, _, err := s.providerKeys.issue(providerKeyRequest{provider: "openai", accountScopeID: "a", userID: "u"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		c.waitForKeyRequest("a", "openai", 30)
		done <- time.Since(start)
	}()
	select {
	case <-done:
		t.Fatal("wait returned while the link was waiting")
	case <-time.After(200 * time.Millisecond):
	}
	request, _, ok := s.providerKeys.begin(code)
	if !ok {
		t.Fatal("code not usable")
	}
	s.providerKeys.finish(request, true, "")
	select {
	case elapsed := <-done:
		if elapsed > 5*time.Second || s.providerKeys.outcome("a", "openai").Status != "saved" {
			t.Fatalf("wait after save: %s", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not wake on the saved key")
	}
	if _, _, err := s.providerKeys.issue(providerKeyRequest{provider: "openai", accountScopeID: "a", userID: "u"}); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	c.waitForKeyRequest("a", "openai", 1)
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("deadline not honoured: %s", elapsed)
	}
}

// Requirement: a session refused for an unregistered workspace path says so
// and points at list_workspaces/add_workspace (never the empty create_workspace
// as the default fix); other refusals and 404s keep
// their meaning, with the id echoed and the list tool named. Owners:
// controlMCPCall.unregisteredWorkspace and controlMCPNotFound; a fake route
// table is the narrowest layer for the mapping.
func TestControlMCPErrorHints(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/workspace/list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"workspaces": []any{map[string]any{"path": "/registered"}}})
	})
	c := controlMCPDirectCall(context.Background(), identity.Principal{Type: "user", UserID: "u", AccountScopeID: "a"}, mux, nil, "sessions:read", "sessions:write")
	binding := controlMCPToolError{message: "Swarm rejected the request (HTTP 400): sessions v3 primary workspace is missing its canonical local binding", status: http.StatusBadRequest}
	if err := c.unregisteredWorkspace(binding, "/etc"); !strings.Contains(err.Error(), `"/etc" is not a registered workspace`) || !strings.Contains(err.Error(), "swarm_add_workspace") || strings.Contains(err.Error(), "swarm_create_workspace") {
		t.Fatalf("unregistered: %v", err)
	}
	if err := c.unregisteredWorkspace(binding, "/registered"); err != error(binding) {
		t.Fatalf("registered path rewritten: %v", err)
	}
	forbidden := controlMCPToolError{message: "Swarm rejected the request (HTTP 403): forbidden", status: http.StatusForbidden}
	if err := c.unregisteredWorkspace(forbidden, "/etc"); err != error(forbidden) {
		t.Fatalf("403 rewritten: %v", err)
	}
	missing := controlMCPToolError{message: "Swarm rejected the request (HTTP 404): session not found", status: http.StatusNotFound}
	if err := controlMCPNotFound(missing, "session", "s_9", "swarm_list_sessions"); !strings.Contains(err.Error(), `"s_9"`) || !strings.Contains(err.Error(), "swarm_list_sessions") || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("404 hint: %v", err)
	}
	if err := controlMCPNotFound(forbidden, "session", "s_9", "swarm_list_sessions"); err != error(forbidden) {
		t.Fatalf("non-404 rewritten: %v", err)
	}
}

// Requirement: after work is started, send_message returns one coherent view:
// the start receipt carries only identity (never the acceptance-time intent
// status), and the session, run and messages come from one read made after
// the message was stored, so message_count includes it. Threat: contradictory
// snapshots (pending_executor beside running, message_count 0 beside 1) that
// mislead a supervising model. Owners: controlMCPCall.sendMessage and
// currentState. The routed session server with real stores is the narrowest
// layer that stores the message and reads it back; no executor runs.
func TestControlMCPSendMessageReturnsCurrentState(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "mcp-send-create", "send")
	base := withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)).Context()
	c := controlMCPDirectCall(base, testPrincipal(), server.apiMux(), server, "sessions:read", "sessions:write")
	result, err := controlMCPSendMessage(c, map[string]any{"session_id": created.ID, "content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	started := controlMCPMap(out["started"])
	session := controlMCPMap(out["session"])
	if started["accepted"] != true || started["status"] != nil || out["latest"] != nil || out["run"] != nil {
		t.Fatalf("start receipt not reduced to identity: %+v", out)
	}
	if count, _ := session["message_count"].(float64); count < 1 {
		t.Fatalf("state read before the message was stored: %+v", session)
	}
	messages, _ := out["messages"].([]map[string]any)
	if len(messages) == 0 || messages[len(messages)-1]["content"] != "hello" {
		t.Fatalf("messages not current: %+v", out["messages"])
	}
	if _, err := controlMCPSendMessage(c, map[string]any{"session_id": "missing-session", "content": "hello"}); err == nil || !strings.Contains(err.Error(), `"missing-session"`) || !strings.Contains(err.Error(), "swarm_list_sessions") {
		t.Fatalf("unknown session: %v", err)
	}
}

// Requirement: get_session can be asked for a tool call's longer output
// (detail_chars) while the default stays compact. Owner:
// controlMCPToolMessageDetail; pure function layer.
func TestControlMCPToolOutputDetail(t *testing.T) {
	record := `{"tool_name":"bash","arguments":"{\"command\":\"go test\"}","output":"` + strings.Repeat("o", 5000) + `"}`
	compact, _ := controlMCPToolMessageSummary(map[string]any{"role": "tool"}, record)
	full, _ := controlMCPToolMessageDetail(map[string]any{"role": "tool"}, record, controlMCPMaxDetailChars)
	if !strings.Contains(compact["output"].(string), "[truncated") || full["output"] != strings.Repeat("o", 5000) {
		t.Fatalf("detail limit not applied: compact %d full %d", len(compact["output"].(string)), len(full["output"].(string)))
	}
	if args, ok := full["arguments"].(map[string]any); !ok || args["command"] != "go test" {
		t.Fatalf("tool arguments not decoded: %+v", full["arguments"])
	}
}
