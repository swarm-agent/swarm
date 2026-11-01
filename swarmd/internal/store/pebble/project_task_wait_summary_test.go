package pebblestore

import (
	"strings"
	"testing"
)

// Purpose: a wait_tasks outcome row whose summary is not the agent's final
// message (its last error) keeps a long one's head and tail; the row then
// says so (result_truncated, result_bytes) and how to read all of it
// (read_full, read_full_args); a summary that fits is unchanged with no flag.
// Threat: the Orchestrator reporting from a preview of a long result, taking
// it for the whole. Boundary: setProjectTaskWakeResult, which both the local
// and the linked outcome rows (projectTaskWatchLocalOutcome,
// projectTaskWatchRemoteOutcome) use. Narrowest layer: the row helper itself;
// the outcome builders only pass it their summary and read hints.
func TestProjectTaskWaitSummaryFlagsAShortenedResult(t *testing.T) {
	long := "first " + strings.Repeat("é result line ", 400) + " last"
	row := map[string]any{}
	setProjectTaskWakeResult(row, ProjectTaskWakeResult{Summary: long, Source: ProjectTaskSummaryLastError, ReadFull: "read it here", ReadFullArgs: map[string]any{"action": "read_messages"}})
	clipped, _ := row["summary"].(string)
	if !strings.HasPrefix(clipped, "first ") || !strings.HasSuffix(clipped, " last") || len(clipped) >= len(long) {
		t.Fatalf("summary not a shortened head and tail: %d of %d bytes", len(clipped), len(long))
	}
	if row["result_truncated"] != true || row["result_bytes"] != len(long) || row["read_full"] != "read it here" || row["read_full_args"] == nil || row["summary_source"] != ProjectTaskSummaryLastError {
		t.Fatalf("shortened summary is not flagged: %v", row)
	}
	short := map[string]any{}
	setProjectTaskWakeResult(short, ProjectTaskWakeResult{Summary: "Done.", ReadFull: "read it here"})
	if short["summary"] != "Done." || len(short) != 1 {
		t.Fatalf("short summary changed: %v", short)
	}
}
