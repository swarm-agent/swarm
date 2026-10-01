package api

import (
	"context"
	"os/exec"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: CreateProjectTask must bind each grouped Coder to an authorized
// catalog repository before reserving state. Approval revalidates all bindings.
// Threat: first-repository fallback, forged bindings, or stale secondary sources
// can grant unrelated write access. This API/store test observes admission, not
// provider execution (which is separately qualified live).
func TestProjectSmallParallelCodersCrossWorkspace(t *testing.T) {
	for _, scenario := range []string{"approved", "foreign", "stale", "mismatched", "ambiguous", "revoked_before_approval"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupMatrixTestFixture(t)
			defer f.db.Close()
			projectID := f.createProject(t)
			p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
			proj, _, err := f.server.sessions.Store().GetProject(f.accountID, projectID)
			if err != nil {
				t.Fatal(err)
			}
			first := proj.Workspaces[0].Path
			second := t.TempDir()
			for _, args := range [][]string{{"init"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "secondary base"}} {
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
			if err := f.server.sessions.Store().PutProject(f.accountID, proj); err != nil {
				t.Fatal(err)
			}
			assignments := []pebblestore.ProjectTaskCoderAssignment{
				{Title: "A", MetaPrompt: "A", Deliverable: "A", OwnedScope: []string{"value.txt"}, AcceptanceCriteria: []string{"A"}, WorkspacePath: first},
				{Title: "B", MetaPrompt: "B", Deliverable: "B", OwnedScope: []string{"value.txt"}, AcceptanceCriteria: []string{"B"}, WorkspacePath: second},
			}
			if scenario == "stale" {
				assignments[1].WorkspaceGeneration = 999999
			}
			if scenario == "mismatched" {
				assignments[1].WorkspaceID = "wrong-id"
			}
			input := tool.ProjectTaskCreateInput{ID: "pair", Title: "Two repositories", WorkspacePath: first, CoderAssignments: assignments}
			if scenario == "ambiguous" {
				input.WorkspacePath = ""
			}
			created, err := f.server.CreateProjectTask(context.Background(), p, projectID, input)
			if scenario != "approved" && scenario != "revoked_before_approval" {
				if err == nil {
					t.Fatal("invalid source accepted")
				}
				if task, ok, e := f.server.sessions.Store().GetProjectTask(f.accountID, projectID, "pair"); e != nil || ok || task != nil {
					t.Fatalf("invalid proposal reserved state: %v", e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if created.Status != "pending_approval" || created.PlanBinding != nil || created.TaskProgram != nil {
				t.Fatalf("wrong route: %+v", created)
			}
			sess, ok, err := f.server.sessions.Store().GetSession(created.SessionID)
			if err != nil || !ok {
				t.Fatalf("session: %v", err)
			}
			if len(sess.WorkspaceGrants) != 3 {
				t.Fatalf("wrong grants: %+v", sess.WorkspaceGrants)
			}
			for _, a := range created.CoderAssignments {
				found := false
				for _, g := range sess.WorkspaceGrants {
					if g.Path == a.WorkspacePath && g.WorkspaceID == a.SourceWorkspace.WorkspaceID && g.WorkspaceGeneration == a.SourceWorkspace.WorkspaceGeneration && g.WorkspaceGeneration > 0 {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing bound source grant: %+v", a)
				}
			}
			if scenario == "revoked_before_approval" {
				proj.Workspaces = proj.Workspaces[:1]
				if err := f.server.sessions.Store().PutProject(f.accountID, proj); err != nil {
					t.Fatal(err)
				}
				if _, err := f.server.ApproveProjectTask(context.Background(), p, projectID, created.ID); err == nil {
					t.Fatal("removed secondary source accepted")
				}
				intents, err := f.server.sessions.Store().ListRunIntents(sess.ID, 10)
				if err != nil || len(intents) != 0 {
					t.Fatalf("invalid approval admitted run: %+v %v", intents, err)
				}
				return
			}
			if _, err := f.server.ApproveProjectTask(context.Background(), p, projectID, created.ID); err != nil {
				t.Fatal(err)
			}
			intents, err := f.server.sessions.Store().ListRunIntents(sess.ID, 10)
			if err != nil || len(intents) != 1 {
				t.Fatalf("expected one parent: %+v %v", intents, err)
			}
			stored, _, err := f.server.sessions.Store().GetProjectTask(f.accountID, projectID, created.ID)
			if err != nil || stored.CoderAssignments[1].SourceWorkspace.Path != second || stored.CoderAssignments[0].SourceWorkspace.WorkspaceID == stored.CoderAssignments[1].SourceWorkspace.WorkspaceID {
				t.Fatalf("bindings lost: %+v %v", stored, err)
			}
		})
	}
}
