package api

import (
	"context"
	"reflect"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: SubmitProjectTaskStructuredPlan must repair an unbound pending card
// without replacing it or executing work. The real API/store fixture is the
// narrowest boundary proving checklist persistence and atomic revision rejection.
// Legacy content loss must also reject acceptance without creating a run intent.
func TestProjectTaskFirstPlanRepairPersistsRequirements(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	f.server.v3SessionExecutor = nil
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	project := f.createProject(t)
	if err := f.server.sessions.Store().CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, err := f.server.CreateProjectTask(context.Background(), p, project, tool.ProjectTaskCreateInput{Title: "Feature", Prompt: "Implement feature", Agent: "swarm", FeatureSize: "big"})
	if err != nil {
		t.Fatal(err)
	}
	doc := &pebblestore.SessionPlanDocument{Title: "Feature plan", Info: pebblestore.SessionPlanInfo{Goal: "Implement feature"}, Requirements: []pebblestore.SessionPlanRequirement{{ID: "behavior", Text: "Behavior works", CheckpointID: "cp-1"}}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Feature", Tasks: []string{"Implement behavior"}, AcceptanceCriteria: []string{"Behavior works"}}}}
	input := sessionruntime.ProjectTaskPlanSubmissionInput{AccountScopeID: f.accountID, UserID: f.userID, ProjectID: project, TaskID: task.ID, SessionID: task.SessionID, ExpectedTaskRevision: task.Revision, Document: doc, Feedback: "Author missing plan"}
	for _, kind := range []string{"stale", "foreign", "unbound-criterion", "missing-checklist"} {
		bad := input
		copyDoc := *doc
		bad.Document = &copyDoc
		switch kind {
		case "stale":
			bad.ExpectedTaskRevision++
		case "foreign":
			bad.AccountScopeID = "foreign-account"
		case "unbound-criterion":
			copyDoc.Requirements = []pebblestore.SessionPlanRequirement{{ID: "bad", Text: "Different outcome", CheckpointID: "cp-1"}}
		case "missing-checklist":
			copyDoc.Requirements = nil
		}
		if _, err := f.server.SubmitProjectTaskPlan(context.Background(), bad); err == nil {
			t.Fatalf("accepted %s", kind)
		}
		unchanged, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, project, task.ID)
		if err != nil || !found || unchanged.PlanBinding != nil || unchanged.Revision != task.Revision {
			t.Fatalf("rejection mutated task: %+v %v", unchanged, err)
		}
	}
	result, err := f.server.SubmitProjectTaskPlan(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.ID != task.ID || result.Task.SessionID != task.SessionID || result.Task.Status != "pending_approval" || result.Plan.ApprovalState != "pending" || result.Plan.AcceptedDefinitionReceipt != "" {
		t.Fatalf("repair changed identity or approved work: %+v", result)
	}
	stored, found, err := f.server.sessions.Store().GetPlan(task.SessionID, result.Plan.ID)
	if err != nil || !found || stored.Document == nil || !reflect.DeepEqual(stored.Document.Requirements, doc.Requirements) {
		t.Fatalf("authored checklist not persisted: %+v %v", stored, err)
	}
	if _, err := f.server.SubmitProjectTaskPlan(context.Background(), input); err == nil {
		t.Fatal("first-plan guard overwrote a bound plan")
	}
	// Simulate a legacy record lacking the mandatory checklist. Exact approval
	// guards must not turn missing review content into executable work.
	stored.Document.Requirements = nil
	if err := f.server.sessions.Store().PutPlan(stored); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.ApproveProjectTask(context.Background(), p, project, task.ID, tool.ProjectTaskApprovalGuards{SessionID: task.SessionID, PlanID: stored.ID, DefinitionRevision: stored.Version}); err == nil {
		t.Fatal("approved legacy plan without checklist")
	}
	unchanged, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, project, task.ID)
	if err != nil || !found || unchanged.Status != "pending_approval" || unchanged.PlanBinding.Receipt != "" {
		t.Fatalf("invalid approval mutated task: %+v %v", unchanged, err)
	}
	intents, err := f.server.sessions.Store().ListRunIntents(task.SessionID, 10)
	if err != nil || len(intents) != 0 {
		t.Fatalf("repair executed work: %+v %v", intents, err)
	}
}
