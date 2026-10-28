package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Byte budgets for the Orchestrator's task answers (whole tool output).
const (
	getTaskBudget     = 2048
	proposeTaskBudget = 600
)

// heavyFinishedTask is a finished coder task as the store keeps it: a long
// report, a long plan, many changed files and every bookkeeping field the
// Orchestrator does not need to decide its next step.
func heavyFinishedTask(account, project, id string) *pebblestore.ProjectTaskRecord {
	report := "Report: " + strings.Repeat("the parser now keeps comments and the tests cover nested blocks. ", 60)
	files := make([]string, 40)
	for i := range files {
		files[i] = "internal/parser/file_" + strings.Repeat("x", 30) + string(rune('a'+i%26)) + ".go"
	}
	return &pebblestore.ProjectTaskRecord{
		ID: id, ProjectID: project, AccountID: account, Title: "Keep comments in the parser", Agent: "coder", Status: "needs_review",
		SessionID: "task-session-1", ActiveAttemptID: "attempt-2", Revision: 4,
		Attempts: []pebblestore.ProjectTaskAttempt{
			{ID: "attempt-1", SessionID: "task-session-0", Role: "coder", Status: "superseded", Summary: "First try " + strings.Repeat("old ", 200)},
			{ID: "attempt-2", SessionID: "task-session-1", Role: "coder", Status: "needs_review", Summary: report},
		},
		PipelineStages: []string{"Code Implementation", "Verification & Pull Request"}, CurrentStageIndex: 1,
		FullPlanMarkdown:   "# Plan\n" + strings.Repeat("A long plan line that the Orchestrator reads only on request.\n", 80),
		ContextPoolSummary: strings.Repeat("context pool ", 100), PlanSummary: strings.Repeat("summary ", 50),
		SubmissionHash: strings.Repeat("f", 64), Tier: "direct", OutcomeType: "code_pr",
		WorktreeBranch: "agent/keep-comments", BaseBranch: "dev", BaseCommit: strings.Repeat("b", 40),
		WhatDidDo: []string{strings.Repeat("did a thing ", 30)}, DiffSummary: strings.Repeat("diff ", 400),
		DeliveryAssessment: &pebblestore.TaskDeliveryAssessment{SourceOID: strings.Repeat("c", 40), SourceBranch: "agent/keep-comments", State: "candidate", CandidateCommits: 3, Files: files},
		Deliverables:       []pebblestore.ProjectTaskDeliverable{{ID: "d1", Title: "Code PR & Verified Tests", Kind: "code_diff", Status: "ready", CodeDiff: strings.Repeat("+line\n", 300)}},
	}
}

// sentBackProgress is a progress as the API's progress hook computes it: a
// review workflow sent the work back once and the follow-up finished.
func sentBackProgress(string, pebblestore.ProjectTaskRecord) any {
	return map[string]any{
		"state": "not_integrated", "summary": "not integrated · workflows done · 3 commits on agent/keep-comments",
		"workflow": map[string]any{"state": "sent_back", "deliveries": []any{map[string]any{
			"id": "dl-1", "workflow": "review", "state": "sent_back", "step": 2, "steps": 3, "step_type": "reopen", "round": 1,
			"sent_back": map[string]any{"step": "reopen", "round": 1, "reason": "review verdict block"},
		}}},
		"integration": map[string]any{"state": "not_integrated", "branch": "agent/keep-comments", "target": "dev", "commits": 3},
		"history":     []any{map[string]any{"at": 1, "what": strings.Repeat("history ", 40)}},
		"updated_at":  1,
	}
}

// taskWorktree makes a real Git worktree: a base commit and three commits
// that change two files. It returns the folder, base and head.
func taskWorktree(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "agent/keep-comments")
	git("commit", "-q", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	for i, name := range []string{"parser.go", "parser_test.go", "parser.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("x", i+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		git("add", name)
		git("commit", "-q", "-m", "change "+name)
	}
	return dir, base, git("rev-parse", "HEAD")
}

func openTaskViewStore(t *testing.T) (*pebblestore.Store, *pebblestore.SessionStore) {
	t.Helper()
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, pebblestore.NewSessionStore(db)
}

func runManageProjects(t *testing.T, r *Runtime, scope WorkspaceScope, args map[string]any) (string, map[string]any, error) {
	t.Helper()
	raw, err := r.executeManageProjects(context.Background(), scope, args)
	if err != nil {
		return "", nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("output is not JSON: %s", raw)
	}
	return raw, decoded, nil
}

