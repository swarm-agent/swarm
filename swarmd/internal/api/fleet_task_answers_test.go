package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: an Orchestrator that delegates to a linked machine, then checks
// the task, reads a few hundred bytes per call that say where the task is
// and what to do next: propose_task answers with the row and the linked
// record's id (within 600 B), get_task with the compact view (within 2 KB)
// carrying status, the report summary, session and next action, and
// detail=full still returns the machine's record. The home's linked-task
// record keeps observing the full answer.
// Threat: the linked forward passed propose_task (~3.7 KB) and get_task
// (~8 KB) through raw on every delegation and check; compacting inside the
// forward would starve the home's record and the linked wait of the fields
// they read.
// Owners: Runtime.executeFleetProjects (CompactProjectTaskAnswer after
// RecordFleetLinkedTask), Server.CallFleetProject and
// observeFleetLinkedTasks, across two in-process daemons over the real
// pinned link; the narrowest layer where the remote answer, the home record
// and the agent-visible output meet. No provider runs.
func TestFleetTaskAnswersCompactForOrchestrator(t *testing.T) {
	h := newRemoteWaitHarness(t, "audit")
	h.createGoal(t, "home-orch", map[string]any{"project_id": "home-project"})
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
	call := func(args map[string]any) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		raw, _ := json.Marshal(args)
		return h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: "home-orch", Principal: h.p}, tool.Call{Name: "manage_projects", Arguments: string(raw)})
	}

	sent, err := call(map[string]any{"action": "propose_task", "machine": "box", "project_id": "remote-project", "title": "Read-only audit", "agent": "finder"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) > 600 {
		t.Fatalf("linked propose_task is %d bytes: %s", len(sent), sent)
	}
	var proposed struct {
		Machine      string `json:"machine"`
		Untrusted    bool   `json:"untrusted_content"`
		LinkedTaskID string `json:"linked_task_id"`
		Data         struct {
			Task       tool.ProjectTaskRow `json:"task"`
			NextAction string              `json:"next_action"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(sent), &proposed); err != nil {
		t.Fatal(err)
	}
	taskID := proposed.Data.Task.ID
	if taskID == "" || proposed.Machine != "box" || !proposed.Untrusted || proposed.LinkedTaskID != taskID || proposed.Data.Task.Title != "Read-only audit" || proposed.Data.Task.Agent != "finder" || proposed.Data.Task.Status == "" || proposed.Data.NextAction == "" {
		t.Fatalf("linked propose_task answer: %s", sent)
	}
	for _, leaked := range []string{"pipeline_stages", "model_preview", "submission_hash", "account_id", "context_pool_summary", "home_session_id"} {
		if strings.Contains(sent, leaked) {
			t.Fatalf("linked propose_task carries %q: %s", leaked, sent)
		}
	}
	if _, found, err := h.home.s.fleet.GetLinkedTask(h.home.account, "box", "remote-project", taskID); err != nil || !found {
		t.Fatalf("no linked record after send: %v %v", found, err)
	}

	// A delegated task (seeded with its attempt and session, no run) finishes
	// on the machine with a long report.
	proposedID := taskID
	taskID = "audit"
	report := "Audit: " + strings.Repeat("the store keeps every record and the routes check ownership. ", 60)
	if _, err := h.remote.s.sessions.Store().UpdateProjectTask(h.remote.account, "remote-project", taskID, func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = "completed"
		task.FullPlanMarkdown = strings.Repeat("plan line the Orchestrator reads only on request\n", 80)
		task.EnsureTaskAttempts()
		if attempt := task.ActiveAttempt(); attempt != nil {
			attempt.Summary = report
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	read, err := call(map[string]any{"action": "get_task", "machine": "box", "project_id": "remote-project", "task_id": taskID})
	if err != nil {
		t.Fatal(err)
	}
	if len(read) > 2048 {
		t.Fatalf("linked get_task is %d bytes: %s", len(read), read)
	}
	var checked struct {
		Untrusted bool `json:"untrusted_content"`
		Data      struct {
			Task tool.ProjectTaskView `json:"task"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(read), &checked); err != nil {
		t.Fatal(err)
	}
	v := checked.Data.Task
	if !checked.Untrusted || v.ID != taskID || v.Status != "completed" || v.SessionID == "" || v.Progress.Now == "" || v.Result == nil || !strings.HasPrefix(v.Result.Summary, "Audit: the store") || v.NextAction == "" || v.More == "" {
		t.Fatalf("linked get_task view: %s", read)
	}
	if strings.Contains(read, "plan line the Orchestrator") || strings.Contains(read, "full_plan_markdown") {
		t.Fatalf("linked get_task carries the plan: %s", read)
	}
	// Reading the sent task refreshes the home's record from the full
	// answer (status and attempt), while the agent reads the compact one.
	if _, err := h.remote.s.sessions.Store().UpdateProjectTask(h.remote.account, "remote-project", proposedID, func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = "needs_review"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sentRead, err := call(map[string]any{"action": "get_task", "machine": "box", "project_id": "remote-project", "task_id": proposedID})
	if err != nil || len(sentRead) > 2048 || !strings.Contains(sentRead, `"status":"needs_review"`) {
		t.Fatalf("sent task read: %v %s", err, sentRead)
	}
	if record, _, _ := h.home.s.fleet.GetLinkedTask(h.home.account, "box", "remote-project", proposedID); record.Status != "needs_review" || record.ObservedAt == 0 {
		t.Fatalf("linked record not refreshed from the full answer: %+v", record)
	}

	full, err := call(map[string]any{"action": "get_task", "machine": "box", "project_id": "remote-project", "task_id": taskID, "detail": "full"})
	if err != nil {
		t.Fatal(err)
	}
	var fullRead struct {
		Data struct {
			Task pebblestore.ProjectTaskRecord `json:"task"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(full), &fullRead); err != nil || fullRead.Data.Task.ID != taskID || !strings.Contains(fullRead.Data.Task.FullPlanMarkdown, "plan line the Orchestrator") || len(fullRead.Data.Task.Attempts) == 0 {
		t.Fatalf("linked detail=full is not the machine's record: %v %.400s", err, full)
	}
	if _, err := call(map[string]any{"action": "get_task", "machine": "box", "project_id": "remote-project", "task_id": taskID, "detail": "all"}); err == nil || !strings.Contains(err.Error(), "detail") {
		t.Fatalf("linked get_task accepted an unknown detail: %v", err)
	}
}
