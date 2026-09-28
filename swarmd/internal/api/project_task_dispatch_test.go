package api

import (
 "context"
 "os"
 "os/exec"
 "path/filepath"
 "strings"
 "sync"
 "testing"

 agentruntime "swarm/packages/swarmd/internal/agent"
 "swarm/packages/swarmd/internal/agentmodelsettings"
 "swarm/packages/swarmd/internal/identity"
 "swarm/packages/swarmd/internal/model"
 sessionruntime "swarm/packages/swarmd/internal/session"
 "swarm/packages/swarmd/internal/workspace"
 pebblestore "swarm/packages/swarmd/internal/store/pebble"
 "swarm/packages/swarmd/internal/tool"
 worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: CreateProjectTask and ApproveProjectTask must carry the explicit product
// repository through the catalog, worktree allocation, grants, seed, and run.
// Threat: a coordination-first project silently dispatches Coder into the first
// workspace. This temp-catalog/real-Git API test is the narrowest end-to-end
// boundary that observes the actual allocated worktree and persisted identity.
func TestProjectTaskDispatchCoordinationFirstProductTarget(t *testing.T) {
 t.Setenv("XDG_DATA_HOME", t.TempDir())
 t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
 t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
 f := setupMatrixTestFixture(t)
 defer f.db.Close()
 projectID := f.createProject(t)
 p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: f.userID, AccountScopeID: f.accountID}
 proj, _, err := f.server.sessions.Store().GetProject(f.accountID, projectID)
 if err != nil { t.Fatal(err) }
 coordination := proj.Workspaces[0].Path
 product := filepath.Join(f.dir, "product")
 if err := os.Mkdir(product, 0700); err != nil { t.Fatal(err) }
 for _, args := range [][]string{{"init"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "base"}} {
  cmd := exec.Command("git", args...); cmd.Dir = product
  if output, err := cmd.CombinedOutput(); err != nil { t.Fatalf("git %v: %v %s", args, err, output) }
 }
 entry, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(f.accountID, product, "product")
 if err != nil { t.Fatal(err) }
 proj.Workspaces = append(proj.Workspaces, pebblestore.ProjectWorkspaceRef{WorkspaceID:entry.WorkspaceID, Path:product, Role:"primary_code"})
 if err := f.server.sessions.Store().PutProject(f.accountID, proj); err != nil { t.Fatal(err) }
 f.server.worktrees = worktreeruntime.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
 // An ambiguous submission must not reserve a task or allocate a worktree.
 if _, err := f.server.CreateProjectTask(context.Background(), p, projectID, tool.ProjectTaskCreateInput{ID:"ambiguous", Title:"Implement", Prompt:"Change product code", Agent:"coder"}); err == nil || !strings.Contains(err.Error(), "execution target unresolved") { t.Fatalf("ambiguous target: %v", err) }
 if _, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, projectID, "ambiguous"); err != nil || found { t.Fatalf("ambiguous task reserved: %v", err) }
 input := tool.ProjectTaskCreateInput{ID:"product-task", Title:"Implement product change", Prompt:"Implement product change", Agent:"coder", WorkspacePath:product, WorkspaceID:entry.WorkspaceID}
 task, err := f.server.CreateProjectTask(context.Background(), p, projectID, input)
 if err != nil { t.Fatal(err) }
 if task.SourceWorkspace.Path != product || task.SourceWorkspace.WorkspaceID != entry.WorkspaceID || task.SourceWorkspace.WorkspaceGeneration <= 0 || task.SourceWorkspace.Provenance != "explicit" { t.Fatalf("source binding: %+v", task.SourceWorkspace) }
 if task.WorkspacePath == coordination || task.WorkspacePath == product || task.SessionID == "" { t.Fatalf("no isolated product execution: %+v", task) }
 owned, found, err := f.server.sessions.Store().GetSession(task.SessionID)
 if err != nil || !found { t.Fatalf("allocated session: %v", err) }
 if !owned.WorktreeEnabled || owned.WorktreeRootPath != task.WorkspacePath || owned.WorkspacePath != task.WorkspacePath || owned.Metadata["swarm_v3_source_workspace_path"] != product || owned.Metadata["swarm_v3_source_workspace_id"] != entry.WorkspaceID || owned.Metadata["swarm_v3_runtime_workspace_path"] != task.WorkspacePath || owned.Metadata["swarm_v3_worktree_owner_session_id"] != task.SessionID { t.Fatalf("inconsistent allocated session: %+v", owned) }
 if _, err := os.Stat(filepath.Join(task.WorkspacePath, ".git")); err != nil { t.Fatalf("missing allocated Git worktree: %v", err) }
 sourceGrant, runtimeGrant := false, false
 for _, grant := range owned.WorkspaceGrants {
  if grant.Kind == pebblestore.WorkspaceGrantPrimary && grant.Path == product && grant.WorkspaceID == entry.WorkspaceID { sourceGrant = true }
  if grant.Kind == pebblestore.WorkspaceGrantWorktree && grant.Path == task.WorkspacePath { runtimeGrant = true }
  if grant.Path == coordination { t.Fatalf("coordination workspace granted to product Coder: %+v", grant) }
 }
 if !sourceGrant || !runtimeGrant { t.Fatalf("missing product source/runtime grants: %+v", owned.WorkspaceGrants) }
 messages, err := f.server.sessions.Store().ListMessages(task.SessionID, 0, 100)
 if err != nil || len(messages) != 1 || messages[0].Metadata["role"] != "project_context_seed" || messages[0].Metadata["task_id"] != task.ID || !strings.Contains(messages[0].Content, product) { t.Fatalf("product seed missing: %+v %v", messages, err) }
 before, err := f.server.sessions.Store().ListRunIntents(task.SessionID, 10)
 if err != nil || len(before) != 0 { t.Fatalf("unapproved task ran: %+v %v", before, err) }
 approved, err := f.server.ApproveProjectTask(context.Background(), p, projectID, task.ID)
 if err != nil { t.Fatal(err) }
 if approved.SessionID != task.SessionID || approved.WorkspacePath != task.WorkspacePath || approved.SourceWorkspace.Path != product { t.Fatalf("approval changed owner: %+v", approved) }
 intents, err := f.server.sessions.Store().ListRunIntents(task.SessionID, 10)
 if err != nil || len(intents) != 1 || intents[0].RunID != "desktop-v3-run:task-"+task.ID { t.Fatalf("wrong run owner: %+v %v", intents, err) }
}

