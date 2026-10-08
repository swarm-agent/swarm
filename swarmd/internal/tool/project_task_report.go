package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// TaskReportBinding is a capability preflight, not mutation authority. The store
// rechecks current attempt, run and parent ownership under mutation locks.
func (r *Runtime) TaskReportBinding(scope WorkspaceScope) (string, string, error) {
	if r == nil || r.sessions == nil || r.projects == nil || scope.SessionID == "" || !scope.Principal.Valid() || scope.Principal.Type != "user" {
		return "", "", errors.New("task reporting requires an authenticated linked session")
	}
	session, found, err := r.sessions.GetSession(scope.SessionID)
	if err != nil {
		return "", "", err
	}
	if !found || session.AccountScopeID != scope.Principal.AccountScopeID || session.UserID != scope.Principal.UserID {
		return "", "", errors.New("task reporting session ownership mismatch")
	}
	projectID, _ := session.Metadata["project_id"].(string)
	taskID, _ := session.Metadata["task_id"].(string)
	if taskID == "" {
		taskID, _ = session.Metadata["project_task_id"].(string)
	}
	if projectID == "" || taskID == "" {
		return "", "", errors.New("task reporting session is not linked")
	}
	task, found, err := r.projects.GetProjectTask(scope.Principal.AccountScopeID, projectID, taskID)
	if err != nil {
		return "", "", err
	}
	if !found || task == nil || task.Archived {
		return "", "", errors.New("task reporting target unavailable")
	}
	task.EnsureTaskAttempts()
	attempt := task.ActiveAttempt()
	if attempt == nil || attempt.SessionID != session.ID || task.SessionID != session.ID {
		return "", "", errors.New("task reporting requires the active attempt")
	}
	return projectID, taskID, nil
}

func (r *Runtime) executeProjectTaskReport(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	projectID, taskID, err := r.TaskReportBinding(scope)
	if err != nil {
		return "", err
	}
	// Reject rather than silently ignoring caller-supplied authority.
	for _, key := range []string{"account_scope_id", "user_id", "parent_session_id", "session_id", "run_id", "attempt_id", "event_id"} {
		if _, exists := args[key]; exists {
			return "", errors.New("report_task derives ownership and event identity from the current task run")
		}
	}
	if asString(args["project_id"]) != projectID || asString(args["task_id"]) != taskID {
		return "", errors.New("report_task must target the current linked task")
	}
	run, ok := VideoRunContextFromContext(ctx)
	if !ok || run.SessionID != scope.SessionID || run.RunID == "" {
		return "", errors.New("report_task requires trusted current provider run context")
	}
	key := strings.TrimSpace(asString(args["client_request_id"]))
	if key == "" || len(key) > 128 {
		return "", errors.New("report_task requires client_request_id (1-128 bytes)")
	}
	result, err := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: scope.SessionID, UserID: scope.Principal.UserID, AccountScopeID: scope.Principal.AccountScopeID,
		Kind: pebblestore.V3SessionMutationReportTask, ClientRequestID: "task-report:" + key, PayloadHash: "derived-by-store",
		TaskReport: &pebblestore.V3ProjectTaskReportMutation{RunID: run.RunID, ProjectID: projectID, TaskID: taskID, Kind: pebblestore.ProjectTaskUpdateKind(asString(args["update_kind"])), Summary: asString(args["summary"])},
	})
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(map[string]any{"tool": "manage_projects", "action": "report_task", "status": "recorded", "project_id": projectID, "task_id": taskID, "session_id": scope.SessionID, "event_ids": result.EventIDs, "event_seq": result.PrimarySeq, "replayed": result.Replayed, "delivery": "pending", "guidance": "Recorded task data only; this receipt does not prove delivery, wake-up or scope acceptance."})
	return string(raw), err
}
