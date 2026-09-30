package tool

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: sendSessionMessageInternal must reject current and superseded task
// attempts before writing messages/run intents or enqueueing. The tool boundary
// is the narrowest layer exposing the reproduced bypass; non-triggering notes
// must preserve terminal state, history and integration evidence.
func TestTaskSessionContinuationRejectsBeforeWrite(t *testing.T) {
	p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
	for _, id := range []string{"current", "superseded"} {
		t.Run(id, func(t *testing.T) {
			store := newMockProjectStore()
			task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: p.AccountScopeID, Title: "Work", Agent: "coder", Status: "completed", SessionID: "current", Revision: 7, ActiveAttemptID: "latest", Attempts: []pebblestore.ProjectTaskAttempt{{ID: "old", SessionID: "superseded", Status: "completed"}, {ID: "latest", SessionID: "current", Status: "completed"}}, Integration: &pebblestore.ProjectTaskIntegration{State: "success", SessionID: "current"}}
			if err := store.PutProjectTask(p.AccountScopeID, task); err != nil { t.Fatal(err) }
			before, _, _ := store.GetProjectTask(p.AccountScopeID, "project", "task")
			service := &gitManageSessionService{sessions: map[string]pebblestore.SessionSnapshot{id: {ID: id, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Metadata: map[string]any{"project_id": "project", "task_id": "task"}}}, messages: make(map[string][]pebblestore.MessageSnapshot)}
			rt := &Runtime{sessions: service, projects: store, sessionController: &mockSessionController{}}
			for i := 0; i < 2; i++ {
				_, err := rt.sendSessionMessageInternal(context.Background(), WorkspaceScope{Principal: p}, id, "continue", "user", true, 0)
				if err == nil || !strings.Contains(err.Error(), "reopen_task") || !strings.Contains(err.Error(), "current_session_id=current") || !strings.Contains(err.Error(), "expected_revision=7") { t.Fatalf("missing actionable rejection: %v", err) }
			}
			if len(service.messages[id]) != 0 || len(service.runStates) != 0 { t.Fatal("rejection wrote session state") }
			out, err := rt.sendSessionMessageInternal(context.Background(), WorkspaceScope{Principal: p}, id, "note only", "user", false, 0)
			if err != nil || out["status"] != "appended" || len(service.messages[id]) != 1 { t.Fatalf("note: %v %v", out, err) }
			after, _, _ := store.GetProjectTask(p.AccountScopeID, "project", "task")
			if !reflect.DeepEqual(before, after) || len(service.runStates) != 0 { t.Fatal("note or rejection reopened task") }
		})
	}
}

type followupToolService struct {
	ProjectTaskLifecycleService
	input ProjectTaskFollowupInput
	principal identity.Principal
}

func (s *followupToolService) ReopenProjectTask(_ context.Context, p identity.Principal, project, task string, input ProjectTaskFollowupInput) (*pebblestore.ProjectTaskRecord, error) {
	s.input, s.principal = input, p
	return &pebblestore.ProjectTaskRecord{ID: task, ProjectID: project, Status: "in_progress", SessionID: "new-attempt", ActiveAttemptID: "next", Attempts: []pebblestore.ProjectTaskAttempt{{ID: "next", SessionID: "new-attempt", RunID: "new-run"}}}, nil
}

// Purpose: reopen_task must delegate unchanged guards to the canonical lifecycle,
// return its authoritative linkage, and fail closed without that authority. This
// tests routing only; real-store retry/concurrency and launch failure tests belong
// to the shared API/store service, not this deliberately non-executing adapter.
func TestManageProjectTaskReopenUsesCanonicalService(t *testing.T) {
	store := newMockProjectStore()
	if err := store.PutProject("account", &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil { t.Fatal(err) }
	scope := WorkspaceScope{Principal: identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}}
	service := &followupToolService{}
	rt := &Runtime{projects: store, projectTaskLifecycle: service}
	args := map[string]any{"project_id": "project", "task_id": "task", "expected_revision": 7, "feedback": " exact feedback ", "client_request_id": "retry-key", "repair": true}
	raw, err := rt.executeManageProjectTasks(scope, "reopen_task", args)
	if err != nil { t.Fatal(err) }
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil { t.Fatal(err) }
	if out["session_id"] != "new-attempt" || out["run_id"] != "new-run" || out["active_attempt_id"] != "next" || out["status"] != "reopened" { t.Fatalf("linkage: %s", raw) }
	if service.input != (ProjectTaskFollowupInput{Feedback: " exact feedback ", ClientRequestID: "retry-key", Revision: 7, Repair: true}) || !reflect.DeepEqual(service.principal, scope.Principal) { t.Fatal("lost guards or principal") }
	rt.projectTaskLifecycle = nil
	if _, err := rt.executeManageProjectTasks(scope, "reopen_task", args); err == nil { t.Fatal("missing authority accepted") }
	foreign := scope
	foreign.Principal.AccountScopeID = "foreign"
	if _, err := rt.executeManageProjectTasks(foreign, "reopen_task", args); err == nil { t.Fatal("foreign project accepted") }
}