// Purpose: repeated submissions of one task ID must reuse the reserved session,
// while independent IDs with identical prompts remain independent. Threat:
// concurrent API retries or approvals allocate duplicate children and runs.
// CreateProjectTask/ApproveProjectTask plus the temp store are the narrowest
// boundaries proving the resulting postconditions, not only the hash helper.
func TestProjectTaskConcurrentRepeatedIdentityAndIndependentIDs(t *testing.T) {
 f := setupMatrixTestFixture(t)
 defer f.db.Close()
 projectID := f.createProject(t)
 p := identity.Principal{Type:identity.PrincipalTypeUser, UserID:f.userID, AccountScopeID:f.accountID}
 proj, _, err := f.server.sessions.Store().GetProject(f.accountID, projectID)
 if err != nil { t.Fatal(err) }
 source := proj.Workspaces[0]
 input := tool.ProjectTaskCreateInput{ID:"same-task", Title:"Fix product", Prompt:"Fix product", Agent:"coder", WorkspacePath:source.Path, WorkspaceID:source.WorkspaceID}
 const callers = 6
 var wg sync.WaitGroup
 results := make([]*pebblestore.ProjectTaskRecord, callers)
 errs := make([]error, callers)
 for i := range results { wg.Add(1); go func(i int) { defer wg.Done(); results[i], errs[i] = f.server.CreateProjectTask(context.Background(), p, projectID, input) }(i) }
 wg.Wait()
 for i := range results { if errs[i] != nil || results[i] == nil || results[i].SessionID != results[0].SessionID || results[i].WorkspacePath != results[0].WorkspacePath { t.Fatalf("retry %d changed owner: %+v %v", i, results[i], errs[i]) } }
 f.wt.mu.Lock(); allocations := f.wt.allocCalls; f.wt.mu.Unlock()
 if allocations != 1 { t.Fatalf("same ID allocated %d worktrees", allocations) }
 approvals := make([]error, callers)
 for i := range approvals { wg.Add(1); go func(i int) { defer wg.Done(); _, approvals[i] = f.server.ApproveProjectTask(context.Background(), p, projectID, input.ID) }(i) }
 wg.Wait()
 for i, err := range approvals { if err != nil { t.Fatalf("approval %d: %v", i, err) } }
 intents, err := f.server.sessions.Store().ListRunIntents(results[0].SessionID, 10)
 if err != nil || len(intents) != 1 { t.Fatalf("same task produced %d runs: %v", len(intents), err) }
 messages, err := f.server.sessions.Store().ListMessages(results[0].SessionID, 0, 100)
 if err != nil || len(messages) != 1 { t.Fatalf("same task produced %d seeds: %v", len(messages), err) }
 // Same prompt is not an idempotency key: a different task ID has its own run.
 input.ID = "other-task"
 other, err := f.server.CreateProjectTask(context.Background(), p, projectID, input)
 if err != nil { t.Fatal(err) }
 if other.SessionID == results[0].SessionID || other.WorkspacePath == results[0].WorkspacePath || other.WorktreeBranch == results[0].WorktreeBranch { t.Fatalf("distinct IDs collapsed: %+v %+v", results[0], other) }
 if _, err := f.server.ApproveProjectTask(context.Background(), p, projectID, other.ID); err != nil { t.Fatal(err) }
 otherRuns, err := f.server.sessions.Store().ListRunIntents(other.SessionID, 10)
 if err != nil || len(otherRuns) != 1 || otherRuns[0].RunID == intents[0].RunID { t.Fatalf("distinct run lost: %+v %v", otherRuns, err) }
 f.wt.mu.Lock(); allocations = f.wt.allocCalls; f.wt.mu.Unlock()
 if allocations != 2 { t.Fatalf("distinct tasks allocated %d worktrees", allocations) }
 input.ID = "same-task"; input.Prompt = "Changed payload"
 if _, err := f.server.CreateProjectTask(context.Background(), p, projectID, input); err == nil || !strings.Contains(err.Error(), "conflicts") { t.Fatalf("changed payload reused reservation: %v", err) }
 saved, _, err := f.server.sessions.Store().GetProjectTask(f.accountID, projectID, input.ID)
 if err != nil || saved.SessionID != results[0].SessionID || saved.SourceWorkspace.Path != source.Path { t.Fatalf("conflict changed owner: %+v %v", saved, err) }
 f.wt.mu.Lock(); allocations = f.wt.allocCalls; f.wt.mu.Unlock()
 if allocations != 2 { t.Fatalf("conflict allocated worktree: %d", allocations) }
}

