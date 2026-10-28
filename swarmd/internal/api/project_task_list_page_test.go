package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// largeTaskProject is a project with 1,000 tasks finished more than a day ago
// and three current ones, the newest last: fresh-done (completed just now),
// fresh-review (needs_review) and fresh-build (in_progress).
type largeTaskProject struct {
	historyTotal int
	current      []string // newest updated first
}

// seedLargeTaskProject writes the project and its tasks through the canonical
// task store (so the compact board index is maintained) and moves the list
// clock to just over 24 hours after the last history task was updated. Each
// task has a 4,000-character description, so the whole board is larger than
// the Swarm Control response limit.
func seedLargeTaskProject(t *testing.T, db *pebblestore.SessionStore, account, projectID string) largeTaskProject {
	t.Helper()
	if err := db.PutProject(account, &pebblestore.ProjectRecord{ID: projectID, AccountID: account, Name: "Large"}); err != nil {
		t.Fatal(err)
	}
	description := strings.Repeat("history detail ", 267)[:4000]
	var lastHistory int64
	const history = 1000
	for i := 0; i < history; i++ {
		task := &pebblestore.ProjectTaskRecord{ID: fmt.Sprintf("history-%04d", i), ProjectID: projectID, AccountID: account, Title: fmt.Sprintf("History task %d", i), Description: description, Agent: "coder", Status: "completed"}
		if i%2 == 1 {
			task.Status = "failed"
		}
		if err := db.PutProjectTask(account, task); err != nil {
			t.Fatal(err)
		}
		lastHistory = task.UpdatedAt
	}
	waitPastMillis(lastHistory)
	fresh := []struct{ id, title, status, agent string }{
		{"fresh-build", "Fresh build of the parser", "in_progress", "coder"},
		{"fresh-review", "Fresh review of the docs", "needs_review", "finder"},
		{"fresh-done", "Fresh done release notes", "completed", "coder"},
	}
	var last int64
	for _, f := range fresh {
		waitPastMillis(last)
		task := &pebblestore.ProjectTaskRecord{ID: f.id, ProjectID: projectID, AccountID: account, Title: f.title, Description: description, Agent: f.agent, Status: f.status}
		if err := db.PutProjectTask(account, task); err != nil {
			t.Fatal(err)
		}
		last = task.UpdatedAt
	}
	now := time.UnixMilli(lastHistory + pebblestore.ProjectTaskRecentFinishMillis + 1)
	previous := projectTaskListNow
	projectTaskListNow = func() time.Time { return now }
	t.Cleanup(func() { projectTaskListNow = previous })
	return largeTaskProject{historyTotal: history, current: []string{"fresh-done", "fresh-review", "fresh-build"}}
}

// waitPastMillis returns once the wall clock has passed after (a millisecond
// tick, so consecutive tasks have distinct, ordered update times).
func waitPastMillis(after int64) {
	for time.Now().UnixMilli() <= after {
		time.Sleep(time.Millisecond)
	}
}

func taskListIDs(t *testing.T, rows any) []string {
	t.Helper()
	var ids []string
	for _, raw := range rows.([]any) {
		ids = append(ids, raw.(map[string]any)["id"].(string))
	}
	return ids
}

