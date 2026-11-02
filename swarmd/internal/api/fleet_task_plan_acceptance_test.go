package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: a task's progress names what it waits for. A task bound to a
// submitted plan waits for the person to accept that plan, also when it was
// sent with auto_approve (which only passed the account's Start tasks rule);
// a task without a plan waits for Start tasks. Threat: the plan wait read as
// "waiting for approval (Start tasks)", sending the person to an account
// setting that is already auto. Owner: computeTaskProgress and
// taskProgressHistory. Narrowest layer: the progress function.
func TestTaskProgressNamesPlanAcceptanceApartFromStartTasks(t *testing.T) {
	bound := pebblestore.ProjectTaskRecord{ID: "t1", Status: "pending_approval", AutoApprove: true, CreatedAt: 10, PlanBinding: &pebblestore.ProjectTaskPlanBinding{SessionID: "s", PlanID: "plan-a", DefinitionRevision: 1}}
	got := computeTaskProgress(bound, nil, nil, 100)
	if got.Summary != pebblestore.ProjectTaskPlanAcceptanceReason || len(got.History) == 0 || got.History[0].What != "created · "+pebblestore.ProjectTaskPlanAcceptanceReason {
		t.Fatalf("plan task progress: %+v", got)
	}
	plain := pebblestore.ProjectTaskRecord{ID: "t2", Status: "pending_approval", CreatedAt: 10}
	got = computeTaskProgress(plain, nil, nil, 100)
	if got.Summary != "waiting for approval (Start tasks)" || len(got.History) == 0 || got.History[0].What != "created · waiting for approval (Start tasks)" {
		t.Fatalf("task without a plan: %+v", got)
	}
}

