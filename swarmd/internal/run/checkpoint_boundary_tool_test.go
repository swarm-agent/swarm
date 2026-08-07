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

func TestCheckpointBoundaryToolPayloadContinuesCurrentRun(t *testing.T) {
	payload := map[string]any{
		"action":            "transition_checkpoint_boundary",
		"next_action":       "continue_current_run",
		"run_id":            "run-current",
		"context_preserved": true,
	}
	raw, err := marshalPlanManagePayload(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if !strings.Contains(raw, `"next_action":"continue_current_run"`) || !strings.Contains(raw, `"run_id":"run-current"`) || strings.Contains(raw, `"next_run_id"`) || strings.Contains(raw, `"parent_turn_terminal"`) {
		t.Fatalf("current-run payload = %s", raw)
	}
	if providerManagedToolRequiresTurnRestart(tool.Call{Name: "plan_manage"}, tool.Result{Output: raw}) {
		t.Fatal("checkpoint assignment restarted the current provider turn")
	}
}
