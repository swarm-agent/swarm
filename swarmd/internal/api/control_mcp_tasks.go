package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// Delegation in one hop: an outside AI (the person's own Claude, ChatGPT or
// Codex) hands one piece of work to a Swarm role, waits for it and reads what
// came back, without a Swarm-side orchestrator in between. The task card is
// auto-approved because the delegating AI is the gate, under its chat's own
// permission prompts; media tasks keep the batch threshold.

// controlMCPTaskSettled lists the statuses a wait returns on: the work is
// done, needs the delegator, or cannot continue.
var controlMCPTaskSettled = map[string]bool{
	"needs_review": true, "completed": true, "failed": true, "blocked": true,
	"rejected": true, "needs_input": true, "pending_approval": true, "cancelled": true, "superseded": true,
}

const controlMCPTaskReportChars = 4000

func controlMCPTaskNext(task map[string]any) string {
	status, _ := task["status"].(string)
	switch status {
	case "needs_review":
		return "the work is done: read it with task_result (include_diff for the patch), then integrate_task merges it into its target branch"
	case "completed":
		return "the task is finished and integrated"
	case "failed", "blocked", "cancelled", "superseded":
		return "the task did not finish: see last_error and the report, then run_task again with a corrected brief"
	case "needs_input":
		return "the agent asked something: see action_needed and answer in its session with send_message"
	case "pending_approval":
		return "the task waits for approval (media batch threshold or a plan): approve_task starts it"
	case "queued", "in_progress", "planning":
		return "the task is running: wait_task returns when it settles"
	}
	return ""
}

func controlMCPRandomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "mcp-" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))[:16]
	}
	return "mcp-" + hex.EncodeToString(b[:])
}

// task reads one task with fresh git and delivery state.
func (c *controlMCPCall) task(projectID, taskID string) (map[string]any, error) {
	response, err := c.dispatch(http.MethodGet, "/v3/projects/"+projectID+"/tasks/"+taskID, nil, nil)
	if err != nil {
		return nil, controlMCPNotFound(err, "task", taskID, "swarm_list_tasks")
	}
	task := controlMCPMap(response["task"])
	if task == nil {
		return nil, controlMCPToolFailure("task %s not found in project %s (see list_tasks)", taskID, projectID)
	}
	return task, nil
}

func controlMCPRunTask(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	projectID, err := controlMCPID(args, "project_id")
	if err != nil {
		return nil, err
	}
	role := controlMCPString(args, "role")
	brief := strings.TrimSpace(controlMCPString(args, "brief"))
	if brief == "" {
		return nil, controlMCPToolFailure("brief is required: what to do and how to know it is done")
	}
	title := strings.TrimSpace(controlMCPString(args, "title"))
	if title == "" {
		title = strings.TrimSpace(strings.SplitN(brief, "\n", 2)[0])
		title = strings.TrimLeft(title, "#- ")
		if len(title) > 80 {
			title = strings.TrimSpace(title[:80])
		}
		if title == "" {
			title = role + " task"
		}
	}
	description := brief
	files := []string{}
	for _, raw := range controlMCPList(args["files"]) {
		path, _ := raw.(string)
		if path = strings.TrimSpace(path); path != "" {
			files = append(files, path)
		}
	}
	if len(files) > 0 {
		description += "\n\nScope: only change files under " + strings.Join(files, ", ") + ". Anything else is out of scope; say so instead of touching it."
	}
	body := map[string]any{
		"title":             title,
		"description":       description,
		"agent":             role,
		"auto_approve":      true,
		"client_request_id": controlMCPRandomID(),
	}
	if workspace := strings.TrimSpace(controlMCPString(args, "workspace")); workspace != "" {
		if filepath.IsAbs(workspace) {
			registered, err := c.registeredWorkspace(filepath.Clean(workspace))
			if err != nil {
				return nil, err
			}
			if registered == nil {
				return nil, controlMCPToolFailure("workspace %q is not registered on this machine (see list_workspaces, add_workspace)", workspace)
			}
			body["workspace_path"] = filepath.Clean(workspace)
		} else {
			body["workspace_id"] = workspace
		}
	} else if role == "coder" {
		return nil, controlMCPToolFailure("a coder task needs workspace: the registered repository path (see get_project for the project's workspaces)")
	}
	if model := controlMCPMap(args["model"]); model != nil {
		preference, err := c.modelPreference(model)
		if err != nil {
			return nil, err
		}
		for key, value := range preference {
			body[key] = value
		}
	}
	created, err := c.dispatch(http.MethodPost, "/v3/projects/"+projectID+"/tasks", nil, body)
	if err != nil {
		return nil, controlMCPNotFound(err, "project", projectID, "swarm_list_projects")
	}
	task := controlMCPMap(created["task"])
	if task == nil {
		return nil, controlMCPToolFailure("Swarm created no task")
	}
	taskID, _ := task["id"].(string)
	wait := map[string]any{}
	if seconds := controlMCPInt(args, "wait_seconds", 0); seconds > 0 && taskID != "" {
		if task, wait, err = c.waitForTask(projectID, taskID, seconds); err != nil {
			return nil, err
		}
	}
	out := controlMCPPick(task, "session_id", "status", "workspace_path", "worktree_branch", "base_branch")
	out["task_id"] = taskID
	out["role"] = task["agent"]
	out["title"] = task["title"]
	for key, value := range wait {
		out[key] = value
	}
	if next := controlMCPTaskNext(task); next != "" {
		out["next"] = next
	}
	return out, nil
}

