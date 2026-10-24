package api

import (
	"context"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: ManageMedia listing and readableMediaTask must isolate session,
// project and user data, including retries. Real store records prove ordering,
// filtering and authorization without involving a provider or transport.
func TestManageMediaListAndRetryIsolation(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	for _, sid := range []string{"session", "other-session", "other-user"} {
		user := p.UserID
		if sid == "other-user" {
			user = "other"
		}
		if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: sid, Metadata: map[string]any{"project_id": "project"}}, user, p.AccountScopeID); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{sessions: session.NewService(store, nil)}
	scope := tool.WorkspaceScope{SessionID: "session", Principal: p}
	for i, sid := range []string{"session", "session", "other-session", "other-user", "session"} {
		id := []string{"old", "new", "other-session", "other-user", "other-project"}[i]
		project := "project"
		if i == 4 {
			project = "other"
		}
		task := &pebblestore.ProjectTaskRecord{ID: id, ProjectID: project, AccountID: p.AccountScopeID, OriginSessionID: sid, Agent: "image", Model: "fixture", Title: "circle", Status: "failed", CreatedAt: int64(i + 1)}
		if i == 1 {
			task.RetryOf = "media/project/old"
			task.Status = "completed"
			task.Deliverables = []pebblestore.ProjectTaskDeliverable{{Status: "ready"}}
			task.Deliverables[0].CostUSD = 0.25
		}
		if err := store.PutProjectTask(p.AccountScopeID, task); err != nil {
			t.Fatal(err)
		}
	}
	list := func(args map[string]any) []map[string]any {
		t.Helper()
		args["action"] = "list"
		value, err := server.ManageMedia(context.Background(), scope, args)
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]any)["jobs"].([]map[string]any)
	}
	jobs := list(map[string]any{})
	if len(jobs) != 2 || jobs[0]["job_id"] != "media/project/new" || jobs[0]["retry_of"] != "media/project/old" || jobs[0]["media_cost_usd"] != 0.25 || jobs[0]["file_count"] != 1 {
		t.Fatalf("jobs %+v", jobs)
	}
	if jobs := list(map[string]any{"status": "failed"}); len(jobs) != 1 || jobs[0]["job_id"] != "media/project/old" {
		t.Fatalf("filtered %+v", jobs)
	}
	if jobs := list(map[string]any{"limit": float64(1)}); len(jobs) != 1 || jobs[0]["job_id"] != "media/project/new" {
		t.Fatalf("limited %+v", jobs)
	}
	for _, job := range []string{"media/other/other-project", "media/project/other-user"} {
		for _, action := range []string{"get", "retry"} {
			if _, err := server.ManageMedia(context.Background(), scope, map[string]any{"action": action, "job_id": job}); err == nil {
				t.Fatalf("%s allowed %s", action, job)
			}
		}
	}
	tasks, err := store.ListProjectTasks(p.AccountScopeID, "project", 100)
	if err != nil || len(tasks) != 4 {
		t.Fatalf("rejection changed tasks: %d %v", len(tasks), err)
	}
}
