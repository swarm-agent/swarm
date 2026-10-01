package session

import (
	"errors"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a published review must bind exactly one definition and its admitted
// sources atomically. SubmitProjectTaskStructuredPlan and ApplySessionMutation
// own this boundary. Injecting failure/concurrent archive at the mutation seam
// is the narrowest test proving no orphan plan survives a failed task CAS.
func TestTaskPlanPublicationAtomic(t *testing.T) {
	for _, scenario := range []string{"publish", "failure", "archive", "revision", "receipt", "source"} {
		t.Run(scenario, func(t *testing.T) {
			svc, cleanup := newPlanTestService(t)
			defer cleanup()
			current, _, err := svc.CreateSessionWithOptions(CreateSessionOptions{UserID: "user", AccountScopeID: "account", WorkspacePath: t.TempDir(), Mode: ModePlan, Metadata: map[string]any{"project_id": "project", "task_id": "task"}})
			if err != nil {
				t.Fatal(err)
			}
			task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", Title: "Plan", Agent: "plan", Status: "planning", SessionID: current.ID, WorkspacePath: current.WorkspacePath}
			if err := svc.Store().PutProjectTask("account", task); err != nil {
				t.Fatal(err)
			}
			doc := &pebblestore.SessionPlanDocument{ID: "review", Title: "Review", Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp", Title: "Implement", Order: 1, Status: "pending"}}}
			input := ProjectTaskPlanSubmissionInput{AccountScopeID: "account", UserID: "user", ProjectID: "project", TaskID: "task", SessionID: current.ID, Document: doc}
			input.ApplySessionMutation = func(in SessionMutationInput) (SessionMutationResult, error) {
				if in.PlanSave != nil {
					switch scenario {
					case "failure":
						return SessionMutationResult{}, errors.New("injected save failure")
					case "archive", "revision":
						if _, err := svc.Store().UpdateProjectTask("account", "project", "task", func(task *pebblestore.ProjectTaskRecord) error {
							task.Archived = scenario == "archive"
							return nil
						}); err != nil {
							t.Fatal(err)
						}
					case "receipt":
						in.PlanSave.TaskPublication.PlanBinding.Receipt = "substituted"
					case "source":
						in.PlanSave.TaskPublication.SourceWorkspace.Path = t.TempDir()
					}
				}
				return svc.ApplySessionMutation(in)
			}
			lifecycle := NewPlanLifecycleService(svc)
			result, err := lifecycle.SubmitProjectTaskStructuredPlan(input)
			stored, _, readErr := svc.Store().GetProjectTask("account", "project", "task")
			if readErr != nil {
				t.Fatal(readErr)
			}
			plan, found, readErr := svc.Store().GetPlan(current.ID, "review")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario != "publish" {
				if err == nil || found || stored.PlanBinding != nil || stored.Status != "planning" {
					t.Fatalf("partial publication: err=%v task=%+v plan=%+v", err, stored, plan)
				}
				return
			}
			if err != nil || !found || stored.PlanBinding == nil || stored.PlanBinding.Receipt != result.Receipt || stored.PlanBinding.DefinitionRevision != plan.Version || stored.Status != "pending_approval" {
				t.Fatalf("publication: err=%v task=%+v plan=%+v", err, stored, plan)
			}
			revision := stored.Revision
			if _, err := lifecycle.SubmitProjectTaskStructuredPlan(input); err != nil {
				t.Fatal(err)
			}
			stored, _, _ = svc.Store().GetProjectTask("account", "project", "task")
			if stored.Revision != revision {
				t.Fatal("duplicate publication changed task revision")
			}
			intents, err := svc.Store().ListRunIntents(current.ID, 10)
			if err != nil || len(intents) != 0 {
				t.Fatalf("publication launched execution: %+v %v", intents, err)
			}
		})
	}
}
