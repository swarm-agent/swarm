package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Project task organization is metadata only. Execution state is owned by the
// lifecycle service; the task's source binding and Git lane are never editable here.
func (r *Runtime) executeManageProjectTasks(scope WorkspaceScope, action string, args map[string]any) (string, error) {
	return r.executeManageProjectTasksContext(context.Background(), scope, action, args)
}

func (r *Runtime) executeManageProjectTasksContext(ctx context.Context, scope WorkspaceScope, action string, args map[string]any) (string, error) {
	account := scope.Principal.AccountScopeID
	if !scope.Principal.Valid() || scope.Principal.UserID == "" || account == "" || scope.Principal.Type != "user" {
		return "", errors.New("project tasks require an authenticated user identity")
	}
	projectID := strings.TrimSpace(asString(args["project_id"]))
	if projectID == "" {
		projectID = strings.TrimSpace(asString(args["id"]))
	}
	if projectID == "" {
		return "", errors.New("project_id is required")
	}
	project, found, err := r.projects.GetProject(account, projectID)
	if err != nil {
		return "", err
	}
	if !found || project == nil {
		return "", errors.New("project not found")
	}
	result := map[string]any{"tool": "manage_projects", "action": action, "project_id": projectID, "status": "ok"}
	taskID := strings.TrimSpace(asString(args["task_id"]))
	switch action {
	case "reopen_task":
		if taskID == "" {
			return "", errors.New("task_id is required")
		}
		revision, err := projectTaskInteger(args, "expected_revision", 1, 1<<30)
		if err != nil {
			return "", err
		}
		feedback, key := asString(args["feedback"]), strings.TrimSpace(asString(args["client_request_id"]))
		if strings.TrimSpace(feedback) == "" || len(feedback) > 32000 || key == "" || len(key) > 128 {
			return "", errors.New("reopen_task requires feedback (1-32000 bytes) and client_request_id (1-128 bytes)")
		}
		service, ok := r.projectTaskLifecycle.(ProjectTaskFollowupService)
		if !ok {
			return "", errors.New("canonical task follow-up service unavailable")
		}
		task, err := service.ReopenProjectTask(ctx, scope.Principal, projectID, taskID, ProjectTaskFollowupInput{Feedback: feedback, ClientRequestID: key, Revision: revision, Repair: asBool(args["repair"])})
		if err != nil {
			return "", err
		}
		result["task"] = projectTaskSummary(*task)
		result["active_attempt_id"], result["session_id"], result["run_id"] = task.ActiveAttemptID, task.SessionID, task.ExecutionRunID()
		result["status"] = "reopened"
	case "get_task":
		if taskID == "" {
			return "", errors.New("task_id is required")
		}
		task, found, err := r.projects.GetProjectTask(account, projectID, taskID)
		if err != nil {
			return "", err
		}
		if !found || task == nil {
			return "", errors.New("task not found")
		}
		task.EnsureTaskAttempts()
		cursor, limit := 0, 25
		if _, ok := args["cursor"]; ok {
			cursor, err = projectTaskInteger(args, "cursor", 0, 1000000)
			if err != nil {
				return "", err
			}
		}
		if _, ok := args["limit"]; ok {
			limit, err = projectTaskInteger(args, "limit", 1, 50)
			if err != nil {
				return "", err
			}
		}
		rows, next, err := task.TaskAttemptPage(cursor, limit)
		if err != nil {
			return "", err
		}
		copyTask := *task
		for i := range rows {
			rows[i].Deliverables = append([]pebblestore.ProjectTaskDeliverable(nil), rows[i].Deliverables...)
			for j := range rows[i].Deliverables {
				rows[i].Deliverables[j].VideoProvenance = rows[i].Deliverables[j].VideoProvenance.ClientSafeCopy()
			}
		}
		copyTask.Attempts = rows
		result["task"], result["next_cursor"] = &copyTask, next
	case "update_task", "archive_task", "delete_task":
		if taskID == "" {
			return "", errors.New("task_id is required")
		}
		revision, err := projectTaskInteger(args, "expected_revision", 1, 1<<30)
		if err != nil {
			return "", err
		}
		if action == "delete_task" {
			deleter, ok := r.projects.(interface {
				DeleteProjectTaskIfRevision(string, string, string, int) error
			})
			if !ok {
				return "", errors.New("atomic guarded task deletion unavailable")
			}
			if err := deleter.DeleteProjectTaskIfRevision(account, projectID, taskID, revision); err != nil {
				return "", err
			}
			result["deleted"] = true
			result["task_id"] = taskID
			break
		}
		if action == "archive_task" {
			archiver, ok := r.projects.(interface {
				ArchiveProjectTaskIfRevision(string, string, string, int) (*pebblestore.ProjectTaskRecord, error)
			})
			if !ok {
				return "", errors.New("atomic guarded task archive unavailable")
			}
			updated, err := archiver.ArchiveProjectTaskIfRevision(account, projectID, taskID, revision)
			if err != nil {
				return "", err
			}
			result["task"] = projectTaskSummary(*updated)
			break
		}
		if action == "update_task" {
			for _, key := range []string{"status", "current_stage_index", "task_program", "agent", "workspace_path", "session_id", "worktree_branch", "source_workspace", "revision"} {
				if _, exists := args[key]; exists {
					return "", fmt.Errorf("%s is not an editable task definition field", key)
				}
			}
			changed := false
			for _, key := range []string{"title", "description", "worker_name", "priority", "group", "order"} {
				if _, ok := args[key]; ok {
					changed = true
				}
			}
			if !changed {
				return "", errors.New("no editable task fields supplied")
			}
		}
		updated, err := r.projects.UpdateProjectTask(account, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			if t.Revision != revision {
				return fmt.Errorf("stale task revision: expected %d, current %d", revision, t.Revision)
			}
			if t.Archived {
				return errors.New("archived task cannot be edited")
			}
			if raw, ok := args["title"]; ok {
				t.Title = strings.TrimSpace(asString(raw))
				if t.Title == "" {
					return errors.New("title cannot be empty")
				}
			}
			if raw, ok := args["description"]; ok {
				t.Description = strings.TrimSpace(asString(raw))
			}
			if raw, ok := args["worker_name"]; ok {
				t.WorkerName = strings.TrimSpace(asString(raw))
			}
			if raw, ok := args["priority"]; ok {
				t.Priority = strings.TrimSpace(asString(raw))
			}
			if raw, ok := args["group"]; ok {
				t.Group = strings.TrimSpace(asString(raw))
			}
			if _, ok := args["order"]; ok {
				n, err := projectTaskInteger(args, "order", 0, 1000000)
				if err != nil {
					return err
				}
				t.Order = n
			}
			t.Revision++
			return t.Validate()
		})
		if err != nil {
			return "", err
		}
		result["task"] = projectTaskSummary(*updated)
	case "list_tasks", "reconcile_tasks":
		limit := 25
		if _, ok := args["limit"]; ok {
			limit, err = projectTaskInteger(args, "limit", 1, 100)
			if err != nil {
				return "", err
			}
		}
		if action == "reconcile_tasks" && limit > 20 {
			return "", errors.New("reconcile_tasks limit cannot exceed 20")
		}
		cursor := 0
		if _, ok := args["cursor"]; ok {
			cursor, err = projectTaskInteger(args, "cursor", 0, 100000)
			if err != nil {
				return "", err
			}
		}
		// The store has a hard scan bound of 10,000. A full page of 1000
		// records is explicitly flagged; never imply a complete inventory.
		tasks, err := r.projects.ListProjectTasks(account, projectID, 1000)
		if err != nil {
			return "", err
		}
		sort.Slice(tasks, func(i, j int) bool {
			if tasks[i].Group != tasks[j].Group {
				return tasks[i].Group < tasks[j].Group
			}
			if tasks[i].Order != tasks[j].Order {
				return tasks[i].Order < tasks[j].Order
			}
			return tasks[i].ID < tasks[j].ID
		})
		filtered := make([]pebblestore.ProjectTaskRecord, 0, len(tasks))
		query := strings.ToLower(strings.TrimSpace(asString(args["query"])))
		for _, task := range tasks {
			if task.Archived && !asBool(args["include_archived"]) {
				continue
			}
			if status := strings.TrimSpace(asString(args["status"])); status != "" && task.Status != status {
				continue
			}
			if agent := strings.TrimSpace(asString(args["agent"])); agent != "" && task.Agent != agent {
				continue
			}
			if group := strings.TrimSpace(asString(args["group"])); group != "" && task.Group != group {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(task.Title+" "+task.Description), query) {
				continue
			}
			filtered = append(filtered, task)
		}
		result["total"] = len(filtered)
		result["cursor"] = cursor
		result["limit"] = limit
		result["scan_truncated"] = len(tasks) == 1000
		if cursor > len(filtered) {
			cursor = len(filtered)
		}
		end := cursor + limit
		if end > len(filtered) {
			end = len(filtered)
		}
		if end < len(filtered) {
			result["next_cursor"] = end
		}
		if action == "list_tasks" {
			rows := make([]projectTaskListRow, 0, end-cursor)
			for _, task := range filtered[cursor:end] {
				rows = append(rows, projectTaskSummary(task))
			}
			result["tasks"] = rows
		} else {
			rows := make([]map[string]any, 0, end-cursor)
			for _, task := range filtered[cursor:end] {
				rows = append(rows, r.reconcileProjectTask(scope, task))
			}
			result["worktrees"] = rows
		}
		result["count"] = end - cursor
	default:
		return "", fmt.Errorf("unknown project task action %q", action)
	}
	raw, err := json.Marshal(result)
	return string(raw), err
}

