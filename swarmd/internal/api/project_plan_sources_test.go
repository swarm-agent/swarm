package api

import (
	"context"
	"os/exec"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: a checkpoint program may use separate repository lanes, but every
// source must be catalog-authorized before reservation and revalidated at exact
// approval. Threats are foreign grants, source replacement and premature runs.
// Late discovery exercises AI publication after initial single-repository admission;
// new grants must appear only at approval, not while the plan awaits review.
// Authority: CreateProjectTask, resolveProjectPlanSources, revalidateProjectTaskSource
// and ApproveProjectTask. This hermetic API/store layer proves admission only.
func TestProjectPlanCrossRepositorySources(t *testing.T) {
	for _, scenario := range []string{"approved", "provenance", "foreign", "removed", "stale", "missing_binding", "late-discovery", "late-removed"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupMatrixTestFixture(t)
			defer f.db.Close()
			projectID := f.createProject(t)
			p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
			db := f.server.sessions.Store()
			proj, _, err := db.GetProject(f.accountID, projectID)
			if err != nil {
				t.Fatal(err)
			}
			first, second := proj.Workspaces[0].Path, t.TempDir()
			for _, args := range [][]string{{"init"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "base"}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = second
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git: %v %s", err, out)
				}
			}
			account := f.accountID
			if scenario == "foreign" {
				account = "foreign-account"
			}
			ws, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(account, second, "secondary")
			if err != nil {
				t.Fatal(err)
			}
			proj.Workspaces = append(proj.Workspaces, pebblestore.ProjectWorkspaceRef{WorkspaceID: ws.WorkspaceID, Path: second})
			if err := db.PutProject(f.accountID, proj); err != nil {
				t.Fatal(err)
			}
			program := &pebblestore.TaskProgramDefinition{ID: "cross", Stages: []pebblestore.TaskProgramStageSpec{{ID: "produce", DependencyEvidence: "clean"}, {ID: "consume", DependsOn: []string{"produce"}, DependencyEvidence: "producer integrated"}}}
			for i, path := range []string{first, second} {
				id := []string{"produce", "consume"}[i]
				job := pebblestore.TaskProgramJobSpec{ID: id, StageID: id, AgentType: "coder", Title: id, MetaPrompt: id, Deliverable: id, WorkspacePath: path, OwnedScope: []string{"value.txt"}, AcceptanceCriteria: []string{"committed"}, DependencyEvidence: "source ready"}
				if i == 1 {
					job.DependsOn = []string{"produce"}
				}
				program.Jobs = append(program.Jobs, job)
			}
			doc := &pebblestore.SessionPlanDocument{Title: "Cross repository", Info: pebblestore.SessionPlanInfo{Goal: "Consume committed interface"}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Implement", Tasks: []string{"Run stored program"}, AcceptanceCriteria: []string{"Verified commits"}, Order: 1, TaskProgram: program}}}
			initialDoc := doc
			late := scenario == "late-discovery" || scenario == "late-removed"
			if late {
				copyDoc := *doc
				copyDoc.Checkpoints = append([]pebblestore.SessionPlanCheckpoint(nil), doc.Checkpoints...)
				copyDoc.Checkpoints[0].TaskProgram = nil
				initialDoc = &copyDoc
			}
			created, err := f.server.CreateProjectTask(context.Background(), p, projectID, tool.ProjectTaskCreateInput{ID: "cross", Title: "Cross repository", WorkspacePath: first, Document: initialDoc})
			if scenario == "foreign" {
				if err == nil {
					t.Fatal("foreign source accepted")
				}
				if _, ok, e := db.GetProjectTask(f.accountID, projectID, "cross"); e != nil || ok {
					t.Fatalf("unauthorized task reserved: %v", e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			stored, _, err := db.GetProjectTask(f.accountID, projectID, created.ID)
			if err != nil || (!late && len(stored.ProgramSources) != 2) || stored.PlanBinding == nil {
				t.Fatalf("lost bindings: %+v %v", stored, err)
			}
			sess, ok, err := db.GetSession(created.SessionID)
			if err != nil || !ok {
				t.Fatal(err)
			}
			for _, source := range stored.ProgramSources {
				found := false
				for _, grant := range sess.WorkspaceGrants {
					found = found || (grant.Path == source.Path && grant.WorkspaceID == source.WorkspaceID && grant.WorkspaceGeneration == source.WorkspaceGeneration && grant.WorkspaceGeneration > 0)
				}
				if !found {
					t.Fatalf("missing exact grant: %+v", source)
				}
			}
			intents, err := db.ListRunIntents(sess.ID, 10)
			if err != nil || len(intents) != 0 {
				t.Fatalf("premature execution: %+v %v", intents, err)
			}
			switch scenario {
			case "provenance":
				stored.ProgramSources[1].Provenance = "unique_project_workspace"
				if err := db.PutProjectTask(f.accountID, stored); err != nil {
					t.Fatal(err)
				}
			case "removed":
				proj.Workspaces = proj.Workspaces[:1]
				if err := db.PutProject(f.accountID, proj); err != nil {
					t.Fatal(err)
				}
			case "stale":
				stored.ProgramSources[1].WorkspaceGeneration++
				if err := db.PutProjectTask(f.accountID, stored); err != nil {
					t.Fatal(err)
				}
			case "missing_binding":
				stored.ProgramSources = nil
				if err := db.PutProjectTask(f.accountID, stored); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "approved" || scenario == "provenance" || late {
				// Exercise the same lifecycle used by exit_plan_mode, not only
				// the direct-plan constructor: a fresh authored revision retains
				// admitted sources and requires its own exact approval binding.
				doc.Info.Goal = "Review the authored revision before execution"
				submitted, err := f.server.planLifecycle.SubmitProjectTaskStructuredPlan(sessionruntime.ProjectTaskPlanSubmissionInput{AccountScopeID: f.accountID, UserID: f.userID, ProjectID: projectID, TaskID: created.ID, SessionID: created.SessionID, Document: doc})
				if err != nil {
					t.Fatal(err)
				}
				expectedSources := 2
				if late {
					expectedSources = 1
				}
				if submitted.Task.PlanBinding.DefinitionRevision != stored.PlanBinding.DefinitionRevision+1 || len(submitted.Task.ProgramSources) != expectedSources {
					t.Fatalf("authored revision lost admission: %+v", submitted.Task)
				}
				stored = &submitted.Task
				if late {
					pendingSession, _, err := db.GetSession(created.SessionID)
					if err != nil {
						t.Fatal(err)
					}
					for _, grant := range pendingSession.WorkspaceGrants {
						if grant.Path == second {
							t.Fatal("publication granted secondary source before approval")
						}
					}
					if scenario == "late-removed" {
						proj.Workspaces = proj.Workspaces[:1]
						if err := db.PutProject(f.accountID, proj); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			guards := tool.ProjectTaskApprovalGuards{SessionID: created.SessionID, PlanID: stored.PlanBinding.PlanID, DefinitionRevision: stored.PlanBinding.DefinitionRevision}
			_, err = f.server.ApproveProjectTask(context.Background(), p, projectID, created.ID, guards)
			if scenario != "approved" && scenario != "provenance" && scenario != "late-discovery" {
				if err == nil {
					t.Fatal("invalid source admitted")
				}
				intents, e := db.ListRunIntents(sess.ID, 10)
				if e != nil || len(intents) != 0 {
					t.Fatalf("rejected approval ran: %+v %v", intents, e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if late {
				approvedSession, _, err := db.GetSession(created.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				for _, source := range stored.ProgramSources {
					matched := false
					for _, grant := range approvedSession.WorkspaceGrants {
						matched = matched || (grant.Path == source.Path && grant.WorkspaceID == source.WorkspaceID && grant.WorkspaceGeneration == source.WorkspaceGeneration)
					}
					if !matched {
						t.Fatalf("approval missing exact grant: %+v", source)
					}
				}
			}
			intents, err = db.ListRunIntents(sess.ID, 10)
			if err != nil || len(intents) != 1 {
				t.Fatalf("expected one parent: %+v %v", intents, err)
			}
		})
	}
}
