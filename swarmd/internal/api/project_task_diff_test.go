package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// taskDiffFixture is a source folder on dev and a task worktree on
// agent/task, recorded in the real store as a project task and its session the
// way task allocation records them.
type taskDiffFixture struct {
	t       *testing.T
	server  *Server
	source  string
	child   string
	base    string
	project string
}

func newTaskDiffFixture(t *testing.T) taskDiffFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	root := t.TempDir()
	source, child := filepath.Join(root, "source"), filepath.Join(root, "child")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	integrationGit(t, source, "init", "-q", "-b", "dev")
	writeRebaseFile(t, source, "edit.txt", "one\ntwo\nthree\n")
	writeRebaseFile(t, source, "gone.txt", "bye\n")
	writeRebaseFile(t, source, "old-name.txt", strings.Repeat("stable line\n", 20))
	integrationGit(t, source, "add", ".")
	integrationGit(t, source, "commit", "-q", "-m", "base")
	base := integrationGit(t, source, "rev-parse", "HEAD")
	integrationGit(t, source, "worktree", "add", "-q", "-b", "agent/task", child)
	return taskDiffFixture{t: t, server: server, source: source, child: child, base: base, project: "diff-project"}
}

func (f taskDiffFixture) commit(message string) string {
	f.t.Helper()
	integrationGit(f.t, f.child, "add", "-A")
	integrationGit(f.t, f.child, "commit", "-q", "-m", message)
	return integrationGit(f.t, f.child, "rev-parse", "HEAD")
}

// record stores the project, the task waiting for review and its session.
func (f taskDiffFixture) record(taskID string) {
	f.t.Helper()
	p := testPrincipal()
	db := f.server.sessions.Store()
	if err := db.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: f.project, AccountID: p.AccountScopeID, Name: "Diff", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: f.source}}}); err != nil {
		f.t.Fatal(err)
	}
	sessionID := "sess-" + taskID
	session := pebblestore.SessionSnapshot{ID: sessionID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, WorkspacePath: f.child, WorktreeEnabled: true, WorktreeRootPath: f.child, WorktreeBranch: "agent/task", WorktreeBaseBranch: "dev",
		Metadata: map[string]any{"swarm_v3_source_workspace_path": f.source, "base_commit": f.base, "project_id": f.project, "task_id": taskID, "swarm_v3_worktree_owner_session_id": sessionID, "swarm_v3_runtime_workspace_path": f.child}}
	if err := db.CreateSessionForAccount(session, p.UserID, p.AccountScopeID); err != nil {
		f.t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: taskID, ProjectID: f.project, AccountID: p.AccountScopeID, Title: "Change things", Agent: "coder", Status: "needs_review", SessionID: sessionID, WorkspacePath: f.child, WorktreeBranch: "agent/task", BaseBranch: "dev", BaseCommit: f.base}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		f.t.Fatal(err)
	}
}

func (f taskDiffFixture) get(req *http.Request) (int, taskDiffAnswer, string) {
	f.t.Helper()
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	var answer taskDiffAnswer
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
			f.t.Fatalf("answer: %v %s", err, rec.Body.String())
		}
	}
	return rec.Code, answer, rec.Body.String()
}

func (f taskDiffFixture) diff(taskID, path string) (int, taskDiffAnswer, string) {
	target := "/v3/projects/" + f.project + "/tasks/" + taskID + "/diff"
	if path != "" {
		target += "?path=" + url.QueryEscape(path)
	}
	return f.get(withTestPrincipal(httptest.NewRequest(http.MethodGet, target, nil)))
}

func taskDiffFileByPath(t *testing.T, answer taskDiffAnswer, path string) taskDiffFile {
	t.Helper()
	for _, file := range answer.Files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("no %s in %+v", path, answer.Files)
	return taskDiffFile{}
}

