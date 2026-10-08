package api

import (
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: reopened assignments must receive verbatim actionable feedback without
// inheriting an old planning mandate or permission grant. projectTaskFollowupContext
// owns the seed appended by deployment and recovery; testing its returned payload
// is the narrowest layer for this prompt contract, not evidence of model behavior.
func TestProjectTaskFollowupContextExecutionContract(t *testing.T) {
	for _, priorStatus := range []string{"completed", "pending_approval"} {
		t.Run(priorStatus, func(t *testing.T) {
			request := "Implement the requested correction within the existing scope."
			task := &pebblestore.ProjectTaskRecord{
				ID: "task", ProjectID: "project", ActiveAttemptID: "followup",
				Attempts: []pebblestore.ProjectTaskAttempt{
					{ID: "initial", Status: priorStatus, Summary: "Historical large plan", PlanBinding: &pebblestore.ProjectTaskPlanBinding{PlanID: "old-plan", SessionID: "old-session", DefinitionRevision: 2}},
					{ID: "followup", Request: request},
				},
			}
			before, err := json.Marshal(task)
			if err != nil {
				t.Fatal(err)
			}
			seed := projectTaskFollowupContext(task)
			for _, required := range []string{
				"Execute the requested changes; use concise internal steps as needed",
				"Do not reflexively propose a fresh approval plan or inherit historical planning mode",
				"Preserve existing requirements and authorization",
				"immutable evidence, not the current assignment or a new approval",
				"explicitly requests a new plan",
				"genuine scope or permission decision requires review",
				"same task and current attempt",
				"Never auto-accept a proposed plan",
				"old-plan",
			} {
				if !strings.Contains(seed, required) {
					t.Errorf("follow-up seed missing %q", required)
				}
			}
			if !strings.HasSuffix(seed, "User/orchestrator follow-up request:\n"+request) {
				t.Fatal("current assignment was lost or rewritten")
			}
			after, err := json.Marshal(task)
			if err != nil || string(before) != string(after) {
				t.Fatal("building follow-up context mutated task history")
			}
		})
	}
}

// Purpose: the execute-first exception belongs only to reopened attempts, not
// initial planning. The seed boundary is the narrowest layer proving isolation.
func TestProjectTaskFollowupContextInitialUnchanged(t *testing.T) {
	for _, task := range []*pebblestore.ProjectTaskRecord{
		{},
		{ActiveAttemptID: "initial", Attempts: []pebblestore.ProjectTaskAttempt{{ID: "initial"}}},
	} {
		if got := projectTaskFollowupContext(task); got != "" {
			t.Fatalf("initial task received follow-up override: %q", got)
		}
	}
}
