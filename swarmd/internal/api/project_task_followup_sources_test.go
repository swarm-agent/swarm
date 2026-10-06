package api

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: ReopenProjectTask -> ReserveTaskFollowup -> deployProjectTaskExecution
// must carry only exact approved repository identities into a new V3 session.
// Threat: lost secondary grants, replayed execution, and historical path-only
// permission minting. Real API/Pebble/Git plus scheduler admission is the narrowest
// joined proof; no provider execution or manually supplied session grants is used.
func TestProjectTaskFollowupApprovedSources(t *testing.T) {
	for _, scenario := range []string{"preserved", "historical", "historical-session", "historical-launched", "removed", "revoked", "rebound", "generation", "foreign", "added", "dirty", "symlink", "foreign-owner", "stale-receipt", "missing-evidence"} {
		t.Run(scenario, func(t *testing.T) {
			f, p, project, original, repos := followupSourcesFixture(t)
			db := f.server.sessions.Store()
			stopFollowupSourceRun(t, f, p, project.ID, original.ID)
			original, _, _ = db.GetProjectTask(p.AccountScopeID, project.ID, original.ID)
			oldSession, oldRun := original.SessionID, original.ExecutionRunID()
			oldBinding := *original.PlanBinding
			if scenario == "historical-session" || scenario == "historical-launched" {
				_, err := f.server.ReopenProjectTask(context.Background(), p, project.ID, original.ID, tool.ProjectTaskFollowupInput{ClientRequestID: "new-reopen", Revision: original.Revision, Feedback: "Continue the approved two-repository scope"})
				if err == nil || !strings.Contains(err.Error(), "executor") {
					t.Fatalf("historical session setup: %v", err)
				}
				retained, _, _ := db.GetProjectTask(p.AccountScopeID, project.ID, original.ID)
				owned, _, err := db.GetSession(retained.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				owned.WorkspaceGrants = owned.WorkspaceGrants[:1]
				key := "legacy-followup-missing-source"
				if _, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: owned.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: sessionruntime.SessionMutationUpdateMetadata, Session: &owned, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key}); err != nil {
					t.Fatal(err)
				}
				if _, err := db.UpdateProjectTask(p.AccountScopeID, project.ID, original.ID, func(task *pebblestore.ProjectTaskRecord) error {
					task.ProgramSources = nil
					if scenario == "historical-launched" {
						task.ActiveAttempt().LaunchState = "launched"
						task.WorkspacePath = owned.WorktreeRootPath
						task.WorktreeBranch = owned.WorktreeBranch
						task.BaseBranch = owned.WorktreeBaseBranch
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "historical" || scenario == "missing-evidence" {
				// Reproduce the old canonical reservation's destructive clearing,
				// retaining the durable original attempt/approved plan/session.
				_, err := db.ReserveTaskFollowup(p.AccountScopeID, project.ID, original.ID, p.UserID, "old-reopen", "continue approved scope", original.Revision, 200)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.UpdateProjectTask(p.AccountScopeID, project.ID, original.ID, func(task *pebblestore.ProjectTaskRecord) error {
					task.ProgramSources = nil
					task.Status = "failed"
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "missing-evidence" {
					owned, _, err := db.GetSession(oldSession)
					if err != nil {
						t.Fatal(err)
					}
					owned.WorkspaceGrants = owned.WorkspaceGrants[:1]
					key := "remove-historical-grant"
					if _, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: owned.ID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Kind: sessionruntime.SessionMutationUpdateMetadata, Session: &owned, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key}); err != nil {
						t.Fatal(err)
					}
				}
			}
			current, _, _ := db.GetProjectTask(p.AccountScopeID, project.ID, original.ID)
			switch scenario {
			case "removed":
				project.Workspaces = project.Workspaces[:1]
				if err := db.PutProject(p.AccountScopeID, project); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if err := pebblestore.NewWorkspaceStore(f.db).DeleteForAccount(p.AccountScopeID, p.UserID, repos[1]); err != nil {
					t.Fatal(err)
				}
			case "rebound":
				catalog := pebblestore.NewWorkspaceStore(f.db)
				entry, _, err := catalog.GetForAccount(p.AccountScopeID, repos[1])
				if err != nil {
					t.Fatal(err)
				}
				if _, err := catalog.UpdateForWorkspaceIDForAccountGuarded(p.AccountScopeID, p.UserID, entry.WorkspaceID, pebblestore.WorkspaceCatalogUpdate{ExpectedGeneration: entry.WorkspaceGeneration, NewPath: repos[2]}); err != nil {
					t.Fatal(err)
				}
			case "generation":
				_, err := db.UpdateProjectTask(p.AccountScopeID, project.ID, original.ID, func(task *pebblestore.ProjectTaskRecord) error {
					task.ProgramSources[1].WorkspaceGeneration++
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			case "added":
				entry, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(p.AccountScopeID, repos[2], "unapproved")
				if err != nil {
					t.Fatal(err)
				}
				project.Workspaces = append(project.Workspaces, pebblestore.ProjectWorkspaceRef{Path: entry.Path, WorkspaceID: entry.WorkspaceID})
				if err := db.PutProject(p.AccountScopeID, project); err != nil {
					t.Fatal(err)
				}
				_, err = db.UpdateProjectTask(p.AccountScopeID, project.ID, original.ID, func(task *pebblestore.ProjectTaskRecord) error {
					task.ProgramSources = append(task.ProgramSources, pebblestore.ProjectTaskSource{Path: entry.Path, WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Provenance: "explicit"})
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			case "symlink":
				moved := filepath.Join(t.TempDir(), "moved")
				if err := os.Rename(repos[1], moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, repos[1]); err != nil {
					t.Fatal(err)
				}
			case "foreign-owner":
				callerSession, _, err := db.GetSession(oldSession)
				if err != nil {
					t.Fatal(err)
				}
				callerSession.Metadata = cloneSessionsV3Metadata(callerSession.Metadata)
				callerSession.Metadata["task_id"] = "unrelated-task"
				key := "foreign-task-owner"
				if _, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: oldSession, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Kind: sessionruntime.SessionMutationUpdateMetadata, Session: &callerSession, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key}); err != nil {
					t.Fatal(err)
				}
			case "stale-receipt":
				if _, err := db.UpdateProjectTask(p.AccountScopeID, project.ID, original.ID, func(task *pebblestore.ProjectTaskRecord) error {
					binding := *task.PlanBinding
					binding.Receipt = "unaccepted"
					task.PlanBinding = &binding
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "dirty":
				if err := os.WriteFile(filepath.Join(repos[1], "unfinished"), []byte("work"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := db.GetProjectTask(p.AccountScopeID, project.ID, original.ID)
			gitBefore := followupSourceGit(t, repos[0], "worktree", "list", "--porcelain")
			caller := p
			if scenario == "foreign" {
				caller.AccountScopeID = "foreign-account"
			}
			req := tool.ProjectTaskFollowupInput{ClientRequestID: "new-reopen", Revision: before.Revision, Feedback: "Continue the approved two-repository scope"}
			if scenario == "historical-session" || scenario == "historical-launched" {
				req.Revision = original.Revision // Exact original retry payload.
			}
			_, err := f.server.ReopenProjectTask(context.Background(), caller, project.ID, original.ID, req)
			valid := scenario == "preserved" || scenario == "historical" || scenario == "historical-session" || scenario == "historical-launched"
			if !valid {
				if err == nil || (scenario == "missing-evidence" && !strings.Contains(err.Error(), "explicitly submit")) {
					t.Fatalf("invalid source admitted or recovery guidance missing: %v", err)
				}
				after, _, _ := db.GetProjectTask(p.AccountScopeID, project.ID, original.ID)
				if !reflect.DeepEqual(before, after) || followupSourceGit(t, repos[0], "worktree", "list", "--porcelain") != gitBefore {
					t.Fatal("rejection changed durable task or allocated a worktree")
				}
				return
			}
			// Absent executor is intentional: verify durable admission, not an LLM.
			if scenario == "historical-launched" {
				if err != nil {
					t.Fatalf("launched projection repair: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "executor") {
				t.Fatalf("expected durable launch with absent executor: %v", err)
			}
			launched, _, _ := db.GetProjectTask(p.AccountScopeID, project.ID, original.ID)
			assertFollowupSources(t, f, p, launched, repos[:2], oldSession, oldRun)
			if (scenario == "historical-session" || scenario == "historical-launched") && launched.SessionID != before.SessionID {
				t.Fatal("historical grant repair replaced the reserved session")
			}
			if launched.PlanBinding != nil || launched.TaskProgramID != "" || launched.TaskProgram != nil || len(launched.CoderAssignments) != 0 {
				t.Fatal("replayed mutable plan/program/assignments")
			}
			if launched.Attempts[0].PlanBinding == nil || *launched.Attempts[0].PlanBinding != oldBinding {
				t.Fatal("lost original approval evidence")
			}
			f.server.v3SessionExecutor = &sessionV3Executor{server: f.server, ctx: context.Background(), inFlightRuns: map[string]bool{sessionV3ExecutorRunKey(launched.SessionID, launched.ExecutionRunID()): true}}
			for i := 0; i < 2; i++ {
				if _, err := f.server.ReopenProjectTask(context.Background(), p, project.ID, original.ID, req); err != nil {
					t.Fatalf("idempotent retry: %v", err)
				}
			}
			intents, err := db.ListRunIntents(launched.SessionID, 10)
			if err != nil || len(intents) != 1 {
				t.Fatalf("duplicate run: %+v %v", intents, err)
			}
			messages, err := db.ListMessages(launched.SessionID, 0, 10)
			if err != nil || len(messages) != 1 {
				t.Fatalf("duplicated seed: %+v %v", messages, err)
			}
			oldPlan, found, err := db.GetPlan(oldSession, oldBinding.PlanID)
			if err != nil || !found || oldPlan.AcceptedDefinitionReceipt != oldBinding.Receipt {
				t.Fatal("reopen changed historical acceptance")
			}
			stopFollowupSourceRun(t, f, p, project.ID, original.ID)
			current, _, _ = db.GetProjectTask(p.AccountScopeID, project.ID, original.ID)
			f.server.v3SessionExecutor = nil
			_, err = f.server.ReopenProjectTask(context.Background(), p, project.ID, original.ID, tool.ProjectTaskFollowupInput{ClientRequestID: "second-reopen", Revision: current.Revision, Feedback: "Continue again"})
			if err == nil || !strings.Contains(err.Error(), "executor") {
				t.Fatalf("repeat reopen: %v", err)
			}
			next, _, _ := db.GetProjectTask(p.AccountScopeID, project.ID, original.ID)
			assertFollowupSources(t, f, p, next, repos[:2], launched.SessionID, launched.ExecutionRunID())
		})
	}
}

func followupSourcesFixture(t *testing.T) (*matrixTestFixture, identity.Principal, *pebblestore.ProjectRecord, *pebblestore.ProjectTaskRecord, []string) {
	t.Helper()
	f := setupMatrixTestFixture(t)
	t.Cleanup(func() { f.db.Close() })
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	catalog := pebblestore.NewWorkspaceStore(f.db)
	proj := &pebblestore.ProjectRecord{ID: "source-followup", Name: "Source followup"}
	var repos []string
	for i := 0; i < 3; i++ {
		repo := t.TempDir()
		followupSourceGit(t, repo, "init", "-b", "dev")
		followupSourceGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base")
		repos = append(repos, repo)
		if i < 2 {
			entry, err := catalog.AddForAccount(f.accountID, repo, fmt.Sprintf("source-%d", i))
			if err != nil {
				t.Fatal(err)
			}
			proj.Workspaces = append(proj.Workspaces, pebblestore.ProjectWorkspaceRef{Path: repo, WorkspaceID: entry.WorkspaceID})
		}
	}
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	f.server.workspace = workspace.NewService(catalog)
	f.server.worktrees = worktreeruntime.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
	db := f.server.sessions.Store()
	if err := db.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatal(err)
	}
	primary, err := f.server.resolveProjectTaskSource(p, proj, repos[0], "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	seedTaskSessionBinding(t, f, primary)
	program := &pebblestore.TaskProgramDefinition{ID: "approved-cross", Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "committed sources"}}}
	for i, repo := range repos[:2] {
		program.Jobs = append(program.Jobs, pebblestore.TaskProgramJobSpec{ID: fmt.Sprintf("job-%d", i), StageID: "build", AgentType: "coder", WorkspacePath: repo, Title: "Implement", MetaPrompt: "Implement scoped change", Deliverable: "Commit", OwnedScope: []string{"value.txt"}, AcceptanceCriteria: []string{"Committed"}, DependencyEvidence: "Source committed"})
	}
	doc := &pebblestore.SessionPlanDocument{Requirements: []pebblestore.SessionPlanRequirement{{ID: "req-1", Text: "Committed", CheckpointID: "cp-1"}}, Title: "Two repositories", Info: pebblestore.SessionPlanInfo{Goal: "Implement approved scope"}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Implement", Order: 1, Tasks: []string{"Implement"}, AcceptanceCriteria: []string{"Committed"}, TaskProgram: program}}}
	task, err := f.server.CreateProjectTask(context.Background(), p, proj.ID, tool.ProjectTaskCreateInput{ID: "two-repos", Title: "Two repos", WorkspacePath: repos[0], Document: doc})
	if err != nil {
		t.Fatal(err)
	}
	task, _, _ = db.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
	approved, err := f.server.ApproveProjectTask(context.Background(), p, proj.ID, task.ID, tool.ProjectTaskApprovalGuards{SessionID: task.SessionID, PlanID: task.PlanBinding.PlanID, DefinitionRevision: task.PlanBinding.DefinitionRevision})
	if err != nil {
		t.Fatal(err)
	}
	return f, p, proj, approved, repos
}

func followupSourceGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func stopFollowupSourceRun(t *testing.T, f *matrixTestFixture, p identity.Principal, project, taskID string) {
	t.Helper()
	db := f.server.sessions.Store()
	task, _, err := db.GetProjectTask(p.AccountScopeID, project, taskID)
	if err != nil {
		t.Fatal(err)
	}
	intent, found, err := db.GetV3SessionActiveRunIntent(task.SessionID)
	if err != nil || !found {
		t.Fatalf("active intent missing: %v", err)
	}
	intent.Status = pebblestore.V3RunIntentCompleted
	key := "stop:" + intent.RunID
	if _, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: task.SessionID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Kind: sessionruntime.SessionMutationRecordRunIntent, RunIntent: &intent, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateProjectTask(p.AccountScopeID, project, taskID, func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = "failed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertFollowupSources(t *testing.T, f *matrixTestFixture, p identity.Principal, task *pebblestore.ProjectTaskRecord, repos []string, oldSession, oldRun string) {
	t.Helper()
	owned, found, err := f.server.sessions.Store().GetSession(task.SessionID)
	if err != nil || !found || task.SessionID == oldSession || task.ExecutionRunID() == oldRun || len(task.ProgramSources) != 2 {
		t.Fatalf("lost new attempt/source authority: %+v %v", task, err)
	}
	for _, source := range task.ProgramSources {
		matched := false
		for _, grant := range owned.WorkspaceGrants {
			matched = matched || (grant.Path == source.Path && grant.WorkspaceID == source.WorkspaceID && grant.WorkspaceGeneration == source.WorkspaceGeneration)
		}
		if !matched {
			t.Fatalf("missing canonical grant from storage->session path: %+v", source)
		}
	}
	// Exercise actual scheduler source admission with the reopen-created snapshot.
	// Revision zero makes preflight nonallocating; a terminal record prevents any
	// child/provider execution after the source guards have accepted both roots.
	perms := permission.NewService(pebblestore.NewPermissionStore(f.db), nil, nil)
	runner := runruntime.NewService(f.server.sessions, nil, nil, nil, perms, nil, nil, nil)
	runner.SetWorktreeService(f.server.worktrees.(*worktreeruntime.Service))
	runner.SetWorkspaceService(f.server.workspace)
	runner.SetSessionWorkspaceCanonicalizer(func(input runruntime.SessionWorkspaceCanonicalizeInput) (runruntime.SessionWorkspaceCanonicalization, error) {
		for _, repo := range repos {
			scope, err := f.server.workspace.ScopeForPathForPrincipal(input.Principal, repo)
			if err != nil {
				return runruntime.SessionWorkspaceCanonicalization{}, err
			}
			if scope.WorkspaceID == input.WorkspaceID {
				return runruntime.SessionWorkspaceCanonicalization{WorkspaceID: scope.WorkspaceID, WorkspaceGeneration: scope.WorkspaceGeneration, WorkspaceState: "active", WorkspaceName: filepath.Base(repo), SourceWorkspacePath: repo, RuntimeWorkspacePath: repo, WorkspaceBindingID: "binding", RuntimeSwarmID: "self", BindingGeneration: 1, PlacementGeneration: 1}, nil
			}
		}
		return runruntime.SessionWorkspaceCanonicalization{}, fmt.Errorf("unknown catalog identity")
	})
	definition := pebblestore.TaskProgramDefinition{ID: "admission-" + task.ActiveAttemptID, Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}}}
	record := pebblestore.TaskProgramRecord{ParentSessionID: owned.ID, ProgramID: definition.ID, DefinitionHash: "admission", State: pebblestore.TaskProgramStateBlocked, ActiveStageID: "build"}
	for i, repo := range repos {
		id := fmt.Sprintf("job-%d", i)
		definition.Jobs = append(definition.Jobs, pebblestore.TaskProgramJobSpec{ID: id, StageID: "build", AgentType: "coder", WorkspacePath: repo, Title: "Admission", MetaPrompt: "Admission", Deliverable: "Admission", OwnedScope: []string{"value.txt"}, AcceptanceCriteria: []string{"ready"}, DependencyEvidence: "ready"})
		record.Jobs = append(record.Jobs, pebblestore.TaskProgramJobRecord{JobID: id, StageID: "build", State: pebblestore.TaskProgramJobDeclared})
	}
	record.Definition = definition
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := runner.ExecuteTaskProgramForCoordinator(ctx, p, owned.ID, task.ExecutionRunID(), record); err != nil {
		t.Fatalf("actual program source admission rejected reopened grants: %v", err)
	}
}
