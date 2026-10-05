package tool

import (
	"context"
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: executeManageProjects must dispatch unarchive_task to the guarded
// durable mutation, return its receipt, and reject missing/stale revisions or
// foreign principals without deployment. Real Pebble is the narrowest wiring proof.
func TestManageProjectUnarchiveTask(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	rt := NewRuntime(1)
	rt.SetManageProjectStore(store)
	if err := store.PutProject("account-a", &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProjectTask("account-a", &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "coder", Status: "needs_review", WorkerName: "Worker", Archived: true, Revision: 3}); err != nil {
		t.Fatal(err)
	}
	call := func(account string, revision any) (string, error) {
		return rt.executeManageProjects(context.Background(), WorkspaceScope{Principal: identity.Principal{Type: "user", UserID: "owner", AccountScopeID: account}}, map[string]any{"action": "unarchive_task", "project_id": "project", "task_id": "task", "expected_revision": revision})
	}
	for _, revision := range []any{nil, 0, 2} {
		if _, err := call("account-a", revision); err == nil {
			t.Fatal("missing/stale revision accepted")
		}
	}
	if _, err := call("account-b", 3); err == nil {
		t.Fatal("foreign account accepted")
	}
	before, _, _ := store.GetProjectTask("account-a", "project", "task")
	if !before.Archived || before.Revision != 3 {
		t.Fatal("failed tool calls mutated task")
	}
	raw, err := call("account-a", 3)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Task.ID != "task" || result.Task.Archived || result.Task.Revision != 4 || result.Task.Status != "needs_review" || result.Task.WorkerName != "Worker" {
		t.Fatalf("incorrect receipt: %s", raw)
	}
	if _, err := call("account-a", 4); err == nil {
		t.Fatal("already restored task accepted")
	}
}
