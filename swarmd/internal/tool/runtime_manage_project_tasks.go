package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"swarm/packages/swarmd/internal/sandbox"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
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
	case "wait_tasks":
		run, ok := VideoRunContextFromContext(ctx)
		if !ok || run.SessionID != scope.SessionID || r.sessions == nil {
			return "", errors.New("wait_tasks requires trusted current provider run context")
		}
		var ids []string
		raw, err := json.Marshal(args["task_ids"])
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal(raw, &ids); err != nil || len(ids) == 0 || len(ids) > 16 {
			return "", errors.New("task_ids must contain 1-16 task IDs")
		}
		until, err := pebblestore.NormalizeProjectTaskWaitUntil(asString(args["until"]))
		if err != nil {
			return "", err
		}
		key := "task-wait:" + run.RunID
		payload, _ := json.Marshal(ids)
		hash := projectID + string(payload)
		if until != pebblestore.ProjectTaskWaitUntilSettled {
			hash += "\x00" + until
		}
		mutation, err := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
			SessionID: scope.SessionID, UserID: scope.Principal.UserID, AccountScopeID: account,
			Kind: pebblestore.V3SessionMutationRecordRunIntent, EventType: "session.run.waiting_tasks",
			ClientRequestID: key, PayloadHash: hash,
			TaskWait: &pebblestore.V3ProjectTaskWaitMutation{RunID: run.RunID, ProjectID: projectID, TaskIDs: ids, Until: until},
		})
		if err != nil {
			return "", err
		}
		result["status"], result["run_id"], result["next_action"] = "waiting_tasks", run.RunID, "yield_until_task_outcome"
		result["event_seq"] = mutation.PrimarySeq
		result["task_ids"] = ids
		result["until"] = until
	case "edit_requirements":
		task, found, err := r.projects.GetProjectTask(account, projectID, taskID)
		if err != nil {
			return "", err
		}
		if !found || task == nil || task.Archived || task.Status != "pending_approval" || task.PlanBinding == nil {
			return "", errors.New("requirement edits require a pending task with a bound plan")
		}
		owner, found, err := r.sessions.GetSession(task.PlanBinding.SessionID)
		if err != nil {
			return "", err
		}
		if !found || owner.AccountScopeID != account || owner.UserID != scope.Principal.UserID {
			return "", errors.New("plan session ownership mismatch")
		}
		object, err := parseJSONEncodedObject(args["document_patch"], "document_patch")
		if err != nil {
			return "", err
		}
		var patch sessionruntime.PlanDocumentPatch
		raw, err := json.Marshal(object)
		if err != nil {
			return "", fmt.Errorf("invalid document_patch: %w", err)
		}
		if err = json.Unmarshal(raw, &patch); err != nil {
			return "", fmt.Errorf("invalid document_patch: %w", err)
		}
		ops := patch.Operations
		if len(ops) == 0 {
			ops = []sessionruntime.PlanDocumentPatch{patch}
		}
		for _, op := range ops {
			switch op.Operation {
			case "add_requirement", "edit_requirement", "remove_requirement", "reorder_requirements":
			default:
				return "", errors.New("edit_requirements accepts only targeted requirement operations")
			}
		}
		service, ok := r.projectTaskLifecycle.(interface {
			EditProjectTaskRequirements(string, string, string, string, sessionruntime.PlanDocumentPatch) (sessionruntime.ProjectTaskPlanSubmissionResult, error)
		})
		if !ok {
			return "", errors.New("canonical plan editing unavailable")
		}
		submitted, err := service.EditProjectTaskRequirements(account, scope.Principal.UserID, projectID, taskID, patch)
		if err != nil {
			return "", err
		}
		result["review"] = submitted
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
		detail, err := ProjectTaskDetail(args)
		if err != nil {
			return "", err
		}
		task, err := service.ReopenProjectTask(ctx, scope.Principal, projectID, taskID, ProjectTaskFollowupInput{Feedback: feedback, ClientRequestID: key, Revision: revision, Repair: asBool(args["repair"])})
		if err != nil {
			return "", err
		}
		if detail == ProjectTaskDetailFull {
			result["task"] = projectTaskSummary(*task)
		} else {
			setProjectTaskRow(result, *task)
		}
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
		detail, err := ProjectTaskDetail(args)
		if err != nil {
			return "", err
		}
		task.EnsureTaskAttempts()
		if detail != ProjectTaskDetailFull && !scope.TaskHistoryOnly {
			// The default answer is what the Orchestrator needs to decide;
			// detail adds one section; full is the stored record below.
			// Task follow-ups (history-only) always read the stored record
			// with a page of attempt history (cursor/limit).
			read := projectTaskRead{ProjectTaskRecord: *task, Progress: r.projectTaskProgress(account, *task), Worktree: projectTaskWorktreeResult(*task)}
			result["task"] = compactProjectTask(read, false)
			switch detail {
			case ProjectTaskDetailPlan:
				plan := projectTaskPlanDetail(*task)
				if task.PlanBinding != nil {
					bound, err := r.boundProjectTaskPlan(scope, task)
					if err != nil {
						return "", err
					}
					plan["bound_plan"] = bound
				}
				result["plan"] = plan
			case ProjectTaskDetailAttempts:
				cursor, limit, err := projectTaskAttemptPageArgs(args)
				if err != nil {
					return "", err
				}
				rows, next, err := task.TaskAttemptPage(cursor, limit)
				if err != nil {
					return "", err
				}
				result["attempts"], result["next_cursor"] = projectTaskAttemptRows(rows), next
			case ProjectTaskDetailFiles:
				result["files"] = projectTaskFilesDetail(*task, read.Worktree)
			}
			break
		}
		cursor, limit, err := projectTaskAttemptPageArgs(args)
		if err != nil {
			return "", err
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
		if task.PlanBinding != nil {
			plan, err := r.boundProjectTaskPlan(scope, task)
			if err != nil {
				return "", err
			}
			result["plan"] = plan
		}
	case "update_task", "archive_task", "unarchive_task", "delete_task":
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
		if action == "unarchive_task" {
			restorer, ok := r.projects.(interface {
				UnarchiveProjectTaskIfRevision(string, string, string, int) (*pebblestore.ProjectTaskRecord, error)
			})
			if !ok {
				return "", errors.New("atomic guarded task unarchive unavailable")
			}
			updated, err := restorer.UnarchiveProjectTaskIfRevision(account, projectID, taskID, revision)
			if err != nil {
				return "", err
			}
			result["task"] = projectTaskSummary(*updated)
			break
		}
		if action == "archive_task" {
			archiver, ok := r.projects.(interface {
				ArchiveProjectTaskIfRevision(string, string, string, int) (*pebblestore.ProjectTaskRecord, error)
			})
			if !ok {
				return "", errors.New("atomic guarded task archive unavailable")
			}
			detail, err := ProjectTaskDetail(args)
			if err != nil {
				return "", err
			}
			updated, err := archiver.ArchiveProjectTaskIfRevision(account, projectID, taskID, revision)
			if err != nil {
				return "", err
			}
			if detail == ProjectTaskDetailFull {
				result["task"] = projectTaskSummary(*updated)
			} else {
				setProjectTaskRow(result, *updated)
			}
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
		if action == "list_tasks" {
			// The same selection as the task list route and linked machines:
			// current tasks by default, newest updated first, one page.
			scopeName, err := pebblestore.NormalizeProjectTaskScope(asString(args["scope"]))
			if err != nil {
				return "", err
			}
			if scopeName == "" {
				scopeName = pebblestore.ProjectTaskScopeCurrent
			}
			all, scanTruncated, err := r.projects.ListProjectTaskRecordsForList(account, projectID)
			if err != nil {
				return "", err
			}
			filter := pebblestore.ProjectTaskListFilter{
				Statuses: pebblestore.ParseProjectTaskStatuses(asString(args["status"])),
				Query:    asString(args["query"]), Agent: strings.TrimSpace(asString(args["agent"])), Group: strings.TrimSpace(asString(args["group"])),
				Scope: scopeName, IncludeArchived: asBool(args["include_archived"]), Limit: limit, Cursor: cursor,
			}
			page := filter.Select(all, projectTaskListNow().UnixMilli())
			rows := make([]projectTaskListRow, 0, len(page.Tasks))
			for _, task := range page.Tasks {
				rows = append(rows, projectTaskSummary(task))
			}
			result["tasks"], result["count"], result["total"] = rows, len(rows), page.Total
			result["scope"], result["cursor"], result["limit"] = scopeName, page.Cursor, limit
			result["scan_truncated"] = scanTruncated
			if page.HasNext {
				result["next_cursor"] = page.NextCursor
			}
			break
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

// projectTaskAttemptPageArgs reads get_task's attempt page (cursor, limit).
func projectTaskAttemptPageArgs(args map[string]any) (int, int, error) {
	cursor, limit := 0, 25
	var err error
	if _, ok := args["cursor"]; ok {
		if cursor, err = projectTaskInteger(args, "cursor", 0, 1000000); err != nil {
			return 0, 0, err
		}
	}
	if _, ok := args["limit"]; ok {
		if limit, err = projectTaskInteger(args, "limit", 1, 50); err != nil {
			return 0, 0, err
		}
	}
	return cursor, limit, nil
}

// boundProjectTaskPlan reads a task's bound plan for its owning principal.
func (r *Runtime) boundProjectTaskPlan(scope WorkspaceScope, task *pebblestore.ProjectTaskRecord) (pebblestore.SessionPlanSnapshot, error) {
	reader, ok := r.sessions.(interface {
		GetPlan(string, string) (pebblestore.SessionPlanSnapshot, bool, error)
	})
	if !ok {
		return pebblestore.SessionPlanSnapshot{}, errors.New("bound plan reader unavailable")
	}
	plan, found, err := reader.GetPlan(task.PlanBinding.SessionID, task.PlanBinding.PlanID)
	if err != nil {
		return pebblestore.SessionPlanSnapshot{}, err
	}
	if !found || plan.AccountScopeID != scope.Principal.AccountScopeID || plan.UserID != scope.Principal.UserID {
		return pebblestore.SessionPlanSnapshot{}, errors.New("bound plan unavailable for principal")
	}
	return plan, nil
}

var (
	projectTaskCommitID   = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	projectTaskBranchName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]*$`)
)

// projectTaskOwnBranch reports whether branch is a plain branch name that can
// be resolved as refs/heads/<branch> without Git reading it as anything else.
func projectTaskOwnBranch(branch string) bool {
	return len(branch) <= 200 && projectTaskBranchName.MatchString(branch) && !strings.Contains(branch, "..") && !strings.Contains(branch, "//") && !strings.HasSuffix(branch, "/") && !strings.HasSuffix(branch, ".") && !strings.HasSuffix(branch, ".lock")
}

// projectTaskFolderDelivered reports whether t worked in the person's own
// folder (no worktree branch, the source folder itself, as api.folderTask)
// and its delivery was frozen when the run ended: integration state in_folder
// with the folder head recorded then.
func projectTaskFolderDelivered(t pebblestore.ProjectTaskRecord) bool {
	return t.WorktreeBranch == "" && t.WorkspacePath != "" && t.WorkspacePath == t.SourceWorkspace.Path && t.Agent != "reviewer" &&
		t.Integration != nil && t.Integration.State == "in_folder" && projectTaskCommitID.MatchString(t.Integration.SourceHead)
}

// projectTaskWorktreeResult reads a task's committed result with read-only
// Git over a range that belongs to the task, never the checkout's HEAD:
//   - a worktree task, once finished: base..refs/heads/<WorktreeBranch>, so a
//     checkout moved off the task branch does not misreport;
//   - a folder task delivered in_folder: base..Integration.SourceHead, the
//     head frozen when its run ended (as the task route reports it), so later
//     folder commits by the person or other tasks are never its output.
//
// Other tasks answer from their stored integration receipt. Only the stored
// path, a validated branch and validated commit IDs reach Git, never model
// arguments. nil when there is nothing to read.
func projectTaskWorktreeResult(t pebblestore.ProjectTaskRecord) *projectTaskWorktree {
	if t.DeliveryAssessment != nil || !projectTaskCommitID.MatchString(t.BaseCommit) || !filepath.IsAbs(t.WorkspacePath) {
		return nil
	}
	root := filepath.Clean(t.WorkspacePath)
	if projectTaskFolderDelivered(t) {
		return projectTaskRangeResult(root, t.BaseCommit, t.Integration.SourceHead)
	}
	if !projectTaskOwnBranch(t.WorktreeBranch) || projectTaskStatusClass(t.Status) != "finished" {
		return nil
	}
	head, err := projectTaskGit(root, "rev-parse", "--verify", "refs/heads/"+t.WorktreeBranch+"^{commit}")
	if err != nil || !projectTaskCommitID.MatchString(head) {
		return nil
	}
	return projectTaskRangeResult(root, t.BaseCommit, head)
}

// projectTaskRangeResult counts the commits base..head and the files they
// change. base and head are validated commit IDs.
func projectTaskRangeResult(root, base, head string) *projectTaskWorktree {
	w := &projectTaskWorktree{Head: head}
	if head == base {
		return w
	}
	if count, err := projectTaskGit(root, "rev-list", "--count", base+".."+head, "--"); err == nil {
		w.Commits, _ = strconv.Atoi(count)
	}
	if names, err := projectTaskGit(root, "diff", "--no-renames", "--name-only", base, head, "--"); err == nil && names != "" {
		w.Files = strings.Split(names, "\n")
	}
	return w
}

// projectTaskProgress is a task's progress from the store's progress hook
// (the same progress wait_tasks resumes with), or nil when none is installed.
func (r *Runtime) projectTaskProgress(account string, task pebblestore.ProjectTaskRecord) *projectTaskProgressRead {
	hook, ok := r.projects.(interface {
		ProjectTaskProgress(string, pebblestore.ProjectTaskRecord) any
	})
	if !ok {
		return nil
	}
	value := hook.ProjectTaskProgress(account, task)
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil || string(raw) == "null" {
		return nil
	}
	var progress projectTaskProgressRead
	if json.Unmarshal(raw, &progress) != nil {
		return nil
	}
	return &progress
}

// projectTaskListNow is the clock list_tasks measures the current scope against.
var projectTaskListNow = time.Now

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
	UpdatedAt         int64  `json:"updated_at,omitempty"`
}

func projectTaskSummary(t pebblestore.ProjectTaskRecord) projectTaskListRow {
	title := []rune(t.Title)
	if len(title) > 256 {
		title = title[:256]
	}
	return projectTaskListRow{ID: t.ID, ProjectID: t.ProjectID, Revision: t.Revision, Title: string(title), Status: t.Status, Archived: t.Archived, Agent: t.Agent, SessionID: t.SessionID, TaskProgramID: t.TaskProgramID, WorktreeBranch: t.WorktreeBranch, SourceWorkspaceID: t.SourceWorkspace.WorkspaceID, Group: t.Group, Order: t.Order, Priority: t.Priority, UpdatedAt: t.UpdatedAt}
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
	sandbox.Prepare(ctx, cmd)
	defer sandbox.ReleaseGit(cmd)
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
