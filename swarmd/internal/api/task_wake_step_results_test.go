package api

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// wakeStepsDelivery is a sent-back delivery of a review loop: a run step
// that passed, a gate run step that failed with a long log, a review that
// blocked with BLOCKING findings, a push that failed with a long reason, the
// reopen that sent the work back on the push's failure, and a console check
// whose script never started (status error, error_kind checkout).
func wakeStepsDelivery() jobautomation.Delivery {
	code := 1
	var log strings.Builder
	for i := 1; i <= 50; i++ {
		log.WriteString("gate line " + strconv.Itoa(i) + "\n")
	}
	log.WriteString("--- FAIL: TestWakeRow\nFAIL")
	pushReason := "exited with code 1: NOT PUSHED: rebase needed: dev moved " + strings.Repeat("and changed shared files ", 120) + "END-OF-REASON"
	return jobautomation.Delivery{ID: "jdel-wake", AutomationID: "auto-1", AutomationName: "review loop", State: jobautomation.StateSentBack, Round: 1,
		SentBack: &jobautomation.SentBack{Step: "rebase", Round: 2, Reason: "push failed"},
		Steps: []jobautomation.StepRecord{
			{ID: "lint", Type: jobautomation.StepRun, State: jobautomation.StateSucceeded, StartedAt: 1000, FinishedAt: 2500, Output: "lint ok"},
			{ID: "gate", Type: jobautomation.StepRun, State: jobautomation.StateFailed, StartedAt: 2500, FinishedAt: 9000, ExitCode: &code, Reason: "exited with code 1: FAIL", LogTail: log.String(), Output: "FAIL"},
			{ID: "review", Type: jobautomation.StepAgent, State: jobautomation.StateSucceeded, StartedAt: 9000, FinishedAt: 20000,
				Outcome: map[string]any{"verdict": "block", "findings": "BLOCKING: summary is the first lines\nNon-blocking: rename a helper\nBLOCKING: failure cut mid-sentence"}},
			{ID: "push", Type: jobautomation.StepRun, State: jobautomation.StateFailed, StartedAt: 20000, FinishedAt: 21000, ExitCode: &code, Reason: pushReason},
			{ID: "rebase", Type: jobautomation.StepReopen, State: jobautomation.StateSucceeded, StartedAt: 21000, FinishedAt: 21500},
			{ID: "console", Type: jobautomation.StepRun, State: jobautomation.StateError, ErrorKind: jobautomation.ErrorKindCheckout, StartedAt: 21500, FinishedAt: 22000,
				Reason: "the script could not run: could not make the step's checkout in the environment: object d1d5 of the folder's repository could not be read: permission denied"},
			{ID: "after", Type: jobautomation.StepAgent, State: jobautomation.StatePending},
		},
		Definition: []jobautomation.Step{{ID: "lint"}, {ID: "gate"}, {ID: "review"}, {ID: "push"}, {ID: "rebase", Type: jobautomation.StepReopen, If: []jobautomation.Condition{{Step: "push", Status: jobautomation.StateFailed}}}, {ID: "console", Type: jobautomation.StepRun}, {ID: "after"}},
	}
}

