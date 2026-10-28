package worktree

import (
	"context"
	"testing"
)

// Purpose: on a host without a project sandbox, a merge's Git inspection gets
// room for a busy machine (agents rendering), while a read-only status check
// keeps its short budget.
func TestHostMergeGetsALongerInspectionBudget(t *testing.T) {
	if got := DeliveryAssessmentBudget(context.Background()); got != deliveryHostBudget {
		t.Fatalf("status budget %s, want %s", got, deliveryHostBudget)
	}
	if got := DeliveryAssessmentBudget(WithDeliveryExecution(context.Background())); got != deliveryHostExecutionBudget {
		t.Fatalf("merge budget %s, want %s", got, deliveryHostExecutionBudget)
	}
	if deliveryHostExecutionBudget <= deliveryHostBudget {
		t.Fatal("a merge must get more than a status read")
	}
}
