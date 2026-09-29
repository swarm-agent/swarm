package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: project task organization is durable metadata, not execution state;
// reject stale writers, status forgery, and cross-account access. The narrow
// manage_projects handler and account-scoped store are the authority.
func TestManageProjectTaskOrganizationAndGuards(t *testing.T) {
	rt := NewRuntime(1)
	store := newMockProjectStore()
	rt.SetManageProjectStore(store)
	owner := WorkspaceScope{Principal: identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account-a"}}
	other := WorkspaceScope{Principal: identity.Principal{Type: "user", UserID: "other", AccountScopeID: "account-b"}}
	project := &pebblestore.ProjectRecord{ID: "project", Name: "Project"}
	if err := store.PutProject("account-a", project); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		task := &pebblestore.ProjectTaskRecord{ID: fmt.Sprintf("task-%d", i), ProjectID: project.ID, Title: fmt.Sprintf("Searchable %d", i), Agent: "coder", Status: "pending_approval", Revision: 1}
		if err := store.PutProjectTask("account-a", task); err != nil {
			t.Fatal(err)
		}
	}
	call := func(scope WorkspaceScope, args map[string]any) (map[string]any, error) {
		raw, err := rt.executeManageProjects(context.Background(), scope, args)
		if err != nil {
			return nil, err
		}
		var result map[string]any
		err = json.Unmarshal([]byte(raw), &result)
		return result, err
	}
	base := map[string]any{"project_id": "project", "task_id": "task-0", "expected_revision": 1}
	if _, err := call(other, map[string]any{"action": "get_task", "project_id": "project", "task_id": "task-0"}); err == nil {
		t.Fatal("foreign account read succeeded")
	}
	if _, err := call(owner, map[string]any{"action": "update_task", "project_id": "project", "task_id": "task-0", "expected_revision": 1, "status": "completed"}); err == nil {
		t.Fatal("status forgery succeeded")
	}
	if _, err := call(owner, map[string]any{"action": "update_task", "project_id": "project", "task_id": "task-0", "expected_revision": 1, "priority": "invalid"}); err == nil {
		t.Fatal("invalid priority succeeded")
	}
	base["action"] = "update_task"
	base["priority"] = "urgent"
	base["group"] = "review"
	base["order"] = float64(2)
	result, err := call(owner, base)
	if err != nil {
		t.Fatal(err)
	}
	task := result["task"].(map[string]any)
	if task["revision"] != float64(2) || task["priority"] != "urgent" || task["status"] != "pending_approval" {
		t.Fatalf("unexpected update: %#v", task)
	}
	if _, err := call(owner, base); err == nil {
		t.Fatal("stale edit succeeded")
	}
	page, err := call(owner, map[string]any{"action": "list_tasks", "project_id": "project", "query": "searchable", "agent": "coder", "limit": float64(2), "cursor": float64(0)})
	if err != nil {
		t.Fatal(err)
	}
	if page["count"] != float64(2) || page["next_cursor"] != float64(2) {
		t.Fatalf("unexpected page: %#v", page)
	}
	if _, err := call(owner, map[string]any{"action": "delete_task", "project_id": "project", "task_id": "task-0", "expected_revision": float64(2)}); err == nil {
		t.Fatal("unarchived task deleted")
	}
	archived, err := call(owner, map[string]any{"action": "archive_task", "project_id": "project", "task_id": "task-0", "expected_revision": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if archived["task"].(map[string]any)["archived"] != true {
		t.Fatal("archive not persisted")
	}
	page, err = call(owner, map[string]any{"action": "list_tasks", "project_id": "project", "group": "review", "include_archived": true})
	if err != nil {
		t.Fatal(err)
	}
	if page["count"] != float64(1) {
		t.Fatalf("archived group filtering failed: %#v", page)
	}
}

// Purpose: list and mutation tool responses are bounded discovery metadata, not
// media or plan transport. executeManageProjectTasks is the narrowest response
// boundary; a large media fixture must not leak inline payloads into pages or
// archive acknowledgements, while get_task retains intentional detail access.
func TestManageProjectTaskBoundedDiscovery(t *testing.T) {
	rt := NewRuntime(1)
	store := newMockProjectStore()
	rt.SetManageProjectStore(store)
	scope := WorkspaceScope{Principal: identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account-a"}}
	if err := store.PutProject("account-a", &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("inline-image-data", 100000)
	for i := 0; i < 3; i++ {
		task := &pebblestore.ProjectTaskRecord{ID: fmt.Sprintf("task-%d", i), ProjectID: "project", Title: fmt.Sprintf("Task %d", i), Agent: "coder", Status: "blocked", Revision: 1, FullPlanMarkdown: payload, Description: payload, Deliverables: []pebblestore.ProjectTaskDeliverable{{Kind: "image", Description: payload}}}
		if err := store.PutProjectTask("account-a", task); err != nil {
			t.Fatal(err)
		}
	}
	call := func(args map[string]any) (string, map[string]any) {
		t.Helper()
		raw, err := rt.executeManageProjects(context.Background(), scope, args)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		return raw, result
	}
	seen := map[string]bool{}
	for cursor := 0; cursor < 3; cursor++ {
		raw, page := call(map[string]any{"action": "list_tasks", "project_id": "project", "limit": 1, "cursor": cursor})
		if len(raw) > 2048 || strings.Contains(raw, "inline-image-data") {
			t.Fatalf("unbounded list page: %d bytes", len(raw))
		}
		tasks := page["tasks"].([]any)
		if len(tasks) != 1 {
			t.Fatalf("pagination: %#v", page)
		}
		row := tasks[0].(map[string]any)
		if row["revision"] != float64(1) || row["id"] == nil {
			t.Fatalf("missing discovery metadata: %#v", row)
		}
		seen[row["id"].(string)] = true
		if cursor < 2 && page["next_cursor"] != float64(cursor+1) {
			t.Fatalf("missing next cursor: %#v", page)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("pagination repeated tasks: %#v", seen)
	}
	detail, _ := call(map[string]any{"action": "get_task", "project_id": "project", "task_id": "task-0"})
	if !strings.Contains(detail, "inline-image-data") {
		t.Fatal("intentional detail retrieval lost")
	}
	ack, _ := call(map[string]any{"action": "archive_task", "project_id": "project", "task_id": "task-0", "expected_revision": 1})
	if len(ack) > 2048 || strings.Contains(ack, "inline-image-data") {
		t.Fatalf("unbounded archive acknowledgement: %d bytes", len(ack))
	}
	_, page := call(map[string]any{"action": "list_tasks", "project_id": "project"})
	if page["total"] != float64(2) {
		t.Fatalf("archive reappeared in default list: %#v", page)
	}
}
