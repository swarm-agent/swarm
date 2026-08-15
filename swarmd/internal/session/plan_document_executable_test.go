package session

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestValidateExecutablePlanDocumentRequiresStructuredDocument(t *testing.T) {
	err := ValidateExecutablePlanDocument(nil)
	var validationErr *ExecutablePlanValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %T %v, want ExecutablePlanValidationError", err, err)
	}
	assertExecutablePlanIssue(t, validationErr, "document", "structured document is required")
}

func TestValidateExecutablePlanDocumentRequiresCheckpoint(t *testing.T) {
	err := ValidateExecutablePlanDocument(&pebblestore.SessionPlanDocument{
		Title: "Executable plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Ship complete plans"},
	})
	var validationErr *ExecutablePlanValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %T %v, want ExecutablePlanValidationError", err, err)
	}
	assertExecutablePlanIssue(t, validationErr, "checkpoints", "at least one checkpoint is required")
}

func TestValidateExecutablePlanDocumentReportsCheckpointFields(t *testing.T) {
	err := ValidateExecutablePlanDocument(&pebblestore.SessionPlanDocument{
		Title: "Executable plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Ship complete plans"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			Status: PlanCheckpointStatusPending,
			Order:  1,
		}},
	})
	var validationErr *ExecutablePlanValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %T %v, want ExecutablePlanValidationError", err, err)
	}
	assertExecutablePlanIssue(t, validationErr, "checkpoints[0].id", "checkpoint id is required")
	assertExecutablePlanIssue(t, validationErr, "checkpoints[0].title", "checkpoint title is required")
	assertExecutablePlanIssue(t, validationErr, "checkpoints[0].objective", "checkpoint objective or at least one concrete task is required")
	assertExecutablePlanIssue(t, validationErr, "checkpoints[0].acceptance_criteria", "at least one acceptance criterion is required")
}

func TestValidateExecutablePlanDocumentRejectsSemanticallyInvalidTaskProgram(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{
		Title: "Invalid staged program", Info: pebblestore.SessionPlanInfo{Goal: "Reject invalid graph"}, ActiveCheckpointID: "cp-1",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Build", Status: PlanCheckpointStatusPending, Order: 1, Tasks: []string{"Build"}, AcceptanceCriteria: []string{"Done"}, TaskProgram: &pebblestore.TaskProgramDefinition{
			ID: "invalid_program", Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "Ready"}, {ID: "audit", DependsOn: []string{"missing"}, DependencyEvidence: "Needs build"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "job", StageID: "build", AgentType: "coder", Title: "Build", MetaPrompt: "Build", Deliverable: "Change", OwnedScope: []string{"swarmd/internal/run/**"}, AcceptanceCriteria: []string{"Done"}, DependencyEvidence: "Ready"}},
		}}},
	}
	err := ValidateExecutablePlanDocument(doc)
	if err == nil || !strings.Contains(err.Error(), "checkpoints[0].task_program") || !strings.Contains(err.Error(), "earlier stage") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateExecutablePlanDocumentAcceptsCompleteDocumentWithoutMutation(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{
		Title: "Executable plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Ship complete plans"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID:                 "cp-1",
			Title:              "Implement validator",
			Tasks:              []string{"Add strict validation"},
			AcceptanceCriteria: []string{"Incomplete plans are rejected"},
			Status:             PlanCheckpointStatusPending,
			Order:              1,
		}},
		ActiveCheckpointID: "cp-1",
	}
	before := clonePlanDocument(doc)
	if err := ValidateExecutablePlanDocument(doc); err != nil {
		t.Fatalf("validate complete document: %v", err)
	}
	if !reflect.DeepEqual(doc, before) {
		t.Fatalf("validator mutated document:\n got %#v\nwant %#v", doc, before)
	}
}

func assertExecutablePlanIssue(t *testing.T, validationErr *ExecutablePlanValidationError, field, message string) {
	t.Helper()
	for _, issue := range validationErr.Issues {
		if issue.Field == field && issue.Message == message {
			return
		}
	}
	t.Fatalf("issues = %#v, want field %q message %q", validationErr.Issues, field, message)
}