// waitForTask returns the task once its status settles (see
// controlMCPTaskSettled) or at the deadline. It wakes on committed realtime
// outbox records for the task's session or the project's task updates, never
// a timer poll, and re-reads through dispatch.
func (c *controlMCPCall) waitForTask(projectID, taskID string, seconds int) (map[string]any, map[string]any, error) {
	started := time.Now()
	task, err := c.task(projectID, taskID)
	if err != nil {
		return nil, nil, err
	}
	settled := func(task map[string]any) bool {
		status, _ := task["status"].(string)
		return controlMCPTaskSettled[status]
	}
	elapsed := func() int { return int(time.Since(started).Seconds()) }
	if seconds <= 0 || settled(task) {
		return task, map[string]any{"settled": settled(task), "waited_seconds": elapsed()}, nil
	}
	if seconds > controlMCPMaxWaitSeconds {
		seconds = controlMCPMaxWaitSeconds
	}
	var hub *v3RealtimeOutboxHub
	if c.server != nil {
		hub = c.server.v3RealtimeOutbox
	}
	sub := hub.subscribe()
	if sub == nil {
		return task, map[string]any{"settled": false, "waited_seconds": elapsed()}, nil
	}
	defer hub.unsubscribe(sub)
	if task, err = c.task(projectID, taskID); err != nil || settled(task) {
		return task, map[string]any{"settled": err == nil, "waited_seconds": elapsed()}, err
	}
	sessionID, _ := task["session_id"].(string)
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	for {
		select {
		case record := <-sub.send:
			var payload struct {
				ProjectID string `json:"project_id"`
				TaskID    string `json:"task_id"`
			}
			mine := sessionID != "" && record.SessionID == sessionID
			if !mine && json.Unmarshal(record.Event.Payload, &payload) == nil {
				mine = payload.ProjectID == projectID && (payload.TaskID == "" || payload.TaskID == taskID)
			}
			if !mine {
				continue
			}
			if task, err = c.task(projectID, taskID); err != nil || settled(task) {
				return task, map[string]any{"settled": err == nil, "waited_seconds": elapsed()}, err
			}
		case <-sub.slow:
			task, err = c.task(projectID, taskID)
			return task, map[string]any{"settled": err == nil && settled(task), "waited_seconds": elapsed()}, err
		case <-timer.C:
			task, err = c.task(projectID, taskID)
			return task, map[string]any{"settled": err == nil && settled(task), "timed_out": true, "waited_seconds": seconds}, err
		case <-c.request.Context().Done():
			return task, map[string]any{"settled": false, "waited_seconds": elapsed()}, nil
		}
	}
}

