package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// planReviewPair is a home linked to a machine "box" that holds one task
// waiting for its structured plan to be accepted, sent from a home session.
type planReviewPair struct {
	home, remote *fleetTestSwarm
	hp           identity.Principal
	binding      pebblestore.ProjectTaskPlanBinding
	document     *pebblestore.SessionPlanDocument
}

const (
	planReviewProject = "remote-project"
	planReviewTask    = "plan-task"
	planReviewSession = "plan-task-session"
	planReviewPlan    = "plan-a"
	planReviewOrigin  = "home-chat"
	// The router's generic text: never shown as the plan.
	planReviewCanned = "Task Mission: canned stage summary"
)

func newPlanReviewPair(t *testing.T) *planReviewPair {
	t.Helper()
	home := newFleetTestSwarm(t, "home")
	t.Cleanup(home.cleanup)
	remote := newFleetTestSwarm(t, "remote")
	t.Cleanup(remote.cleanup)
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "box"})
	if status, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); status != http.StatusCreated {
		t.Fatalf("link: %d %v", status, out)
	}
	hp := identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}
	rp := identity.Principal{Type: identity.PrincipalTypeUser, UserID: remote.user, AccountScopeID: remote.account}

	remote.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(remote.s.sessions.Store().Underlying()))
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	_, entry, _, err := remote.s.workspace.AddForPrincipalWithEntry(rp, folder, "project", "", false)
	if err != nil {
		t.Fatal(err)
	}
	db := remote.s.sessions.Store()
	if err := db.PutProject(remote.account, &pebblestore.ProjectRecord{ID: planReviewProject, Name: "Remote project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: folder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	if err := db.CreateSession(pebblestore.SessionSnapshot{
		ID: planReviewSession, UserID: remote.user, AccountScopeID: remote.account, WorkspacePath: worktree, WorktreeEnabled: true, WorktreeRootPath: worktree, WorktreeBranch: "agent/plan-task",
		Metadata: map[string]any{
			"project_id": planReviewProject, "task_id": planReviewTask,
			"swarm_v3_source_workspace_path": folder, "swarm_v3_source_workspace_id": entry.WorkspaceID, "swarm_v3_source_workspace_generation": entry.WorkspaceGeneration,
			"swarm_v3_worktree_owner_session_id": planReviewSession, "swarm_v3_runtime_workspace_path": worktree,
		},
	}); err != nil {
		t.Fatal(err)
	}
	document := &pebblestore.SessionPlanDocument{
		ID: planReviewPlan, Title: "Synthetic review plan",
		Info: pebblestore.SessionPlanInfo{
			Goal: "Prove the person reviews this exact plan", Scope: "Only the review surface",
			Decisions: []string{"Read the plan document, not the router summary"}, Constraints: []string{"Never accept an unseen version"},
			SuccessCriteria: []string{"The plan reaches the client unchanged"}, ValidationStrategy: "Named tests only",
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Only step", Objective: "Do the one step", Tasks: []string{"Do the step"},
			AcceptanceCriteria: []string{"The step is done."}, Validation: []string{"go test the step"}, Status: "pending"}},
		Requirements: []pebblestore.SessionPlanRequirement{{ID: "req-1", CheckpointID: "cp-1", Text: "The step is done."}},
	}
	if err := db.PutPlan(pebblestore.SessionPlanSnapshot{ID: planReviewPlan, SessionID: planReviewSession, UserID: remote.user, AccountScopeID: remote.account, Title: document.Title, Document: document, Status: "draft", ApprovalState: "pending", Active: true, Version: 2}); err != nil {
		t.Fatal(err)
	}
	binding := pebblestore.ProjectTaskPlanBinding{SessionID: planReviewSession, PlanID: planReviewPlan, DefinitionRevision: 2, Receipt: "synthetic-receipt"}
	if err := db.PutProjectTask(remote.account, &pebblestore.ProjectTaskRecord{
		ID: planReviewTask, ProjectID: planReviewProject, AccountID: remote.account, Title: "Plan task", Agent: "coder", OutcomeType: "code_pr",
		Status: "pending_approval", AutoApprove: true, SessionID: planReviewSession, WorkspacePath: worktree,
		SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: folder, Provenance: "task"},
		PlanBinding:     &binding, PlanSummary: planReviewCanned, FullPlanMarkdown: "# " + planReviewCanned + "\n\nSwarm Execution\n\nVerification",
	}); err != nil {
		t.Fatal(err)
	}
	// The home session that sent it, owned by the home's person.
	if err := home.s.sessions.Store().CreateSession(pebblestore.SessionSnapshot{ID: planReviewOrigin, UserID: home.user, AccountScopeID: home.account, WorkspacePath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	// The record a send keeps (RecordFleetLinkedTask, as propose_task with
	// machine keeps it), from the machine's own answer.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, err := home.s.CallFleetProject(ctx, hp, "box", "get_task", map[string]any{"project_id": planReviewProject, "task_id": planReviewTask})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := home.s.RecordFleetLinkedTask(ctx, hp, tool.FleetLinkedTaskOrigin{SessionID: planReviewOrigin}, "box", planReviewProject, raw); err != nil {
		t.Fatal(err)
	}
	return &planReviewPair{home: home, remote: remote, hp: hp, binding: binding, document: document}
}

