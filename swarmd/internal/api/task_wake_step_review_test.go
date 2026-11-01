package api

import (
	"strings"
	"testing"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a step result's blocking list names only real BLOCKING findings
// of a review whose verdict is block, and a failed step's error keeps its
// head and tail with a read of the delivery that holds the longer recorded
// reason. Threat (review findings): an ok review saying "No blocking issues"
// or "BLOCKING: none" woke the Orchestrator with blocking findings, and a
// step's error lost its end. Boundary: deliveryStepsResult, the one builder
// of steps_result for task progress, wake rows and get_task.
func TestDeliveryStepsResultListsOnlyRealBlockingFindings(t *testing.T) {
	reason := "the script could not run: " + strings.Repeat("detail ", 600) + "END-OF-REASON"
	d := jobautomation.Delivery{ID: "jdel-ok", State: jobautomation.StateFailed, Steps: []jobautomation.StepRecord{
		{ID: "review", Type: jobautomation.StepAgent, State: jobautomation.StateSucceeded,
			Outcome: map[string]any{"verdict": "ok", "findings": "No blocking issues.\nBLOCKING: none\nnot blocking: rename X"}},
		{ID: "warn", Type: jobautomation.StepAgent, State: jobautomation.StateSucceeded,
			Outcome: map[string]any{"verdict": "warn", "findings": "BLOCKING: would be, but the verdict is warn"}},
		{ID: "block", Type: jobautomation.StepAgent, State: jobautomation.StateSucceeded,
			Outcome: map[string]any{"verdict": "block", "findings": "None blocking in the docs.\nBLOCKING: the gate never ran"}},
		{ID: "gate", Type: jobautomation.StepRun, State: jobautomation.StateFailed, Reason: reason},
	}}
	steps := deliveryStepsResult(d)
	if len(steps) != 4 {
		t.Fatalf("finished steps: %+v", steps)
	}
	if steps[0].Verdict != "ok" || steps[0].Blocking != nil || steps[1].Blocking != nil {
		t.Fatalf("a passing review listed blocking findings: ok %q warn %q", steps[0].Blocking, steps[1].Blocking)
	}
	if len(steps[2].Blocking) != 1 || steps[2].Blocking[0] != "BLOCKING: the gate never ran" {
		t.Fatalf("block review findings: %q", steps[2].Blocking)
	}
	gate := steps[3]
	if !gate.ErrorTruncated || utf8.RuneCountInString(gate.Error) > pebblestore.ProjectTaskWakeReasonRunes || !strings.HasPrefix(gate.Error, "the script could not run:") || !strings.HasSuffix(gate.Error, "END-OF-REASON") || gate.More != deliveryFindingsMore("jdel-ok") {
		t.Fatalf("long step error not head+tail with a read: %d runes, more %q", utf8.RuneCountInString(gate.Error), gate.More)
	}
	// The recorded reason (what more reads) is longer than the shown error.
	if utf8.RuneCountInString(reason) <= pebblestore.ProjectTaskWakeReasonRunes || utf8.RuneCountInString(reason) > jobautomation.MaxStepReasonRunes {
		t.Fatalf("fixture reason must sit between the shown and the recorded bound: %d", utf8.RuneCountInString(reason))
	}
}