func controlMCPWaitTask(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	projectID, err := controlMCPID(args, "project_id")
	if err != nil {
		return nil, err
	}
	taskID, err := controlMCPID(args, "task_id")
	if err != nil {
		return nil, err
	}
	task, wait, err := c.waitForTask(projectID, taskID, controlMCPInt(args, "wait_seconds", controlMCPMaxWaitSeconds))
	if err != nil {
		return nil, err
	}
	out := controlMCPPick(task, "status", "session_id")
	out["task_id"] = taskID
	for key, value := range wait {
		out[key] = value
	}
	for key, limit := range map[string]int{"action_needed": 400, "last_error": 400} {
		if text, ok := task[key].(string); ok && strings.TrimSpace(text) != "" {
			out[key] = controlMCPTruncate(text, limit)
		}
	}
	if next := controlMCPTaskNext(task); next != "" {
		out["next"] = next
	}
	return out, nil
}

func controlMCPTaskResult(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	projectID, err := controlMCPID(args, "project_id")
	if err != nil {
		return nil, err
	}
	taskID, err := controlMCPID(args, "task_id")
	if err != nil {
		return nil, err
	}
	task, err := c.task(projectID, taskID)
	if err != nil {
		return nil, err
	}
	out := controlMCPPick(task, "title", "status", "session_id", "workspace_path", "worktree_branch", "base_branch", "base_commit", "diff_summary", "is_integrated", "unintegrated_commits")
	out["task_id"] = taskID
	out["role"] = task["agent"]
	assessment := controlMCPMap(task["delivery_assessment"])
	if head, _ := assessment["source_oid"].(string); head != "" {
		out["head"] = head
	}
	if state, _ := assessment["state"].(string); state != "" {
		out["delivery_state"] = state
	}
	for _, key := range []string{"what_did_do", "what_not_done"} {
		lines := []string{}
		for _, raw := range controlMCPList(task[key]) {
			if line, _ := raw.(string); strings.TrimSpace(line) != "" {
				lines = append(lines, controlMCPTruncate(line, 300))
			}
		}
		if len(lines) > 0 {
			out[key] = lines
		}
	}
	if attempts := controlMCPList(task["attempts"]); len(attempts) > 0 {
		if summary, _ := controlMCPMap(attempts[len(attempts)-1])["summary"].(string); strings.TrimSpace(summary) != "" {
			out["report"] = controlMCPTruncate(summary, controlMCPTaskReportChars)
		}
	}
	deliverables := []map[string]any{}
	for i, raw := range controlMCPList(task["deliverables"]) {
		if i >= 10 {
			break
		}
		if item := controlMCPPick(controlMCPMap(raw), "id", "title", "kind", "status"); len(item) > 0 {
			deliverables = append(deliverables, item)
		}
	}
	if len(deliverables) > 0 {
		out["deliverables"] = deliverables
	}
	if integration := controlMCPPick(controlMCPMap(task["integration"]), "state", "error", "resulting_target_head", "target_branch"); len(integration) > 0 {
		out["integration"] = integration
	}
	if text, ok := task["last_error"].(string); ok && strings.TrimSpace(text) != "" {
		out["last_error"] = controlMCPTruncate(text, 400)
	}
	if sessionID, _ := task["session_id"].(string); sessionID != "" {
		if detail, err := c.dispatch(http.MethodGet, controlMCPSessionPath(sessionID), url.Values{"message_limit": {"0"}, "event_limit": {"0"}}, nil); err == nil {
			if usage := controlMCPPick(controlMCPMap(detail["usage_summary"]), "estimated_cost_usd", "total_tokens", "turn_count", "provider", "model"); len(usage) > 0 {
				out["cost"] = usage
			}
		}
		changesArgs := map[string]any{"session_id": sessionID, "diff_chars": 0}
		if include, _ := args["include_diff"].(bool); include {
			changesArgs["diff_chars"] = controlMCPInt(args, "diff_chars", controlMCPDefaultDiffChars)
		}
		if changes, err := controlMCPGetChanges(c, changesArgs); err == nil {
			for key, value := range controlMCPPick(controlMCPMap(changes), "commits", "files", "files_truncated", "diff", "diff_truncated", "diff_note", "note") {
				out[key] = value
			}
		} else {
			out["changes_error"] = controlMCPTruncate(err.Error(), 300)
		}
	}
	if next := controlMCPTaskNext(task); next != "" {
		out["next"] = next
	}
	return out, nil
}
