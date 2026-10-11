package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a finished task whose work is committed on its branch must offer
// integrate (GET /v3/projects/{id}/tasks/{task}) and integrate successfully
// while the source workspace only holds untracked files the task does not
// write, such as an app's own state file; the file must survive untouched.
// When delivery truly is blocked, action_needed must name the exact files.
// Threat: "Source or target has uncommitted changes" with allowed_actions []
// hides integrate for cleanly committed work, or relaxing it lets integration
// overwrite an untracked file or ship uncommitted task work.
// Owner: inspectTaskGitStateContext/AssessTaskDelivery and the registered
// integrate handler with PrepareTaskIntegration. The HTTP handler over a real
// temporary Git repository and task worktree is the narrowest layer proving
// the client-visible fields and the integrated Git result together.
func TestProjectTaskUntrackedTargetFilesStayIntegrable(t *testing.T) {
	root := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(root, "empty-config"), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	run(repo, "init", "-b", "main")
	write(filepath.Join(repo, "base"), "base\n")
	run(repo, "add", "base")
	run(repo, "commit", "-m", "base")
	base := run(repo, "rev-parse", "HEAD")
	child := filepath.Join(t.TempDir(), "child")
	run(repo, "worktree", "add", "-b", "agent/task", child)
	write(filepath.Join(child, "feature.txt"), "feature\n")
	run(child, "add", "feature.txt")
	run(child, "commit", "-m", "task work")
	head := run(child, "rev-parse", "HEAD")

	server, _, raw := newWorkspaceOverviewTopologyTestServer(t)
	db := pebblestore.NewSessionStore(raw)
	if err := db.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	account := testPrincipal().AccountScopeID
	entry, err := pebblestore.NewWorkspaceStore(raw).AddForAccount(account, repo, "App")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutProject(account, &pebblestore.ProjectRecord{ID: "project", Name: "App", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}); err != nil {
		t.Fatal(err)
	}
	session := pebblestore.SessionSnapshot{WorkspacePath: child, ID: "task-session", UserID: testPrincipal().UserID, AccountScopeID: account, WorktreeEnabled: true, WorktreeRootPath: child, WorktreeBranch: "agent/task", WorktreeBaseBranch: "main", Metadata: map[string]any{"project_id": "project", "task_id": "task", "swarm_v3_worktree_owner_session_id": "task-session", "swarm_v3_runtime_workspace_path": child, "swarm_v3_source_workspace_id": entry.WorkspaceID, "swarm_v3_source_workspace_generation": entry.WorkspaceGeneration, "swarm_v3_source_workspace_path": repo, "base_commit": base}}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{SessionID: session.ID, UserID: testPrincipal().UserID, AccountScopeID: account, ClientRequestID: "create:untracked", IdempotencyKey: "create:untracked", PayloadHash: "create:untracked", RequestHash: "create:untracked", Kind: sessionruntime.SessionMutationCreateSession, Session: &session, WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: child, SourcePath: repo, OwnerSessionID: session.ID, Branch: session.WorktreeBranch, AllocatedRuntimeRoot: true}, NowUnixMs: 1}); err != nil {
		t.Fatal(err)
	}
	task := pebblestore.ProjectTaskRecord{ID: "task", Title: "Add feature", Agent: "coder", ProjectID: "project", WorkspacePath: child, SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}, SessionID: session.ID, AccountID: account, Status: "needs_review", WorktreeBranch: "agent/task", BaseBranch: "main", BaseCommit: base}
	if err := db.PutProjectTask(account, &task); err != nil {
		t.Fatal(err)
	}
	call := func(method, suffix, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, ProjectsPath+"/project/tasks/task"+suffix, strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), productPrincipalRequestContextKey, testPrincipal()))
		w := httptest.NewRecorder()
		server.handleProjects(w, r)
		return w
	}
	detail := func() pebblestore.ProjectTaskRecord {
		t.Helper()
		response := call(http.MethodGet, "", "")
		var result struct {
			Task pebblestore.ProjectTaskRecord `json:"task"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Task.DeliveryAssessment == nil {
			t.Fatalf("detail: %d %s", response.Code, response.Body)
		}
		return result.Task
	}
	assertBlocked := func(wantInMessage string) {
		t.Helper()
		got := detail()
		if len(got.DeliveryAssessment.AllowedActions) != 0 || !strings.Contains(got.ActionNeeded, wantInMessage) {
			t.Fatalf("blocked delivery not named: actions=%v action_needed=%q", got.DeliveryAssessment.AllowedActions, got.ActionNeeded)
		}
	}

	// The app keeps its own untracked state next to the code.
	appState := filepath.Join(repo, "app-state.json")
	write(appState, `{"projects":["demo"]}`)
	got := detail()
	if got.DeliveryAssessment.State != "candidate_work" || got.DeliveryAssessment.TargetDirty || len(got.DeliveryAssessment.AllowedActions) != 1 || got.DeliveryAssessment.AllowedActions[0] != "integrate" || strings.Contains(got.ActionNeeded, "uncommitted") {
		t.Fatalf("committed task not integrable beside an untracked app file: %+v action_needed=%q", got.DeliveryAssessment, got.ActionNeeded)
	}

	// Real blockers still block and say exactly what blocks.
	write(filepath.Join(repo, "feature.txt"), "local\n")
	assertBlocked(`Target checkout on "main" has uncommitted changes: feature.txt (untracked, and the task writes this path)`)
	if err := os.Remove(filepath.Join(repo, "feature.txt")); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(repo, "base"), "edited\n")
	assertBlocked(`Target checkout on "main" has uncommitted changes: base (modified)`)
	refused := call(http.MethodPost, "/integrate", `{"session_id":"task-session","source_branch":"agent/task","target_branch":"main"}`)
	if refused.Code != http.StatusConflict || !strings.Contains(refused.Body.String(), "base (modified)") || run(repo, "rev-parse", "HEAD") != base {
		t.Fatalf("tracked target change admitted: %d %s", refused.Code, refused.Body)
	}
	run(repo, "checkout", "--", "base")
	write(filepath.Join(child, "notes.txt"), "not committed\n")
	assertBlocked(`Task branch "agent/task" has uncommitted changes: notes.txt (untracked)`)
	if err := os.Remove(filepath.Join(child, "notes.txt")); err != nil {
		t.Fatal(err)
	}

	response := call(http.MethodPost, "/integrate", `{"session_id":"task-session","source_branch":"agent/task","target_branch":"main"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"integrated"`) {
		t.Fatalf("integrate beside an untracked app file: %d %s", response.Code, response.Body)
	}
	run(repo, "merge-base", "--is-ancestor", head, "HEAD")
	if data, err := os.ReadFile(appState); err != nil || string(data) != `{"projects":["demo"]}` {
		t.Fatalf("integration touched the untracked app file: %q %v", data, err)
	}
	if status := run(repo, "status", "--porcelain", "--untracked-files=all"); status != "?? app-state.json" {
		t.Fatalf("target status after integration = %q", status)
	}
}
