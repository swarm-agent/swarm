package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: get_task reports a landed task's own commits and files after its
// checkout was removed (the landed-worktree cleanup): from base..its branch
// in the task's recorded source repository while the branch exists, and from
// base..the head its integration recorded once the branch is deleted, never
// the 0/0 the integrated progress (no unintegrated commits) would leave.
// Without a readable source the answer stays the stored one and never reads
// another folder. Threat: a pushed and integrated task reported as having
// produced nothing. Owners: projectTaskWorktreeResult /
// projectTaskRemovedWorktreeResult and compactProjectTask through
// Runtime.executeManageProjects get_task, over a temp Pebble store with the
// progress hook installed and a real temp Git repository with a real
// worktree; the tool entry is the narrowest layer that proves the answer.
func TestManageProjectsGetTaskCommitsAfterWorktreeRemoved(t *testing.T) {
	db, store := openTaskViewStore(t)
	const account, project, taskID = "acct-landed", "proj-landed", "task-landed"
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: project, AccountID: account, Name: "Landed"}); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageProjectStore(store)
	scope := WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-landed", AccountScopeID: account}}

	// The source repository on dev, the task's worktree on its own branch
	// with two commits changing two files, merged into dev (landed).
	source := t.TempDir()
	testGit(t, source, "init", "-q", "-b", "dev")
	testGit(t, source, "commit", "-q", "--allow-empty", "-m", "base")
	base := testGit(t, source, "rev-parse", "HEAD")
	worktree := filepath.Join(t.TempDir(), "wt")
	testGit(t, source, "worktree", "add", "-q", "-b", "agent/landed", worktree, base)
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(worktree, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		testGit(t, worktree, "add", name)
		testGit(t, worktree, "commit", "-q", "-m", "add "+name)
	}
	head := testGit(t, worktree, "rev-parse", "HEAD")
	testGit(t, source, "merge", "-q", "--ff-only", "agent/landed")

	task := &pebblestore.ProjectTaskRecord{
		ID: taskID, ProjectID: project, AccountID: account, Title: "Landed task", Agent: "coder", Status: "completed", Revision: 3,
		SessionID: "landed-session", WorktreeBranch: "agent/landed", BaseBranch: "dev", BaseCommit: base, WorkspacePath: worktree, IsIntegrated: true,
		SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: "ws-landed", WorkspaceGeneration: 1, Path: source, Provenance: "unique_project_workspace"},
		Integration:     &pebblestore.ProjectTaskIntegration{State: "integrated", SourceBranch: "agent/landed", SourceHead: head, TargetBranch: "dev", ResultingTargetHead: head, TargetWorkspacePath: source},
	}
	if err := store.PutProjectTask(account, task); err != nil {
		t.Fatal(err)
	}
	// Integrated: progress counts no unintegrated commits.
	db.SetProjectTaskProgress(func(string, pebblestore.ProjectTaskRecord) any {
		return map[string]any{"state": "integrated", "summary": "integrated into dev", "integration": map[string]any{"state": "integrated", "branch": "agent/landed", "commits": 0}}
	})
	defer db.SetProjectTaskProgress(nil)
	read := func(stage string) ProjectTaskView {
		t.Helper()
		raw, _, err := runManageProjects(t, r, scope, map[string]any{"action": "get_task", "project_id": project, "task_id": taskID})
		if err != nil {
			t.Fatal(err)
		}
		var view struct {
			Task ProjectTaskView `json:"task"`
		}
		if err := json.Unmarshal([]byte(raw), &view); err != nil {
			t.Fatal(err)
		}
		if view.Task.Result == nil {
			t.Fatalf("%s: no result: %s", stage, raw)
		}
		return view.Task
	}
	want := func(stage string, v ProjectTaskView) {
		t.Helper()
		if v.Result.Commits != 2 || v.Result.Files != 2 || v.Result.Head != head {
			t.Fatalf("%s: commits %d files %d head %q, want 2, 2, %s", stage, v.Result.Commits, v.Result.Files, v.Result.Head, head)
		}
	}
	want("checkout present", read("checkout present"))

	// The landed-worktree cleanup removes the checkout; the branch stays.
	testGit(t, source, "worktree", "remove", "--force", worktree)
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
	want("checkout removed", read("checkout removed"))

	// The branch is deleted too: the integration's recorded head decides.
	testGit(t, source, "branch", "-q", "-D", "agent/landed")
	want("branch deleted", read("branch deleted"))

	// Without a readable source repository nothing else is read: the stored
	// (integrated: zero unintegrated) count stands, never another folder's.
	if _, err := store.UpdateProjectTask(account, project, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		t.SourceWorkspace.Path = filepath.Join(source, "missing")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if v := read("source missing"); v.Result.Files != 0 || v.Result.Commits != 0 {
		t.Fatalf("source missing: read files from elsewhere: %+v", v.Result)
	}
	if _, err := store.UpdateProjectTask(account, project, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		t.SourceWorkspace.Path = "relative/source"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if v := read("relative source"); v.Result.Files != 0 || v.Result.Commits != 0 {
		t.Fatalf("relative source path was read: %+v", v.Result)
	}
}

// Purpose: a review outcome is a field. get_task's view carries the task's
// latest review (review: verdict ok, warn or block, the round, the step and a
// findings summary bounded to projectTaskTextBytes with findings_truncated
// and the call with the full text), also when the review passed and the
// workflow loop says only "done", for a task here and on a linked machine;
// a task whose workflow recorded no verdict has no review. Threat: an
// Orchestrator or MCP client that can see only the review loop's state and
// must guess whether the review passed, warned or blocked. Owners:
// compactProjectTask / projectTaskLatestReview through
// Runtime.executeManageProjects get_task (progress hook installed, the API's
// task progress review fields) and CompactProjectTaskAnswer for the linked
// path; the tool entry is the narrowest layer that proves the answer.
func TestManageProjectsGetTaskShowsReviewVerdict(t *testing.T) {
	db, store := openTaskViewStore(t)
	const account, project, taskID = "acct-verdict", "proj-verdict", "task-verdict"
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: project, AccountID: account, Name: "Verdict"}); err != nil {
		t.Fatal(err)
	}
	task := heavyFinishedTask(account, project, taskID)
	if err := store.PutProjectTask(account, task); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageProjectStore(store)
	scope := WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-verdict", AccountScopeID: account}}
	findings := "1. low parser.go:40 a comment could be clearer. " + strings.Repeat("detail ", 200)
	progress := map[string]any{
		"state": "delivered", "summary": "delivered · review passed",
		"workflow": map[string]any{"state": "done", "deliveries": []any{map[string]any{
			"id": "dl-7", "workflow": "review", "state": "done", "step": 3, "steps": 3, "step_type": "push", "round": 2,
			"review": map[string]any{"step": "review", "verdict": "warn", "round": 2, "findings": findings},
		}}},
	}
	read := func() (string, ProjectTaskView) {
		t.Helper()
		raw, _, err := runManageProjects(t, r, scope, map[string]any{"action": "get_task", "project_id": project, "task_id": taskID})
		if err != nil {
			t.Fatal(err)
		}
		var view struct {
			Task ProjectTaskView `json:"task"`
		}
		if err := json.Unmarshal([]byte(raw), &view); err != nil {
			t.Fatal(err)
		}
		return raw, view.Task
	}
	check := func(where string, review *projectTaskReview, more string) {
		t.Helper()
		if review == nil || review.Verdict != "warn" || review.Round != 2 || review.Step != "review" || review.DeliveryID != "dl-7" {
			t.Fatalf("%s: review %+v", where, review)
		}
		if len(review.Findings) > projectTaskTextBytes || !strings.HasPrefix(review.Findings, "1. low parser.go:40") || !review.FindingsTruncated || review.More != more {
			t.Fatalf("%s: findings summary %+v", where, review)
		}
	}

	db.SetProjectTaskProgress(func(string, pebblestore.ProjectTaskRecord) any { return progress })
	raw, view := read()
	check("here", view.Review, "manage_workflows action=deliveries delivery_id=dl-7")
	if view.Progress.Workflow == nil || view.Progress.Workflow.Verdict != "" {
		t.Fatalf("a passed review is only in review, not as a blocking verdict: %s", raw)
	}

	// A linked machine's answer: the same field from its task route.
	answer, _ := json.Marshal(map[string]any{"task": map[string]any{"id": taskID, "project_id": project, "title": "Linked", "status": "completed", "progress": progress}})
	compact, err := CompactProjectTaskAnswer("get_task", "", answer)
	if err != nil {
		t.Fatal(err)
	}
	var linked struct {
		Task ProjectTaskView `json:"task"`
	}
	if err := json.Unmarshal(compact, &linked); err != nil {
		t.Fatal(err)
	}
	check("linked", linked.Task.Review, "manage_workflows action=deliveries delivery_id=dl-7 with this task's machine")

	// No verdict recorded: no review.
	db.SetProjectTaskProgress(func(string, pebblestore.ProjectTaskRecord) any {
		return map[string]any{"state": "not_integrated", "workflow": map[string]any{"state": "running", "deliveries": []any{map[string]any{"id": "dl-8", "workflow": "review", "state": "running", "round": 1}}}}
	})
	if raw, view := read(); view.Review != nil || strings.Contains(raw, `"review":{`) {
		t.Fatalf("review without a verdict: %s", raw)
	}
	db.SetProjectTaskProgress(nil)
	if raw, view := read(); view.Review != nil {
		t.Fatalf("review without a workflow: %s", raw)
	}
}
