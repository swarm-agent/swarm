package api

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// Purpose: a coding task proposed without workspace_path or workspace_id
// runs in the project's folder when the project has exactly one; when it
// has several, the proposal is refused naming each folder by path (and no
// task is created), and naming one of them is accepted. The default is the
// project's own folder, bound and checked as if named; it never guesses
// among several.
// Threat (GPUI-vs-MCP parity rerun): Swarm Control's propose_task refused
// every coding task without workspace_path ("coding task source required")
// even for a project with one folder, and the refusal did not say which
// folders there were, so an MCP caller had to look them up and retry.
// Boundary: Server.CreateProjectTask's source resolution
// (defaultProjectTaskSource, resolveProjectTaskSource), reached through
// Swarm Control's manage_projects propose_task over the real link to the
// machine whose project it is.
// Narrowest layer: the service decides the source for both surfaces; the
// Swarm Control call proves the caller's path reaches it without a
// workspace_path being filled in on the way.
func TestProposeTaskWithoutFolderUsesTheProjectsOnlyFolder(t *testing.T) {
	h := newRemoteWaitHarness(t)
	h.createGoal(t, "home-orch", map[string]any{})
	h.runtime.SetManageProjectStore(h.home.s.sessions.Store())
	h.remote.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(h.remote.s.sessions.Store().Underlying()))
	// A coding task starts in a worktree of its folder, as on a daemon.
	h.remote.s.SetWorktreeService(worktree.NewService(pebblestore.NewWorktreeStore(h.remote.s.sessions.Store().Underlying()), h.remote.s.workspace, nil))
	for ready := false; !ready; {
		var err error
		if ready, err = h.remote.s.sessions.Store().BackfillRepositoryHistory(100); err != nil {
			t.Fatal(err)
		}
	}
	owner := identity.Principal{Type: identity.PrincipalTypeUser, UserID: h.remote.user, AccountScopeID: h.remote.account}
	folder := func(name string) pebblestore.ProjectWorkspaceRef {
		t.Helper()
		dir := t.TempDir()
		outboxPinGit(t, dir, "init", "-q", "-b", "dev")
		outboxPinGit(t, dir, "commit", "-q", "--allow-empty", "-m", "start")
		added, err := h.remote.s.workspace.AddForPrincipal(owner, dir, name, "", false)
		if err != nil {
			t.Fatal(err)
		}
		return pebblestore.ProjectWorkspaceRef{WorkspaceID: added.WorkspaceID, Path: dir, Role: "primary_code"}
	}
	store := h.remote.s.sessions.Store()
	one := folder("one")
	if err := store.PutProject(h.remote.account, &pebblestore.ProjectRecord{ID: "remote-project", Name: "Remote project", Workspaces: []pebblestore.ProjectWorkspaceRef{one}}); err != nil {
		t.Fatal(err)
	}
	h.home.s.SetToolCatalogRuntime(h.runtime)
	parity := &catalogParityHarness{remoteWaitHarness: h}
	propose := func(extra map[string]any) (string, string) {
		t.Helper()
		args := map[string]any{"action": "propose_task", "machine": "box", "project_id": "remote-project", "title": "Fix it", "agent": "coder", "prompt": "Fix the bug."}
		for key, value := range extra {
			args[key] = value
		}
		return parity.control(t, catalogParityFullKey, args)
	}
	tasks := func() int {
		t.Helper()
		list, err := store.ListProjectTasks(h.remote.account, "remote-project", 100)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}

	// One folder: accepted, bound to that folder.
	out, errText := propose(nil)
	if errText != "" {
		t.Fatalf("one-folder project refused a propose_task without workspace_path: %s", errText)
	}
	id, _ := envelopeDataMap(parityDecode(t, out))["linked_task_id"].(string)
	task, found, err := store.GetProjectTask(h.remote.account, "remote-project", id)
	if err != nil || !found || task.SourceWorkspace.Path != one.Path || task.SourceWorkspace.WorkspaceID != one.WorkspaceID || task.SourceWorkspace.Provenance != "unique_project_workspace" {
		t.Fatalf("task not bound to the project's only folder: found=%v err=%v source=%+v (%s)", found, err, task, out)
	}

	// Two folders: refused, naming both by path, and nothing is created.
	two := folder("two")
	if err := store.PutProject(h.remote.account, &pebblestore.ProjectRecord{ID: "remote-project", Name: "Remote project", Workspaces: []pebblestore.ProjectWorkspaceRef{one, two}}); err != nil {
		t.Fatal(err)
	}
	before := tasks()
	if _, errText := propose(nil); !strings.Contains(errText, "workspace_path") || !strings.Contains(errText, one.Path) || !strings.Contains(errText, two.Path) {
		t.Fatalf("two-folder refusal does not name both folders: %q", errText)
	}
	if after := tasks(); after != before {
		t.Fatalf("a refused proposal created a task: %d -> %d", before, after)
	}
	// Naming one of them is accepted, bound to the one named.
	out, errText = propose(map[string]any{"workspace_path": two.Path})
	if errText != "" {
		t.Fatalf("a named folder was refused: %s", errText)
	}
	id, _ = envelopeDataMap(parityDecode(t, out))["linked_task_id"].(string)
	if task, found, err := store.GetProjectTask(h.remote.account, "remote-project", id); err != nil || !found || task.SourceWorkspace.Path != two.Path {
		t.Fatalf("named folder not bound: found=%v err=%v %+v", found, err, task)
	}
}
