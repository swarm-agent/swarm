package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Explicitly not a PlanDocument or TaskProgramRecord: detail remains on demand.
type projectTaskBoardRelated struct {
	Plan *pebblestore.ProjectTaskPlanSummary `json:"plan,omitempty"`
	PlanBindingStale bool `json:"plan_binding_stale,omitempty"`
	Program *projectTaskBoardProgram `json:"program,omitempty"`
}
type projectTaskBoardProgram struct {
	ID string `json:"program_id"`
	State string `json:"state"`
	Jobs []projectTaskBoardJob `json:"jobs"`
}
type projectTaskBoardJob struct {
	ID string `json:"job_id"`
	SessionID string `json:"current_session_id,omitempty"`
	RunID string `json:"current_run_id,omitempty"`
	ExcludedSessionIDs []string `json:"excluded_session_ids,omitempty"`
}
type projectTaskBoardRow struct {
	pebblestore.ProjectTaskRecord
	BoardSummary projectTaskBoardRelated `json:"board_summary"`
}

func projectTaskBoardSummary(task *pebblestore.ProjectTaskRecord, reader *pebblestore.ProjectTaskBoardReader) projectTaskBoardRelated {
	var out projectTaskBoardRelated
	if task.PlanBinding != nil && task.PlanBinding.PlanID != "" {
		out.PlanBindingStale = true
		if plan, ok, _ := reader.GetPlan(task.SessionID, task.PlanBinding.PlanID); ok {
			summary := pebblestore.SummarizeProjectTaskPlan(plan)
			out.Plan, out.PlanBindingStale = &summary, false
		}
	}
	if program, ok := reader.CurrentTaskProgram(task); ok {
		out.Program = &projectTaskBoardProgram{ID: program.ProgramID, State: program.State, Jobs: make([]projectTaskBoardJob, 0, len(program.Jobs))}
		for _, job := range program.Jobs {
			sid := job.CurrentSessionID
			if sid == "" { sid = job.ChildSessionID }
			if _, ok, _ := reader.GetSession(sid); !ok { continue }
			item := projectTaskBoardJob{ID: job.JobID, SessionID: sid, RunID: job.CurrentRunID}
			for _, generation := range job.GenerationHistory {
				if generation.SessionID != "" && generation.SessionID != sid { item.ExcludedSessionIDs = append(item.ExcludedSessionIDs, generation.SessionID) }
			}
			out.Program.Jobs = append(out.Program.Jobs, item)
		}
	}
	return out
}

func writeProjectTaskBoard(w http.ResponseWriter, rows []projectTaskBoardRow, stats pebblestore.ProjectTaskReadStats, started time.Time, reconcile time.Duration) {
	encodeStart := time.Now()
	body, err := json.Marshal(map[string]any{"tasks": rows, "count": len(rows)})
	encode := time.Since(encodeStart)
	if err != nil { writeError(w, http.StatusInternalServerError, err); return }
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Server-Timing", fmt.Sprintf("task_scan;dur=%.3f, task_related;dur=%.3f, task_reconcile;dur=%.3f, task_encode;dur=%.3f, task_total;dur=%.3f", float64(stats.ScanElapsed)/float64(time.Millisecond), float64(stats.RelatedElapsed)/float64(time.Millisecond), float64(reconcile)/float64(time.Millisecond), float64(encode)/float64(time.Millisecond), float64(time.Since(started))/float64(time.Millisecond)))
	for name, value := range map[string]int64{
		"X-Task-Scanned-Rows": int64(stats.ScannedRows), "X-Task-Decoded-Bytes": stats.DecodedBytes,
		"X-Task-Related-Reads": int64(stats.RelatedRecordReads), "X-Task-Related-Bytes": stats.RelatedDecodedBytes,
		"X-Task-Backfill-Rows": int64(stats.BackfillRows), "X-Task-Backfill-Bytes": stats.BackfillBytes,
		"X-Task-Response-Bytes": int64(len(body)),
	} { w.Header().Set(name, strconv.FormatInt(value, 10)) }
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
