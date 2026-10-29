package tool

import (
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Task tool errors name the id that failed and the action that lists the
// right ones, so the Orchestrator's next call can be correct.

// WorkflowTasksHint tells the reader of a list_tasks page that left workflow
// tasks out how to list them. include_workflow_tasks is not in the tool schema
// (the Orchestrator's tool budget); the page names it when it applies.
const WorkflowTasksHint = "include_workflow_tasks: true lists the workflow tasks (reviewer runs) counted here"

func projectNotFoundError(projectID string) error {
	return fmt.Errorf("project %q not found; manage_projects action=list lists your projects and their ids", projectID)
}

func projectTaskNotFoundError(projectID, taskID string) error {
	return fmt.Errorf("task %q not found in project %q; manage_projects action=list_tasks project_id=%s lists its tasks (scope all for older ones)", taskID, projectID, projectID)
}

// checkProjectTaskWaitIDs validates wait_tasks ids on this machine before the
// wait is recorded: each distinct, a task of the project, not archived and
// started. The error names the first bad id and what to do. It returns each
// task's title (its id when untitled), in ids order, for the wait notice.
func (r *Runtime) checkProjectTaskWaitIDs(account, projectID string, ids []string) ([]string, error) {
	titles := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
			return nil, fmt.Errorf("task_ids has an empty or padded id %q; pass ids as list_tasks shows them", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("task_ids lists %s twice; pass each task once", id)
		}
		seen[id] = true
		task, found, err := r.projects.GetProjectTask(account, projectID, id)
		if err != nil {
			return nil, err
		}
		if !found || task == nil {
			return nil, projectTaskNotFoundError(projectID, id)
		}
		if task.Archived {
			return nil, fmt.Errorf("task %s is archived; unarchive_task it first, or wait on another task", id)
		}
		task.EnsureTaskAttempts()
		if a := task.ActiveAttempt(); a == nil || a.SessionID == "" {
			return nil, errors.New("task " + id + " " + pebblestore.ProjectTaskNotStartedReason(task.Status))
		}
		title := task.Title
		if strings.TrimSpace(title) == "" {
			title = id
		}
		titles = append(titles, title)
	}
	return titles, nil
}
