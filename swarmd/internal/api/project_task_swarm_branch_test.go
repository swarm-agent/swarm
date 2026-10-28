package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

type swarmBranchFixture struct {
	server   *Server
	sessions *sessionruntime.Service
	project  *pebblestore.ProjectRecord
	folder   string
}

// newSwarmBranchFixture registers folder as the only workspace of a project
// with the real workspace and worktree services, as daemon startup does.
func newSwarmBranchFixture(t *testing.T, folder string) swarmBranchFixture {
	t.Helper()
	server, sessions, _, _, _ := newRoutedSessionTestServerAt(t, filepath.Join(t.TempDir(), "swarm-branch.pebble"), false)
	store := sessions.Store().Underlying()
	principal := testPrincipal()
	ids := pebblestore.NewIdentityStore(store)
	if _, err := ids.PutUser(principal.User); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountScope(principal.AccountScope); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountUser(principal.AccountUser); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	workspaces.SetLocalBindingService(server.topology)
	server.workspace = workspaces
	server.SetWorktreeService(worktree.NewService(pebblestore.NewWorktreeStore(store), workspaces, nil))
	for ready := false; !ready; {
		var err error
		if ready, err = sessions.Store().BackfillRepositoryHistory(100); err != nil {
			t.Fatal(err)
		}
	}
	// A Git repository is added as a workspace; any other folder is
	// registered in place, as swarm_create_project does for the person's
	// existing folders.
	var added workspace.Resolution
	var err error
	if _, statErr := os.Stat(filepath.Join(folder, ".git")); statErr == nil {
		added, err = workspaces.AddForPrincipal(principal, folder, "tally", "", false)
	} else {
		added, err = workspaces.RegisterExistingFolderForPrincipal(principal, folder, "tally", "")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.topology.EnsureLocalSelfPlacementForPrincipal(principal.AccountScopeID, principal.UserID); err != nil {
		t.Fatal(err)
	}
	entry, found, err := workspaces.GetByWorkspaceIDForPrincipal(principal, added.WorkspaceID)
	if err != nil || !found {
		t.Fatal(err)
	}
	if _, err := server.topology.EnsureLocalWorkspaceSelfBindingForPrincipal(principal.AccountScopeID, principal.UserID, entry); err != nil {
		t.Fatal(err)
	}
	project := &pebblestore.ProjectRecord{ID: "proj-tally", Name: "Tally", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: added.WorkspacePath, Role: "primary_code"}}}
	if err := sessions.Store().PutProject(principal.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	return swarmBranchFixture{server: server, sessions: sessions, project: project, folder: added.WorkspacePath}
}