// route calls a task plan review route on the home as user.
func (p *planReviewPair) route(t *testing.T, user, method, path string, body any) (int, map[string]any, tool.ProjectTaskPlanReview) {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(method, path, reader), user, p.home.account)
	w := httptest.NewRecorder()
	p.home.s.handleTaskPlanReview(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	var envelope struct {
		Review tool.ProjectTaskPlanReview `json:"review"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	return w.Code, out, envelope.Review
}

// remoteUnchanged fails when the machine's task or plan moved.
func (p *planReviewPair) remoteUnchanged(t *testing.T, label string) {
	t.Helper()
	db := p.remote.s.sessions.Store()
	task, found, err := db.GetProjectTask(p.remote.account, planReviewProject, planReviewTask)
	if err != nil || !found || task.Status != "pending_approval" || task.PlanBinding == nil || *task.PlanBinding != p.binding {
		t.Fatalf("%s changed the remote task: %+v %v %v", label, task, found, err)
	}
	plan, ok, err := db.GetPlan(planReviewSession, planReviewPlan)
	if err != nil || !ok || plan.ApprovalState != "pending" || plan.Version != p.binding.DefinitionRevision {
		t.Fatalf("%s changed the remote plan: %+v %v %v", label, plan, ok, err)
	}
	if _, active, err := db.GetV3SessionActiveRunIntent(planReviewSession); err != nil || active {
		t.Fatalf("%s started a run: %v %v", label, active, err)
	}
}

const planReviewReadPath = "/v3/task-plan-review?machine=box&project_id=" + planReviewProject + "&task_id=" + planReviewTask

// Purpose: a structured plan proposed on a linked machine from a home
// conversation is reviewable, whole, from the home, and only the exact
// version the person read can be accepted. Requirements: (1) the home's
// review route returns the machine's plan_document unchanged (not the
// router's plan_summary or full_plan_markdown), its exact plan_binding,
// the machine/project/task and markdown derived from the document; (2)
// another user of the account, a task not sent from here and a gateway
// token read nothing; (3) a decision without the binding, or with a stale
// or other session, plan or revision, is refused with 409 and the current
// review, and neither the remote task, its plan nor a run changes; (4) a
// plan replaced on the machine after the read is refused for the old
// version and the fresh review shows the new one; (5) revise needs
// feedback; (6) the exact binding passes every guard on both sides and
// reaches the machine's approve service. Threat (forge, 2026-11-02): the
// console's approve posted an empty body, a linked plan had no home review
// surface, and the person was asked to accept a plan they could not see.
// Owners: Server.ReadTaskPlanReview, Server.DecideTaskPlan,
// tool.BuildProjectTaskPlanReview, tool.ProjectTaskPlanReviewMatches,
// Server.CallFleetProject, Server.ApproveProjectTask. Narrowest layer: two
// in-process daemons over the real pinned link, since only the machine's own
// routes show what crossed. A successful acceptance's run needs a configured
// execution model and agent service on the machine, which this harness does
// not provide; the test asserts the exact binding is past every guard (the
// refusal is the machine's missing execution service, not a guard) and that
// no run started.
func TestTaskPlanReviewLinkedExactPlanAndBinding(t *testing.T) {
	p := newPlanReviewPair(t)

	// (1) The review: the plan document exactly as the machine holds it.
	status, out, review := p.route(t, p.home.user, http.MethodGet, planReviewReadPath, nil)
	if status != http.StatusOK {
		t.Fatalf("read: %d %v", status, out)
	}
	if review.Machine != "box" || review.ProjectID != planReviewProject || review.TaskID != planReviewTask || review.RunStatus != "pending_approval" || !review.PlanBound {
		t.Fatalf("identity: %+v", review)
	}
	if review.Decision != tool.ProjectTaskReviewAcceptPlan || review.Problem != "" {
		t.Fatalf("decision: %s %q", review.Decision, review.Problem)
	}
	if b := review.PlanBinding; b == nil || b.SessionID != planReviewSession || b.PlanID != planReviewPlan || b.DefinitionRevision != 2 {
		t.Fatalf("binding: %+v", review.PlanBinding)
	}
	if !reflect.DeepEqual(review.PlanDocument, p.document) {
		got, _ := json.Marshal(review.PlanDocument)
		want, _ := json.Marshal(p.document)
		t.Fatalf("plan_document changed in transit:\n got %s\nwant %s", got, want)
	}
	for _, want := range []string{"# Synthetic review plan", "## Objective", "Prove the person reviews this exact plan", "## Scope", "## Decisions", "## Constraints", "Never accept an unseen version",
		"## Requirements", "**req-1** The step is done. (checkpoint cp-1)", "### 1. Only step", "- Do the step", "Acceptance criteria:", "Validation:", "go test the step", "## Success criteria", "## Validation", "Named tests only"} {
		if !strings.Contains(review.PlanMarkdown, want) {
			t.Fatalf("plan_markdown lacks %q:\n%s", want, review.PlanMarkdown)
		}
	}
	if strings.Contains(review.PlanMarkdown, planReviewCanned) || strings.Contains(review.PlanMarkdown, "Swarm Execution") || strings.Contains(out["review"].(map[string]any)["plan_markdown"].(string), "Task Mission") {
		t.Fatalf("the router's canned text shown as the plan:\n%s", review.PlanMarkdown)
	}
	if _, has := out["review"].(map[string]any)["full_plan_markdown"]; has {
		t.Fatal("the review carries full_plan_markdown")
	}
	if strings.Contains(string(planReviewJSON(t, out)), "synthetic-receipt") {
		t.Fatal("the binding receipt left the machine")
	}
	p.remoteUnchanged(t, "reading the review")

	// (2) Who may read: another user of the account, a task not sent from
	// here, a gateway token.
	if status, _, _ := p.route(t, "someone-else", http.MethodGet, planReviewReadPath, nil); status != http.StatusNotFound {
		t.Fatalf("another user read the plan: %d", status)
	}
	if status, _, _ := p.route(t, p.home.user, http.MethodGet, "/v3/task-plan-review?machine=box&project_id="+planReviewProject+"&task_id=not-sent-here", nil); status != http.StatusNotFound {
		t.Fatalf("a task not sent from here: %d", status)
	}
	if status, _, _ := p.route(t, p.home.user, http.MethodGet, "/v3/task-plan-review?machine=box&project_id="+planReviewProject, nil); status != http.StatusBadRequest {
		t.Fatalf("no task id: %d", status)
	}

	// (3) Inexact decisions are refused before anything crosses.
	decide := func(binding map[string]any, decision, feedback string) (int, map[string]any, tool.ProjectTaskPlanReview) {
		body := map[string]any{"machine": "box", "project_id": planReviewProject, "task_id": planReviewTask, "decision": decision}
		if binding != nil {
			body["plan_binding"] = binding
		}
		if feedback != "" {
			body["feedback"] = feedback
		}
		return p.route(t, p.home.user, http.MethodPost, taskPlanReviewDecisionPath, body)
	}
	for _, c := range []struct {
		label   string
		binding map[string]any
		want    string
	}{
		{"no binding", nil, "names its exact version"},
		{"a stale revision", map[string]any{"session_id": planReviewSession, "plan_id": planReviewPlan, "definition_revision": 1}, "the plan changed"},
		{"another plan", map[string]any{"session_id": planReviewSession, "plan_id": "plan-b", "definition_revision": 2}, "the plan changed"},
		{"another session", map[string]any{"session_id": "other-session", "plan_id": planReviewPlan, "definition_revision": 2}, "the plan changed"},
	} {
		status, out, current := decide(c.binding, "accept", "")
		if status != http.StatusConflict || !strings.Contains(asStringValue(out["error"]), c.want) || current.Decision != tool.ProjectTaskReviewAcceptPlan || current.PlanBinding.DefinitionRevision != 2 {
			t.Fatalf("%s: %d %v", c.label, status, out)
		}
		p.remoteUnchanged(t, c.label)
	}
	exact := map[string]any{"session_id": planReviewSession, "plan_id": planReviewPlan, "definition_revision": 2}
	// (5) Revise names what to change.
	if status, out, _ := decide(exact, "revise", ""); status != http.StatusBadRequest || !strings.Contains(asStringValue(out["error"]), "feedback") {
		t.Fatalf("revise without feedback: %d %v", status, out)
	}
	p.remoteUnchanged(t, "revise without feedback")
	// Only a person decides.
	agent := identity.Principal{Type: "service", UserID: p.home.user, AccountScopeID: p.home.account}
	if _, err := p.home.s.DecideTaskPlan(context.Background(), agent, TaskPlanDecision{Machine: "box", ProjectID: planReviewProject, TaskID: planReviewTask, Decision: "accept",
		PlanBinding: &tool.ProjectTaskPlanBindingView{SessionID: planReviewSession, PlanID: planReviewPlan, DefinitionRevision: 2}}); err == nil || !strings.Contains(err.Error(), "only a person") {
		t.Fatalf("a non-person decided: %v", err)
	}
	p.remoteUnchanged(t, "a non-person's decision")

	// (6) The exact binding passes the home's check and every guard of the
	// machine's approve service; this harness has no execution model there.
	status, out, after := decide(exact, "accept", "")
	if status == http.StatusOK {
		if after.RunStatus == "pending_approval" {
			t.Fatalf("accepted but still waiting: %+v", after)
		}
	} else {
		msg := asStringValue(out["error"])
		t.Logf("exact accept reached the machine's approve service, which answered: %s", msg)
		for _, guard := range []string{"guards are required", "mismatch", "stale", "the plan changed", "not found"} {
			if strings.Contains(msg, guard) {
				t.Fatalf("the exact binding was refused by a guard: %s", msg)
			}
		}
		if status != http.StatusConflict || !strings.Contains(msg, "box refused approve_task") || after.Decision != tool.ProjectTaskReviewAcceptPlan {
			t.Fatalf("exact accept: %d %v", status, out)
		}
		p.remoteUnchanged(t, "an accept the machine could not run")
	}
}

// Purpose: a plan replaced on its machine after the person read it is never
// accepted for the version they read. Requirement: the decision with the
// old binding is refused (409) with the fresh review showing the new
// version, and the remote task keeps waiting on the new one. Threat: an
// approval of "the latest" plan the person never saw. Owners:
// Server.DecideTaskPlan, tool.ProjectTaskPlanReviewMatches. Narrowest
// layer: the two-daemon link, where the replacement happens on the machine.
func TestTaskPlanReviewRefusesAReplacedPlan(t *testing.T) {
	p := newPlanReviewPair(t)
	if status, out, _ := p.route(t, p.home.user, http.MethodGet, planReviewReadPath, nil); status != http.StatusOK {
		t.Fatalf("read: %d %v", status, out)
	}
	db := p.remote.s.sessions.Store()
	replaced := *p.document
	replaced.Title = "Replaced plan"
	if err := db.PutPlan(pebblestore.SessionPlanSnapshot{ID: planReviewPlan, SessionID: planReviewSession, UserID: p.remote.user, AccountScopeID: p.remote.account, Title: replaced.Title, Document: &replaced, Status: "draft", ApprovalState: "pending", Active: true, Version: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateProjectTask(p.remote.account, planReviewProject, planReviewTask, func(task *pebblestore.ProjectTaskRecord) error {
		task.PlanBinding.DefinitionRevision = 3
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	status, out, current := p.route(t, p.home.user, http.MethodPost, taskPlanReviewDecisionPath, map[string]any{
		"machine": "box", "project_id": planReviewProject, "task_id": planReviewTask, "decision": "accept",
		"plan_binding": map[string]any{"session_id": planReviewSession, "plan_id": planReviewPlan, "definition_revision": 2},
	})
	if status != http.StatusConflict || !strings.Contains(asStringValue(out["error"]), "current definition_revision 3") {
		t.Fatalf("old version decided: %d %v", status, out)
	}
	if current.PlanBinding == nil || current.PlanBinding.DefinitionRevision != 3 || current.PlanDocument == nil || current.PlanDocument.Title != "Replaced plan" {
		t.Fatalf("the refusal does not show the new version: %+v", current)
	}
	task, _, _ := db.GetProjectTask(p.remote.account, planReviewProject, planReviewTask)
	plan, _, _ := db.GetPlan(planReviewSession, planReviewPlan)
	if task.Status != "pending_approval" || plan.ApprovalState != "pending" {
		t.Fatalf("the replaced plan moved: %s %s", task.Status, plan.ApprovalState)
	}
}

// Purpose: this Swarm's own tasks get the same review and rule as linked
// ones. Requirements: a home plan task's review carries its plan_document
// and binding with no machine; an ordinary task (no plan) is decided as
// start and refuses a plan binding; a running task decides nothing, so a
// repeated click never starts it twice; another account reads nothing.
// Owners: Server.ReadTaskPlanReview (home path), Server.DecideTaskPlan,
// tool.ProjectTaskPlanReviewMatches. Narrowest layer: one daemon's route.
func TestTaskPlanReviewHomeTasks(t *testing.T) {
	home := newFleetTestSwarm(t, "home")
	defer home.cleanup()
	db := home.s.sessions.Store()
	const sessionID, planID = "home-plan-session", "home-plan"
	if err := db.CreateSession(pebblestore.SessionSnapshot{ID: sessionID, UserID: home.user, AccountScopeID: home.account, WorkspacePath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	document := &pebblestore.SessionPlanDocument{ID: planID, Title: "Home plan", Info: pebblestore.SessionPlanInfo{Goal: "Home goal"},
		Checkpoints:  []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Step", Tasks: []string{"Do it"}, AcceptanceCriteria: []string{"Done."}}},
		Requirements: []pebblestore.SessionPlanRequirement{{ID: "req-1", CheckpointID: "cp-1", Text: "Done."}}}
	if err := db.PutPlan(pebblestore.SessionPlanSnapshot{ID: planID, SessionID: sessionID, UserID: home.user, AccountScopeID: home.account, Document: document, Status: "draft", ApprovalState: "pending", Active: true, Version: 4}); err != nil {
		t.Fatal(err)
	}
	for _, task := range []*pebblestore.ProjectTaskRecord{
		{ID: "home-plan-task", ProjectID: "p", AccountID: home.account, Title: "Plan", Agent: "coder", Status: "pending_approval", SessionID: sessionID, PlanBinding: &pebblestore.ProjectTaskPlanBinding{SessionID: sessionID, PlanID: planID, DefinitionRevision: 4}, PlanSummary: planReviewCanned},
		{ID: "plain-task", ProjectID: "p", AccountID: home.account, Title: "Plain", Status: "pending_approval", Agent: "coder"},
		{ID: "running-task", ProjectID: "p", AccountID: home.account, Title: "Running", Status: "in_progress", Agent: "coder"},
	} {
		if err := db.PutProjectTask(home.account, task); err != nil {
			t.Fatal(err)
		}
	}
	call := func(user, method, path string, body any) (int, map[string]any, tool.ProjectTaskPlanReview) {
		raw, _ := json.Marshal(body)
		req := requestWithTestPrincipalForAccount(httptest.NewRequest(method, path, strings.NewReader(string(raw))), user, home.account)
		w := httptest.NewRecorder()
		home.s.handleTaskPlanReview(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		var envelope struct {
			Review tool.ProjectTaskPlanReview `json:"review"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &envelope)
		return w.Code, out, envelope.Review
	}
	status, out, review := call(home.user, http.MethodGet, "/v3/task-plan-review?project_id=p&task_id=home-plan-task", nil)
	if status != http.StatusOK || review.Machine != "" || review.Decision != tool.ProjectTaskReviewAcceptPlan || !reflect.DeepEqual(review.PlanDocument, document) || review.PlanBinding.DefinitionRevision != 4 || strings.Contains(review.PlanMarkdown, planReviewCanned) {
		t.Fatalf("home plan review: %d %v", status, out)
	}
	status, _, plain := call(home.user, http.MethodGet, "/v3/task-plan-review?project_id=p&task_id=plain-task", nil)
	if status != http.StatusOK || plain.Decision != tool.ProjectTaskReviewStart || plain.PlanBound || plain.PlanDocument != nil {
		t.Fatalf("ordinary task review: %d %+v", status, plain)
	}
	status, out, _ = call(home.user, http.MethodPost, taskPlanReviewDecisionPath, map[string]any{"project_id": "p", "task_id": "plain-task", "decision": "accept",
		"plan_binding": map[string]any{"session_id": sessionID, "plan_id": planID, "definition_revision": 4}})
	if status != http.StatusConflict || !strings.Contains(asStringValue(out["error"]), "no plan to accept") {
		t.Fatalf("a plan binding on an ordinary task: %d %v", status, out)
	}
	status, out, running := call(home.user, http.MethodPost, taskPlanReviewDecisionPath, map[string]any{"project_id": "p", "task_id": "running-task", "decision": "accept"})
	if status != http.StatusConflict || running.Decision != tool.ProjectTaskReviewNone || running.RunStatus != "in_progress" {
		t.Fatalf("a running task decided again: %d %v", status, out)
	}
	if task, _, _ := db.GetProjectTask(home.account, "p", "running-task"); task.Status != "in_progress" {
		t.Fatalf("running task moved: %s", task.Status)
	}
	if status, _, _ := call(home.user, http.MethodGet, "/v3/task-plan-review?project_id=p&task_id=missing", nil); status != http.StatusNotFound {
		t.Fatalf("missing task: %d", status)
	}
	// Another account's principal reads nothing of this account's task.
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, "/v3/task-plan-review?project_id=p&task_id=home-plan-task", nil), home.user, "another-account")
	w := httptest.NewRecorder()
	home.s.handleTaskPlanReview(w, req)
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "Home goal") {
		t.Fatalf("cross-account read: %d %s", w.Code, w.Body.String())
	}
}

func planReviewJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