// Purpose: interrupted reservation (without a session) and allocated session
// (before final task persist) must replay the same identity after Pebble reopen.
// Threat: a retry creates another worktree/run, or accepts changed payload.
// CreateProjectTask recovery over the reopened store is the narrowest durable
// layer proving both failure windows and no unauthorized partial replacement.
func TestProjectTaskInterruptedCreationReopensSameOwner(t *testing.T) {
 for _, failure := range []string{"reservation_without_session", "session_before_task_persist"} {
  t.Run(failure, func(t *testing.T) {
   f := setupMatrixTestFixture(t)
   projectID := f.createProject(t)
   p := identity.Principal{Type:identity.PrincipalTypeUser, UserID:f.userID, AccountScopeID:f.accountID}
   proj, _, err := f.server.sessions.Store().GetProject(f.accountID, projectID)
   if err != nil { t.Fatal(err) }
   source := proj.Workspaces[0]
   input := tool.ProjectTaskCreateInput{ID:"recover-task", Title:"Recover", Prompt:"Recover product task", Agent:"coder", WorkspacePath:source.Path, WorkspaceID:source.WorkspaceID}
   var original *pebblestore.ProjectTaskRecord
   if failure == "reservation_without_session" {
    f.wt.failAlloc = true
    if _, err := f.server.CreateProjectTask(context.Background(), p, projectID, input); err == nil || !strings.Contains(err.Error(), "worktree allocation failed") { t.Fatalf("expected allocation failure: %v", err) }
    original, _, err = f.server.sessions.Store().GetProjectTask(f.accountID, projectID, input.ID)
    if err != nil || original == nil || original.SessionID == "" || original.WorkspacePath != source.Path { t.Fatalf("reservation not durable: %+v %v", original, err) }
    if _, found, err := f.server.sessions.Store().GetSession(original.SessionID); err != nil || found { t.Fatalf("failed allocation left a session: %v", err) }
   } else {
    original, err = f.server.CreateProjectTask(context.Background(), p, projectID, input)
    if err != nil { t.Fatal(err) }
    _, err = f.server.sessions.Store().UpdateProjectTask(f.accountID, projectID, input.ID, func(task *pebblestore.ProjectTaskRecord) error {
     task.WorkspacePath = task.SourceWorkspace.Path
     task.BaseBranch, task.BaseCommit, task.WorktreeName = "", "", ""
     return nil
    })
    if err != nil { t.Fatal(err) }
   }
   dir := f.dir
   if err := f.db.Close(); err != nil { t.Fatal(err) }
   db, err := pebblestore.Open(dir)
   if err != nil { t.Fatal(err) }
   f.db = db
   defer db.Close()
   // Restore the API services against the reopened store, never the stale handle.
   reopened := setupProjectTaskReopenedServer(t, f)
   f.server = reopened
   f.wt.failAlloc = false
   recovered, err := f.server.CreateProjectTask(context.Background(), p, projectID, input)
   if err != nil { t.Fatalf("recover: %v", err) }
   if recovered.SessionID != original.SessionID || recovered.WorkspacePath == source.Path || recovered.SourceWorkspace.Path != source.Path { t.Fatalf("recovery changed owner: %+v %+v", original, recovered) }
   f.wt.mu.Lock(); allocations := f.wt.allocCalls; f.wt.mu.Unlock()
   want := 1; if failure == "reservation_without_session" { want = 2 }
   if allocations != want { t.Fatalf("recovery allocated %d times, want %d", allocations, want) }
   messages, err := f.server.sessions.Store().ListMessages(recovered.SessionID, 0, 100)
   if err != nil || len(messages) != 1 { t.Fatalf("recovery duplicated seed: %+v %v", messages, err) }
   if _, err := f.server.ApproveProjectTask(context.Background(), p, projectID, input.ID); err != nil { t.Fatal(err) }
   if _, err := f.server.ApproveProjectTask(context.Background(), p, projectID, input.ID); err != nil { t.Fatal(err) }
   runs, err := f.server.sessions.Store().ListRunIntents(recovered.SessionID, 10)
   if err != nil || len(runs) != 1 { t.Fatalf("recovery duplicated run: %+v %v", runs, err) }
   changed := input; changed.Prompt = "Changed after recovery"
   if _, err := f.server.CreateProjectTask(context.Background(), p, projectID, changed); err == nil || !strings.Contains(err.Error(), "conflicts") { t.Fatalf("changed payload accepted: %v", err) }
   persisted, _, err := f.server.sessions.Store().GetProjectTask(f.accountID, projectID, input.ID)
   if err != nil || persisted.SessionID != original.SessionID || persisted.WorkspacePath != recovered.WorkspacePath { t.Fatalf("changed payload corrupted persisted owner: %+v %v", persisted, err) }
  })
 }
}

