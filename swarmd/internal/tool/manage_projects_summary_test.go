package tool

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the Orchestrator reads projects with manage_projects list and get.
// Requirement: list returns only id, name, workspaces (id, path, role) and
// current task counts; get adds project.md cut to about 2 KB, the whole text
// only with detail="context"; neither ever returns the icon image or other
// stored fields. Threat: the old handlers returned whole ProjectRecords, so
// every list carried each project's full project.md and base64 icon into the
// model's context, and a new stored field would leak the same way. Owners:
// Runtime.executeManageProjects list/get and Runtime.projectSummary over a
// real temp Pebble SessionStore (ListProjects, GetProject,
// ListProjectTaskRecordsForList, ProjectTaskListFilter.Select); the tool entry
// is the narrowest layer that proves the agent-visible answer.
func TestManageProjectsListAndGetReturnCompactProjects(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "state.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	const account, otherAccount, projectID = "acct-compact", "acct-other", "proj-compact"

	var icon bytes.Buffer
	if err := png.Encode(&icon, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	iconURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(icon.Bytes())
	// Multi-byte text puts rune boundaries across the 2 KB cut.
	fullContext := "# Project\n\n" + strings.Repeat("Délégué l'œuvre — ", 400)
	if err := store.PutProject(account, &pebblestore.ProjectRecord{
		ID: projectID, AccountID: account, Name: "Compact", Description: "A project", IconPNGDataURL: iconURL, ThemeID: "builtin:dusk",
		ProjectContext: fullContext, PrimarySessionID: "session-hidden", AutomationIDs: []string{"automation-hidden"},
		Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: "ws-1", Path: "/repo/app", Role: "primary_code", Label: "App"}, {Path: "/repo/docs", Role: "docs"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProject(otherAccount, &pebblestore.ProjectRecord{ID: "proj-foreign", AccountID: otherAccount, Name: "Foreign"}); err != nil {
		t.Fatal(err)
	}

	old := &pebblestore.ProjectTaskRecord{ID: "task-old", ProjectID: projectID, AccountID: account, Agent: "coder", Title: "Old", Status: "completed"}
	if err := store.PutProjectTask(account, old); err != nil {
		t.Fatal(err)
	}
	for time.Now().UnixMilli() <= old.UpdatedAt {
		time.Sleep(time.Millisecond)
	}
	for _, task := range []*pebblestore.ProjectTaskRecord{
		{ID: "task-running", ProjectID: projectID, AccountID: account, Agent: "coder", Title: "Running", Status: "in_progress"},
		{ID: "task-done", ProjectID: projectID, AccountID: account, Agent: "coder", Title: "Done", Status: "completed"},
		{ID: "task-archived", ProjectID: projectID, AccountID: account, Agent: "coder", Title: "Archived", Status: "in_progress", Archived: true},
	} {
		if err := store.PutProjectTask(account, task); err != nil {
			t.Fatal(err)
		}
	}
	// The old task finished just over a day before now; the others are recent.
	now := time.UnixMilli(old.UpdatedAt + pebblestore.ProjectTaskRecentFinishMillis + 1)
	previous := projectTaskListNow
	projectTaskListNow = func() time.Time { return now }
	defer func() { projectTaskListNow = previous }()

	runtime := NewRuntime(1)
	defer runtime.Close()
	runtime.SetManageProjectStore(store)
	scope := WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-compact", AccountScopeID: account}}
	call := func(args map[string]any) (string, error) {
		return runtime.executeManageProjects(context.Background(), scope, args)
	}
	forbidden := []string{iconURL[:40], "icon_png_data_url", "theme", "session-hidden", "automation-hidden", "Foreign", "App\""}

	raw, err := call(map[string]any{"action": "list"})
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range append(forbidden, "project_context", "Délégué") {
		if strings.Contains(raw, leak) {
			t.Fatalf("list leaks %q: %s", leak, raw)
		}
	}
	var listed struct {
		Count    int              `json:"count"`
		Projects []map[string]any `json:"projects"`
	}
	if err := json.Unmarshal([]byte(raw), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Count != 1 || len(listed.Projects) != 1 {
		t.Fatalf("list must return only this account's project: %s", raw)
	}
	row := listed.Projects[0]
	if len(row) != 4 || row["id"] != projectID || row["name"] != "Compact" {
		t.Fatalf("list row must be exactly id, name, workspaces, current_tasks: %v", row)
	}
	workspaces, _ := json.Marshal(row["workspaces"])
	if string(workspaces) != `[{"id":"ws-1","path":"/repo/app","role":"primary_code"},{"path":"/repo/docs","role":"docs"}]` {
		t.Fatalf("workspaces = %s", workspaces)
	}
	counts, _ := json.Marshal(row["current_tasks"])
	if string(counts) != `{"by_status":{"completed":1,"in_progress":1},"total":2}` {
		t.Fatalf("current_tasks must count running and recently finished, unarchived tasks only: %s", counts)
	}

	raw, err = call(map[string]any{"action": "get", "id": projectID})
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range forbidden {
		if strings.Contains(raw, leak) {
			t.Fatalf("get leaks %q: %s", leak, raw)
		}
	}
	var got struct {
		Project struct {
			ID               string         `json:"id"`
			Description      string         `json:"description"`
			ProjectContext   string         `json:"project_context"`
			ContextBytes     int            `json:"project_context_bytes"`
			ContextTruncated bool           `json:"project_context_truncated"`
			CurrentTasks     map[string]any `json:"current_tasks"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	preview := got.Project.ProjectContext
	if got.Project.ID != projectID || got.Project.Description != "A project" || got.Project.CurrentTasks == nil {
		t.Fatalf("get lacks the summary: %s", raw)
	}
	if !got.Project.ContextTruncated || got.Project.ContextBytes != len(fullContext) || len(preview) > projectContextPreviewBytes || len(preview) < projectContextPreviewBytes-64 {
		t.Fatalf("get must cut project.md to about 2 KB: len=%d truncated=%v bytes=%d", len(preview), got.Project.ContextTruncated, got.Project.ContextBytes)
	}
	if !utf8.ValidString(preview) || !strings.HasPrefix(fullContext, preview) {
		t.Fatal("the project.md preview must be a valid UTF-8 prefix of the full text")
	}

	raw, err = call(map[string]any{"action": "get", "project_id": projectID, "detail": "context"})
	if err != nil {
		t.Fatal(err)
	}
	got.Project.ContextTruncated = false
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Project.ProjectContext != fullContext || got.Project.ContextTruncated || strings.Contains(raw, iconURL[:40]) {
		t.Fatalf("detail=context must return the whole project.md and still no icon (len=%d)", len(got.Project.ProjectContext))
	}

	if _, err := call(map[string]any{"action": "get", "id": projectID, "detail": "everything"}); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("an unknown detail must be refused, got %v", err)
	}
	if _, err := call(map[string]any{"action": "get", "id": "proj-foreign"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("another account's project must not be readable, got %v", err)
	}
}

// Purpose: a short project.md is returned whole without a truncation flag,
// and truncation never splits a multi-byte character.
func TestTruncateProjectContextKeepsRuneBoundaries(t *testing.T) {
	if text, cut := truncateProjectContext("short", projectContextPreviewBytes); text != "short" || cut {
		t.Fatalf("short text changed: %q %v", text, cut)
	}
	text := strings.Repeat("é", 10) // 20 bytes
	got, cut := truncateProjectContext(text, 5)
	if !cut || got != "éé" || !utf8.ValidString(got) {
		t.Fatalf("got %q cut=%v", got, cut)
	}
}