// Purpose: GET /v3/projects/{id}/tasks with list parameters must answer one
// bounded page, selected before rows are hydrated, so a project with 1,003
// tasks answers current tasks, status and search queries with a few rows.
// Threat: the route returning (and encoding) the whole board, which is cut
// off or exceeds callers' size limits, hiding the tasks an AI just sent.
// Owners: projectTaskListFilterFromQuery, ReadProjectTaskBoardSelected and
// ProjectTaskListFilter.Select behind the real routed handler. The handler is
// the narrowest layer that proves selection, hydration and encoding together;
// a request without parameters must still return the full board unchanged.
func TestProjectTaskListRouteFiltersAndPagesLargeProject(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	db := server.sessions.Store()
	seed := seedLargeTaskProject(t, db, p.AccountScopeID, "proj-large")
	get := func(target string, want int) (map[string]any, http.Header, int) {
		t.Helper()
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(httptest.NewRequest(http.MethodGet, target, nil)))
		if rec.Code != want {
			t.Fatalf("%s: %d %s", target, rec.Code, controlMCPTruncate(rec.Body.String(), 300))
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out, rec.Header(), rec.Body.Len()
	}

	current, header, size := get("/v3/projects/proj-large/tasks?scope=current", http.StatusOK)
	if ids := taskListIDs(t, current["tasks"]); strings.Join(ids, ",") != strings.Join(seed.current, ",") {
		t.Fatalf("current tasks newest first = %v, want %v", ids, seed.current)
	}
	if current["total"].(float64) != 3 || current["count"].(float64) != 3 || current["next_cursor"] != nil || current["scope"] != "current" {
		t.Fatalf("current page facts: total %v count %v next %v scope %v", current["total"], current["count"], current["next_cursor"], current["scope"])
	}
	if size > controlMCPMaxToolBytes/50 {
		t.Fatalf("current page is %d bytes", size)
	}
	// Only the page was hydrated: the response is three rows, not 1,003.
	if header.Get("X-Task-Response-Bytes") == "" {
		t.Fatal("board timing headers missing")
	}

	status, _, _ := get("/v3/projects/proj-large/tasks?status=needs_review,in_progress", http.StatusOK)
	if ids := taskListIDs(t, status["tasks"]); strings.Join(ids, ",") != "fresh-review,fresh-build" || status["total"].(float64) != 2 {
		t.Fatalf("status filter = %v total %v", ids, status["total"])
	}
	search, _, _ := get("/v3/projects/proj-large/tasks?query=RELEASE+notes&scope=all", http.StatusOK)
	if ids := taskListIDs(t, search["tasks"]); len(ids) != 1 || ids[0] != "fresh-done" {
		t.Fatalf("search = %v", ids)
	}
	agent, _, _ := get("/v3/projects/proj-large/tasks?agent=finder&scope=all", http.StatusOK)
	if ids := taskListIDs(t, agent["tasks"]); len(ids) != 1 || ids[0] != "fresh-review" {
		t.Fatalf("agent filter = %v", ids)
	}

	// History is reachable by paging: every task exactly once, newest first.
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("paging did not end")
		}
		target := "/v3/projects/proj-large/tasks?scope=all&limit=100"
		if cursor != "" {
			target += "&cursor=" + cursor
		}
		page, _, size := get(target, http.StatusOK)
		ids := taskListIDs(t, page["tasks"])
		if page["total"].(float64) != float64(seed.historyTotal+3) || len(ids) > 100 || size > controlMCPMaxToolBytes {
			t.Fatalf("page %d: total %v rows %d bytes %d", pages, page["total"], len(ids), size)
		}
		if pages == 0 && strings.Join(ids[:3], ",") != strings.Join(seed.current, ",") {
			t.Fatalf("first history page is not newest first: %v", ids[:3])
		}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("task %s returned twice", id)
			}
			seen[id] = true
		}
		next, _ := page["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != seed.historyTotal+3 {
		t.Fatalf("paging reached %d tasks, want %d", len(seen), seed.historyTotal+3)
	}

	// Invalid parameters are refused, never widened to the board.
	for _, target := range []string{"?scope=everything", "?limit=101", "?limit=0", "?cursor=-1", "?view=archived&scope=current"} {
		get("/v3/projects/proj-large/tasks"+target, http.StatusBadRequest)
	}
	// Without parameters the board is unchanged: every row, no page facts.
	board, _, _ := get("/v3/projects/proj-large/tasks", http.StatusOK)
	if board["count"].(float64) != float64(seed.historyTotal+3) || board["total"] != nil || board["next_cursor"] != nil {
		t.Fatalf("legacy board: count %v total %v", board["count"], board["total"])
	}
}