// Requirement: the task diff route answers a task's base and head commits and,
// per file, its status (added, modified, deleted, renamed), added and deleted
// line counts and its hunks; a binary file is marked binary without content;
// a file whose diff passes the per-file bound is cut and marked truncated, and
// ?path= returns that one file in full within the total bound. Uncommitted
// worktree edits are not part of the range integrate would bring in.
// Owners: handleProjectTaskDiff over the real store, the daemon's route and
// real Git in a task worktree (resolveChangesRange, shared with
// swarm_get_changes). No model provider is involved.
func TestProjectTaskDiffShowsTheTaskBranch(t *testing.T) {
	f := newTaskDiffFixture(t)
	writeRebaseFile(t, f.child, "edit.txt", "one\n2\nthree\nfour\n")
	if err := os.Remove(filepath.Join(f.child, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	integrationGit(t, f.child, "mv", "old-name.txt", "new-name.txt")
	writeRebaseFile(t, f.child, "added.txt", "fresh\n")
	if err := os.WriteFile(filepath.Join(f.child, "logo.bin"), []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3, 0, 255}, 0o644); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("a line that makes this file larger than one file's diff bound\n", 1400)
	writeRebaseFile(t, f.child, "big.txt", big)
	head := f.commit("task work")
	writeRebaseFile(t, f.child, "edit.txt", "uncommitted\n")
	f.record("diff-task")

	code, answer, body := f.diff("diff-task", "")
	if code != http.StatusOK {
		t.Fatalf("diff: %d %s", code, body)
	}
	if answer.Base != f.base || answer.Head != head || answer.Branch != "agent/task" || answer.BaseBranch != "dev" || answer.FilesTruncated {
		t.Fatalf("range: %+v", answer)
	}
	if len(answer.Files) != 6 {
		t.Fatalf("files: %+v", answer.Files)
	}
	edit := taskDiffFileByPath(t, answer, "edit.txt")
	if edit.Status != "modified" || edit.Additions != 2 || edit.Deletions != 1 || edit.Truncated || !strings.HasPrefix(edit.Diff, "@@ ") || !strings.Contains(edit.Diff, "\n+four\n") || strings.Contains(edit.Diff, "uncommitted") || strings.Contains(edit.Diff, "diff --git") {
		t.Fatalf("edit: %+v", edit)
	}
	if gone := taskDiffFileByPath(t, answer, "gone.txt"); gone.Status != "deleted" || gone.Deletions != 1 || !strings.Contains(gone.Diff, "-bye") {
		t.Fatalf("gone: %+v", gone)
	}
	if moved := taskDiffFileByPath(t, answer, "new-name.txt"); moved.Status != "renamed" || moved.OldPath != "old-name.txt" || moved.Additions != 0 || moved.Deletions != 0 || moved.Diff != "" {
		t.Fatalf("moved: %+v", moved)
	}
	if added := taskDiffFileByPath(t, answer, "added.txt"); added.Status != "added" || added.Additions != 1 || added.Diff != "@@ -0,0 +1 @@\n+fresh\n" {
		t.Fatalf("added: %+v", added)
	}
	if logo := taskDiffFileByPath(t, answer, "logo.bin"); !logo.Binary || logo.Diff != "" || logo.Status != "added" || logo.Truncated {
		t.Fatalf("binary: %+v", logo)
	}
	cut := taskDiffFileByPath(t, answer, "big.txt")
	if !cut.Truncated || cut.Additions != 1400 || len(cut.Diff) > taskDiffFileBytes || len(cut.Diff) < taskDiffFileBytes/2 || !strings.HasSuffix(cut.Diff, "\n") || !answer.Truncated {
		t.Fatalf("per-file bound: truncated=%v additions=%d len=%d answer.truncated=%v", cut.Truncated, cut.Additions, len(cut.Diff), answer.Truncated)
	}

	code, one, body := f.diff("diff-task", "big.txt")
	if code != http.StatusOK || len(one.Files) != 1 || one.Path != "big.txt" || one.Files[0].Truncated || one.Truncated || strings.Count(one.Files[0].Diff, "\n+") != 1400 {
		t.Fatalf("one file: %d truncated=%v %s", code, one.Truncated, controlMCPTruncate(body, 300))
	}
	code, renamed, body := f.diff("diff-task", "old-name.txt")
	if code != http.StatusOK || len(renamed.Files) != 1 || renamed.Files[0].Status != "renamed" || renamed.Files[0].Path != "new-name.txt" {
		t.Fatalf("renamed by old path: %d %s", code, body)
	}
	if code, _, body := f.diff("diff-task", "README"); code != http.StatusConflict || !strings.Contains(body, "not changed by this task") {
		t.Fatalf("unchanged path: %d %s", code, body)
	}
	if code, _, body := f.diff("diff-task", "../source/edit.txt"); code != http.StatusBadRequest {
		t.Fatalf("escaping path: %d %s", code, body)
	}
}

// Requirement: the answer's diff text stays within the total bound however
// many large files a task changed; files past the bound are listed with their
// counts, marked truncated, without text.
func TestProjectTaskDiffTotalBound(t *testing.T) {
	f := newTaskDiffFixture(t)
	line := strings.Repeat("x", 99) + "\n"
	for i := 0; i < 9; i++ {
		writeRebaseFile(t, f.child, "big-"+string(rune('a'+i))+".txt", strings.Repeat(line, 700))
	}
	f.commit("large files")
	f.record("big-task")
	code, answer, body := f.diff("big-task", "")
	if code != http.StatusOK || len(answer.Files) != 9 || !answer.Truncated {
		t.Fatalf("diff: %d %s", code, controlMCPTruncate(body, 300))
	}
	total := 0
	for _, file := range answer.Files {
		total += len(file.Diff)
		if !file.Truncated || file.Additions != 700 {
			t.Fatalf("file %s: truncated=%v additions=%d", file.Path, file.Truncated, file.Additions)
		}
	}
	if last := answer.Files[8]; total > taskDiffTotalBytes || last.Diff != "" {
		t.Fatalf("total %d (limit %d), last file %d bytes", total, taskDiffTotalBytes, len(last.Diff))
	}
}

// Requirement: a folder task already in its folder shows its recorded range
// only, never later commits of the folder.
func TestProjectTaskDiffFolderTaskRecordedRange(t *testing.T) {
	f := newTaskDiffFixture(t)
	writeRebaseFile(t, f.source, "card.html", "<p>card</p>\n")
	integrationGit(t, f.source, "add", "card.html")
	integrationGit(t, f.source, "commit", "-q", "-m", "design")
	head := integrationGit(t, f.source, "rev-parse", "HEAD")
	writeRebaseFile(t, f.source, "later.txt", "later\n")
	integrationGit(t, f.source, "add", "later.txt")
	integrationGit(t, f.source, "commit", "-q", "-m", "later")
	p := testPrincipal()
	db := f.server.sessions.Store()
	if err := db.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: f.project, AccountID: p.AccountScopeID, Name: "Diff", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: f.source}}}); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "folder-task", ProjectID: f.project, AccountID: p.AccountScopeID, Title: "Design", Agent: "designer", Status: "completed", SessionID: "design", WorkspacePath: f.source, SourceWorkspace: pebblestore.ProjectTaskSource{Path: f.source, WorkspaceID: "ws-source", WorkspaceGeneration: 1, Provenance: "project"}, BaseBranch: "dev", BaseCommit: f.base,
		Integration: &pebblestore.ProjectTaskIntegration{State: "in_folder", SessionID: "design", SourceHead: head}}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	code, answer, body := f.diff("folder-task", "")
	if code != http.StatusOK || answer.Base != f.base || answer.Head != head || len(answer.Files) != 1 || answer.Files[0].Path != "card.html" || answer.Files[0].Status != "added" {
		t.Fatalf("folder task: %d %s", code, body)
	}
}

