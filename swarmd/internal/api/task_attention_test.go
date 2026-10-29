package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// seedAttentionProject is seedOutcomeProject plus three tasks the old count
// got wrong:
//   - approval: waiting for the person's approval to start (the person must
//     act, so it needs attention; it was counted as waiting)
//   - media: an image task whose file is ready, no workflow (delivered, as
//     get_task says; its outcome said "ready to integrate")
//   - task_wf_fix: a workflow step's own task holding the job's two commits
//     (the workflow hands them over on the task it serves; it read "ready to
//     integrate" and counted as attention when workflow tasks were listed)
func seedAttentionProject(t *testing.T, db *pebblestore.SessionStore, autos *jobautomation.Store, account, user, projectID, folder string) {
	t.Helper()
	seedOutcomeProject(t, db, autos, account, user, projectID, folder)
	for _, task := range []pebblestore.ProjectTaskRecord{
		{ID: "approval", Agent: "coder", Status: "pending_approval"},
		{ID: "media", Agent: "image", Status: "needs_review", Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "img-1", Title: "Logo", Kind: "artifact", Status: "ready"}}},
		{ID: "task_wf_fix", Agent: "coder", Status: "needs_review", AutomationID: "workflow:fix-1", WorktreeBranch: "agent/fix", BaseBranch: "dev", UnintegratedCommits: 2},
	} {
		task.ProjectID, task.AccountID, task.Title = projectID, account, "Task "+task.ID
		if err := db.PutProjectTask(account, &task); err != nil {
			t.Fatal(err)
		}
	}
}

// attentionWant is which tasks of seedAttentionProject (workflow tasks
// listed) the person must act on: the blocked workflow, the commits no
// workflow hands over, and the approval. Pushed, sent back, finished
// (finder), delivered (media), the workflow's own tasks, the running one
// and the stale one do not.
var attentionWant = map[string]bool{"blocked": true, "plain": true, "approval": true}

// attentionCountsWant is the same listing by bucket: 13 tasks, 3 of them
// needing attention.
var attentionCountsWant = map[string]float64{"done": 7, "in_progress": 2, "waiting": 0, "needs_attention": 3, "idle": 0, "stopped": 1}

// checkAttentionRows asserts every row's needs_attention flag and outcome
// against the rule, by task id.
func checkAttentionRows(t *testing.T, where string, rows map[string]map[string]any) {
	t.Helper()
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "abandoned,approval,blocked,busy,finder,media,plain,pushed,sent-back,task_wf_fix,task_wf_r1,task_wf_r2,task_wf_r3" {
		t.Fatalf("%s listing = %v", where, ids)
	}
	for id, row := range rows {
		got, _ := row["needs_attention"].(bool)
		if got != attentionWant[id] {
			t.Fatalf("%s row %s needs_attention = %v, want %v (row %v)", where, id, row["needs_attention"], attentionWant[id], row)
		}
	}
	if rows["media"]["outcome"] != "delivered files ready" {
		t.Fatalf("%s media outcome = %v, want delivered files ready", where, rows["media"]["outcome"])
	}
}