// Purpose: each finished workflow step's outcome reaches the wake row and
// get_task the same: a run step (status, duration, output tail), a failing
// run step (exit code, error, last 30 lines of its log), a review step
// (verdict and its BLOCKING findings only), with the push's failure kept
// head and tail, a run step that never started (status error with its
// error_kind, never failed); steps not run yet have no result. Then
// (parity) the task
// route's progress, the wake row's view of it (ProjectTaskWaitProgressFrom,
// which the local hook and a linked machine's get_task both feed) and MCP
// get_task's progress.workflow (tool.ProjectTaskWorkflow) carry identical
// step results and reopen failure; the wait contract and the wait_tasks and
// get_task descriptions name the fields. Threat: the wake only saying
// workflow_state/step; the API, the wake rows and MCP drifting apart.
// Boundary: deliveryStepsResult and computeTaskProgress (the route's
// progress), pebblestore.ProjectTaskWaitProgressFrom, tool.ProjectTaskWorkflow
// and the manage_projects catalog; pure functions over real records, the
// narrowest layer that holds all three surfaces together.
func TestTaskWakeStepResultsMatchAcrossAPIWakeAndMCP(t *testing.T) {
	d := wakeStepsDelivery()
	steps := deliveryStepsResult(d)
	if len(steps) != 6 {
		t.Fatalf("finished steps: %d %+v", len(steps), steps)
	}
	lint, gate, review, push, console := steps[0], steps[1], steps[2], steps[3], steps[5]
	if console.Status != jobautomation.StateError || console.ErrorKind != jobautomation.ErrorKindCheckout || console.ExitCode != nil || !strings.Contains(console.Error, "permission denied") {
		t.Fatalf("step that never started: %+v", console)
	}
	if gate.ErrorKind != "" || push.ErrorKind != "" {
		t.Fatalf("a failed step carries an error_kind: %+v %+v", gate, push)
	}
	if lint.Status != jobautomation.StateSucceeded || lint.DurationMs != 1500 || lint.OutputTail != "lint ok" {
		t.Fatalf("run step: %+v", lint)
	}
	gateLines := strings.Split(gate.OutputTail, "\n")
	if gate.Status != jobautomation.StateFailed || gate.ExitCode == nil || *gate.ExitCode != 1 || gate.Error != "exited with code 1: FAIL" || len(gateLines) != pebblestore.ProjectTaskStepOutputLines || gateLines[len(gateLines)-1] != "FAIL" || !gate.OutputTruncated || !strings.Contains(gate.More, "jdel-wake") {
		t.Fatalf("failing run step: %+v (%d lines)", gate, len(gateLines))
	}
	if review.Verdict != "block" || !reflect.DeepEqual(review.Blocking, []string{"BLOCKING: summary is the first lines", "BLOCKING: failure cut mid-sentence"}) {
		t.Fatalf("review step: %+v", review)
	}
	if !push.ErrorTruncated || !strings.HasPrefix(push.Error, "exited with code 1: NOT PUSHED") || !strings.HasSuffix(push.Error, "END-OF-REASON") {
		t.Fatalf("push error not head+tail: %+v", push)
	}

	// The task route's progress (taskWaitProgressFor fills steps_result).
	task := pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Status: "needs_review", Agent: "coder"}
	p := computeTaskProgress(task, []jobautomation.Delivery{d}, nil, 30000)
	p.Workflow.Deliveries[0].StepsResult = steps
	view := p.Workflow.Deliveries[0]
	// The review before the push blocked, so the reopen is check_failed
	// (jobautomation.ReopenCause); its failure is the push's reason.
	if view.ReopenCause != pebblestore.ProjectTaskReopenCheckFailed || !view.ReopenFailureTruncated || !strings.HasPrefix(view.ReopenFailure, "push failed: exited with code 1: NOT PUSHED") || !strings.HasSuffix(view.ReopenFailure, "END-OF-REASON") {
		t.Fatalf("route reopen failure: cause %q truncated %v %q", view.ReopenCause, view.ReopenFailureTruncated, view.ReopenFailure)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	// The wake row's view, as the local hook and a linked get_task feed it.
	wake := pebblestore.ProjectTaskWaitProgressFrom(json.RawMessage(raw))
	if wake == nil || !reflect.DeepEqual(wake.StepsResult, steps) || wake.ReopenFailure != view.ReopenFailure || !wake.ReopenFailureTruncated || wake.DeliveryID != "jdel-wake" || wake.DeliveryRun != 1 || wake.DeliveryRun != view.Run {
		t.Fatalf("wake progress drifted from the route: %+v", wake)
	}
	// The bounded wake row (ClipProjectTaskStepResults, also a linked
	// machine's) keeps status error and error_kind.
	rowJSON, _ := json.Marshal(pebblestore.ClipProjectTaskStepResults(wake.StepsResult))
	var rows []map[string]any
	if err := json.Unmarshal(rowJSON, &rows); err != nil || len(rows) != 6 || rows[5]["id"] != "console" || rows[5]["status"] != "error" || rows[5]["error_kind"] != "checkout" {
		t.Fatalf("wake row lost status error or error_kind: %s", rowJSON)
	}
	for _, row := range rows[:5] {
		if _, ok := row["error_kind"]; ok {
			t.Fatalf("a step that ran carries error_kind: %v", row)
		}
	}

	// MCP get_task's progress.workflow.
	wfRaw, _ := json.Marshal(tool.ProjectTaskWorkflow(json.RawMessage(raw)))
	var wf struct {
		DeliveryRun            int                                 `json:"delivery_run"`
		ReopenFailure          string                              `json:"reopen_failure"`
		ReopenFailureTruncated bool                                `json:"reopen_failure_truncated"`
		StepsResult            []pebblestore.ProjectTaskStepResult `json:"steps_result"`
		More                   string                              `json:"more"`
	}
	if err := json.Unmarshal(wfRaw, &wf); err != nil || !reflect.DeepEqual(wf.StepsResult, steps) || wf.DeliveryRun != wake.DeliveryRun || wf.ReopenFailure != view.ReopenFailure || !wf.ReopenFailureTruncated || !strings.Contains(wf.More, "jdel-wake") {
		t.Fatalf("get_task workflow drifted from the route: %s", wfRaw)
	}

	// The contract and the tool descriptions name the fields.
	contract, err := os.ReadFile("project_task_wait_contract.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"summary_source", "`handoff`", "read_full_args", "result_bytes", "failure_truncated", "reopen_failure_truncated", "reopen_failure_more", "delivery_id", "delivery_attempt_id", "delivery_run", "previous_attempt", "previous_run", "workflow_steps_result", "steps_result", "output_tail", "blocking", "error_kind", "ProjectTaskWakeRowKeys", "agent_stall", "`stalled`", "sleep_polling", "long_command", "no_activity", "agent_poll_stall_minutes"} {
		if !strings.Contains(string(contract), key) {
			t.Fatalf("wait contract does not name %s", key)
		}
	}
	descriptions := map[string]string{}
	for _, catalog := range tool.Catalog() {
		if catalog.Name != "manage_projects" {
			continue
		}
		for _, a := range catalog.Actions {
			descriptions[a.Action] = a.Description
		}
	}
	for action, keys := range map[string][]string{"wait_tasks": {"handoff", "final result message", "read_full_args", "reopen_failure", "workflow_steps_result", "delivery_attempt_id", "delivery_run", "previous_run", "stalled", "agent_stall", "sleep_polling", "long_command", "no_activity"}, "get_task": {"reopen_failure", "steps_result", "delivery_attempt_id", "delivery_run", "previous_run", "BLOCKING"}} {
		for _, key := range keys {
			if !strings.Contains(descriptions[action], key) {
				t.Fatalf("%s description does not name %s: %q", action, key, descriptions[action])
			}
		}
	}
}