// Requirement: the route is an owner read: a token without a project or
// session read scope, and an application gateway's token, are refused; a
// task of another account is not found.
func TestProjectTaskDiffRefusals(t *testing.T) {
	f := newTaskDiffFixture(t)
	writeRebaseFile(t, f.child, "added.txt", "fresh\n")
	f.commit("work")
	f.record("diff-task")
	target := "/v3/projects/" + f.project + "/tasks/diff-task/diff"
	request := func(token *pebblestore.ScopedTokenRecord) *http.Request {
		return requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodGet, target, nil)), token)
	}
	if code, _, body := f.get(request(&pebblestore.ScopedTokenRecord{Scopes: []string{"automations:read"}})); code != http.StatusForbidden {
		t.Fatalf("missing scope: %d %s", code, body)
	}
	if code, _, body := f.get(request(&pebblestore.ScopedTokenRecord{Scopes: []string{"projects:read", "sessions:read"}, AgentName: "frontdesk"})); code != http.StatusForbidden {
		t.Fatalf("gateway token: %d %s", code, body)
	}
	if code, _, body := f.get(request(&pebblestore.ScopedTokenRecord{Scopes: []string{"projects:read"}})); code != http.StatusOK {
		t.Fatalf("read scope: %d %s", code, body)
	}
	if err := f.server.sessions.Store().PutProjectTask("other-account", &pebblestore.ProjectTaskRecord{ID: "foreign", ProjectID: f.project, AccountID: "other-account", Title: "x", Agent: "coder", Status: "pending_approval"}); err != nil {
		t.Fatal(err)
	}
	if code, _, body := f.diff("foreign", ""); code != http.StatusNotFound {
		t.Fatalf("foreign task: %d %s", code, body)
	}
	if code, _, body := f.get(withTestPrincipal(httptest.NewRequest(http.MethodPost, target, nil))); code != http.StatusMethodNotAllowed {
		t.Fatalf("post: %d %s", code, body)
	}
}
