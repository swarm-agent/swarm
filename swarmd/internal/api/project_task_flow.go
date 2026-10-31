package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The flow report service: where a task's (or worker job's) time and tokens
// went, and a project's roll-up over a period. One service function each,
// served by GET /v3/projects/{id}/tasks/{task}/flow and GET
// /v3/projects/{id}/flow?period=, and by manage_projects get_task
// detail=flow and manage_projects flow (tool.ProjectTaskFlowService). The
// report is computed from durable records only (jobautomation.BuildFlowReport):
// the task's session events and run records, the runs' usage records and the
// job's workflow deliveries. Nothing is stored.

const (
	flowRunsPerSession   = 1000
	flowMessagesPerSess  = 100000
	flowEventsPage       = 1000
	flowRollupTasks      = 200
	flowRollupTaskListed = 1000
)

// flowError carries the HTTP status of a refused flow read.
type flowError struct {
	status int
	err    error
}

func (e *flowError) Error() string { return e.err.Error() }
func (e *flowError) Unwrap() error { return e.err }

func flowStatus(err error) int {
	var fe *flowError
	if errors.As(err, &fe) {
		return fe.status
	}
	return http.StatusInternalServerError
}

// ProjectTaskFlow is a task's flow report (and its worker job's, when the
// task carries one).
func (s *Server) ProjectTaskFlow(ctx context.Context, p identity.Principal, projectID, taskID string) (jobautomation.FlowReport, error) {
	db, err := s.flowStore(p)
	if err != nil {
		return jobautomation.FlowReport{}, err
	}
	task, found, err := db.GetProjectTask(p.AccountScopeID, strings.TrimSpace(projectID), strings.TrimSpace(taskID))
	if err != nil {
		return jobautomation.FlowReport{}, err
	}
	if !found || task == nil {
		return jobautomation.FlowReport{}, &flowError{http.StatusNotFound, errors.New("project task not found")}
	}
	src, err := s.taskFlowSource(ctx, db, p.AccountScopeID, task, 0, 0)
	if err != nil {
		return jobautomation.FlowReport{}, err
	}
	return jobautomation.BuildFlowReport(src), nil
}

// ProjectFlowRollup is a project's flow over a period (today, 7d, 30d): the
// tasks active in it, each bounded to the period, summed.
func (s *Server) ProjectFlowRollup(ctx context.Context, p identity.Principal, projectID, period string) (jobautomation.FlowRollup, error) {
	db, err := s.flowStore(p)
	if err != nil {
		return jobautomation.FlowRollup{}, err
	}
	projectID = strings.TrimSpace(projectID)
	if _, found, err := db.GetProject(p.AccountScopeID, projectID); err != nil {
		return jobautomation.FlowRollup{}, err
	} else if !found {
		return jobautomation.FlowRollup{}, &flowError{http.StatusNotFound, errors.New("project not found")}
	}
	name, from, to, err := jobautomation.FlowPeriod(period, time.Now())
	if err != nil {
		return jobautomation.FlowRollup{}, &flowError{http.StatusBadRequest, err}
	}
	tasks, err := db.ListProjectTasks(p.AccountScopeID, projectID, flowRollupTaskListed)
	if err != nil {
		return jobautomation.FlowRollup{}, err
	}
	reports := []jobautomation.FlowReport{}
	partial := len(tasks) >= flowRollupTaskListed
	for i := range tasks {
		task := &tasks[i]
		// A task untouched since the period began did nothing in it.
		if task.UpdatedAt > 0 && task.UpdatedAt < from {
			continue
		}
		if len(reports) == flowRollupTasks {
			partial = true
			break
		}
		if err := ctx.Err(); err != nil {
			return jobautomation.FlowRollup{}, err
		}
		src, err := s.taskFlowSource(ctx, db, p.AccountScopeID, task, from, to)
		if err != nil {
			return jobautomation.FlowRollup{}, err
		}
		reports = append(reports, jobautomation.BuildFlowReport(src))
	}
	rollup := jobautomation.BuildFlowRollup(projectID, name, from, to, reports)
	rollup.Partial = rollup.Partial || partial
	return rollup, nil
}

func (s *Server) flowStore(p identity.Principal) (*pebblestore.SessionStore, error) {
	if !p.Valid() || strings.TrimSpace(p.AccountScopeID) == "" {
		return nil, &flowError{http.StatusForbidden, errors.New("authenticated principal required")}
	}
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.sessions.Store(), nil
}

