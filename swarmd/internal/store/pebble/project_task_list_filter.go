package pebblestore

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Task list scopes. "current" is what is running, waiting or finished
// recently; "all" is every task that is not archived.
const (
	ProjectTaskScopeCurrent = "current"
	ProjectTaskScopeAll     = "all"

	ProjectTaskListDefaultLimit = 25
	ProjectTaskListMaxLimit     = 100
	// ProjectTaskAgentListMaxLimit bounds one page of the Orchestrator's
	// list_tasks, on this machine and on linked machines.
	ProjectTaskAgentListMaxLimit = 50

	// ProjectTaskRecentFinishMillis is how long a finished task stays current.
	ProjectTaskRecentFinishMillis int64 = 24 * 60 * 60 * 1000
)

// ProjectTaskFinalStatus reports whether a task status is a final outcome.
// needs_review and pending_approval wait on a person, so they are not final.
func ProjectTaskFinalStatus(status string) bool {
	switch status {
	case "completed", "failed", "rejected", "cancelled", "superseded":
		return true
	}
	return false
}

// ProjectTaskListFilter selects and pages task list rows. Every field is
// optional. Archived tasks are kept only when IncludeArchived is set; Scope
// current additionally drops tasks that finished more than 24 hours ago.
type ProjectTaskListFilter struct {
	Statuses        []string
	ExcludeStatuses []string
	Query           string
	Agent           string
	Group           string
	OriginSessionID string
	WorkerID        string
	WorkerOnly      bool
	Scope           string
	IncludeArchived bool
	// ExcludeWorkflowTasks leaves out tasks a workflow step started (its
	// reviewer runs and other agent steps). The page counts them instead:
	// Reviews per reviewed task and WorkflowTasks for those left out.
	ExcludeWorkflowTasks bool
	Limit                int
	Cursor               int
}

// projectTaskWorkflowIDPrefix starts the id of every task a workflow agent
// step starts (api.startBuiltinAgentStep).
const projectTaskWorkflowIDPrefix = "task_wf_"

// ProjectTaskWorkflowSpawned reports whether a workflow step started t, such
// as a reviewer run: its automation is a workflow's, or its id is a workflow
// step's (kept when a follow-up relaunch clears the automation).
func ProjectTaskWorkflowSpawned(t ProjectTaskRecord) bool {
	return strings.HasPrefix(t.AutomationID, "workflow:") || strings.HasPrefix(t.ID, projectTaskWorkflowIDPrefix)
}

// ProjectTaskReviewedTaskID is the task of t's own project that the reviewer
// task t reviews (its review job is "task/<project>/<task>"), or "".
func ProjectTaskReviewedTaskID(t ProjectTaskRecord) string {
	parts := strings.Split(strings.TrimSpace(t.ReviewJob), "/")
	if len(parts) != 3 || parts[0] != "task" || parts[1] != t.ProjectID || parts[2] == "" {
		return ""
	}
	return parts[2]
}

// ProjectTaskListPage is one page of a filtered task list, newest-updated first.
type ProjectTaskListPage struct {
	Tasks        []ProjectTaskRecord
	Total        int
	Cursor       int
	NextCursor   int
	HasNext      bool
	StatusCounts map[string]int
	// With ExcludeWorkflowTasks: HiddenWorkflowTasks are the workflow tasks
	// the other filters matched and the page left out (WorkflowTasks of
	// them), and Reviews counts those that review a task, by reviewed task id.
	Reviews             map[string]int
	WorkflowTasks       int
	HiddenWorkflowTasks []ProjectTaskRecord
}

// ParseProjectTaskStatuses splits one or comma-separated statuses.
func ParseProjectTaskStatuses(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// NormalizeProjectTaskScope validates a scope; "" stays "".
func NormalizeProjectTaskScope(raw string) (string, error) {
	switch scope := strings.ToLower(strings.TrimSpace(raw)); scope {
	case "", ProjectTaskScopeCurrent, ProjectTaskScopeAll:
		return scope, nil
	}
	return "", errors.New("scope must be current or all")
}

// ParseProjectTaskListLimit reads a page size; "" is the default.
func ParseProjectTaskListLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ProjectTaskListDefaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > ProjectTaskListMaxLimit {
		return 0, errors.New("limit must be an integer from 1 to 100")
	}
	return n, nil
}

