package run

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

func TestPlanManageLegacyFollowupActionsAreDisabled(t *testing.T) {
	runSvc, _, cleanup := newPlanManageRunTestService(t)
	defer cleanup()
	for _, action := range []string{"request_followup_checkpoint", "request-followup-checkpoint", "followup_checkpoint", "request_changes"} {
		args := `{"action":"` + action + `","change_request":"legacy"}`
		if _, err := runSvc.executePlanManageTool("unused-session", args, ""); err == nil || !strings.Contains(err.Error(), "disabled") || !strings.Contains(err.Error(), "transition_checkpoint_boundary") {
			t.Fatalf("legacy follow-up action %q error = %v", action, err)
		}
	}
}

func TestCheckpointBoundaryToolPayloadIsTerminalAndCarriesRunIdentity(t *testing.T) {
	payload := map[string]any{
		"action":               "transition_checkpoint_boundary",
		"next_action":          "run_checkpoint_with_current_context",
		"next_run_id":          "run-next",
		"parent_turn_terminal": true,
	}
	raw, err := marshalPlanManagePayload(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if !strings.Contains(raw, `"parent_turn_terminal":true`) || !strings.Contains(raw, `"next_run_id":"run-next"`) {
		t.Fatalf("terminal payload = %s", raw)
	}
	if !providerManagedToolRequiresTurnRestart(tool.Call{Name: "plan_manage"}, tool.Result{Output: raw}) {
		t.Fatal("checkpoint boundary result did not terminate the parent provider turn")
	}
}
