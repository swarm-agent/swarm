package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const (
	retainedTaskProject = "retained-project"
	retainedTaskID      = "retained-task"
	retainedTaskSession = "retained-child"
	retainedTaskBranch  = "agent/retained-task"
	// The repository's own Git identity: what the task's agent commits with
	// (git_commit runs plain `git commit` with identity overrides removed).
	retainedTaskAuthor = "Task Agent <task-agent@example.invalid>"
)

// retainedTask is a finished project task whose attempt ran in a retained
// task worktree (child) of a real Git source on dev, as the task allocator
// leaves it: an owned child session, its worktree admission, a bound source
// workspace, the project and the task record at status.
type retainedTask struct {
	p                    identity.Principal
	source, child, base  string
	projectID, taskID    string
	sessionID, branch    string
	initialTaskRevision  int
	initialAttemptsCount int
}

// seedRetainedTask builds that task on server for principal p under root.
// Shared by the follow-up tests that need the canonical reopen authority
// (ReopenProjectTask) to run for real: catalog, topology binding, worktree
// service and task store. The base commit holds tracked.txt.
func seedRetainedTask(t *testing.T, server *Server, sessions *sessionruntime.Service, p identity.Principal, root, status string) retainedTask {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	// Task worktrees live at the allocator's own path for their branch.
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	integrationGit(t, source, "init", "-b", "dev")
	integrationGit(t, source, "config", "user.name", "Task Agent")
	integrationGit(t, source, "config", "user.email", "task-agent@example.invalid")
	integrationGit(t, source, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	integrationGit(t, source, "add", "tracked.txt")
	integrationGit(t, source, "commit", "-m", "base")
	base := integrationGit(t, source, "rev-parse", "HEAD")
	laneParent, err := appstorage.WorktreeDataDir(source)
	if err != nil {
		t.Fatal(err)
	}
	laneID, err := worktreeruntime.WorkspaceIdentityForRequestedBranch(retainedTaskBranch)
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(laneParent, laneID)
	integrationGit(t, source, "worktree", "add", "-b", retainedTaskBranch, child)

	db := sessions.Store()
	catalogDB, err := pebblestore.Open(filepath.Join(root, "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalogDB.Close() })
	server.workspace = workspaceruntime.NewService(pebblestore.NewWorkspaceStore(catalogDB))
	if _, _, err := pebblestore.NewWorkspaceStore(catalogDB).CreateForAccountIfAbsent(p.AccountScopeID, source, "source", ""); err != nil {
		t.Fatal(err)
	}
	proj := &pebblestore.ProjectRecord{ID: retainedTaskProject, AccountID: p.AccountScopeID, Name: "Retained", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: source}}}
	if err := db.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatal(err)
	}
	// The account's local self runtime and placement, as the first session
	// on this machine records them.
	now := time.Now().UnixMilli()
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: "host-swarm-id", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Name: "host-swarm", Role: "master", Relationship: "self", Status: "online", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.topology.EnsureLocalSelfPlacementForPrincipal(p.AccountScopeID, p.UserID); err != nil {
		t.Fatal(err)
	}
	bound, err := server.resolveProjectTaskSource(p, proj, source, "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID: "binding-retained-task-source", UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		SourceWorkspaceID: bound.WorkspaceID, SourceWorkspaceGeneration: bound.WorkspaceGeneration, SourceWorkspacePath: source, SourceWorkspaceName: "source",
		DestinationRuntimeSwarmID: "host-swarm-id", DestinationAuthorityHostSwarmID: "host-swarm-id", DestinationHostSwarmID: "host-swarm-id",
		DestinationRuntimeKind: pebblestore.TopologyRuntimeKindHost, DestinationWorkspacePath: source, PlacementGeneration: 1, BindingGeneration: 1,
		State: pebblestore.TopologyWorkspaceBindingStateBound, AccessMode: pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind: pebblestore.TopologyWorkspaceBindingMaterializationSource, AttestedByHostSwarmID: "host-swarm-id", Writable: true,
	}); err != nil {
		t.Fatal(err)
	}
	session := pebblestore.SessionSnapshot{ID: retainedTaskSession, AccountScopeID: p.AccountScopeID, UserID: p.UserID, WorkspacePath: child, WorktreeEnabled: true, WorktreeRootPath: child, WorktreeBranch: retainedTaskBranch, WorktreeBaseBranch: "dev", Metadata: map[string]any{"swarm_v3_source_workspace_path": source, "base_commit": base, "project_id": proj.ID, "task_id": retainedTaskID, "swarm_v3_source_workspace_id": bound.WorkspaceID, "swarm_v3_source_workspace_generation": bound.WorkspaceGeneration, "swarm_v3_worktree_owner_session_id": retainedTaskSession, "swarm_v3_runtime_workspace_path": child}}
	if err := db.CreateSessionForAccount(session, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "legacy", AllocatedRuntimeRoot: true, Path: child, SourcePath: source, OwnerSessionID: session.ID, Branch: session.WorktreeBranch}, Kind: sessionruntime.SessionMutationUpdateMetadata, SessionID: session.ID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, ClientRequestID: "create-retained-child", PayloadHash: "create-retained-child", Session: &session}); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: retainedTaskID, ProjectID: proj.ID, AccountID: p.AccountScopeID, Title: "Retained", Agent: "swarm", Status: status, SessionID: session.ID, WorkspacePath: child, WorktreeBranch: retainedTaskBranch, BaseBranch: "dev", BaseCommit: base, SourceWorkspace: bound}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	server.worktrees = &worktreeruntime.Service{}
	stored, _, err := db.GetProjectTask(p.AccountScopeID, proj.ID, retainedTaskID)
	if err != nil || stored == nil {
		t.Fatalf("seeded task: %v", err)
	}
	stored.EnsureTaskAttempts()
	return retainedTask{p: p, source: source, child: child, base: base, projectID: proj.ID, taskID: retainedTaskID, sessionID: session.ID, branch: retainedTaskBranch, initialTaskRevision: stored.Revision, initialAttemptsCount: len(stored.Attempts)}
}

// task reads the task record as stored.
func (r retainedTask) task(t *testing.T, server *Server) *pebblestore.ProjectTaskRecord {
	t.Helper()
	task, found, err := server.sessions.Store().GetProjectTask(r.p.AccountScopeID, r.projectID, r.taskID)
	if err != nil || !found || task == nil {
		t.Fatalf("task: %v %v", found, err)
	}
	task.EnsureTaskAttempts()
	return task
}