// Purpose: on this machine a task needs attention only when the person must
// act: it is blocked or failed, waits for approval or an answer, or has work
// no workflow hands over and nobody integrated. A pushed, sent-back,
// finished (nothing to integrate), delivered (media files ready) or stale
// task never does, and neither does a workflow step's own task (its commits
// are the job's, handed over on the task it serves). list_tasks' row flag,
// its needs_attention count and get_task's needs_attention give the same
// answer for every task (the task list route's progress and counts are
// proven by the linked test, which reads that route on the machine).
// Threat: the live report: about 100 tasks "needing attention" on a board of
// finished work (pushed, finder tests, workflow runs), so the Orchestrator
// has to explain the number away; and a task waiting for approval not
// counted although only the person can start it.
// Owners: pebblestore.ProjectTaskNeedsAttention / ProjectTaskBucket, through
// tool executeManageProjectTasksContext list_tasks and compactProjectTask
// (get_task) over the store progress hook (taskWaitProgressFor ->
// markTaskProgressAttention), over real session, task and job automation
// stores: the narrowest layer holding the tasks, their deliveries and both
// answers.
func TestNeedsAttentionCountsOnlyTasksThePersonMustActOnThisMachine(t *testing.T) {
	w := newWaitWorkflowFixture(t)
	db := w.f.s.sessions.Store()
	seedAttentionProject(t, db, w.store, w.f.account, w.f.user, "proj-attn", w.folder)
	moveTaskProgressClock(t)
	runtime := tool.NewRuntime(1)
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.SetManageSessionService(w.f.s.sessions)
	runtime.SetManageProjectStore(db)
	scope := tool.WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: w.f.user, AccountScopeID: w.f.account}}
	call := func(args map[string]any) string {
		t.Helper()
		raw, _ := json.Marshal(args)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{Name: "manage_projects", Arguments: string(raw)})
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	rows, answer := outcomeTestRows(t, []byte(call(map[string]any{"action": "list_tasks", "project_id": "proj-attn", "include_workflow_tasks": true, "limit": float64(50)})))
	checkAttentionRows(t, "home", rows)
	checkOutcomeCountsAre(t, "home", answer, attentionCountsWant)
	for id := range rows {
		view, _ := decodeToolAnswer(t, call(map[string]any{"action": "get_task", "project_id": "proj-attn", "task_id": id}))["task"].(map[string]any)
		if got, ok := view["needs_attention"].(bool); !ok || got != attentionWant[id] {
			t.Fatalf("home get_task %s needs_attention = %v, want %v (%v)", id, view["needs_attention"], attentionWant[id], view)
		}
	}
}

// Purpose: the rule of TestNeedsAttentionCountsOnlyTasksThePersonMustActOnThisMachine
// holds for a linked machine's tasks: the machine decides with the same
// function, its list rows carry needs_attention (only on the tasks the
// person must act on), its counts agree, and get_task from the home says
// the same for every task. Threat as that test, across the link: the home
// reporting a linked board's finished work as needing attention.
// Owners: the machine's task list route (projectTaskOutcomeCounts,
// taskProgressFor -> markTaskProgressAttention), compactFleetTaskList and
// tool.CompactProjectTaskAnswer (compactProjectTask) at home, over the real
// pinned link between two in-process daemons.
func TestNeedsAttentionCountsOnlyTasksThePersonMustActOnLinkedMachine(t *testing.T) {
	home := newFleetTestSwarm(t, "home")
	defer home.cleanup()
	remote := newFleetTestSwarm(t, "remote")
	defer remote.cleanup()
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "box"})
	if status, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); status != http.StatusCreated {
		t.Fatalf("link: %d %v", status, out)
	}
	autos := jobautomation.NewStore(remote.s.sessions.Store().Underlying())
	remote.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: autos}))
	seedAttentionProject(t, remote.s.sessions.Store(), autos, remote.account, remote.user, "remote-attn", t.TempDir())
	moveTaskProgressClock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}
	raw, err := home.s.CallFleetProject(ctx, p, "box", "list_tasks", map[string]any{"project_id": "remote-attn", "include_workflow_tasks": true, "limit": float64(50)})
	if err != nil {
		t.Fatal(err)
	}
	rows, answer := outcomeTestRows(t, raw)
	checkAttentionRows(t, "linked", rows)
	checkOutcomeCountsAre(t, "linked", answer, attentionCountsWant)
	for id := range rows {
		if id == "plain" {
			// The machine's task route re-reads a task's branch from Git
			// (reconcileTaskGitStateContext); this fixture has no repository,
			// so there plain truthfully has no commits. Its row and get_task
			// agreement over the same facts is proven on this machine.
			continue
		}
		raw, err := home.s.CallFleetProject(ctx, p, "box", "get_task", map[string]any{"project_id": "remote-attn", "task_id": id})
		if err != nil {
			t.Fatal(err)
		}
		compact, err := tool.CompactProjectTaskAnswer("get_task", "", raw)
		if err != nil {
			t.Fatal(err)
		}
		view, _ := decodeToolAnswer(t, string(compact))["task"].(map[string]any)
		if got, ok := view["needs_attention"].(bool); !ok || got != attentionWant[id] {
			t.Fatalf("linked get_task %s needs_attention = %v, want %v (%s)", id, view["needs_attention"], attentionWant[id], compact)
		}
	}
}
