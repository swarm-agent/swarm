package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Explicitly not a PlanDocument or TaskProgramRecord: detail remains on demand.
type projectTaskBoardRelated struct {
	Plan             *pebblestore.ProjectTaskPlanSummary `json:"plan,omitempty"`
	PlanBindingStale bool                                `json:"plan_binding_stale,omitempty"`
	Program          *projectTaskBoardProgram            `json:"program,omitempty"`
}
type projectTaskBoardProgram struct {
	ID    string                `json:"program_id"`
	State string                `json:"state"`
	Jobs  []projectTaskBoardJob `json:"jobs"`
}
type projectTaskBoardJob struct {
	ID                 string   `json:"job_id"`
	SessionID          string   `json:"current_session_id,omitempty"`
	RunID              string   `json:"current_run_id,omitempty"`
	ExcludedSessionIDs []string `json:"excluded_session_ids,omitempty"`
}
type projectTaskBoardRow struct {
	pebblestore.ProjectTaskRecord
	BoardSummary projectTaskBoardRelated `json:"board_summary"`
	// Progress is where the task is and what happened (task_progress.go).
	Progress *taskProgress `json:"progress,omitempty"`
	// Reviews counts the workflow reviewer tasks of this task a page left
	// out (include_workflow_tasks=false).
	Reviews int `json:"-"`
}

// MarshalJSON keeps the task's privacy-aware encoder without promoting it over
// the entire row, which would silently omit board_summary (even when empty).
func (row projectTaskBoardRow) MarshalJSON() ([]byte, error) {
	task, err := json.Marshal(row.ProjectTaskRecord)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(task, &fields); err != nil {
		return nil, err
	}
	summary, err := json.Marshal(row.BoardSummary)
	if err != nil {
		return nil, err
	}
	fields["board_summary"] = summary
	if row.Progress != nil {
		if fields["progress"], err = json.Marshal(row.Progress); err != nil {
			return nil, err
		}
	}
	if row.Reviews > 0 {
		fields["reviews"] = json.RawMessage(strconv.Itoa(row.Reviews))
	}
	return json.Marshal(fields)
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
			if sid == "" {
				sid = job.ChildSessionID
			}
			if _, ok, _ := reader.GetSession(sid); !ok {
				continue
			}
			item := projectTaskBoardJob{ID: job.JobID, SessionID: sid, RunID: job.CurrentRunID}
			for _, generation := range job.GenerationHistory {
				if generation.SessionID != "" && generation.SessionID != sid {
					item.ExcludedSessionIDs = append(item.ExcludedSessionIDs, generation.SessionID)
				}
			}
			out.Program.Jobs = append(out.Program.Jobs, item)
		}
	}
	return out
}

// projectTaskHiddenReviewReads bounds the full task reads one page makes to
// count reviews whose board summary does not carry the reviewed task.
const projectTaskHiddenReviewReads = 200

// projectTaskHiddenReviews counts, per reviewed task, the workflow reviewer
// tasks a page left out (include_workflow_tasks=false). Board summaries do
// not carry a reviewer's review job, so a hidden workflow task without one is
// read in full (at most projectTaskHiddenReviewReads per page); partial
// reports that the bound left some uncounted.
func projectTaskHiddenReviews(db *pebblestore.SessionStore, account, projectID string, page pebblestore.ProjectTaskListPage) (map[string]int, bool) {
	reviews := map[string]int{}
	reads, partial := 0, false
	for _, hidden := range page.HiddenWorkflowTasks {
		target := pebblestore.ProjectTaskReviewedTaskID(hidden)
		if target == "" && hidden.ReviewJob == "" {
			if reads >= projectTaskHiddenReviewReads {
				partial = true
				continue
			}
			reads++
			full, found, err := db.GetProjectTask(account, projectID, hidden.ID)
			if err != nil {
				partial = true
				continue
			}
			if found && full != nil {
				target = pebblestore.ProjectTaskReviewedTaskID(*full)
			}
		}
		if target != "" {
			reviews[target]++
		}
	}
	return reviews, partial
}

// projectTaskListNow is the clock the current scope is measured against.
var projectTaskListNow = time.Now

