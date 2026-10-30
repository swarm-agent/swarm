package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

// Purpose: get_task's compact view shows a shortened report; when it does,
// it says so (result_truncated, the full report's size in result_bytes) and
// how to read all of it as the exact call (result.next, which the envelope
// lifts to its next: get_task detail=full at the report's attempt here,
// read_task_session on a linked machine), while a report that fits is
// unchanged and unflagged. The view keeps its 2 KB budget.
// Threat: the Orchestrator or an MCP client taking a 1 KB preview of a long
// report for the whole result, or told only in prose how to read the rest.
// Boundary: compactProjectTask (markProjectTaskResultCut,
// fitProjectTaskView) and CompactProjectTaskAnswer for linked machines.
// Narrowest layer: the pure view functions both paths share.
func TestCompactProjectTaskFlagsAShortenedResult(t *testing.T) {
	task := heavyFinishedTask("acct", "proj", "task-1")
	full := strings.TrimSpace(task.Attempts[1].Summary)
	view := compactProjectTask(projectTaskRead{ProjectTaskRecord: *task}, false)
	if view.Result == nil || !view.Result.ResultTruncated || view.Result.ResultBytes != len(full) || !strings.HasPrefix(full, strings.TrimSuffix(view.Result.Summary, "…")) {
		t.Fatalf("shortened report is not flagged: %+v", view.Result)
	}
	if next, _ := json.Marshal(view.Result.Next); string(next) != `{"action":"get_task","cursor":1,"detail":"full","limit":1,"project_id":"proj","task_id":"task-1"}` || view.Result.ReadFull == "" {
		t.Fatalf("the cut report does not name the call that reads it: %s %q", next, view.Result.ReadFull)
	}
	if raw, _ := json.Marshal(view); len(raw) > projectTaskViewBytes+200 {
		t.Fatalf("view grew to %d bytes", len(raw))
	}

	answer, _ := json.Marshal(map[string]any{"task": task})
	compact, err := CompactProjectTaskAnswer("get_task", "", answer)
	if err != nil {
		t.Fatal(err)
	}
	var linked struct {
		Task ProjectTaskView `json:"task"`
	}
	if err := json.Unmarshal(compact, &linked); err != nil || linked.Task.Result == nil || !linked.Task.Result.ResultTruncated || linked.Task.Result.Next["action"] != FleetReadTaskSessionAction || linked.Task.Result.Next["task_id"] != "task-1" {
		t.Fatalf("linked view does not flag the shortened report: %s %v", compact, err)
	}

	task.Attempts[1].Summary = "Done: one short report."
	short := compactProjectTask(projectTaskRead{ProjectTaskRecord: *task}, false)
	if short.Result == nil || short.Result.Summary != "Done: one short report." || short.Result.ResultTruncated || short.Result.ResultBytes != 0 || short.Result.ReadFull != "" || short.Result.Next != nil {
		t.Fatalf("short report changed: %+v", short.Result)
	}
}
