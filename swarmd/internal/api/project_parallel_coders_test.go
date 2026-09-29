package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: One approved small project card must reserve a non-delegated, isolated
// Swarm parent with two durable, distinct Coder assignments and no plan/program.
// Threat: premature run admission or lost workspace identity leaves delegation
// disabled or launches against an unapproved or stale source. The canonical API
// creation and approval boundary is the narrowest place to observe both records.
func TestProjectSmallParallelCodersApproval(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projectID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	assignments := []pebblestore.ProjectTaskCoderAssignment{
		{Title: "Fix alpha", MetaPrompt: "Change alpha.go only", Deliverable: "Committed alpha fix", OwnedScope: []string{"alpha.go"}, AcceptanceCriteria: []string{"Alpha works"}},
		{Title: "Fix beta", MetaPrompt: "Change beta.go only", Deliverable: "Committed beta fix", OwnedScope: []string{"beta.go"}, AcceptanceCriteria: []string{"Beta works"}},
	}
	created, err := f.server.CreateProjectTask(context.Background(), p, projectID, tool.ProjectTaskCreateInput{Title: "Two fixes", Prompt: "Fix alpha and beta independently", CoderAssignments: assignments})
	if err != nil {
		t.Fatalf("create parallel task: %v", err)
	}
	if created.Agent != "swarm" || created.FeatureSize != "small" || created.Status != "pending_approval" || created.PlanBinding != nil || created.TaskProgram != nil || len(created.CoderAssignments) != 2 {
		t.Fatalf("wrong small parallel task contract: %+v", created)
	}
	sess, ok, err := f.server.sessions.Store().GetSession(created.SessionID)
	if err != nil || !ok {
		t.Fatalf("read reserved parent: %v, found=%v", err, ok)
	}
	if !sess.WorktreeEnabled || sess.WorktreeRootPath == "" || sess.WorkspacePath != sess.WorktreeRootPath || sess.Metadata["resolved_agent_name"] != "swarm" || sess.Metadata["agent_mode"] != "primary" || sess.Preference.Model != "gemini-2.5-action" || sess.Metadata["parent_task_call_id"] != nil || sess.Mode != "auto" {
		t.Fatalf("expected primary Swarm Action parent in isolated worktree: %+v", sess)
	}
	if len(sess.WorkspaceGrants) != 2 || sess.WorkspaceGrants[0].WorkspaceID != created.SourceWorkspace.WorkspaceID || sess.WorkspaceGrants[0].WorkspaceGeneration != created.SourceWorkspace.WorkspaceGeneration || sess.WorkspaceGrants[1].WorkspaceID != created.SourceWorkspace.WorkspaceID || sess.WorkspaceGrants[1].WorkspaceGeneration != created.SourceWorkspace.WorkspaceGeneration {
		t.Fatalf("source/worktree grants lost catalog identity: %+v", sess.WorkspaceGrants)
	}
	messages, err := f.server.sessions.Store().ListMessages(sess.ID, 0, 10)
	if err != nil || len(messages) == 0 || !strings.Contains(messages[0].Content, "Fix alpha") || !strings.Contains(messages[0].Content, "Fix beta") || !strings.Contains(messages[0].Content, "one regular task launch") {
		t.Fatalf("parent missing assignment instructions: %+v, err=%v", messages, err)
	}
	intents, err := f.server.sessions.Store().ListRunIntents(sess.ID, 10)
	if err != nil || len(intents) != 0 {
		t.Fatalf("execution admitted before approval: %+v, err=%v", intents, err)
	}
	if err := f.server.DeployProjectTask(context.Background(), p, projectID, created.ID); err == nil || !strings.Contains(err.Error(), "awaiting approval") {
		t.Fatalf("pending task deployment must be rejected, got %v", err)
	}
	approved, err := f.server.ApproveProjectTask(context.Background(), p, projectID, created.ID)
	if err != nil || approved.Status != "in_progress" || len(approved.CoderAssignments) != 2 {
		t.Fatalf("approve parallel task: %+v, %v", approved, err)
	}
	intents, err = f.server.sessions.Store().ListRunIntents(sess.ID, 10)
	if err != nil || len(intents) != 1 || intents[0].Status != pebblestore.V3RunIntentPendingExecutor || intents[0].ParentSessionID != "" {
		t.Fatalf("expected one approved parent run: %+v, err=%v", intents, err)
	}
	again, err := f.server.ApproveProjectTask(context.Background(), p, projectID, created.ID)
	if err != nil || again.ID != created.ID {
		t.Fatalf("idempotent approval: %+v, %v", again, err)
	}
	intents, err = f.server.sessions.Store().ListRunIntents(sess.ID, 10)
	if err != nil || len(intents) != 1 {
		t.Fatalf("duplicate parent execution on retry: %+v, err=%v", intents, err)
	}
	stored, ok, err := f.server.sessions.Store().GetProjectTask(f.accountID, projectID, created.ID)
	if err != nil || !ok || len(stored.CoderAssignments) != 2 || stored.CoderAssignments[1].OwnedScope[0] != "beta.go" {
		t.Fatalf("approved task lost durable second assignment: %+v, err=%v", stored, err)
	}
}

// Purpose: invalid overlapping or mixed plan/parallel specifications never reserve
// a task or create a run; the same canonical boundary preserves single-Coder routing.
func TestProjectSmallParallelCodersRejectsOverlapAndPreservesSingleCoder(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projectID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	assignments := []pebblestore.ProjectTaskCoderAssignment{
		{Title: "A", MetaPrompt: "A", Deliverable: "A", OwnedScope: []string{"pkg/**"}, AcceptanceCriteria: []string{"A"}},
		{Title: "B", MetaPrompt: "B", Deliverable: "B", OwnedScope: []string{"pkg/b.go"}, AcceptanceCriteria: []string{"B"}},
	}
	_, err := f.server.CreateProjectTask(context.Background(), p, projectID, tool.ProjectTaskCreateInput{ID: "overlap", Title: "Bad", CoderAssignments: assignments})
	if err == nil || !strings.Contains(err.Error(), "non-overlapping") {
		t.Fatalf("overlap must reject before execution, got %v", err)
	}
	if task, ok, err := f.server.sessions.Store().GetProjectTask(f.accountID, projectID, "overlap"); err != nil || ok || task != nil {
		t.Fatalf("invalid task reserved: %+v, %v", task, err)
	}
	w := f.callAPI(http.MethodPost, "/"+projectID+"/tasks", map[string]any{"title": "Single fix", "agent": "coder", "feature_size": "small"}, p)
	task := requireMatrixTaskResponse(t, w, http.StatusCreated)
	if task["agent"] != "coder" || task["status"] != "pending_approval" || task["coder_assignments"] != nil {
		t.Fatalf("single Coder contract changed: %+v", task)
	}
}
