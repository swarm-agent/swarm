package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

// Purpose: get_task's compact view shows a shortened report; when it does,
// it says so (result_truncated, the full report's size in result_bytes) and
// how to read all of it (read_full: read_messages on the task session here,
// read_task_session on a linked machine), while a report that fits is
// unchanged and unflagged. The view keeps its 2 KB budget.
// Threat: the Orchestrator taking a 1 KB preview of a long report for the
// whole result.
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
	if !strings.Contains(view.Result.ReadFull, "read_messages session_id=task-session-1") {
		t.Fatalf("read_full does not name the task session: %q", view.Result.ReadFull)
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
	if err := json.Unmarshal(compact, &linked); err != nil || linked.Task.Result == nil || !linked.Task.Result.ResultTruncated || !strings.Contains(linked.Task.Result.ReadFull, "read_task_session") {
		t.Fatalf("linked view does not flag the shortened report: %s %v", compact, err)
	}

	task.Attempts[1].Summary = "Done: one short report."
	short := compactProjectTask(projectTaskRead{ProjectTaskRecord: *task}, false)
	if short.Result == nil || short.Result.Summary != "Done: one short report." || short.Result.ResultTruncated || short.Result.ResultBytes != 0 || short.Result.ReadFull != "" {
		t.Fatalf("short report changed: %+v", short.Result)
	}
}
