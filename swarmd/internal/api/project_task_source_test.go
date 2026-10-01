package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: submission identity is scoped to the project and exact source binding;
// threat: retrying the same task ID with changed payload or a different repository
// must not be treated as the original dispatch. projectTaskSubmissionHash is the
// narrowest pre-persistence boundary for this assertion.
func TestProjectTaskSubmissionHashBindsPayloadAndSource(t *testing.T) {
	input := tool.ProjectTaskCreateInput{ID: "task-1", ClientRequestID: "request-1", Title: "Change", Prompt: "Implement"}
	source := pebblestore.ProjectTaskSource{WorkspaceID: "workspace-1", WorkspaceGeneration: 2, Path: "/repo/one", Provenance: "explicit"}
	first, err := projectTaskSubmissionHash("project", input, source)
	if err != nil {
		t.Fatal(err)
	}
	input.ClientRequestID = "request-2"
	again, err := projectTaskSubmissionHash("project", input, source)
	if err != nil || first != again {
		t.Fatalf("request transport changed contract: %q %q %v", first, again, err)
	}
	input.Prompt = "Different"
	changed, err := projectTaskSubmissionHash("project", input, source)
	if err != nil || changed == first {
		t.Fatalf("changed prompt reused reservation: %q %v", changed, err)
	}
	input.Prompt = "Implement"
	source.Path = "/repo/two"
	changed, err = projectTaskSubmissionHash("project", input, source)
	if err != nil || changed == first {
		t.Fatalf("changed target reused reservation: %q %v", changed, err)
	}
}

// Purpose: an approved Coder can run only in the owned allocated worktree of
// the exact task and account, with its original source distinct from runtime.
// Threat: stale or forged session lineage could execute a task in another repo.
// verifyProjectTaskSession is the narrowest pre-run authority check.
func TestVerifyProjectTaskSessionRejectsMismatchedLineage(t *testing.T) {
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", SessionID: "session", Agent: "coder", WorkspacePath: "/runtime", SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: "workspace", WorkspaceGeneration: 2, Path: "/source", Provenance: "explicit"}}
	session := pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "account", WorkspacePath: "/runtime", WorktreeRootPath: "/runtime", WorktreeEnabled: true, Metadata: map[string]any{"project_id": "project", "task_id": "task", "swarm_v3_source_workspace_path": "/source", "swarm_v3_source_workspace_id": "workspace", "swarm_v3_source_workspace_generation": float64(2), "swarm_v3_runtime_workspace_path": "/runtime", "swarm_v3_worktree_owner_session_id": "session"}}
	if err := verifyProjectTaskSession(task, session, "account"); err != nil {
		t.Fatal(err)
	}
	for name, corrupt := range map[string]func(*pebblestore.SessionSnapshot){
		"owner":   func(s *pebblestore.SessionSnapshot) { s.Metadata["swarm_v3_worktree_owner_session_id"] = "other" },
		"source":  func(s *pebblestore.SessionSnapshot) { s.Metadata["swarm_v3_source_workspace_path"] = "/other" },
		"runtime": func(s *pebblestore.SessionSnapshot) { s.WorktreeRootPath = "/other" },
		"account": func(s *pebblestore.SessionSnapshot) { s.AccountScopeID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := session
			copy.Metadata = make(map[string]any)
			for k, v := range session.Metadata {
				copy.Metadata[k] = v
			}
			corrupt(&copy)
			if err := verifyProjectTaskSession(task, copy, "account"); err == nil || !strings.Contains(err.Error(), "reservation") {
				t.Fatalf("accepted unrelated session: %v", err)
			}
		})
	}
}

// Purpose: execution must bind the account-scoped catalog
// root and generation, never infer the first of multiple repositories. Threat:
// stale identity, an ambiguous omission, or a symlink escapes source authority.
// resolveProjectTaskSource is the narrowest pre-allocation boundary.
func TestResolveProjectTaskSourceRejectsAmbiguousAndStaleCatalog(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	root := t.TempDir()
	one, two := filepath.Join(root, "one"), filepath.Join(root, "two")
	for _, path := range []string{one, two} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "base"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = path
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
	}
	catalog := pebblestore.NewWorkspaceStore(f.db)
	first, err := catalog.AddForAccount(f.accountID, one, "one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.AddForAccount(f.accountID, two, "two")
	if err != nil {
		t.Fatal(err)
	}
	f.server.workspace = workspace.NewService(catalog)
	proj := &pebblestore.ProjectRecord{ID: "project", Name: "Project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: first.WorkspaceID, Path: one}, {WorkspaceID: second.WorkspaceID, Path: two}}}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: f.userID, AccountScopeID: f.accountID}
	if _, err := f.server.resolveProjectTaskSource(p, proj, "", "", 0, true); err == nil {
		t.Fatal("ambiguous omission selected a workspace")
	}
	source, err := f.server.resolveProjectTaskSource(p, proj, two, second.WorkspaceID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if source.Path != two || source.WorkspaceID != second.WorkspaceID || source.WorkspaceGeneration <= 0 {
		t.Fatalf("wrong target: %+v", source)
	}
	for name, candidate := range map[string]struct {
		path, id   string
		generation int64
	}{
		"stale id": {two, first.WorkspaceID, 0}, "stale generation": {two, second.WorkspaceID, source.WorkspaceGeneration + 1}, "unknown": {filepath.Join(root, "other"), "", 0},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.server.resolveProjectTaskSource(p, proj, candidate.path, candidate.id, candidate.generation, true); err == nil {
				t.Fatal("invalid target accepted")
			}
		})
	}
	other := p
	other.AccountScopeID = "other-account"
	if _, err := f.server.resolveProjectTaskSource(other, proj, two, second.WorkspaceID, 0, true); err == nil {
		t.Fatal("cross-account target accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(two, link); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.resolveProjectTaskSource(p, proj, link, "", 0, true); err == nil {
		t.Fatal("symlink alias accepted")
	}
	// Model availability survives stale workspace diagnostics; execution still rejects them.
	if err := f.server.sessions.Store().PutProject(f.accountID, proj); err != nil {
		t.Fatal(err)
	}
	w := f.callAPI("POST", "/project/tasks:preview", map[string]any{"prompt": "Implement", "agent": "coder", "workspace_path": two, "workspace_id": second.WorkspaceID, "workspace_generation": source.WorkspaceGeneration + 1}, p)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "source workspace generation is stale") || !strings.Contains(w.Body.String(), "model_preview") {
		t.Fatalf("workspace diagnostic suppressed model preview: %d %s", w.Code, w.Body.String())
	}
	w = f.callAPI("POST", "/project/tasks:preview", map[string]any{"prompt": "Implement", "agent": "coder"}, p)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "model_preview") {
		t.Fatalf("ambiguous project suppressed model preview: %d %s", w.Code, w.Body.String())
	}
	w = f.callAPI("POST", "/project/tasks:preview", map[string]any{"prompt": "Implement", "agent": "coder", "workspace_path": filepath.Join(root, "unknown")}, p)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "workspace_diagnostic") || !strings.Contains(w.Body.String(), "model_preview") {
		t.Fatalf("invalid workspace suppressed model preview: %d %s", w.Code, w.Body.String())
	}
}
