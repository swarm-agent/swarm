package pebblestore

import (
	"strings"
	"testing"
)

// Purpose: a wait_tasks outcome row shows a shortened result summary (over
// 2,000 bytes it keeps 500 characters); the row then says so
// (result_truncated, result_bytes) and how to read all of it (read_full),
// and a summary that fits is unchanged with no flag.
// Threat: the Orchestrator reporting from a 500-character preview of a long
// result, taking it for the whole.
// Boundary: setProjectTaskWaitSummary, which both the local and the linked
// outcome rows (projectTaskWatchOutcome, projectTaskWatchRemoteOutcome) use.
// Narrowest layer: the row helper itself; the outcome builders only pass it
// their summary and read hint.
func TestProjectTaskWaitSummaryFlagsAShortenedResult(t *testing.T) {
	long := strings.Repeat("é result line ", 400)
	row := map[string]any{}
	setProjectTaskWaitSummary(row, long, "read it here")
	clipped, _ := row["summary"].(string)
	if !strings.HasPrefix(long, clipped) || len(clipped) >= len(long) {
		t.Fatalf("summary not a shortened prefix: %d of %d bytes", len(clipped), len(long))
	}
	if row["result_truncated"] != true || row["result_bytes"] != len(long) || row["read_full"] != "read it here" {
		t.Fatalf("shortened summary is not flagged: %v", row)
	}
	short := map[string]any{}
	setProjectTaskWaitSummary(short, "Done.", "read it here")
	if short["summary"] != "Done." || len(short) != 1 {
		t.Fatalf("short summary changed: %v", short)
	}
}