func setupProjectTaskReopenedServer(t *testing.T, f *matrixTestFixture) *Server {
 t.Helper()
 sessionsStore := pebblestore.NewSessionStore(f.db)
 events, err := pebblestore.NewEventLog(f.db)
 if err != nil { t.Fatal(err) }
 sessions := sessionruntime.NewService(sessionsStore, events)
 // A reopened daemon resumes only after the persisted repository-history
 // migration is ready; preserve that precondition in the recovery fixture.
 requireMatrixRepositoryHistoryReady(t, sessionsStore)
 plans := sessionruntime.NewPlanLifecycleService(sessions)
 server := &Server{
  sessions:sessions, planLifecycle:plans,
  agents:agentruntime.NewService(pebblestore.NewAgentStore(f.db), events),
  model:model.NewService(pebblestore.NewModelStore(f.db), events, nil),
  agentModelSettings:agentmodelsettings.NewService(pebblestore.NewAgentModelSettingsStore(f.db)),
  workspace:workspace.NewService(pebblestore.NewWorkspaceStore(f.db)),
  runner:f.runSvc,
 }
 server.worktrees = f.wt
 server.runCtx, server.runCancel = context.WithCancel(context.Background())
 t.Cleanup(server.runCancel)
 server.v3SessionExecutor = newSessionV3Executor(server)
 plans.SetApplySessionMutation(server.applySessionV3PrimaryMutation)
 return server
}