// Purpose: the Orchestrator's get_task on this machine answers with what it
// needs to decide (status, stage, now/next, the workflow loop and its
// outcome, the bounded report, branch/head/commit and file counts,
// deliverables, integration, session and the next action) within a fixed
// byte budget, and returns the stored record only on request.
// Threat: the full record (plan text, attempts, assessment files, hashes) was
// dumped into the agent's context on every check (~8 KB), and a misspelt
// detail could silently fall back to some other shape.
// Owners: Runtime.executeManageProjects -> executeManageProjectTasksContext
// get_task, compactProjectTask and SessionStore.ProjectTaskProgress over a
// real temp Pebble store with the progress hook installed; the tool entry is
// the narrowest layer that proves the agent-visible answer.
func TestManageProjectsGetTaskCompactByDefault(t *testing.T) {
	db, store := openTaskViewStore(t)
	const account, project, taskID = "acct-view", "proj-view", "task-view"
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: project, AccountID: account, Name: "View"}); err != nil {
		t.Fatal(err)
	}
	// The store never keeps a delivery assessment; the result comes from the
	// task's own worktree.
	folder, base, head := taskWorktree(t)
	heavy := heavyFinishedTask(account, project, taskID)
	heavy.WorkspacePath, heavy.BaseCommit = folder, base
	if err := store.PutProjectTask(account, heavy); err != nil {
		t.Fatal(err)
	}
	db.SetProjectTaskProgress(sentBackProgress)
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageProjectStore(store)
	scope := WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-view", AccountScopeID: account}}
	get := func(extra map[string]any) (string, map[string]any, error) {
		args := map[string]any{"action": "get_task", "project_id": project, "task_id": taskID}
		for k, v := range extra {
			args[k] = v
		}
		return runManageProjects(t, r, scope, args)
	}

	raw, out, err := get(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > getTaskBudget {
		t.Fatalf("default get_task is %d bytes, budget %d: %s", len(raw), getTaskBudget, raw)
	}
	var view struct {
		Task ProjectTaskView `json:"task"`
	}
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	v := view.Task
	if v.ID != taskID || v.Title != "Keep comments in the parser" || v.Agent != "coder" || v.Status != "needs_review" || v.SessionID != "task-session-1" || v.UpdatedAt == 0 {
		t.Fatalf("identity: %+v", v)
	}
	if v.Progress.Stage != "Verification & Pull Request (2 of 2)" || !strings.HasPrefix(v.Progress.Now, "not integrated") || v.Progress.Next == "" {
		t.Fatalf("progress: %+v", v.Progress)
	}
	if wf := v.Progress.Workflow; wf == nil || wf.State != "sent_back" || wf.Running || wf.Name != "review" || wf.LastStep != "2 of 3 (reopen)" || wf.Reason != "review verdict block" {
		t.Fatalf("workflow: %+v", v.Progress.Workflow)
	}
	res := v.Result
	if res == nil || !strings.HasPrefix(res.Summary, "Report: the parser") || len(res.Summary) > projectTaskReportBytes || len(res.Summary) < 900 || res.Branch != "agent/keep-comments" || res.Head != head || res.Commits != 3 || res.Files != 2 || len(res.Deliverables) != 1 || res.Deliverables[0] != "Code PR & Verified Tests" {
		t.Fatalf("result: %+v", res)
	}
	if v.Integration != "not_integrated" || v.NextAction != "wait_tasks" || !strings.Contains(v.More, "detail=") {
		t.Fatalf("decision: integration=%q next=%q more=%q", v.Integration, v.NextAction, v.More)
	}
	for _, leaked := range []string{"full_plan_markdown", "context_pool_summary", "submission_hash", "account_id", "A long plan line", "First try", "+line"} {
		if strings.Contains(raw, leaked) {
			t.Fatalf("default view carries %q: %s", leaked, raw)
		}
	}
	if out["status"] != "ok" {
		t.Fatalf("status: %v", out["status"])
	}

	// Each detail adds its section; full is the stored record with attempts.
	if raw, _, err := get(map[string]any{"detail": "plan"}); err != nil || !strings.Contains(raw, "A long plan line") || !strings.Contains(raw, `"pipeline_stages"`) {
		t.Fatalf("detail=plan: %v %.300s", err, raw)
	}
	if raw, out, err := get(map[string]any{"detail": "attempts"}); err != nil || len(out["attempts"].([]any)) != 2 || !strings.Contains(raw, "attempt-1") {
		t.Fatalf("detail=attempts: %v %.300s", err, raw)
	}
	if raw, _, err := get(map[string]any{"detail": "files"}); err != nil || !strings.Contains(raw, `"files":["parser.go","parser_test.go"]`) || !strings.Contains(raw, `"diff_summary"`) {
		t.Fatalf("detail=files: %v %.300s", err, raw)
	}
	raw, _, err = get(map[string]any{"detail": "full"})
	if err != nil {
		t.Fatal(err)
	}
	var full struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal([]byte(raw), &full); err != nil || full.Task.FullPlanMarkdown == "" || full.Task.SubmissionHash == "" || len(full.Task.Attempts) != 2 || full.Task.ContextPoolSummary == "" {
		t.Fatalf("detail=full is not the stored record: %v %.300s", err, raw)
	}
	if _, _, err := get(map[string]any{"detail": "everything"}); err == nil || !strings.Contains(err.Error(), "detail") {
		t.Fatalf("unknown detail accepted: %v", err)
	}

	// Without a progress hook the view still answers from the record and
	// its worktree: three commits to integrate.
	db.SetProjectTaskProgress(nil)
	if raw, _, err := get(nil); err != nil || len(raw) > getTaskBudget || !strings.Contains(raw, `"next_action":"integrate_task"`) || !strings.Contains(raw, `"commits":3,"files":2`) {
		t.Fatalf("no-hook view: %v %s", err, raw)
	}
	// A base that is not a commit ID never reaches Git as an argument.
	if _, err := store.UpdateProjectTask(account, project, taskID, func(task *pebblestore.ProjectTaskRecord) error {
		task.BaseCommit = "--output=" + filepath.Join(folder, "written")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if raw, _, err := get(nil); err != nil || strings.Contains(raw, head) {
		t.Fatalf("invalid base still read the worktree: %v %s", err, raw)
	}
	if _, err := os.Stat(filepath.Join(folder, "written")); !os.IsNotExist(err) {
		t.Fatalf("git wrote through an option base: %v", err)
	}
}

// heavyLifecycle returns a fully planned task, as the real lifecycle does.
type heavyLifecycle struct{ approved int }

func (f *heavyLifecycle) heavy(projectID, status string) *pebblestore.ProjectTaskRecord {
	task := heavyFinishedTask("acct-propose", projectID, "task-proposed")
	task.Status, task.Attempts, task.ActiveAttemptID, task.DeliveryAssessment = status, nil, "", nil
	task.Provider, task.Model, task.Thinking = "provider-x", "model-y", "medium"
	return task
}
func (f *heavyLifecycle) CreateProjectTask(_ context.Context, _ identity.Principal, projectID string, _ ProjectTaskCreateInput) (*pebblestore.ProjectTaskRecord, error) {
	return f.heavy(projectID, "pending_approval"), nil
}
func (f *heavyLifecycle) DeployProjectTask(context.Context, identity.Principal, string, string) error {
	return errors.New("unexpected deploy")
}
func (f *heavyLifecycle) ApproveProjectTask(_ context.Context, _ identity.Principal, projectID, _ string, _ ...ProjectTaskApprovalGuards) (*pebblestore.ProjectTaskRecord, error) {
	f.approved++
	return f.heavy(projectID, "in_progress"), nil
}
func (f *heavyLifecycle) SubmitProjectTaskPlan(context.Context, session.ProjectTaskPlanSubmissionInput) (session.ProjectTaskPlanSubmissionResult, error) {
	return session.ProjectTaskPlanSubmissionResult{}, errors.New("unexpected plan submission")
}

// Purpose: propose_task/create_task and archive_task on this machine answer
// with the compact row (id, title, agent, status, session_id, revision) and
// the next action within a fixed byte budget; the model is one field;
// detail=full keeps the previous answer reachable; a bad detail is refused
// before anything changes.
// Threat: each delegation put the full planned record (~3.7 KB) into the
// Orchestrator's context; an invalid detail could archive or create a task
// and then fail.
// Owners: Runtime.executeManageProjects propose_task/create_task ->
// compactProjectTaskMutation, executeManageProjectTasksContext archive_task
// -> setProjectTaskRow, with a real temp store and account rules and a
// recording lifecycle that returns a fully planned record (no provider).
func TestManageProjectsTaskMutationsAnswerCompactRow(t *testing.T) {
	db, store := openTaskViewStore(t)
	const account, project = "acct-propose", "proj-propose"
	if err := db.PutJSON(pebblestore.KeySession("s"), pebblestore.SessionSnapshot{ID: "s", AccountScopeID: account, UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: project, Name: "P"}); err != nil {
		t.Fatal(err)
	}
	rules := pebblestore.DefaultAccountPermissions()
	rules.Tasks = "auto"
	if _, err := db.SetAccountPermissions(account, 0, rules); err != nil {
		t.Fatal(err)
	}
	lifecycle := &heavyLifecycle{}
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageSessionService(session.NewService(store, nil))
	r.SetManageProjectStore(store)
	r.SetProjectTaskLifecycleService(lifecycle)
	scope := WorkspaceScope{SessionID: "s", Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u", AccountScopeID: account}}

	for _, action := range []string{"propose_task", "create_task"} {
		raw, out, err := runManageProjects(t, r, scope, map[string]any{"action": action, "project_id": project, "title": "Keep comments in the parser", "agent": "image"})
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if len(raw) > proposeTaskBudget {
			t.Fatalf("%s is %d bytes, budget %d: %s", action, len(raw), proposeTaskBudget, raw)
		}
		task, _ := out["task"].(map[string]any)
		if task["id"] != "task-proposed" || task["title"] != "Keep comments in the parser" || task["agent"] != "coder" || task["status"] != "in_progress" || task["session_id"] != "task-session-1" || out["task_status"] != "in_progress" {
			t.Fatalf("%s row: %s", action, raw)
		}
		if out["next_action"] != "wait_tasks" || out["model"] != "provider-x/model-y (thinking medium)" {
			t.Fatalf("%s decision fields: %s", action, raw)
		}
		if strings.Contains(raw, "full_plan_markdown") || strings.Contains(raw, "pipeline_stages") || strings.Contains(raw, "proposal") {
			t.Fatalf("%s carries the record: %s", action, raw)
		}
	}
	raw, _, err := runManageProjects(t, r, scope, map[string]any{"action": "propose_task", "project_id": project, "title": "Keep comments", "agent": "image", "detail": "full"})
	if err != nil || !strings.Contains(raw, `"full_plan_markdown"`) || !strings.Contains(raw, `"proposal"`) {
		t.Fatalf("propose_task detail=full lost the record: %v %.300s", err, raw)
	}
	approvedBefore := lifecycle.approved
	if _, _, err := runManageProjects(t, r, scope, map[string]any{"action": "propose_task", "project_id": project, "title": "Keep comments", "agent": "image", "detail": "most"}); err == nil {
		t.Fatal("propose_task accepted an unknown detail")
	}
	if lifecycle.approved != approvedBefore {
		t.Fatal("a refused detail still started a task")
	}

	// archive_task: compact row; a bad detail leaves the task unarchived.
	if err := store.PutProjectTask(account, heavyFinishedTask(account, project, "to-archive")); err != nil {
		t.Fatal(err)
	}
	stored, _, _ := store.GetProjectTask(account, project, "to-archive")
	if _, _, err := runManageProjects(t, r, scope, map[string]any{"action": "archive_task", "project_id": project, "task_id": "to-archive", "expected_revision": stored.Revision, "detail": 7}); err == nil {
		t.Fatal("archive_task accepted a non-text detail")
	}
	if unchanged, _, _ := store.GetProjectTask(account, project, "to-archive"); unchanged.Archived || unchanged.Revision != stored.Revision {
		t.Fatalf("refused archive changed the task: %+v", unchanged)
	}
	raw, out, err := runManageProjects(t, r, scope, map[string]any{"action": "archive_task", "project_id": project, "task_id": "to-archive", "expected_revision": stored.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > proposeTaskBudget || out["next_action"] != "none" || out["task"].(map[string]any)["id"] != "to-archive" || strings.Contains(raw, "worktree_branch") {
		t.Fatalf("archive_task answer: %s", raw)
	}
}

// Purpose: a linked machine's task answers compact into the same view as
// this machine's: get_task within its budget with the decision fields,
// mutations to the row with the resolved model as one field, detail=full
// byte-identical to what the machine answered, and an answer without a task
// reduced to its top-level text and numbers.
// Threat: the linked forward passed every non-list answer through raw
// (~3.7 KB per propose_task, ~8 KB per get_task), and a malformed answer
// could be forwarded as-is.
// Owners: CompactProjectTaskAnswer, the compactor executeFleetProjects
// applies after the home records the linked task. Pure function over the
// task routes' JSON shape; the two-daemon path is covered in the api tests.
func TestCompactProjectTaskAnswerLinked(t *testing.T) {
	task := heavyFinishedTask("remote-account", "remote-project", "remote-task")
	// As the task route answers: the record with its progress merged in.
	var read map[string]any
	recordJSON, _ := json.Marshal(task)
	if err := json.Unmarshal(recordJSON, &read); err != nil {
		t.Fatal(err)
	}
	read["progress"] = sentBackProgress("", *task)
	preview := map[string]any{"task_id": task.ID, "agent": "coder", "resolved_agent": "coder", "model_source": "account_settings", "account_settings_path": "/v3/agents/model-settings",
		"resolved_model": map[string]any{"provider": "provider-x", "model": "model-y", "thinking": "low", "service_tier": "", "context_mode": ""}}
	getAnswer, _ := json.Marshal(map[string]any{"task": read, "model_preview": preview})
	if len(getAnswer) < 6000 {
		t.Fatalf("fixture too small to prove compaction: %d", len(getAnswer))
	}
	compact, err := CompactProjectTaskAnswer("get_task", "", getAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) > getTaskBudget-200 { // the wrapper adds machine and flags
		t.Fatalf("linked get_task is %d bytes: %s", len(compact), compact)
	}
	var view struct {
		Task ProjectTaskView `json:"task"`
	}
	if err := json.Unmarshal(compact, &view); err != nil {
		t.Fatal(err)
	}
	if v := view.Task; v.ID != "remote-task" || v.Status != "needs_review" || v.Progress.Workflow == nil || v.Progress.Workflow.Reason != "review verdict block" || v.Result == nil || v.Result.Files != 40 || v.Result.Commits != 3 || v.Integration != "not_integrated" || v.SessionID != "task-session-1" || v.NextAction != "wait_tasks" {
		t.Fatalf("linked view: %s", compact)
	}
	if full, err := CompactProjectTaskAnswer("get_task", ProjectTaskDetailFull, getAnswer); err != nil || string(full) != string(getAnswer) {
		t.Fatalf("detail=full changed the answer: %v", err)
	}
	if files, err := CompactProjectTaskAnswer("get_task", ProjectTaskDetailFiles, getAnswer); err != nil || !strings.Contains(string(files), "internal/parser/file_") {
		t.Fatalf("detail=files: %v %.200s", err, files)
	}

	task.Status = "in_progress"
	proposeAnswer, _ := json.Marshal(map[string]any{"task": task, "model_preview": preview})
	row, err := CompactProjectTaskAnswer("propose_task", "", proposeAnswer)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(row, &decoded); err != nil || len(row) > proposeTaskBudget-150 {
		t.Fatalf("linked propose_task row is %d bytes: %s", len(row), row)
	}
	if decoded["model"] != "provider-x/model-y (thinking low)" || decoded["next_action"] != "wait_tasks" || decoded["task"].(map[string]any)["session_id"] != "task-session-1" || strings.Contains(string(row), "model_preview") {
		t.Fatalf("linked propose_task row: %s", row)
	}

	noTask, err := CompactProjectTaskAnswer("archive_task", "", json.RawMessage(`{"status":"ok","nested":{"big":"`+strings.Repeat("x", 5000)+`"},"note":"`+strings.Repeat("y", 900)+`"}`))
	if err != nil || strings.Contains(string(noTask), "nested") || len(noTask) > 500 {
		t.Fatalf("answer without a task: %v %s", err, noTask)
	}
	if _, err := CompactProjectTaskAnswer("get_task", "", json.RawMessage(`[not json`)); err == nil {
		t.Fatal("malformed answer accepted")
	}
	// Non-task actions keep their own compaction (list, list_tasks).
	if out, err := CompactProjectTaskAnswer("list", "", json.RawMessage(`{"projects":[]}`)); err != nil || string(out) != `{"projects":[]}` {
		t.Fatalf("list changed: %v %s", err, out)
	}
}
