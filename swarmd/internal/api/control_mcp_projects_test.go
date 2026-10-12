package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

// controlMCPToolText runs one tool through callTool (schema validation, level
// check and the tool), returning its text and isError.
func controlMCPToolText(t *testing.T, c *controlMCPCall, name string, args map[string]any) (string, bool) {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := c.callTool(params)
	if rpcErr != nil {
		t.Fatalf("%s: %s", name, rpcErr.Message)
	}
	content, _ := result["content"].([]map[string]any)
	if len(content) == 0 {
		t.Fatalf("%s: empty result", name)
	}
	text, _ := content[0]["text"].(string)
	isError, _ := result["isError"].(bool)
	return text, isError
}

// newControlMCPProjectFixture is the Desktop's folder-registration fixture
// (real temporary workspace, topology and session stores; one registered Git
// repository) with project realtime publication wired to the outbox hub.
func newControlMCPProjectFixture(t *testing.T) (*Server, string, *pebblestore.Store) {
	t.Helper()
	server, repo, db := newWorkspaceOverviewTopologyTestServer(t)
	swarmStore := pebblestore.NewSwarmStore(db)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "host-swarm-id", Name: "host", Role: "host"}); err != nil {
		t.Fatal(err)
	}
	server.SetTopologyService(topologyruntime.NewService(pebblestore.NewTopologyStore(db), swarmStore))
	server.ConfigureProjectRealtime(db)
	t.Cleanup(func() {
		server.BeginShutdown()
		server.CancelInFlightRuns()
		if !server.WaitForInFlightRuns(10 * time.Second) {
			t.Error("fixture runs did not stop before store cleanup")
		}
	})
	return server, repo, db
}