// ParseProjectTaskListCursor reads an opaque offset cursor; "" is the start.
func ParseProjectTaskListCursor(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > 100000 {
		return 0, errors.New("cursor must come from a previous next_cursor")
	}
	return n, nil
}

// matchesExceptStatus reports whether one task passes every filter except paging. The
// status filters are applied separately so status counts can ignore them.
func (f ProjectTaskListFilter) matchesExceptStatus(t ProjectTaskRecord, nowMillis int64) bool {
	if t.Archived && !f.IncludeArchived {
		return false
	}
	if f.Scope == ProjectTaskScopeCurrent && ProjectTaskFinalStatus(t.Status) && nowMillis-t.UpdatedAt > ProjectTaskRecentFinishMillis {
		return false
	}
	if f.Agent != "" && t.Agent != f.Agent {
		return false
	}
	if f.Group != "" && t.Group != f.Group {
		return false
	}
	if f.OriginSessionID != "" && t.OriginSessionID != f.OriginSessionID {
		return false
	}
	if (f.WorkerOnly || f.WorkerID != "") && t.WorkerID == "" {
		return false
	}
	if f.WorkerID != "" && t.WorkerID != f.WorkerID {
		return false
	}
	if query := strings.ToLower(strings.TrimSpace(f.Query)); query != "" && !strings.Contains(strings.ToLower(t.Title+" "+t.Description), query) {
		return false
	}
	return true
}

// MatchesStatus reports whether a status passes the status filters.
func (f ProjectTaskListFilter) MatchesStatus(status string) bool {
	if len(f.Statuses) > 0 {
		found := false
		for _, s := range f.Statuses {
			if s == status {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, s := range f.ExcludeStatuses {
		if s == status {
			return false
		}
	}
	return true
}

// Select filters tasks, orders them newest-updated first and returns one page.
// It only reads the given records, so callers can select before building any
// per-row detail.
func (f ProjectTaskListFilter) Select(tasks []ProjectTaskRecord, nowMillis int64) ProjectTaskListPage {
	limit := f.Limit
	if limit <= 0 {
		limit = ProjectTaskListDefaultLimit
	}
	if limit > ProjectTaskListMaxLimit {
		limit = ProjectTaskListMaxLimit
	}
	page := ProjectTaskListPage{StatusCounts: map[string]int{}, Reviews: map[string]int{}}
	matched := make([]ProjectTaskRecord, 0)
	for _, t := range tasks {
		if f.ExcludeWorkflowTasks && ProjectTaskWorkflowSpawned(t) {
			if f.matchesExceptStatus(t, nowMillis) && f.MatchesStatus(t.Status) {
				page.WorkflowTasks++
				page.HiddenWorkflowTasks = append(page.HiddenWorkflowTasks, t)
				if reviewed := ProjectTaskReviewedTaskID(t); reviewed != "" {
					page.Reviews[reviewed]++
				}
			}
			continue
		}
		if !f.matchesExceptStatus(t, nowMillis) {
			continue
		}
		page.StatusCounts[t.Status]++
		if f.MatchesStatus(t.Status) {
			matched = append(matched, t)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].UpdatedAt != matched[j].UpdatedAt {
			return matched[i].UpdatedAt > matched[j].UpdatedAt
		}
		if matched[i].CreatedAt != matched[j].CreatedAt {
			return matched[i].CreatedAt > matched[j].CreatedAt
		}
		return matched[i].ID > matched[j].ID
	})
	page.Total = len(matched)
	cursor := f.Cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(matched) {
		cursor = len(matched)
	}
	end := cursor + limit
	if end > len(matched) {
		end = len(matched)
	}
	page.Cursor = cursor
	page.Tasks = append([]ProjectTaskRecord(nil), matched[cursor:end]...)
	if end < len(matched) {
		page.NextCursor, page.HasNext = end, true
	}
	return page
}
