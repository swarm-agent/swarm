package tool

import (
	"context"
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManageProjectTaskHistoryPagination(t *testing.T) {
	// Purpose: manage_projects get_task returns authorized, paginated chronology;
	// threat: cross-account history leakage and unbounded model context. The real
	// temporary store plus tool dispatcher is the narrowest read authority proof.
	db, err := pebblestore.Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	rt := NewRuntime(1)
	rt.SetManageProjectStore(store)
	if err := store.PutProject("account", &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil { t.Fatal(err) }
	if err := store.PutProjectTask("account", &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "swarm", Status: "needs_review", SessionID: "original"}); err != nil { t.Fatal(err) }
	if _, err := store.ReserveTaskFollowup("account", "project", "task", "user", "key", "request", 1, 200); err != nil { t.Fatal(err) }
	args := map[string]any{"action": "get_task", "project_id": "project", "task_id": "task", "cursor": 1, "limit": 1}
	scope := WorkspaceScope{Principal: identity.Principal{Type: "user", UserID: "user", AccountScopeID: "account"}}
	raw, err := rt.executeManageProjects(context.Background(), scope, args)
	if err != nil { t.Fatal(err) }
	var response struct { Task pebblestore.ProjectTaskRecord `json:"task"`; Next int `json:"next_cursor"` }
	if err := json.Unmarshal([]byte(raw), &response); err != nil { t.Fatal(err) }
	if len(response.Task.Attempts) != 1 || response.Task.Attempts[0].Request != "request" || response.Next != 0 { t.Fatal("wrong history page") }
	scope.Principal.AccountScopeID = "foreign"
	if _, err := rt.executeManageProjects(context.Background(), scope, args); err == nil { t.Fatal("foreign task history returned") }
}
