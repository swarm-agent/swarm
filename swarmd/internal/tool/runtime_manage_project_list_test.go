package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: manage_projects list_tasks on the home machine answers the same
// way as the task list route and linked machines: current tasks by default
// (running, waiting or finished in the last 24 hours), newest updated first,
// with its existing status/query/agent filters and cursor paging.
// Threat: on a project with 1,003 tasks the old read kept only the oldest
// 1,000 and sorted by group/order, so the three newest tasks were missing.
// Owners: Runtime.executeManageProjects -> executeManageProjectTasksContext
// list_tasks over SessionStore.ListProjectTaskRecordsForList and
// ProjectTaskListFilter.Select with a real temp Pebble store; the tool entry
// is the narrowest layer that proves the agent-visible answer.
func TestManageProjectsListTasksCurrentNewestFirstOnLargeProject(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "state.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	const account, project = "acct-large", "proj-large"
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: project, AccountID: account, Name: "Large"}); err != nil {
		t.Fatal(err)
	}
	var lastHistory int64
	for i := 0; i < 1000; i++ {
		task := &pebblestore.ProjectTaskRecord{ID: fmt.Sprintf("history-%04d", i), ProjectID: project, AccountID: account, Title: fmt.Sprintf("History task %d", i), Agent: "coder", Status: "completed", Group: "a"}
		if err := store.PutProjectTask(account, task); err != nil {
			t.Fatal(err)
		}
		lastHistory = task.UpdatedAt
	}
	var last int64 = lastHistory
	for _, f := range []struct{ id, title, status, agent string }{
		{"fresh-build", "Fresh build of the parser", "in_progress", "coder"},
		{"fresh-review", "Fresh review of the docs", "needs_review", "finder"},
		{"fresh-done", "Fresh done release notes", "completed", "coder"},
	} {
		for time.Now().UnixMilli() <= last {
			time.Sleep(time.Millisecond)
		}
		// Group "z" sorts after the history: the old group/order sort would
		// put these last, past the first page.
		task := &pebblestore.ProjectTaskRecord{ID: f.id, ProjectID: project, AccountID: account, Title: f.title, Agent: f.agent, Status: f.status, Group: "z"}
		if err := store.PutProjectTask(account, task); err != nil {
			t.Fatal(err)
		}
		last = task.UpdatedAt
	}
	now := time.UnixMilli(lastHistory + pebblestore.ProjectTaskRecentFinishMillis + 1)
	previous := projectTaskListNow
	projectTaskListNow = func() time.Time { return now }
	defer func() { projectTaskListNow = previous }()

	runtime := NewRuntime(1)
	defer runtime.Close()
	runtime.SetManageProjectStore(store)
	scope := WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-large", AccountScopeID: account}}
	type listing struct {
		Tasks []struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			UpdatedAt int64  `json:"updated_at"`
		} `json:"tasks"`
		Total      int    `json:"total"`
		Count      int    `json:"count"`
		Scope      string `json:"scope"`
		NextCursor *int   `json:"next_cursor"`
	}
	list := func(args map[string]any) (listing, int) {
		t.Helper()
		args["action"], args["project_id"] = "list_tasks", project
		raw, err := runtime.executeManageProjects(context.Background(), scope, args)
		if err != nil {
			t.Fatal(err)
		}
		// The action's own answer is the envelope's data.
		data, err := EnvelopeData(raw)
		if err != nil {
			t.Fatal(err)
		}
		var out listing
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		return out, len(raw)
	}
	ids := func(l listing) string {
		var got []string
		for _, task := range l.Tasks {
			got = append(got, task.ID)
		}
		return strings.Join(got, ",")
	}

	current, size := list(map[string]any{})
	if ids(current) != "fresh-done,fresh-review,fresh-build" || current.Total != 3 || current.Count != 3 || current.Scope != "current" || current.NextCursor != nil {
		t.Fatalf("default list = %s total %d scope %q", ids(current), current.Total, current.Scope)
	}
	if size > 8<<10 || current.Tasks[0].UpdatedAt == 0 {
		t.Fatalf("default list is %d bytes, first row %+v", size, current.Tasks[0])
	}
	if status, _ := list(map[string]any{"status": "needs_review,in_progress"}); ids(status) != "fresh-review,fresh-build" {
		t.Fatalf("status filter = %s", ids(status))
	}
	if search, _ := list(map[string]any{"query": "PARSER", "scope": "all"}); ids(search) != "fresh-build" {
		t.Fatalf("search = %s", ids(search))
	}
	if agent, _ := list(map[string]any{"agent": "finder", "scope": "all"}); ids(agent) != "fresh-review" {
		t.Fatalf("agent filter = %s", ids(agent))
	}
	// All 1,003 tasks are reachable by paging, newest first, at most 50 a page.
	all, _ := list(map[string]any{"scope": "all", "limit": float64(50)})
	if all.Total != 1003 || all.Count != 50 || all.NextCursor == nil || *all.NextCursor != 50 || !strings.HasPrefix(ids(all), "fresh-done,fresh-review,fresh-build,") {
		t.Fatalf("history page: total %d count %d next %v", all.Total, all.Count, all.NextCursor)
	}
	if _, err := runtime.executeManageProjects(context.Background(), scope, map[string]any{"action": "list_tasks", "project_id": project, "limit": float64(100)}); err == nil || !strings.Contains(err.Error(), "between 1 and 50") {
		t.Fatalf("limit above 50: %v", err)
	}
	tail, _ := list(map[string]any{"scope": "all", "limit": float64(50), "cursor": float64(1000)})
	if tail.Count != 3 || tail.NextCursor != nil || tail.Tasks[2].ID != "history-0000" {
		t.Fatalf("last page: %s next %v", ids(tail), tail.NextCursor)
	}
	if _, err := runtime.executeManageProjects(context.Background(), scope, map[string]any{"action": "list_tasks", "project_id": project, "scope": "everything"}); err == nil || !strings.Contains(err.Error(), "scope must be current or all") {
		t.Fatalf("invalid scope: %v", err)
	}
	// Another account sees nothing of this project.
	other := scope
	other.Principal.AccountScopeID = "acct-other"
	if _, err := runtime.executeManageProjects(context.Background(), other, map[string]any{"action": "list_tasks", "project_id": project}); err == nil || !strings.Contains(err.Error(), `project "proj-large" not found`) {
		t.Fatalf("cross-account list: %v", err)
	}
}
