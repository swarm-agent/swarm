package api

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A task whose record says running can be stuck: its run waits on a pending
// permission or question, or an update drain or execution admission holds it
// before it starts. The record cannot say so (nothing writes it while the run
// waits), so the task progress reads the run's live records on this machine
// (the session's pending permissions, its run intent and the execution
// admission) and gives the task its state from them: needs_you with the
// request, or waiting with "held: ...". Local list_tasks/get_task, the task
// routes (MCP) and a home's linked reads all read it from here.

const (
	// taskLivePendingScan bounds the pending requests read for one task.
	taskLivePendingScan = 50
	// taskLiveSummaryRunes bounds a pending request's argument summary.
	taskLiveSummaryRunes = 120
)

// markTaskProgressLive sets p.Live for a task whose run status says it
// works. It reads only; a record that cannot be read leaves the task as its
// record says (live is a claim).
func (s *Server) markTaskProgressLive(p *taskProgress, account string, task pebblestore.ProjectTaskRecord) {
	if p == nil || s == nil || taskStatusClass(task.Status) != progressWorking || task.Archived {
		return
	}
	sessionID := taskLiveSessionID(task)
	if sessionID == "" {
		return
	}
	live := s.taskPendingLive(sessionID)
	if live == nil {
		live = s.taskHeldLive(account, sessionID)
	}
	if live == nil {
		return
	}
	p.Live = live
	// A run waiting on a person or held has not gone quiet by itself.
	p.Stale = false
}

// taskLiveSessionID is the session of the task's current attempt.
func taskLiveSessionID(task pebblestore.ProjectTaskRecord) string {
	task.EnsureTaskAttempts()
	if a := task.ActiveAttempt(); a != nil && strings.TrimSpace(a.SessionID) != "" {
		return strings.TrimSpace(a.SessionID)
	}
	return strings.TrimSpace(task.SessionID)
}

// taskPendingLive is needs_you with the oldest pending permission or
// question of the session; nil when none waits on a person. A client tool's
// pending call waits on an app, not a person, and is left out.
func (s *Server) taskPendingLive(sessionID string) *pebblestore.ProjectTaskLive {
	if s.perm == nil {
		return nil
	}
	pending, err := s.perm.ListPending(sessionID, taskLivePendingScan)
	if err != nil {
		log.Printf("swarmd task progress: read pending requests of %s: %v", sessionID, err)
		return nil
	}
	waiting := make([]pebblestore.PermissionRecord, 0, len(pending))
	for _, record := range pending {
		if record.Requirement == permission.RequirementClientTool || record.Status != pebblestore.PermissionStatusPending {
			continue
		}
		waiting = append(waiting, record)
	}
	if len(waiting) == 0 {
		return nil
	}
	sort.SliceStable(waiting, func(i, j int) bool { return waiting[i].CreatedAt < waiting[j].CreatedAt })
	request := taskPendingRequest(waiting[0])
	if len(waiting) > 1 {
		request.Pending = len(waiting)
	}
	what := "waiting for approval"
	if request.Kind == pebblestore.ProjectTaskRequestQuestion {
		what = "waiting for an answer"
	}
	reason := fmt.Sprintf("%s (permission %s): %s", what, request.PermissionID, request.Tool)
	if request.Summary != "" {
		reason += ": " + request.Summary
	}
	if request.Pending > 1 {
		reason += fmt.Sprintf(" (+%d more)", request.Pending-1)
	}
	return &pebblestore.ProjectTaskLive{State: pebblestore.ProjectTaskStateNeedsYou, Reason: reason, Request: request}
}

// taskPendingRequest is a pending record as the task state shows it: the
// tool and a one-line summary of its arguments (the command for bash, the
// question for ask_user, else the compact arguments), bounded.
func taskPendingRequest(record pebblestore.PermissionRecord) *pebblestore.ProjectTaskPendingRequest {
	tool := strings.TrimSpace(record.ToolName)
	kind := pebblestore.ProjectTaskRequestPermission
	if tool == "ask_user" {
		kind = pebblestore.ProjectTaskRequestQuestion
	}
	// The call's arguments as stored (sanitized): the first that carries any.
	raw := ""
	for _, candidate := range []string{record.ToolCallArguments, record.ToolArguments} {
		if text := strings.TrimSpace(candidate); text != "" && text != "{}" && text != "null" {
			raw = text
			break
		}
	}
	summary := raw
	var args map[string]any
	if raw != "" && json.Unmarshal([]byte(raw), &args) == nil && args != nil {
		summary = ""
		for _, key := range []string{"command", "question", "prompt", "path", "action"} {
			if text, ok := args[key].(string); ok && strings.TrimSpace(text) != "" {
				summary = text
				if key == "action" {
					summary = "action=" + text
				}
				break
			}
		}
		if summary == "" {
			if compact, err := json.Marshal(args); err == nil {
				summary = string(compact)
			}
		}
	}
	return &pebblestore.ProjectTaskPendingRequest{
		PermissionID: record.ID, SessionID: record.SessionID, Kind: kind, Tool: tool,
		Summary: oneLineProgress(summary, taskLiveSummaryRunes), RequestedAt: firstNonZero(record.PermissionRequested, record.CreatedAt),
	}
}

func firstNonZero(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

// taskHeldLive is waiting with why the session's run cannot run: an update
// drain holding a run that was admitted but has not started (or waits for
// admission), or the account's execution admission queue. nil when the run
// holds a lease or nothing holds it.
func (s *Server) taskHeldLive(account, sessionID string) *pebblestore.ProjectTaskLive {
	manager := s.ExecutionCapacity()
	if manager == nil || s.sessions == nil || s.sessions.Store() == nil {
		return nil
	}
	if lease := manager.ActiveLeaseForSession(account, sessionID); lease != nil {
		return nil
	}
	queued := manager.SessionWaiting(account, sessionID)
	notStarted := false
	if state, found, err := s.sessions.Store().GetV3SessionRunState(sessionID); err == nil && found && state.RunID != "" {
		if intent, ok, err := s.sessions.Store().GetV3SessionRunIntent(sessionID, state.RunID); err == nil && ok {
			notStarted = intent.Status == pebblestore.V3RunIntentPendingExecutor
		}
	}
	if !queued && !notStarted {
		return nil
	}
	if drain := manager.DrainStatus(); drain.Draining {
		return &pebblestore.ProjectTaskLive{State: pebblestore.ProjectTaskStateWaiting, Reason: "held: update drain since " + drain.Since.UTC().Format(time.RFC3339)}
	}
	if !queued {
		// Admitted to start and not held: about to run.
		return nil
	}
	snapshot := manager.Snapshot(account)
	return &pebblestore.ProjectTaskLive{State: pebblestore.ProjectTaskStateWaiting, Reason: fmt.Sprintf("held: execution admission (%d of %d runs active, %d waiting)", snapshot.TotalActive, snapshot.EffectiveLimit, snapshot.Pending)}
}