// Purpose: a task sent to a linked machine with a structured plan waits there
// for the person to accept that exact plan, and the conversation that sent it
// is where the person sees and accepts it. Requirements: (1) the home's
// get_task through the link shows the wait as plan acceptance (never the
// account's Start tasks rule, even though the home sent it with auto_approve),
// with the exact plan_binding, approve_task as the next action and the full
// plan under detail=plan; (2) approve_task through the link carries the
// binding to the machine's approve route, which refuses a call without the
// binding and a stale or other session, plan or revision, leaving the task
// waiting and its plan unaccepted: nothing accepts the plan by itself.
// Threat (forge, 2026-11-02): a plan task sent to a linked machine read as
// "waiting for approval (Start tasks)" under auto_approve, the Orchestrator's
// schema had no approve_task or binding fields, and the person was told to
// accept a card no home surface showed. Owners: Server.CallFleetProject
// (approve route forwarding), Server.ApproveProjectTask (the exact-binding
// guard), pebblestore.ProjectTaskState and computeTaskProgress (the wait's
// reason) and tool.CompactProjectTaskAnswer (the linked get_task view).
// Narrowest layer: two in-process daemons over the real pinned link, because
// only the machine's own approve route shows the binding was carried and
// enforced there. Successful acceptance starts the plan's run, which needs a
// provider-backed runner and is not exercised here.
func TestLinkedPlanTaskShowsPlanAcceptanceAndRefusesInexactApproval(t *testing.T) {
	home := newFleetTestSwarm(t, "home")
	defer home.cleanup()
	remote := newFleetTestSwarm(t, "remote")
	defer remote.cleanup()
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "box"})
	if status, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); status != http.StatusCreated {
		t.Fatalf("link: %d %v", status, out)
	}
	hp := identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}
	rp := identity.Principal{Type: identity.PrincipalTypeUser, UserID: remote.user, AccountScopeID: remote.account}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	remote.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(remote.s.sessions.Store().Underlying()))
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	_, entry, _, err := remote.s.workspace.AddForPrincipalWithEntry(rp, folder, "project", "", false)
	if err != nil {
		t.Fatal(err)
	}
	db := remote.s.sessions.Store()
	const projectID, taskID, sessionID, planID = "remote-project", "plan-task", "plan-task-session", "plan-a"
	if err := db.PutProject(remote.account, &pebblestore.ProjectRecord{ID: projectID, Name: "Remote project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: folder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	source := pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: folder, Provenance: "task"}
	if err := db.CreateSession(pebblestore.SessionSnapshot{
		ID: sessionID, UserID: remote.user, AccountScopeID: remote.account, WorkspacePath: worktree, WorktreeEnabled: true, WorktreeRootPath: worktree, WorktreeBranch: "agent/plan-task",
		Metadata: map[string]any{
			"project_id": projectID, "task_id": taskID,
			"swarm_v3_source_workspace_path": folder, "swarm_v3_source_workspace_id": entry.WorkspaceID, "swarm_v3_source_workspace_generation": entry.WorkspaceGeneration,
			"swarm_v3_worktree_owner_session_id": sessionID, "swarm_v3_runtime_workspace_path": worktree,
		},
	}); err != nil {
		t.Fatal(err)
	}
	document := &pebblestore.SessionPlanDocument{
		ID: planID, Title: "Synthetic plan", Info: pebblestore.SessionPlanInfo{Goal: "Prove exact plan acceptance"},
		Checkpoints:  []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Only step", Tasks: []string{"Do the step"}, AcceptanceCriteria: []string{"The step is done."}, Status: "pending"}},
		Requirements: []pebblestore.SessionPlanRequirement{{ID: "req-1", CheckpointID: "cp-1", Text: "The step is done."}},
	}
	if err := db.PutPlan(pebblestore.SessionPlanSnapshot{ID: planID, SessionID: sessionID, UserID: remote.user, AccountScopeID: remote.account, Title: "Synthetic plan", Document: document, Status: "draft", ApprovalState: "pending", Active: true, Version: 2}); err != nil {
		t.Fatal(err)
	}
	binding := pebblestore.ProjectTaskPlanBinding{SessionID: sessionID, PlanID: planID, DefinitionRevision: 2, Receipt: "synthetic-receipt"}
	// auto_approve as the home sends every linked task: it passed Start
	// tasks, and is no acceptance of the plan.
	if err := db.PutProjectTask(remote.account, &pebblestore.ProjectTaskRecord{
		ID: taskID, ProjectID: projectID, AccountID: remote.account, Title: "Plan task", Agent: "coder", OutcomeType: "code_pr",
		Status: "pending_approval", AutoApprove: true, SessionID: sessionID, WorkspacePath: worktree, SourceWorkspace: source, PlanBinding: &binding,
	}); err != nil {
		t.Fatal(err)
	}
	unchanged := func(label string) {
		t.Helper()
		task, found, err := db.GetProjectTask(remote.account, projectID, taskID)
		if err != nil || !found || task.Status != "pending_approval" || task.PlanBinding == nil || *task.PlanBinding != binding {
			t.Fatalf("%s changed the remote task: %+v %v %v", label, task, found, err)
		}
		plan, ok, err := db.GetPlan(sessionID, planID)
		if err != nil || !ok || plan.ApprovalState != "pending" || plan.Version != 2 {
			t.Fatalf("%s changed the remote plan: %+v %v %v", label, plan, ok, err)
		}
	}

	// (1) The sending conversation sees the plan wait, its exact binding
	// and the plan itself.
	raw, err := home.s.CallFleetProject(ctx, hp, "box", "get_task", map[string]any{"project_id": projectID, "task_id": taskID})
	if err != nil {
		t.Fatalf("linked get_task: %v", err)
	}
	compact, err := tool.CompactProjectTaskAnswer("get_task", tool.ProjectTaskDetailPlan, raw)
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Task struct {
			State       string `json:"state"`
			StateReason string `json:"state_reason"`
			NextAction  string `json:"next_action"`
			PlanBinding struct {
				SessionID          string `json:"session_id"`
				PlanID             string `json:"plan_id"`
				DefinitionRevision int    `json:"definition_revision"`
			} `json:"plan_binding"`
			Progress struct {
				Now  string `json:"now"`
				Next string `json:"next"`
			} `json:"progress"`
		} `json:"task"`
		Plan struct {
			PlanBinding  pebblestore.ProjectTaskPlanBinding `json:"plan_binding"`
			PlanDocument *pebblestore.SessionPlanDocument   `json:"plan_document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(compact, &view); err != nil {
		t.Fatalf("view: %s %v", compact, err)
	}
	v := view.Task
	if v.State != pebblestore.ProjectTaskStateNeedsYou || v.StateReason != pebblestore.ProjectTaskPlanAcceptanceReason || v.NextAction != "approve_task" {
		t.Fatalf("plan wait not shown as plan acceptance: %s", compact)
	}
	if strings.Contains(v.Progress.Now, "Start tasks") || v.Progress.Now != pebblestore.ProjectTaskPlanAcceptanceReason {
		t.Fatalf("progress names the account rule instead of plan acceptance: %q", v.Progress.Now)
	}
	if !strings.Contains(v.Progress.Next, "detail=plan") || !strings.Contains(v.Progress.Next, "machine") || !strings.Contains(v.Progress.Next, "explicit acceptance") {
		t.Fatalf("next does not say how the person sees and accepts the plan: %q", v.Progress.Next)
	}
	if v.PlanBinding.SessionID != sessionID || v.PlanBinding.PlanID != planID || v.PlanBinding.DefinitionRevision != 2 {
		t.Fatalf("plan binding: %+v", v.PlanBinding)
	}
	if view.Plan.PlanBinding != binding || view.Plan.PlanDocument == nil || view.Plan.PlanDocument.Title != "Synthetic plan" || len(view.Plan.PlanDocument.Checkpoints) != 1 {
		t.Fatalf("detail=plan does not carry the exact plan: %s", compact)
	}
	unchanged("reading the task")

	// (2) approve_task through the link: without the binding, or with a
	// stale or other one, the machine refuses and nothing is accepted.
	approve := func(guards map[string]any) error {
		args := map[string]any{"project_id": projectID, "task_id": taskID}
		for key, value := range guards {
			args[key] = value
		}
		_, err := home.s.CallFleetProject(ctx, hp, "box", "approve_task", args)
		return err
	}
	for _, c := range []struct {
		label  string
		guards map[string]any
		want   string
	}{
		{"no binding", nil, "exact session, plan and definition revision guards are required"},
		{"a stale revision", map[string]any{"session_id": sessionID, "plan_id": planID, "definition_revision": float64(1)}, "plan definition is stale"},
		{"another plan", map[string]any{"session_id": sessionID, "plan_id": "plan-b", "definition_revision": float64(2)}, "plan ID mismatch"},
		{"another session", map[string]any{"session_id": "other-session", "plan_id": planID, "definition_revision": float64(2)}, "session ID mismatch"},
	} {
		err := approve(c.guards)
		if err == nil || !strings.Contains(err.Error(), "box refused approve_task") || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %v, want the machine's refusal %q", c.label, err, c.want)
		}
		unchanged(c.label)
	}
}