// projectTaskListRow is deliberately independent of the persisted record: adding
// media or plan fields to storage must never expand list/mutation tool output.
type projectTaskListRow struct {
	ID                string `json:"id"`
	ProjectID         string `json:"project_id"`
	Revision          int    `json:"revision"`
	Title             string `json:"title"`
	Status            string `json:"status"`
	Archived          bool   `json:"archived"`
	Agent             string `json:"agent,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	TaskProgramID     string `json:"task_program_id,omitempty"`
	WorktreeBranch    string `json:"worktree_branch,omitempty"`
	SourceWorkspaceID string `json:"source_workspace_id,omitempty"`
	Group             string `json:"group,omitempty"`
	Order             int    `json:"order"`
	Priority          string `json:"priority,omitempty"`
}

func projectTaskSummary(t pebblestore.ProjectTaskRecord) projectTaskListRow {
	title := []rune(t.Title)
	if len(title) > 256 {
		title = title[:256]
	}
	return projectTaskListRow{ID: t.ID, ProjectID: t.ProjectID, Revision: t.Revision, Title: string(title), Status: t.Status, Archived: t.Archived, Agent: t.Agent, SessionID: t.SessionID, TaskProgramID: t.TaskProgramID, WorktreeBranch: t.WorktreeBranch, SourceWorkspaceID: t.SourceWorkspace.WorkspaceID, Group: t.Group, Order: t.Order, Priority: t.Priority}
}

func projectTaskInteger(args map[string]any, key string, min, max int) (int, error) {
	raw, exists := args[key]
	if !exists {
		return 0, fmt.Errorf("%s is required", key)
	}
	var n int64
	switch v := raw.(type) {
	case int:
		n = int64(v)
	case float64:
		if v != float64(int64(v)) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		n = int64(v)
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return 0, err
		}
		n = parsed
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if n < int64(min) || n > int64(max) {
		return 0, fmt.Errorf("%s must be between %d and %d", key, min, max)
	}
	return int(n), nil
}

// Git inspection is read-only and runs only at a task's exact catalog-bound
// source root. No user-controlled paths are passed to Git as execution roots.
func projectTaskGit(root string, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, argv...)...)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if len(out) > 1<<20 {
		return "", errors.New("git inspection output too large")
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r *Runtime) reconcileProjectTask(scope WorkspaceScope, task pebblestore.ProjectTaskRecord) map[string]any {
	row := map[string]any{"project_id": task.ProjectID, "task_id": task.ID, "session_id": task.SessionID, "task_status": task.Status, "archived": task.Archived, "source_workspace_id": task.SourceWorkspace.WorkspaceID, "source_workspace_path": task.SourceWorkspace.Path, "worktree_branch": task.WorktreeBranch, "worktree_name": task.WorktreeName, "base_commit": task.BaseCommit, "captured_target_branch": task.BaseBranch}
	root := task.SourceWorkspace.Path
	if root == "" || task.SourceWorkspace.WorkspaceID == "" || task.SourceWorkspace.WorkspaceGeneration <= 0 {
		row["state"] = "source_binding_missing"
		return row
	}
	// Only roots independently granted to this invocation can be inspected.
	authorized := false
	for _, allowed := range scope.Roots {
		if allowed == root {
			authorized = true
			break
		}
	}
	if scope.SourceWorkspacePath == root {
		authorized = true
	}
	if !authorized || r.workspace == nil {
		row["state"] = "source_not_in_scope"
		return row
	}
	saved, scopeErr := r.workspace.ScopeForPathForPrincipal(scope.Principal, root)
	if scopeErr != nil || !saved.Matched || filepath.Clean(saved.WorkspacePath) != filepath.Clean(root) {
		row["state"] = "source_authorization_failed"
		return row
	}
	if top, err := projectTaskGit(root, "rev-parse", "--show-toplevel"); err != nil || filepath.Clean(top) != filepath.Clean(root) {
		row["state"] = "source_missing_or_not_root"
		return row
	}
	main, mainErr := projectTaskGit(root, "rev-parse", "--verify", "refs/heads/main^{commit}")
	row["main_exists"] = mainErr == nil
	target := task.BaseBranch
	if target == "" {
		target, _ = projectTaskGit(root, "symbolic-ref", "--quiet", "--short", "HEAD")
	}
	row["target_branch"] = target
	if mainErr == nil {
		row["main_head"] = main
	} else {
		row["main_absent"] = true
	}
	if task.WorktreeBranch == "" {
		row["state"] = "no_worktree_assigned"
		return row
	}
	head, err := projectTaskGit(root, "rev-parse", "--verify", "refs/heads/"+task.WorktreeBranch+"^{commit}")
	if err != nil {
		row["state"] = "branch_missing"
		return row
	}
	row["head"] = head
	listed, err := projectTaskGit(root, "worktree", "list", "--porcelain")
	if err != nil {
		row["state"] = "inspection_failed"
		return row
	}
	var path, branch string
	for _, line := range strings.Split(listed+"\n\n", "\n") {
		if strings.HasPrefix(line, "worktree ") {
			path = strings.TrimPrefix(line, "worktree ")
			branch = ""
		}
		if strings.HasPrefix(line, "branch refs/heads/") {
			branch = strings.TrimPrefix(line, "branch refs/heads/")
		}
		if line == "" && branch == task.WorktreeBranch {
			row["worktree_path"] = path
			break
		}
	}
	if row["worktree_path"] == nil {
		row["state"] = "worktree_missing_or_detached"
	} else {
		path := row["worktree_path"].(string)
		top, topErr := projectTaskGit(path, "rev-parse", "--show-toplevel")
		if topErr != nil || filepath.Clean(top) != filepath.Clean(path) {
			row["state"] = "worktree_missing"
			return row
		}
		status, err := projectTaskGit(path, "status", "--porcelain", "--untracked-files=normal")
		if err != nil {
			row["state"] = "worktree_missing"
		} else {
			row["dirty"] = status != ""
			row["dirty_entries"] = len(strings.Split(status, "\n"))
			if status == "" {
				row["dirty_entries"] = 0
			}
		}
	}
	if target != "" {
		if targetHead, err := projectTaskGit(root, "rev-parse", "--verify", "refs/heads/"+target+"^{commit}"); err == nil {
			row["target_head"] = targetHead
			if counts, err := projectTaskGit(root, "rev-list", "--left-right", "--count", "refs/heads/"+target+"...refs/heads/"+task.WorktreeBranch); err == nil {
				parts := strings.Fields(counts)
				if len(parts) == 2 {
					row["behind"], _ = strconv.Atoi(parts[0])
					row["ahead"], _ = strconv.Atoi(parts[1])
				}
			}
			if task.BaseCommit != "" {
				_, baseErr := projectTaskGit(root, "merge-base", "--is-ancestor", task.BaseCommit, head)
				_, ancestorErr := projectTaskGit(root, "merge-base", "--is-ancestor", head, targetHead)
				row["descends_from_base"] = baseErr == nil
				row["integrated"] = baseErr == nil && head != task.BaseCommit && ancestorErr == nil
			}
		} else {
			row["target_missing"] = true
		}
	}
	if mainErr == nil {
		if counts, e := projectTaskGit(root, "rev-list", "--count", "refs/heads/main..refs/heads/"+task.WorktreeBranch); e == nil {
			row["unmerged_into_main"], _ = strconv.Atoi(counts)
		}
	}
	if row["state"] == nil {
		row["state"] = "present"
	}
	return row
}
