package session

import (
	"errors"
	"reflect"
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
