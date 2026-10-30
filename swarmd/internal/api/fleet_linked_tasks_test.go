package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// linkedTaskCall runs one native manage_projects call as the home session and
// returns its envelope (tool, action, machine, state, data, ...).
func linkedTaskCall(t *testing.T, h *remoteWaitHarness, session string, args map[string]any) (map[string]any, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, _ := json.Marshal(args)
	out, err := h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: session, Principal: h.p}, tool.Call{Name: "manage_projects", Arguments: string(raw)})
	if err != nil {
		return nil, err
	}
	if _, err := tool.EnvelopeData(out); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("tool output: %s %v", out, err)
	}
	return decoded, nil
}

// envelopeDataMap is an envelope's data object (nil when it is not one).
func envelopeDataMap(envelope map[string]any) map[string]any {
	data, _ := envelope["data"].(map[string]any)
	return data
}

// linkedTaskRoute calls a linked-task route on the home as user.
func linkedTaskRoute(t *testing.T, h *remoteWaitHarness, user, path string) (int, map[string]any) {
	t.Helper()
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, path, nil), user, h.home.account)
	w := httptest.NewRecorder()
	h.home.s.handleFleetLinkedTasks(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// Purpose: when the home's Orchestrator sends a task to a linked machine, the
// home keeps a durable record (machine, remote project/task, title, sending
// session and project, last observed state) so the session and project can
// list it, and refreshes it from what the link later answers (get_task,
// list_tasks, the linked wait's reads, a 404). Threats: a send that leaves no
// trace at home, a read that invents records, cross-principal listing, and an
// observation that silently goes stale. Boundary: the native manage_projects
// runtime (executeFleetProjects), Server.RecordFleetLinkedTask,
// Server.CallFleetProject (observeFleetLinkedTasks), Server.ListFleetLinkedTasks
// and handleFleetLinkedTasks, across two in-process daemons over the real
// pinned link. This is the narrowest layer where the send, the remote answer
// and the home record meet. No provider runs.
func TestFleetLinkedTaskRecordAndList(t *testing.T) {
	h := newRemoteWaitHarness(t, "pre")
	h.createGoal(t, "home-orch", map[string]any{"project_id": "home-project"})
	h.createGoal(t, "other-orch", map[string]any{"project_id": "home-project"})
	// The remote project needs a real folder for a task to be sent there.
	h.remote.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(h.remote.s.sessions.Store().Underlying()))
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	added, err := h.remote.s.workspace.AddForPrincipal(identity.Principal{Type: identity.PrincipalTypeUser, UserID: h.remote.user, AccountScopeID: h.remote.account}, folder, "project", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.remote.s.sessions.Store().PutProject(h.remote.account, &pebblestore.ProjectRecord{ID: "remote-project", Name: "Remote project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: folder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}

	// Nothing is recorded before a send, and reading a remote task never creates a record.
	if _, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "get_task", "machine": "box", "project_id": "remote-project", "task_id": "pre"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := h.home.s.fleet.GetLinkedTask(h.home.account, "box", "remote-project", "pre"); err != nil || found {
		t.Fatalf("a read created a record: %v %v", found, err)
	}

	sent, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "propose_task", "machine": "box", "project_id": "remote-project", "title": "Linked audit", "agent": "finder"})
	if err != nil {
		t.Fatal(err)
	}
	if sent["untrusted_content"] != true || sent["machine"] != "box" || envelopeDataMap(sent)["linked_record_error"] != nil || envelopeDataMap(sent)["linked_task_id"] == nil {
		t.Fatalf("send result: %v", sent)
	}
	var created struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	data, _ := json.Marshal(sent["data"])
	if err := json.Unmarshal(data, &created); err != nil || created.Task.ID == "" {
		t.Fatalf("remote answer: %s %v", data, err)
	}
	taskID := created.Task.ID
	record, found, err := h.home.s.fleet.GetLinkedTask(h.home.account, "box", "remote-project", taskID)
	if err != nil || !found {
		t.Fatalf("no record after send: %v %v", found, err)
	}
	if record.Machine != "box" || record.ProjectID != "remote-project" || record.TaskID != taskID || record.Title != "Linked audit" || record.HomeSessionID != "home-orch" || record.HomeProjectID != "home-project" || record.CreatedAt == 0 || record.Status == "" {
		t.Fatalf("record: %+v", record)
	}
	if _, found, _ := h.home.s.sessions.Store().GetProjectTask(h.home.account, "remote-project", taskID); found {
		t.Fatal("the remote task was copied into the home's own tasks")
	}

	// An observation through get_task refreshes status and progress.
	if _, err := h.remote.s.sessions.Store().UpdateProjectTask(h.remote.account, "remote-project", taskID, func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = "needs_review"
		task.EnsureTaskAttempts()
		if attempt := task.ActiveAttempt(); attempt != nil {
			attempt.Summary = "Audit written"
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "get_task", "machine": "box", "project_id": "remote-project", "task_id": taskID}); err != nil {
		t.Fatal(err)
	}
	record, _, _ = h.home.s.fleet.GetLinkedTask(h.home.account, "box", "remote-project", taskID)
	if record.Status != "needs_review" || record.ObservedAt == 0 {
		t.Fatalf("get_task observation not kept: %+v", record)
	}
	// list_tasks observations, the path a list read takes.
	if _, err := h.remote.s.sessions.Store().UpdateProjectTask(h.remote.account, "remote-project", taskID, func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = "completed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "list_tasks", "machine": "box", "project_id": "remote-project"}); err != nil {
		t.Fatal(err)
	}
	record, _, _ = h.home.s.fleet.GetLinkedTask(h.home.account, "box", "remote-project", taskID)
	if record.Status != "completed" || record.HomeSessionID != "home-orch" {
		t.Fatalf("list_tasks observation not kept: %+v", record)
	}
	// The linked wait's follower reads through CallFleetProject; a task the
	// machine no longer has is kept and marked missing, never dropped.
	if _, err := h.home.s.fleet.PutLinkedTask(pebblestore.FleetLinkedTask{AccountScopeID: h.home.account, Machine: "box", ProjectID: "remote-project", TaskID: "gone", Title: "Gone", HomeSessionID: "home-orch", HomeProjectID: "home-project", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.home.s.CallFleetProject(context.Background(), h.p, "box", "get_task", map[string]any{"project_id": "remote-project", "task_id": "gone"}); err == nil {
		t.Fatal("missing remote task read succeeded")
	}
	gone, _, _ := h.home.s.fleet.GetLinkedTask(h.home.account, "box", "remote-project", "gone")
	if !gone.Missing || gone.Status != "running" {
		t.Fatalf("missing observation: %+v", gone)
	}

	// List by session (route and native) and by project.
	code, out := linkedTaskRoute(t, h, h.home.user, fleetLinkedTasksPath+"?session_id=home-orch")
	if code != http.StatusOK || out["untrusted_content"] != true || len(out["linked_tasks"].([]any)) != 2 {
		t.Fatalf("session list: %d %v", code, out)
	}
	if code, out := linkedTaskRoute(t, h, h.home.user, fleetLinkedTasksPath+"?session_id=other-orch"); code != http.StatusOK || len(out["linked_tasks"].([]any)) != 0 {
		t.Fatalf("another session listed this session's tasks: %d %v", code, out)
	}
	if code, out := linkedTaskRoute(t, h, h.home.user, fleetLinkedTasksPath+"?project_id=home-project"); code != http.StatusOK || len(out["linked_tasks"].([]any)) != 2 {
		t.Fatalf("project list: %d %v", code, out)
	}
	native, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "list_linked_tasks"})
	listed, _ := envelopeDataMap(native)["linked_tasks"].([]any)
	if err != nil || envelopeDataMap(native)["session_id"] != "home-orch" || len(listed) != 2 || native["untrusted_content"] != true {
		t.Fatalf("native list: %v %v", native, err)
	}
	// Another user cannot list this user's session; bad requests are refused.
	if code, _ := linkedTaskRoute(t, h, "someone-else", fleetLinkedTasksPath+"?session_id=home-orch"); code != http.StatusNotFound {
		t.Fatalf("cross-user session list: %d", code)
	}
	if code, _ := linkedTaskRoute(t, h, h.home.user, fleetLinkedTasksPath+"?session_id=home-orch&project_id=home-project"); code != http.StatusBadRequest {
		t.Fatalf("both scopes: %d", code)
	}
	if code, _ := linkedTaskRoute(t, h, h.home.user, fleetLinkedTasksPath); code != http.StatusBadRequest {
		t.Fatalf("no scope: %d", code)
	}
	if other, err := h.home.s.ListFleetLinkedTasks(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: h.home.user, AccountScopeID: "other-account"}, "", "home-project"); err != nil || len(other) != 0 {
		t.Fatalf("cross-account project list: %v %v", other, err)
	}
}