// runSwarmTask creates a swarm-role task (swarm_run_task's default role) and
// returns its stored record.
func (f swarmBranchFixture) runSwarmTask(t *testing.T, id string) *pebblestore.ProjectTaskRecord {
	t.Helper()
	p := testPrincipal()
	created, err := f.server.CreateProjectTask(context.Background(), p, f.project.ID, tool.ProjectTaskCreateInput{
		ID: id, Title: "Add a --csv flag", Description: "Add a --csv flag to tally.sh.", Agent: "swarm", WorkspacePath: f.folder,
		AutoApprove: true, ClientRequestID: "swarm-branch-" + id,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, found, err := f.sessions.Store().GetProjectTask(p.AccountScopeID, f.project.ID, created.ID)
	if err != nil || !found {
		t.Fatalf("task %s: %v %v", created.ID, found, err)
	}
	return task
}

func swarmBranchRepo(t *testing.T) (string, string) {
	t.Helper()
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(folder, "tally.sh"), []byte("#!/bin/sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, folder, "add", "tally.sh")
	outboxPinGit(t, folder, "commit", "-q", "-m", "start")
	return folder, outboxPinGit(t, folder, "rev-parse", "HEAD")
}

// assertSourceUntouched checks the person's checkout: still on main at head,
// with nothing uncommitted (no file the task wrote).
func assertSourceUntouched(t *testing.T, folder, head string) {
	t.Helper()
	if branch := outboxPinGit(t, folder, "symbolic-ref", "--short", "HEAD"); branch != "main" {
		t.Fatalf("source branch moved to %q", branch)
	}
	if got := outboxPinGit(t, folder, "rev-parse", "HEAD"); got != head {
		t.Fatalf("source head moved to %s, want %s", got, head)
	}
	if status := outboxPinGit(t, folder, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("source checkout has changes: %q", status)
	}
}

// Purpose: a swarm-role project task (swarm_run_task without a role) in a Git
// repository on a branch must work on its own worktree and agent branch, as a
// coder task does, so its edits never land in the person's folder and only its
// commits reach it, through IntegrateProjectTask. Threat: the v0.1.59 bug, where
// the task wrote straight into the folder on main, left the files uncommitted,
// and blocked every later delivery into that folder. Owners: the project-task
// session create in projects.go (swarmTaskForksBranch, the worktree allocator,
// admission evidence and the seed's swarmTaskOwnBranchContext) and
// IntegrateProjectTask. This uses the real store, workspace and worktree
// services and real Git (no provider): the narrowest layer that allocates the
// lane and merges it. The run the task enqueues has no provider here.
func TestSwarmProjectTaskWorksOnItsOwnBranch(t *testing.T) {
	t.Run("committed work is integrated, source untouched until then", func(t *testing.T) {
		folder, head := swarmBranchRepo(t)
		f := newSwarmBranchFixture(t, folder)
		task := f.runSwarmTask(t, "task_swarm_commit")
		if !strings.HasPrefix(task.WorktreeBranch, "agent/") || task.BaseBranch != "main" || task.BaseCommit != head || task.WorkspacePath == folder || task.WorkspacePath == "" || task.SourceWorkspace.Path != folder {
			t.Fatalf("swarm task lane: branch %q base %q@%s path %q source %q", task.WorktreeBranch, task.BaseBranch, task.BaseCommit, task.WorkspacePath, task.SourceWorkspace.Path)
		}
		if folderTask(task) {
			t.Fatal("swarm task in a Git repository is a folder task")
		}
		session, found, err := f.sessions.Store().GetSession(task.SessionID)
		if err != nil || !found || !session.WorktreeEnabled || session.WorktreeRootPath != task.WorkspacePath || session.WorktreeBranch != task.WorktreeBranch || session.WorktreeBaseBranch != "main" || session.Metadata["base_commit"] != head || session.Metadata["swarm_v3_source_workspace_path"] != folder {
			t.Fatalf("swarm session is not bound to its own worktree: %+v %v", session, err)
		}
		if got := outboxPinGit(t, task.WorkspacePath, "symbolic-ref", "--short", "HEAD"); got != task.WorktreeBranch {
			t.Fatalf("worktree on %q, want %q", got, task.WorktreeBranch)
		}
		messages, err := f.sessions.Store().ListMessages(task.SessionID, 0, 10)
		if err != nil || len(messages) == 0 {
			t.Fatalf("seed: %v %v", messages, err)
		}
		seed := messages[0].Content
		if !strings.Contains(seed, "You work on branch "+task.WorktreeBranch+" in your own worktree") || !strings.Contains(seed, "git_add and git_commit") || !strings.Contains(seed, "Do not touch the source folder ("+folder+")") {
			t.Fatalf("seed lacks the own-branch line:\n%s", seed)
		}

		// The task writes and commits in its worktree.
		if err := os.WriteFile(filepath.Join(task.WorkspacePath, "tally.sh"), []byte("#!/bin/sh\n# --csv\n"), 0600); err != nil {
			t.Fatal(err)
		}
		outboxPinGit(t, task.WorkspacePath, "add", "tally.sh")
		outboxPinGit(t, task.WorkspacePath, "commit", "-q", "-m", "Add --csv")
		commit := outboxPinGit(t, task.WorkspacePath, "rev-parse", "HEAD")
		assertSourceUntouched(t, folder, head)

		p := testPrincipal()
		result, err := f.server.IntegrateProjectTask(context.Background(), p, f.project.ID, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result["status"] != "integrated" {
			t.Fatalf("integrate result: %#v", result)
		}
		outboxPinGit(t, folder, "merge-base", "--is-ancestor", commit, "HEAD")
		if branch := outboxPinGit(t, folder, "symbolic-ref", "--short", "HEAD"); branch != "main" {
			t.Fatalf("source branch after integration %q", branch)
		}
		if data, err := os.ReadFile(filepath.Join(folder, "tally.sh")); err != nil || !strings.Contains(string(data), "--csv") {
			t.Fatalf("integrated file: %q %v", data, err)
		}
	})

	t.Run("uncommitted work is refused, source stays clean", func(t *testing.T) {
		folder, head := swarmBranchRepo(t)
		f := newSwarmBranchFixture(t, folder)
		task := f.runSwarmTask(t, "task_swarm_dirty")
		if task.WorktreeBranch == "" || task.WorkspacePath == folder {
			t.Fatalf("swarm task has no lane: %+v", task)
		}
		if err := os.WriteFile(filepath.Join(task.WorkspacePath, "README.md"), []byte("csv\n"), 0600); err != nil {
			t.Fatal(err)
		}
		state := inspectTaskGitState(*task, f.sessions.Store())
		a := state.deliveryAssessment
		if a == nil || a.State != "empty" || !a.SourceDirty {
			t.Fatalf("assessment: %+v", a)
		}
		p := testPrincipal()
		_, err := f.server.IntegrateProjectTask(context.Background(), p, f.project.ID, task.ID)
		if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
			t.Fatalf("integrate of uncommitted work: %v", err)
		}
		current, _, err := f.sessions.Store().GetProjectTask(p.AccountScopeID, f.project.ID, task.ID)
		if err != nil || (current.Integration != nil && current.Integration.State == "integrated") {
			t.Fatalf("refusal recorded an integration: %+v %v", current.Integration, err)
		}
		assertSourceUntouched(t, folder, head)
		if _, err := os.Stat(filepath.Join(folder, "README.md")); !os.IsNotExist(err) {
			t.Fatalf("uncommitted task file reached the source folder: %v", err)
		}
	})
}

// Purpose: a swarm task works in the folder itself only where it has no branch
// to fork from, and then its result must say so plainly rather than look like
// branch work. A folder that is not a Git repository is refused before any
// session exists (resolveProjectTaskSource: not coding-ready), so nothing is
// written there; a repository on a detached HEAD has no branch, so the task
// stays a folder task and swarm_task_result names why. Threat: inventing a
// lane, writing into a non-repository, or a silent in-folder result. Owners:
// resolveProjectTaskSource, swarmTaskForksBranch, captureFolderTaskBase and
// swarmFolderTaskNote via controlMCPTaskResult; the real create path and MCP
// result route are the narrowest layer covering both.
func TestSwarmProjectTaskWithoutABranchStaysAFolderTask(t *testing.T) {
	t.Run("not a Git repository", func(t *testing.T) {
		folder := t.TempDir()
		if err := os.WriteFile(filepath.Join(folder, "tally.sh"), []byte("#!/bin/sh\n"), 0600); err != nil {
			t.Fatal(err)
		}
		f := newSwarmBranchFixture(t, folder)
		_, err := f.server.CreateProjectTask(context.Background(), testPrincipal(), f.project.ID, tool.ProjectTaskCreateInput{
			ID: "task_swarm_plain", Title: "Add a --csv flag", Description: "Add a --csv flag to tally.sh.", Agent: "swarm", WorkspacePath: f.folder,
			AutoApprove: true, ClientRequestID: "swarm-branch-plain",
		})
		if err == nil || !strings.Contains(err.Error(), "not coding-ready") {
			t.Fatalf("non-Git swarm task: %v", err)
		}
		entries, readErr := os.ReadDir(folder)
		if readErr != nil || len(entries) != 1 || entries[0].Name() != "tally.sh" {
			t.Fatalf("non-Git folder changed: %v %v", entries, readErr)
		}
	})

	t.Run("detached HEAD", func(t *testing.T) {
		folder, head := swarmBranchRepo(t)
		outboxPinGit(t, folder, "checkout", "-q", "--detach")
		f := newSwarmBranchFixture(t, folder)
		task := f.runSwarmTask(t, "task_swarm_detached")
		if task.WorktreeBranch != "" || task.WorktreeName != "" || task.BaseCommit != "" || task.WorkspacePath != folder || !folderTask(task) {
			t.Fatalf("detached swarm task: %+v", task)
		}
		session, found, err := f.sessions.Store().GetSession(task.SessionID)
		if err != nil || !found || session.WorktreeEnabled {
			t.Fatalf("detached swarm session: %+v %v", session, err)
		}
		messages, err := f.sessions.Store().ListMessages(task.SessionID, 0, 10)
		if err != nil || len(messages) == 0 || strings.Contains(messages[0].Content, "## Your branch") {
			t.Fatalf("detached seed: %v %v", messages, err)
		}
		if got := outboxPinGit(t, folder, "rev-parse", "HEAD"); got != head {
			t.Fatalf("folder head moved: %s", got)
		}
		req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
			&pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read"}})
		call := &controlMCPCall{request: req, next: f.server.internalRouteHandler(), server: f.server}
		result, err := controlMCPTaskResult(call, map[string]any{"project_id": f.project.ID, "task_id": task.ID})
		if err != nil {
			t.Fatal(err)
		}
		note, _ := controlMCPMap(result)["worked_in_folder"].(string)
		if !strings.Contains(note, "done in the folder itself") || !strings.Contains(note, "not a Git repository") {
			t.Fatalf("result does not say the work was done in the folder: %+v", result)
		}
	})

	// Finder and designer folder tasks and lane tasks carry no such note.
	if swarmFolderTaskNote("designer", "", "", "/f", "/f") != "" || swarmFolderTaskNote("finder", "", "", "/f", "/f") != "" || swarmFolderTaskNote("swarm", "agent/x", "", "/wt", "/f") != "" || swarmFolderTaskNote("swarm", "", "abc", "/f", "/f") != "" {
		t.Fatal("note applied outside a swarm folder task without a base")
	}
}