// taskFlowSource reads the records of one task's flow: its sessions' runs,
// events and usage, its job's deliveries and the messages sent to it.
func (s *Server) taskFlowSource(ctx context.Context, db *pebblestore.SessionStore, account string, task *pebblestore.ProjectTaskRecord, from, to int64) (jobautomation.FlowSource, error) {
	src := jobautomation.FlowSource{
		JobID: taskRunJobID(task.ProjectID, task.ID), ProjectID: task.ProjectID, TaskID: task.ID, Title: task.Title, Agent: task.Agent,
		Now: time.Now().UnixMilli(), From: from, To: to, Events: map[string][]pebblestore.V3SessionEvent{},
	}
	src.Live, src.WaitingOnPerson = flowTaskLive(task.Status)
	// The task's sessions: its own and each attempt's, with the attempt's role.
	roles := map[string]string{}
	order := []string{}
	addSession := func(id, role string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := roles[id]; !ok {
			order = append(order, id)
			roles[id] = ""
		}
		if roles[id] == "" {
			roles[id] = strings.TrimSpace(role)
		}
	}
	addSession(task.SessionID, task.Agent)
	for _, attempt := range task.Attempts {
		addSession(attempt.SessionID, firstNonEmptyString(attempt.Role, task.Agent))
	}
	for _, sessionID := range order {
		if err := ctx.Err(); err != nil {
			return src, err
		}
		session, found, err := db.GetSession(sessionID)
		if err != nil {
			return src, err
		}
		// Only the account's own sessions are read.
		if !found || strings.TrimSpace(session.AccountScopeID) != account {
			continue
		}
		// One more than the bound: a session with more runs is reported
		// as partial (the store reads runs in key order, not time order).
		intents, err := db.ListV3SessionRunIntents(sessionID, 0, flowRunsPerSession+1)
		if err != nil {
			return src, err
		}
		if len(intents) > flowRunsPerSession {
			intents = intents[:flowRunsPerSession]
			src.RunsTruncated = true
		}
		for _, intent := range intents {
			run := jobautomation.FlowRun{SessionID: sessionID, RunID: intent.RunID, Role: roles[sessionID], Status: intent.Status, Reason: intent.BlockedReason,
				StartedAt: intent.StartedAt}
			if run.StartedAt == 0 {
				run.StartedAt = intent.CreatedAt
			}
			switch intent.Status {
			case pebblestore.V3RunIntentRunning, pebblestore.V3RunIntentPendingExecutor, pebblestore.V3RunIntentWaitingTasks:
			default:
				run.EndedAt = intent.CompletedAt
				if run.EndedAt == 0 {
					run.EndedAt = intent.UpdatedAt
				}
			}
			if usage, ok, err := db.GetTurnUsage(sessionID, intent.RunID); err != nil {
				return src, err
			} else if ok {
				u := usage
				run.Usage = &u
			}
			src.Runs = append(src.Runs, run)
		}
		events := []pebblestore.V3SessionEvent{}
		after := uint64(0)
		for len(events) < jobautomation.FlowMaxEventsPerSession {
			page, err := db.ListV3SessionEvents(sessionID, after, flowEventsPage)
			if err != nil {
				return src, err
			}
			for _, ev := range page {
				if flowEventKept(ev.EventType) {
					events = append(events, ev)
				}
				after = ev.Seq
			}
			if len(page) < flowEventsPage {
				break
			}
			if len(events) >= jobautomation.FlowMaxEventsPerSession {
				src.EventsTruncated = true
			}
		}
		src.Events[sessionID] = events
	}
	// The job's workflow deliveries: the task's job, and its worker job.
	if store, err := s.jobAutomationStore(); err == nil {
		jobs := []string{src.JobID}
		if task.WorkerID != "" && task.WorkerRunID != "" {
			jobs = append(jobs, "worker/"+task.WorkerID+"/"+task.WorkerRunID)
		}
		for _, job := range jobs {
			list, err := store.JobDeliveries(account, job)
			if err != nil {
				return src, err
			}
			src.Deliveries = append(src.Deliveries, list...)
		}
	}
	// Every message sent to the task, not the task view's newest few.
	messages, truncated, err := db.ProjectTaskMessagesAll(task, flowMessagesPerSess)
	if err != nil {
		return src, err
	}
	src.MessagesTruncated = truncated
	for _, message := range messages {
		if (from == 0 || message.At >= from) && (to == 0 || message.At <= to) {
			src.Interventions++
		}
	}
	return src, nil
}

// flowTaskLive reports whether a task with status is unfinished (the time
// since its last activity counts) and whether it waits on the person. The
// statuses are the project task statuses (pebblestore.ProjectTaskCountBucket)
// and the job statuses the daemon treats as still running.
func flowTaskLive(status string) (live, waitingOnPerson bool) {
	switch strings.TrimSpace(status) {
	case "pending_approval", "needs_input", "waiting_on_approval":
		return true, true
	case "queued", "pending", "starting", "planning", "in_progress", "running", pebblestore.ProjectTaskStatusLimitWaiting:
		// A task waiting on a provider limit counts it from its run's
		// limit end.
		return true, false
	}
	return false, false
}

// flowEventKept is an event the flow report reads: tool calls and
// permission waits. Others are skipped so the bound holds more of them.
func flowEventKept(eventType string) bool {
	return strings.HasPrefix(eventType, "session.tool.") && eventType != "session.tool.delta" ||
		eventType == "permission.requested" || eventType == "permission.updated"
}

// handleProjectTaskFlow serves GET /v3/projects/{id}/tasks/{task}/flow.
func (s *Server) handleProjectTaskFlow(w http.ResponseWriter, r *http.Request, p identity.Principal, projectID, taskID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
		return
	}
	if fleetActivityGatewayCaller(r) {
		writeError(w, http.StatusForbidden, errors.New("gateway tokens cannot read a task's flow"))
		return
	}
	report, err := s.ProjectTaskFlow(r.Context(), p, projectID, taskID)
	if err != nil {
		writeError(w, flowStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"flow": report, "markdown": jobautomation.FlowMarkdown(report)})
}

// handleProjectFlow serves GET /v3/projects/{id}/flow?period=today|7d|30d.
func (s *Server) handleProjectFlow(w http.ResponseWriter, r *http.Request, p identity.Principal, projectID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
		return
	}
	if fleetActivityGatewayCaller(r) {
		writeError(w, http.StatusForbidden, errors.New("gateway tokens cannot read a project's flow"))
		return
	}
	rollup, err := s.ProjectFlowRollup(r.Context(), p, projectID, r.URL.Query().Get("period"))
	if err != nil {
		writeError(w, flowStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"flow": rollup, "markdown": jobautomation.FlowRollupMarkdown(rollup)})
}