// Purpose: the home user and Orchestrator can read what a linked task's
// session is doing, read-only, through the existing link. Threats: a missing
// task or unreachable machine surfacing as success or a hang, a machine the
// account does not own being reachable, a read that changes the remote
// session, and remote text losing its untrusted marking. Boundary:
// Server.ReadFleetLinkedTaskSession behind handleFleetLinkedTasks and the
// native read_task_session action, plus the permission gate's linked rule.
func TestFleetLinkedTaskSessionRead(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.createGoal(t, "home-orch", map[string]any{"project_id": "home-project"})
	for i, text := range []string{"Reading the repository.", "Found the failing test."} {
		id := "remote-msg-" + string(rune('a'+i))
		if _, err := h.remote.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "rs-t1", AccountScopeID: h.remote.account, UserID: h.remote.user, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: id, PayloadHash: id, Message: &pebblestore.MessageSnapshot{ID: id, Role: "assistant", Content: text}}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := h.remote.s.sessions.ListSessionMessages("rs-t1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	code, view := linkedTaskRoute(t, h, h.home.user, fleetLinkedTaskSessionPath+"?machine=box&project_id=remote-project&task_id=t1")
	if code != http.StatusOK || view["session_id"] != "rs-t1" || view["read_only"] != true || view["untrusted_content"] != true || view["status"] != "running" {
		t.Fatalf("session read: %d %v", code, view)
	}
	messages, _ := view["messages"].([]any)
	if len(messages) != 2 || messages[1].(map[string]any)["content"] != "Found the failing test." {
		t.Fatalf("messages: %v", view["messages"])
	}
	native, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "read_task_session", "machine": "box", "project_id": "remote-project", "task_id": "t1", "limit": 1})
	read, _ := envelopeDataMap(native)["messages"].([]any)
	if err != nil || native["untrusted_content"] != true || native["machine"] != "box" || len(read) != 1 || envelopeDataMap(native)["has_more_older"] != true {
		t.Fatalf("native read: %v %v", native, err)
	}
	after, err := h.remote.s.sessions.ListSessionMessages("rs-t1", 0, 100)
	if err != nil || len(after) != len(before) {
		t.Fatalf("a read changed the remote session: %d -> %d %v", len(before), len(after), err)
	}

	// Under ask, the read runs without a card: it sends and starts nothing.
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleAsk)
	perms := permission.NewService(pebblestore.NewPermissionStore(h.home.s.sessions.Store().Underlying()), nil, nil)
	perms.SetSessionResolver(h.home.s.sessions)
	auth, err := perms.AuthorizeToolCall(permission.AuthorizationInput{SessionID: "home-orch", AccountScopeID: h.home.account, RunID: "run-1", CallID: "read", ToolName: "manage_projects", ToolArguments: `{"action":"read_task_session","machine":"box","project_id":"remote-project","task_id":"t1"}`, Mode: "auto"})
	if err != nil || auth.Decision != permission.AuthorizationApprove || auth.Record != nil {
		t.Fatalf("ask gate on a read: %+v %v", auth, err)
	}

	// Failures are explicit.
	if code, out := linkedTaskRoute(t, h, h.home.user, fleetLinkedTaskSessionPath+"?machine=box&project_id=remote-project&task_id=no-such-task"); code != http.StatusNotFound || !strings.Contains(out["error"].(string), "not found on box") {
		t.Fatalf("missing task: %d %v", code, out)
	}
	if code, _ := linkedTaskRoute(t, h, h.home.user, fleetLinkedTaskSessionPath+"?machine=box&project_id=remote-project"); code != http.StatusBadRequest {
		t.Fatalf("missing task_id: %d", code)
	}
	fleetActivityTestSetWork(t, h.home, "nobox", pebblestore.RuleAuto)
	if code, _ := linkedTaskRoute(t, h, h.home.user, fleetLinkedTaskSessionPath+"?machine=nobox&project_id=remote-project&task_id=t1"); code != http.StatusNotFound {
		t.Fatalf("unlinked machine: %d", code)
	}
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleOff)
	if code, _ := linkedTaskRoute(t, h, h.home.user, fleetLinkedTaskSessionPath+"?machine=box&project_id=remote-project&task_id=t1"); code != http.StatusForbidden {
		t.Fatalf("work off: %d", code)
	}
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleAuto)
	h.remote.s.CloseFleetListener()
	start := time.Now()
	code, out := linkedTaskRoute(t, h, h.home.user, fleetLinkedTaskSessionPath+"?machine=box&project_id=remote-project&task_id=t1")
	if code != http.StatusBadGateway || !strings.Contains(out["error"].(string), "could not reach linked machine box") {
		t.Fatalf("unreachable machine: %d %v", code, out)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("unreachable machine read did not fail promptly")
	}
	if _, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "read_task_session", "machine": "box", "project_id": "remote-project", "task_id": "t1"}); err == nil || !strings.Contains(err.Error(), "could not reach linked machine box") {
		t.Fatalf("native unreachable: %v", err)
	}
}
