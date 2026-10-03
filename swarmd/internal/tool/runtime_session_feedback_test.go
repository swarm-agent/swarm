package tool

import (
	"context"
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: exercise the actual V3 mutation authority through the tool boundary:
// active-task notes must persist once without launching, conflicting retries and
// malformed execution intent must reject without writes, and task start guards
// must remain enforced. A temporary store is the narrowest durable proof.
func TestSessionFeedbackAdmission(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil { t.Fatal(err) }
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
	snapshot := pebblestore.SessionSnapshot{ID: "target", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Metadata: map[string]any{"task_id": "task", "project_id": "project"}}
	created, err := sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{SessionID: snapshot.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: pebblestore.V3SessionMutationCreateSession, Session: &snapshot, ClientRequestID: "create", IdempotencyKey: "create", PayloadHash: "create", RequestHash: "create"})
	if err != nil || created.Error != nil || created.Conflict != nil { t.Fatalf("create: %+v %v", created, err) }
	projects := newMockProjectStore()
	if err := projects.PutProjectTask(p.AccountScopeID, &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: p.AccountScopeID, Status: "in_progress", SessionID: snapshot.ID, Revision: 7}); err != nil { t.Fatal(err) }
	rt := &Runtime{sessions: sessions, projects: projects}
	scope := WorkspaceScope{Principal: p, SessionID: "sender"}
	args := map[string]any{"session_id": snapshot.ID, "prompt": "Please consider this feedback", "trigger": false, "client_request_id": "retry"}
	for i := 0; i < 2; i++ {
		raw, err := rt.manageSessionsSendMessage(context.Background(), scope, args)
		if err != nil { t.Fatal(err) }
		var out map[string]any
		if err := json.Unmarshal([]byte(raw), &out); err != nil { t.Fatal(err) }
		if out["status"] != "saved/queued" || out["delivery_status"] != "unconfirmed" || out["incorporation_status"] != "unconfirmed" || out["trigger_run"] != false { t.Fatalf("false acknowledgement: %s", raw) }
	}
	args["prompt"] = "conflicting payload"
	if _, err := rt.manageSessionsSendMessage(context.Background(), scope, args); err == nil { t.Fatal("accepted conflicting retry") }
	args["trigger_run"] = true
	if _, err := rt.manageSessionsSendMessage(context.Background(), scope, args); err == nil { t.Fatal("accepted conflicting aliases") }
	delete(args, "trigger_run")
	args["trigger"] = "false"
	if _, err := rt.manageSessionsSendMessage(context.Background(), scope, args); err == nil { t.Fatal("accepted malformed trigger") }
	args["trigger"] = true
	if _, err := rt.manageSessionsSendMessage(context.Background(), scope, args); err == nil { t.Fatal("started task-linked session") }
	foreign := scope
	foreign.Principal.AccountScopeID = "foreign"
	args["trigger"] = false
	if _, err := rt.manageSessionsSendMessage(context.Background(), foreign, args); err == nil { t.Fatal("accepted foreign note") }
	messages, err := sessions.ListSessionMessages(snapshot.ID, 0, 100)
	if err != nil || len(messages) != 1 || messages[0].Content != "Please consider this feedback" { t.Fatalf("mutation leak: %+v %v", messages, err) }
	task, _, _ := projects.GetProjectTask(p.AccountScopeID, "project", "task")
	if task.Revision != 7 || task.Status != "in_progress" { t.Fatalf("note changed task: %+v", task) }
	state, found, err := sessions.GetSessionRunState(snapshot.ID)
	if err != nil || (found && state.Active) { t.Fatalf("note started run: %+v %v", state, err) }

	// A retained terminal task can store evidence, but a note must not reopen it
	// or invent delivery when no subsequent provider step exists.
	task.Status = "completed"
	if err := projects.PutProjectTask(p.AccountScopeID, task); err != nil { t.Fatal(err) }
	args["prompt"] = "Terminal evidence only"
	args["client_request_id"] = "terminal-note"
	raw, err := rt.manageSessionsSendMessage(context.Background(), scope, args)
	if err != nil { t.Fatal(err) }
	var terminal map[string]any
	if err := json.Unmarshal([]byte(raw), &terminal); err != nil { t.Fatal(err) }
	if terminal["status"] != "saved/queued" || terminal["delivery_status"] != "unconfirmed" || terminal["incorporation_status"] != "unconfirmed" { t.Fatalf("terminal note overclaimed: %s", raw) }
	task, _, _ = projects.GetProjectTask(p.AccountScopeID, "project", "task")
	if task.Status != "completed" || task.Revision != 7 { t.Fatalf("terminal task reopened: %+v", task) }
	messages, err = sessions.ListSessionMessages(snapshot.ID, 0, 100)
	if err != nil || len(messages) != 2 { t.Fatalf("terminal messages: %+v %v", messages, err) }
	for _, message := range messages {
		if message.Metadata["source"] == "feedback_delivery" { t.Fatal("terminal note falsely delivered") }
	}
	state, found, err = sessions.GetSessionRunState(snapshot.ID)
	if err != nil || (found && state.Active) { t.Fatalf("terminal note started run: %+v %v", state, err) }
}