// projectTaskListParams are the query parameters that select a filtered page
// instead of the whole board.
var projectTaskListParams = []string{"status", "exclude_status", "query", "agent", "origin_session_id", "limit", "cursor", "scope", "include_workflow_tasks"}

// projectTaskListFilterFromQuery reads the list parameters. paged is false
// when none is present, so a caller without parameters keeps the full board.
func projectTaskListFilterFromQuery(q url.Values, archivedView bool) (pebblestore.ProjectTaskListFilter, bool, error) {
	var f pebblestore.ProjectTaskListFilter
	paged := false
	for _, key := range projectTaskListParams {
		if q.Has(key) {
			paged = true
		}
	}
	if !paged {
		return f, false, nil
	}
	var err error
	if f.Scope, err = pebblestore.NormalizeProjectTaskScope(q.Get("scope")); err != nil {
		return f, true, err
	}
	if archivedView {
		if f.Scope != "" {
			return f, true, errors.New("scope applies to tasks that are not archived; omit it with view=archived")
		}
		f.IncludeArchived = true
	} else if f.Scope == "" {
		f.Scope = pebblestore.ProjectTaskScopeAll
	}
	if f.Limit, err = pebblestore.ParseProjectTaskListLimit(q.Get("limit")); err != nil {
		return f, true, err
	}
	if f.Cursor, err = pebblestore.ParseProjectTaskListCursor(q.Get("cursor")); err != nil {
		return f, true, err
	}
	f.Statuses = pebblestore.ParseProjectTaskStatuses(q.Get("status"))
	f.ExcludeStatuses = pebblestore.ParseProjectTaskStatuses(q.Get("exclude_status"))
	f.Query = strings.TrimSpace(q.Get("query"))
	f.Agent = strings.TrimSpace(q.Get("agent"))
	f.OriginSessionID = strings.TrimSpace(q.Get("origin_session_id"))
	f.WorkerOnly = q.Get("worker_only") == "true"
	f.WorkerID = strings.TrimSpace(q.Get("worker_id"))
	// Workflow tasks (reviewer runs and other workflow agent steps) are
	// listed unless the caller asks to leave them out, as the Orchestrator's
	// list_tasks does; they are then counted on the task they review.
	switch value := strings.TrimSpace(q.Get("include_workflow_tasks")); value {
	case "", "true":
	case "false":
		f.ExcludeWorkflowTasks = true
	default:
		return f, true, fmt.Errorf("include_workflow_tasks must be true or false, not %q", value)
	}
	return f, true, nil
}

func writeProjectTaskBoard(w http.ResponseWriter, rows []projectTaskBoardRow, page map[string]any, stats pebblestore.ProjectTaskReadStats, started time.Time, reconcile time.Duration) {
	encodeStart := time.Now()
	response := map[string]any{"tasks": rows, "count": len(rows)}
	for key, value := range page {
		response[key] = value
	}
	body, err := json.Marshal(response)
	encode := time.Since(encodeStart)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Server-Timing", fmt.Sprintf("task_scan;dur=%.3f, task_related;dur=%.3f, task_reconcile;dur=%.3f, task_encode;dur=%.3f, task_total;dur=%.3f", float64(stats.ScanElapsed)/float64(time.Millisecond), float64(stats.RelatedElapsed)/float64(time.Millisecond), float64(reconcile)/float64(time.Millisecond), float64(encode)/float64(time.Millisecond), float64(time.Since(started))/float64(time.Millisecond)))
	for name, value := range map[string]int64{
		"X-Task-Scanned-Rows": int64(stats.ScannedRows), "X-Task-Decoded-Bytes": stats.DecodedBytes,
		"X-Task-Related-Reads": int64(stats.RelatedRecordReads), "X-Task-Related-Bytes": stats.RelatedDecodedBytes,
		"X-Task-Backfill-Rows": int64(stats.BackfillRows), "X-Task-Backfill-Bytes": stats.BackfillBytes,
		"X-Task-Response-Bytes": int64(len(body)),
	} {
		w.Header().Set(name, strconv.FormatInt(value, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