// Requirement: an AI onboarding someone registers the person's EXISTING
// folders (never an empty or system folder, never one inside a registered
// workspace), groups them into a project whose project.md Swarm generates, and
// may change the project's workspaces or project.md only once generation is
// ready, with a refusal that names the next step (wait or retry). Threats: an
// AI handing the person an empty repository or a system/Swarm storage folder
// as their work, adopting files by git init, or a refused edit silently
// changing the project. Owners: controlMCPAddWorkspace with
// workspace.RegisterExistingFolderForPrincipal (through POST
// /v1/workspace/add context_only + require_existing_content), the project
// tools and PATCH /v3/projects/{id} / POST .../context:retry. The API mux with
// real temporary stores is the narrowest layer that runs the tool, route
// allowlist, scopes, registration checks and the project record together; no
// provider is configured, so generation fails durably and ready is reached
// through the store's attempt-fenced finish, never a provider call.
func TestControlMCPOnboardsExistingFolders(t *testing.T) {
	server, repo, _ := newControlMCPProjectFixture(t)
	p := testPrincipal()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	write := controlMCPDirectCall(ctx, p, server.apiMux(), server, "sessions:read", "sessions:write")
	read := controlMCPDirectCall(ctx, p, server.apiMux(), server, "sessions:read")
	store := server.sessions.Store()

	docs := t.TempDir()
	if err := os.WriteFile(filepath.Join(docs, "README.md"), []byte("# Notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	docs, _ = filepath.EvalSymlinks(docs)
	empty := t.TempDir()
	nested := filepath.Join(repo, "sub")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	protected := t.TempDir()
	server.workspace.SetProtectedRoots(protected)
	inProtected := filepath.Join(protected, "data")
	if err := os.MkdirAll(inProtected, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inProtected, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	registered := func() int {
		t.Helper()
		entries, err := server.workspace.ListKnownForPrincipal(p, 100)
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	before := registered()

	if text, isError := controlMCPToolText(t, read, "swarm_add_workspace", map[string]any{"path": docs}); !isError || !strings.Contains(text, "sessions:write") {
		t.Fatalf("read grant registered a folder: %s", text)
	}
	for _, tc := range []struct{ path, want string }{
		{empty, "is empty"},
		{"/etc", "system or Swarm storage directory"},
		{inProtected, "system or Swarm storage directory"},
		{nested, "inside the registered workspace"},
		{"notes", "absolute"},
		{filepath.Join(docs, "not-there"), "install.sh workspace add"},
	} {
		if text, isError := controlMCPToolText(t, write, "swarm_add_workspace", map[string]any{"path": tc.path}); !isError || !strings.Contains(text, tc.want) {
			t.Fatalf("%s: want %q, got %s", tc.path, tc.want, text)
		}
	}
	if _, err := write.dispatch(http.MethodPost, "/v1/workspace/add", nil, map[string]any{"path": docs, "require_existing_content": true}); err == nil || !strings.Contains(err.Error(), "context_only") {
		t.Fatalf("require_existing_content outside context_only: %v", err)
	}
	if registered() != before {
		t.Fatal("a refused folder was registered")
	}
	text, isError := controlMCPToolText(t, write, "swarm_add_workspace", map[string]any{"path": docs, "name": "Notes"})
	if isError || !strings.Contains(text, `"added":true`) || !strings.Contains(text, docs) {
		t.Fatalf("add: %s", text)
	}
	if _, err := os.Stat(filepath.Join(docs, ".git")); !os.IsNotExist(err) {
		t.Fatal("adding a folder initialised Git")
	}
	if text, isError := controlMCPToolText(t, write, "swarm_add_workspace", map[string]any{"path": docs}); isError || !strings.Contains(text, `"added":false`) {
		t.Fatalf("repeat add: %s", text)
	}
	if registered() != before+1 {
		t.Fatal("repeat add registered twice")
	}

	// Projects take registered workspaces only, with a valid role.
	for _, bad := range []map[string]any{
		{"name": "Notes", "workspaces": []any{map[string]any{"path": empty}}},
		{"name": "Notes", "workspaces": []any{map[string]any{"path": repo, "role": "owner"}}},
		{"name": "Notes", "workspaces": []any{map[string]any{"path": repo, "label": "x"}}},
	} {
		if text, isError := controlMCPToolText(t, write, "swarm_create_project", bad); !isError {
			t.Fatalf("bad project accepted: %s", text)
		}
	}
	if projects, _ := store.ListProjects(p.AccountScopeID, 10); len(projects) != 0 {
		t.Fatal("a refused project was created")
	}
	text, isError = controlMCPToolText(t, write, "swarm_create_project", map[string]any{
		"name": "Notes", "description": "The person's notes and code",
		"workspaces": []any{map[string]any{"path": repo}, map[string]any{"path": docs, "role": "docs"}},
	})
	if isError {
		t.Fatalf("create project: %s", text)
	}
	var created struct {
		Project struct {
			ID                string           `json:"id"`
			Workspaces        []map[string]any `json:"workspaces"`
			ContextGeneration map[string]any   `json:"context_generation"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(text), &created); err != nil || created.Project.ID == "" {
		t.Fatalf("create project result: %s", text)
	}
	if len(created.Project.Workspaces) != 2 || created.Project.Workspaces[0]["role"] != "primary_code" || created.Project.Workspaces[1]["role"] != "docs" || created.Project.ContextGeneration["status"] == "" {
		t.Fatalf("created project: %s", text)
	}
	projectID := created.Project.ID
	// Generation runs in the background; get_project waits for it to end.
	settle := func() {
		t.Helper()
		text, isError := controlMCPToolText(t, read, "swarm_get_project", map[string]any{"project_id": projectID, "wait_seconds": 20})
		if isError || !strings.Contains(text, `"settled":true`) {
			t.Fatalf("generation did not settle: %s", text)
		}
	}
	settle()
	stored := func() *pebblestore.ProjectRecord {
		t.Helper()
		record, found, err := store.GetProject(p.AccountScopeID, projectID)
		if err != nil || !found {
			t.Fatalf("project: %v", err)
		}
		return record
	}
	if got := stored(); got.ContextGeneration.Status != "failed" || got.ProjectContext != "" {
		t.Fatalf("generation without a provider: %+v", got.ContextGeneration)
	}

	// While project.md is not ready, edits are refused with the next step and
	// change nothing.
	if text, isError := controlMCPToolText(t, write, "swarm_set_project_workspaces", map[string]any{"project_id": projectID, "workspaces": []any{map[string]any{"path": docs}}}); !isError || !strings.Contains(text, "swarm_retry_project_context") {
		t.Fatalf("workspace edit while failed: %s", text)
	}
	if text, isError := controlMCPToolText(t, write, "swarm_update_project_context", map[string]any{"project_id": projectID, "project_md": "# Mine"}); !isError || !strings.Contains(text, "swarm_retry_project_context") {
		t.Fatalf("project.md edit while failed: %s", text)
	}
	if got := stored(); len(got.Workspaces) != 2 || got.ProjectContext != "" {
		t.Fatal("a refused edit changed the project")
	}
	if text, isError := controlMCPToolText(t, write, "swarm_retry_project_context", map[string]any{"project_id": projectID}); isError {
		t.Fatalf("retry: %s", text)
	}
	settle()
	failed := stored()
	if failed.ContextGeneration.Attempt != 2 || failed.ContextGeneration.Status != "failed" {
		t.Fatalf("retry attempt: %+v", failed.ContextGeneration)
	}

	// project.md becomes ready (attempt-fenced store finish, as the generator does).
	claimed, err := store.ClaimProjectContext(p.AccountScopeID, projectID, failed.ContextGeneration.Attempt, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishProjectContext(p.AccountScopeID, projectID, claimed.ContextGeneration.Attempt, "# Notes\nGenerated.", "", ""); err != nil {
		t.Fatal(err)
	}
	if text, isError := controlMCPToolText(t, write, "swarm_retry_project_context", map[string]any{"project_id": projectID}); !isError || !strings.Contains(text, "already ready") {
		t.Fatalf("retry when ready: %s", text)
	}
	if text, isError := controlMCPToolText(t, read, "swarm_get_project", map[string]any{"project_id": projectID}); isError || !strings.Contains(text, "Generated.") || !strings.Contains(text, `"status":"ready"`) {
		t.Fatalf("get project: %s", text)
	}
	if text, isError := controlMCPToolText(t, read, "swarm_update_project_context", map[string]any{"project_id": projectID, "project_md": "# Read grant"}); !isError || !strings.Contains(text, "HTTP 403") {
		t.Fatalf("read grant edited project.md: %s", text)
	}
	if text, isError := controlMCPToolText(t, write, "swarm_update_project_context", map[string]any{"project_id": projectID, "project_md": "# Notes\nEdited."}); isError {
		t.Fatalf("edit project.md: %s", text)
	}
	if got := stored(); got.ProjectContext != "# Notes\nEdited." {
		t.Fatalf("project.md not stored: %q", got.ProjectContext)
	}
	if text, isError := controlMCPToolText(t, write, "swarm_set_project_workspaces", map[string]any{"project_id": projectID, "workspaces": []any{map[string]any{"path": docs, "role": "primary_code"}}}); isError {
		t.Fatalf("set workspaces: %s", text)
	}
	if got := stored(); len(got.Workspaces) != 1 || got.Workspaces[0].Path != docs || got.Workspaces[0].Role != "primary_code" || got.ProjectContext != "# Notes\nEdited." {
		t.Fatalf("workspaces not replaced: %+v", got.Workspaces)
	}
	if text, isError := controlMCPToolText(t, read, "swarm_list_projects", map[string]any{}); isError || !strings.Contains(text, `"context_status":"ready"`) || !strings.Contains(text, docs) {
		t.Fatalf("list projects: %s", text)
	}
	if text, isError := controlMCPToolText(t, read, "swarm_get_project", map[string]any{"project_id": "proj_missing"}); !isError || !strings.Contains(text, "swarm_list_projects") {
		t.Fatalf("missing project: %s", text)
	}
}

// Requirement: get_project wait_seconds returns as soon as project.md
// generation leaves pending/running, woken by the committed project update on
// the realtime outbox hub rather than polling, and returns at the deadline
// otherwise without leaking its subscription. Owners:
// controlMCPCall.waitForProjectContext and the project realtime publisher; a
// real store with the hub wired is the narrowest layer.
func TestControlMCPWaitsForProjectContext(t *testing.T) {
	server, repo, _ := newControlMCPProjectFixture(t)
	p := testPrincipal()
	store := server.sessions.Store()
	refs, err := server.authorizeProjectWorkspaces(p, []pebblestore.ProjectWorkspaceRef{{Path: repo}})
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := store.CreateProjectWithContext(p.AccountScopeID, "wait-test", "hash", pebblestore.ProjectRecord{Name: "Wait", Workspaces: refs})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimProjectContext(p.AccountScopeID, record.ID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := controlMCPDirectCall(ctx, p, server.apiMux(), server, "sessions:read")

	type outcome struct {
		text    string
		elapsed time.Duration
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		text, _ := controlMCPToolText(t, c, "swarm_get_project", map[string]any{"project_id": record.ID, "wait_seconds": 30})
		done <- outcome{text, time.Since(start)}
	}()
	select {
	case got := <-done:
		t.Fatalf("wait returned while generating: %s", got.text)
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := store.FinishProjectContext(p.AccountScopeID, record.ID, claimed.ContextGeneration.Attempt, "# Wait\nReady.", "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.elapsed > 5*time.Second || !strings.Contains(got.text, `"status":"ready"`) || !strings.Contains(got.text, `"settled":true`) || !strings.Contains(got.text, "Ready.") {
			t.Fatalf("wait after ready: %s after %s", got.text, got.elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not wake on the finished project.md")
	}

	second, _, err := store.CreateProjectWithContext(p.AccountScopeID, "wait-deadline", "hash2", pebblestore.ProjectRecord{Name: "Deadline", Workspaces: refs})
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	text, isError := controlMCPToolText(t, c, "swarm_get_project", map[string]any{"project_id": second.ID, "wait_seconds": 1})
	if elapsed := time.Since(start); isError || elapsed < 900*time.Millisecond || elapsed > 5*time.Second || !strings.Contains(text, `"timed_out":true`) {
		t.Fatalf("deadline: %s after %s", text, elapsed)
	}
	server.v3RealtimeOutbox.mu.Lock()
	leaked := len(server.v3RealtimeOutbox.subs)
	server.v3RealtimeOutbox.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("wait leaked %d hub subscriptions", leaked)
	}
}
