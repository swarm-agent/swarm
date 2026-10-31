package tool

import (
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a follow-up to a finder, coder or designer task keeps the task's
// agent as the attempt's role (pebblestore.FollowupTaskAgent), and its
// session (which runs as Swarm) still reads the task's history: the binding
// ties the session to the reserved attempt and get_task answers. Threat: the
// review finding where the kept role broke TaskHistoryBinding's role check,
// so the follow-up session lost manage_projects (and its history tools) and
// could not do what its prompt asks. Negative cases: an attempt whose role is
// not a follow-up role, a session tied to another attempt, and another user
// are refused. Authority: SessionStore.ReserveTaskFollowup (the real
// reservation), Runtime.TaskHistoryBinding and authorizeTaskHistoryCall via
// executeManageProjects, over a temp Pebble store.
func TestFollowupSessionOfAKeptAgentReadsTaskHistory(t *testing.T) {
	db, store := openTaskViewStore(t)
	const account, user, project = "acct-hist", "user-hist", "proj-hist"
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: project, AccountID: account, Name: "History"}); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageSessionService(session.NewService(store, nil))
	r.SetManageProjectStore(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: user, AccountScopeID: account}
	putSession := func(id, taskID, attemptID string) {
		t.Helper()
		if err := db.PutJSON(pebblestore.KeySession(id), pebblestore.SessionSnapshot{ID: id, AccountScopeID: account, UserID: user, Mode: "auto", Metadata: map[string]any{
			"resolved_agent_name": "swarm", "agent_mode": "primary", "role": "project_task", "project_id": project, "task_id": taskID, "task_attempt_id": attemptID,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	for _, agent := range []string{"finder", "coder", "designer"} {
		taskID := "task-" + agent
		if err := store.PutProjectTask(account, &pebblestore.ProjectTaskRecord{ID: taskID, ProjectID: project, AccountID: account, Title: "Look", Agent: agent, Status: "needs_review"}); err != nil {
			t.Fatal(err)
		}
		task, _, err := store.GetProjectTask(account, project, taskID)
		if err != nil {
			t.Fatal(err)
		}
		reserved, err := store.ReserveTaskFollowup(account, project, taskID, user, "msg-"+agent, "Which file?", task.Revision, 1)
		if err != nil {
			t.Fatal(err)
		}
		attempt := reserved.ActiveAttempt()
		if attempt == nil || attempt.Role != agent {
			t.Fatalf("%s: reserved attempt %+v", agent, attempt)
		}
		putSession(attempt.SessionID, taskID, attempt.ID)
		scope := WorkspaceScope{SessionID: attempt.SessionID, Principal: principal}
		if p, tk, err := r.TaskHistoryBinding(scope); err != nil || p != project || tk != taskID {
			t.Fatalf("%s: binding %q %q %v", agent, p, tk, err)
		}
		raw, _, err := runManageProjects(t, r, scope, map[string]any{"action": "get_task", "project_id": project, "task_id": taskID})
		if err != nil || !strings.Contains(raw, taskID) {
			t.Fatalf("%s: follow-up get_task: %v %.300s", agent, err, raw)
		}
		// Another user on the same session is refused.
		other := WorkspaceScope{SessionID: attempt.SessionID, Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "someone-else", AccountScopeID: account}}
		if _, _, err := r.TaskHistoryBinding(other); err == nil {
			t.Fatalf("%s: another user bound", agent)
		}
	}

	// An attempt whose role is not one a follow-up records is refused, and so
	// is a session that names an attempt other than its own.
	task := heavyFinishedTask(account, project, "task-odd")
	task.Attempts = append(task.Attempts,
		pebblestore.ProjectTaskAttempt{ID: "attempt-odd", SessionID: "odd-session", Role: "reviewer", UserID: user, Status: "in_progress"},
		pebblestore.ProjectTaskAttempt{ID: "attempt-ok", SessionID: "ok-session", Role: "finder", UserID: user, Status: "in_progress"})
	if err := store.PutProjectTask(account, task); err != nil {
		t.Fatal(err)
	}
	putSession("odd-session", "task-odd", "attempt-odd")
	putSession("wrong-session", "task-odd", "attempt-ok")
	for _, id := range []string{"odd-session", "wrong-session"} {
		if _, _, err := r.TaskHistoryBinding(WorkspaceScope{SessionID: id, Principal: principal}); err == nil || !strings.Contains(err.Error(), "not associated with retained attempt") {
			t.Fatalf("%s bound: %v", id, err)
		}
	}
	for role, want := range map[string]bool{"swarm": true, "finder": true, "coder": true, "designer": true, "reviewer": false, "image": false, "": false, " finder": false} {
		if got := pebblestore.IsFollowupAttemptRole(role); got != want {
			t.Fatalf("IsFollowupAttemptRole(%q) = %v", role, got)
		}
	}
}

// Purpose: manage-sessions get's last_message spends its 500 bytes on
// readable text, not on a provider thought signature, as read_messages does.
// Threat: the review finding that get still truncated the raw stored content,
// so a signature filled last_message. Authority: manageSessionLastMessage,
// the one builder manageSessionsGet uses; a pure function is the narrowest
// layer for the budget.
func TestManageSessionsGetLastMessageLeavesOutProviderInternals(t *testing.T) {
	stored, _ := json.Marshal(map[string]any{"metadata": map[string]any{"google": map[string]any{"thought_signature": strings.Repeat("U2lnbmF0dXJl", 500)}}, "text": "Done: the limit is set in upload.go."})
	got := manageSessionLastMessage(pebblestore.MessageSnapshot{ID: "m1", GlobalSeq: 4, Role: "assistant", Content: string(stored)})
	content, _ := got["content"].(string)
	if strings.Contains(content, "U2lnbmF0dXJl") || !strings.HasPrefix(content, `{"text":"Done: the limit is set in upload.go."`) || !strings.Contains(content, ProviderInternalMarkerKey) || got["id"] != "m1" {
		t.Fatalf("last_message: %v", got)
	}
	// Plain text is unchanged and still bounded to 500 bytes.
	plain := strings.Repeat("a", 900)
	if got := manageSessionLastMessage(pebblestore.MessageSnapshot{Content: plain}); len(got["content"].(string)) > 500 || !strings.HasPrefix(plain, got["content"].(string)) {
		t.Fatalf("plain content: %d bytes", len(got["content"].(string)))
	}
}