// Purpose: swarm_list_tasks (and the other Swarm Control reads of a
// project's tasks) must work on a project whose whole board exceeds the
// recorder limit: the status filter is applied by the route and the default
// answer is current tasks, newest first.
// Threat: "response too large" for every call on a 1,000+ task project, and a
// status argument that was only applied after the oversized read.
// Owners: controlMCPListTasks, controlMCPCall.projectStatus and
// sessionChildren dispatching to the real internal route handler. The
// negative control proves the unbounded board read overflows here.
func TestControlMCPListTasksLargeProject(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	seed := seedLargeTaskProject(t, server.sessions.Store(), p.AccountScopeID, "proj-large")
	c := &controlMCPCall{request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "projects:read"}}), next: server.internalRouteHandler(), server: server}

	if _, err := c.dispatch(http.MethodGet, "/v3/projects/proj-large/tasks", nil, nil); err == nil || !strings.Contains(err.Error(), "response too large") {
		t.Fatalf("negative control: the whole board must exceed the tool limit, got %v", err)
	}
	ids := func(out any) []string {
		var got []string
		for _, task := range out.(map[string]any)["tasks"].([]map[string]any) {
			got = append(got, task["id"].(string))
		}
		return got
	}
	out, err := controlMCPListTasks(c, map[string]any{"project_id": "proj-large"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(out); strings.Join(got, ",") != strings.Join(seed.current, ",") || out.(map[string]any)["total"].(float64) != 3 {
		t.Fatalf("default list = %v total %v", got, out.(map[string]any)["total"])
	}
	out, err = controlMCPListTasks(c, map[string]any{"project_id": "proj-large", "status": "needs_review"})
	if err != nil || strings.Join(ids(out), ",") != "fresh-review" {
		t.Fatalf("status list = %v %v", out, err)
	}
	out, err = controlMCPListTasks(c, map[string]any{"project_id": "proj-large", "status": "failed", "scope": "all", "limit": float64(10)})
	if err != nil {
		t.Fatal(err)
	}
	page := out.(map[string]any)
	if len(ids(out)) != 10 || page["total"].(float64) != 500 || page["next_cursor"] != "10" {
		t.Fatalf("history page: %d rows total %v next %v", len(ids(out)), page["total"], page["next_cursor"])
	}
	out, err = controlMCPListTasks(c, map[string]any{"project_id": "proj-large", "status": "failed", "scope": "all", "limit": float64(10), "cursor": "10"})
	if err != nil || len(ids(out)) != 10 || ids(out)[0] == ids(page)[0] {
		t.Fatalf("second history page: %v %v", out, err)
	}
	if _, err := controlMCPListTasks(c, map[string]any{"project_id": "proj-large", "scope": "everything"}); err == nil || !strings.Contains(err.Error(), "scope must be current or all") {
		t.Fatalf("invalid scope: %v", err)
	}
	if _, err := controlMCPListTasks(c, map[string]any{"project_id": "proj-large", "limit": float64(500)}); err == nil {
		t.Fatal("oversized limit accepted")
	}

	// The project status read is bounded the same way and still counts all.
	status, err := c.projectStatus("proj-large", map[string]any{"id": "proj-large"})
	if err != nil {
		t.Fatal(err)
	}
	counts := status["task_counts"].(map[string]int)
	if counts["completed"] != 501 || counts["failed"] != 500 || status["finished_tasks"] != 501 {
		t.Fatalf("status counts %v finished %v", counts, status["finished_tasks"])
	}
	open := status["open_tasks"].([]map[string]any)
	if len(open) != controlMCPMaxStatusTasks || open[0]["id"] != "fresh-review" {
		t.Fatalf("open tasks: %d first %v", len(open), open[0]["id"])
	}
}

// Purpose: manage_projects list_tasks on a linked machine must forward the
// list arguments to that machine's task list route (default current tasks,
// 25 per page) and return compact rows, never the machine's whole board.
// Threat: a bare GET that drops status/query/limit and returns 1,003 full
// records, cut off before the tasks the Orchestrator just sent.
// Owners: Server.CallFleetProject, fleetTaskListQuery and
// compactFleetTaskList over the real pinned link between two in-process
// daemons, the narrowest layer that proves what crosses the link.
func TestFleetListTasksForwardsFiltersOnLargeProject(t *testing.T) {
	home := newFleetTestSwarm(t, "home")
	defer home.cleanup()
	remote := newFleetTestSwarm(t, "remote")
	defer remote.cleanup()
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "box"})
	if status, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); status != http.StatusCreated {
		t.Fatalf("link: %d %v", status, out)
	}
	seed := seedLargeTaskProject(t, remote.s.sessions.Store(), remote.account, "remote-large")
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list := func(args map[string]any) map[string]any {
		t.Helper()
		args["project_id"] = "remote-large"
		raw, err := home.s.CallFleetProject(ctx, p, "box", "list_tasks", args)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 64<<10 {
			t.Fatalf("linked list is %d bytes", len(raw))
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	current := list(map[string]any{})
	if ids := taskListIDs(t, current["tasks"]); strings.Join(ids, ",") != strings.Join(seed.current, ",") || current["total"].(float64) != 3 {
		t.Fatalf("linked current = %v total %v", ids, current["total"])
	}
	row := current["tasks"].([]any)[0].(map[string]any)
	for key := range row {
		switch key {
		case "id", "project_id", "title", "status", "archived", "agent", "session_id", "updated_at":
		default:
			t.Fatalf("linked row carries %q: %v", key, row)
		}
	}
	if row["updated_at"] == nil || row["status"] != "completed" {
		t.Fatalf("linked row %v", row)
	}
	status := list(map[string]any{"status": []any{"needs_review"}})
	if ids := taskListIDs(t, status["tasks"]); strings.Join(ids, ",") != "fresh-review" {
		t.Fatalf("linked status = %v", ids)
	}
	search := list(map[string]any{"query": "parser", "scope": "all"})
	if ids := taskListIDs(t, search["tasks"]); strings.Join(ids, ",") != "fresh-build" {
		t.Fatalf("linked search = %v", ids)
	}
	history := list(map[string]any{"scope": "all"})
	if len(history["tasks"].([]any)) != 25 || history["total"].(float64) != 1003 || history["next_cursor"] != "25" {
		t.Fatalf("linked history: %d rows total %v next %v", len(history["tasks"].([]any)), history["total"], history["next_cursor"])
	}
	next := list(map[string]any{"scope": "all", "cursor": float64(25), "limit": float64(5)})
	if len(next["tasks"].([]any)) != 5 || next["next_cursor"] != "30" {
		t.Fatalf("linked page 2: %v", next["next_cursor"])
	}
	// Bad arguments are refused at home before anything is sent.
	for _, args := range []map[string]any{{"scope": "board"}, {"limit": float64(1000)}, {"cursor": "x"}} {
		args["project_id"] = "remote-large"
		if _, err := home.s.CallFleetProject(ctx, p, "box", "list_tasks", args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
