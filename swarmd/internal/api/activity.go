package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const activityStreamLimit = 32
const activityPagePath = "/v3/activity"
const activityStreamPath = "/v3/activity/stream"
const activityHeartbeat = 15 * time.Second

type activityEvent struct {
	Seq        uint64 `json:"seq"`
	At         int64  `json:"at"`
	Kind       string `json:"kind"`
	SessionID  string `json:"session_id"`
	JobID      string `json:"job_id"`
	WorkerID   string `json:"worker_id"`
	WorkerName string `json:"worker_name"`
	ProjectID  string `json:"project_id"`
	State      string `json:"state"`
	Summary    string `json:"summary"`
	ToolName   string `json:"tool_name,omitempty"`
	FilePath   string `json:"file_path,omitempty"`
}

// No free-form summaries, messages, output, or arbitrary payload maps cross this boundary.
func activityMetadata(row sessionruntime.RealtimeOutboxRecord) (activityEvent, bool) {
	if row.Event.EventType == pebblestore.ProjectUpdatedEventType {
		return projectActivityMetadata(row)
	}
	if row.Event.EventType == pebblestore.WorkerUpdatedEventType {
		var metadata pebblestore.WorkerRealtimePayload
		if json.Unmarshal(row.Event.Payload, &metadata) != nil {
			return activityEvent{}, false
		}
		switch metadata.ActivityKind {
		case "job.started", "job.state_changed", "job.finished", "job.failed", "review.recorded":
		default:
			return activityEvent{}, false
		}
		return activityEvent{Seq: row.EndpointSeq, At: row.CreatedAt, Kind: metadata.ActivityKind, SessionID: metadata.SessionID, JobID: "worker/" + metadata.WorkerID + "/" + metadata.RunID, WorkerID: metadata.WorkerID, WorkerName: metadata.WorkerName, ProjectID: metadata.ProjectID, State: metadata.State, Summary: metadata.ActivityKind}, true
	}
	state := ""
	switch row.Event.EventType {
	case "session.created":
		state = "started"
	case "session.assistant.started":
		state = "running"
	case "session.assistant.completed", "session.run.completed":
		state = "finished"
	case "session.run.failed", "session.tool.failed":
		state = "failed"
	case "session.run.cancelled", "session.run.interrupted":
		state = "stopped"
	case "session.tool.started":
		state = "started"
	case "session.tool.completed":
		state = "finished"
	case "session.review.recorded":
		state = "recorded"
	case "session.delivery.updated", "session.lifecycle.updated", "session.task.reported", "session.task.delivered", "session.run.waiting_tasks", "session.run.tasks_ready", "session.run_intent.updated", "session.run_intent.recorded":
		state = "changed"
	default:
		return activityEvent{}, false
	}
	event := activityEvent{Seq: row.EndpointSeq, At: row.CreatedAt, Kind: row.Event.EventType, SessionID: row.SessionID, State: state, Summary: row.Event.EventType}
	if row.Membership != nil {
		for key, dst := range map[string]*string{"job_id": &event.JobID, "worker_id": &event.WorkerID, "worker_name": &event.WorkerName, "project_id": &event.ProjectID} {
			if value, ok := row.Membership.Metadata[key].(string); ok {
				*dst = value
			}
		}
	}
	if event.Kind == "session.delivery.updated" {
		var receipt struct {
			State     string `json:"state"`
			JobID     string `json:"job_id"`
			ProjectID string `json:"project_id"`
		}
		if json.Unmarshal(row.Event.Payload, &receipt) == nil {
			switch receipt.State {
			case "job.delivered", "job.publish_failed", "job.pull_request":
				event.State = receipt.State
			}
			event.JobID, event.ProjectID = receipt.JobID, receipt.ProjectID
		}
	}
	if row.Membership != nil {
		metadata := row.Membership.Metadata
		if run, ok := metadata["worker_run_id"].(string); ok && event.WorkerID != "" && run != "" {
			event.JobID = "worker/" + event.WorkerID + "/" + run
		}
	}
	var lifecycle struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(row.Event.Payload, &lifecycle) == nil {
		switch lifecycle.Status {
		case "pending_executor", "running", "completed", "failed", "cancelled", "interrupted", "waiting_tasks":
			event.State = lifecycle.Status
		}
	}
	if strings.HasPrefix(event.Kind, "session.tool.") {
		var payload struct {
			ToolName  string `json:"tool_name"`
			Arguments string `json:"arguments"`
		}
		if json.Unmarshal(row.Event.Payload, &payload) == nil {
			event.ToolName = payload.ToolName
			switch payload.ToolName {
			case "read", "write", "edit":
				var args struct {
					Path string `json:"path"`
				}
				if json.Unmarshal([]byte(payload.Arguments), &args) == nil {
					event.FilePath = args.Path
				}
			}
		}
	}
	return event, true
}

// projectActivityMetadata is a project.updated record as activity: only that
// the project (or one of its tasks, its workflow deliveries or job-end
// evaluations) changed, so a reader such as a linked wait reads it again. The
// payload's project must be the record's own project session; records without
// one (the project order) are not activity.
func projectActivityMetadata(row sessionruntime.RealtimeOutboxRecord) (activityEvent, bool) {
	var payload struct {
		ProjectID string `json:"project_id"`
	}
	if json.Unmarshal(row.Event.Payload, &payload) != nil {
		return activityEvent{}, false
	}
	project := strings.TrimSpace(payload.ProjectID)
	if project == "" || row.AccountScopeID == "" || row.SessionID != pebblestore.ProjectRealtimeSessionID(row.AccountScopeID, project) {
		return activityEvent{}, false
	}
	return activityEvent{Seq: row.EndpointSeq, At: row.CreatedAt, Kind: pebblestore.ProjectUpdatedEventType, ProjectID: project, State: "changed", Summary: pebblestore.ProjectUpdatedEventType}, true
}

// activityPage reads the principal's activity after cursor. projects admits
// project.updated records of the principal's account: projects are account
// scoped, so every explicit user of the account may read them, as the project
// routes allow; an application gateway (confined to one project) may not.
func (s *Server) activityPage(principal identity.Principal, cursor uint64, project, worker string, projects bool) ([]activityEvent, uint64, bool, error) {
	head, err := s.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		return nil, cursor, false, err
	}
	oldest, err := s.sessions.OldestRetainedRealtimeOutboxRevision()
	if err != nil {
		return nil, cursor, false, err
	}
	if cursor > head || (oldest > 0 && cursor < oldest-1) {
		return nil, head, true, nil
	}
	rows, err := s.sessions.ListRealtimeOutboxAfter(cursor, 256)
	if err != nil {
		return nil, cursor, false, err
	}
	events := make([]activityEvent, 0)
	for _, row := range rows {
		if row.EndpointSeq != cursor+1 {
			return nil, head, true, nil
		}
		cursor = row.EndpointSeq
		if row.AccountScopeID != principal.AccountScopeID {
			continue
		}
		if row.Event.EventType == pebblestore.ProjectUpdatedEventType {
			// Committed by the store, not by a user: account-scoped.
			if !projects || principal.Type != identity.PrincipalTypeUser {
				continue
			}
		} else if row.UserID != principal.UserID {
			continue
		}
		event, ok := activityMetadata(row)
		if !ok {
			continue
		}
		allowed, err := s.authorizeActivityEvent(principal, row, event)
		if err != nil {
			return nil, cursor, false, err
		}
		if !allowed {
			continue
		}
		if ok && (project == "" || event.ProjectID == project) && (worker == "" || event.WorkerID == worker) {
			events = append(events, event)
		}
	}
	return events, cursor, false, nil
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !s.requireScope(w, r, "sessions:read") {
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.sessions == nil || s.v3RealtimeOutbox == nil {
		http.Error(w, "activity unavailable", http.StatusServiceUnavailable)
		return
	}
	cursor := uint64(0)
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var err error
		cursor, err = strconv.ParseUint(raw, 10, 64)
		if err != nil {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
	}
	project, worker := r.URL.Query().Get("project"), r.URL.Query().Get("worker")
	projects := !fleetActivityGatewayCaller(r)
	if r.URL.Path == activityPagePath {
		events, next, gap, err := s.activityPage(principal, cursor, project, worker, projects)
		if err != nil {
			http.Error(w, "activity replay failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "cursor": strconv.FormatUint(next, 10), "gap": gap})
		return
	}
	if s.activityStreams.Add(1) > activityStreamLimit {
		s.activityStreams.Add(-1)
		http.Error(w, "activity stream limit", http.StatusTooManyRequests)
		return
	}
	defer s.activityStreams.Add(-1)
	sub := s.v3RealtimeOutbox.subscribe()
	defer s.v3RealtimeOutbox.unsubscribe(sub)
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(kind string, value any) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	// Flush headers immediately so a client can establish its subscription before
	// committing work. The deadline is refreshed before every subsequent write.
	_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	if controller.Flush() != nil {
		return
	}
	ticker := time.NewTicker(activityHeartbeat)
	defer ticker.Stop()
	heartbeat := func() bool {
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if gap := s.followActivity(r.Context(), sub, principal, cursor, project, worker, projects, send, ticker.C, heartbeat); gap != nil {
		send("gap", map[string]any{"from": gap.From, "to": gap.To, "cursor": strconv.FormatUint(gap.Cursor, 10)})
	}
}

// activityGap is where a followed feed lost records: from..to were not
// delivered, and Cursor is where a reader resumes.
type activityGap struct {
	From, To, Cursor uint64
}

// followActivity replays the principal's activity after cursor and follows it
// live through sub, calling emit for each activity and cursor frame. It returns
// when ctx ends or emit fails (nil), or with the gap that ends the feed: the
// subscription overflowed (resume at the last delivered cursor) or the cursor
// is expired or ahead (resume at the head). beat runs on each heartbeat tick;
// a nil heartbeat channel never ticks.
func (s *Server) followActivity(ctx context.Context, sub *v3RealtimeOutboxSubscriber, principal identity.Principal, cursor uint64, project, worker string, projects bool, emit func(kind string, value any) bool, heartbeat <-chan time.Time, beat func() bool) *activityGap {
	for {
		select {
		case slow := <-sub.slow:
			return &activityGap{From: cursor + 1, To: slow.EndpointSeq, Cursor: cursor}
		default:
		}
		events, next, expired, err := s.activityPage(principal, cursor, project, worker, projects)
		if err != nil {
			return nil
		}
		if expired {
			return &activityGap{From: cursor + 1, To: next, Cursor: next}
		}
		for _, event := range events {
			if !emit("activity", event) {
				return nil
			}
		}
		advanced := next != cursor
		cursor = next
		// These records are wakeups, not a second delivery authority. Discard
		// already observed notifications in bounded work before replaying again.
		for i := 0; advanced && i < v3RealtimeSubscriberBufSize; i++ {
			select {
			case <-sub.send:
			default:
				i = v3RealtimeSubscriberBufSize
			}
		}
		if advanced {
			if !emit("cursor", map[string]string{"cursor": strconv.FormatUint(cursor, 10)}) {
				return nil
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case slow := <-sub.slow:
			return &activityGap{From: cursor + 1, To: slow.EndpointSeq, Cursor: cursor}
		case <-sub.send: // A wakeup only: always replay committed rows in durable sequence order.
		case <-heartbeat:
			if !beat() {
				return nil
			}
		}
	}
}

// Only absent, never tombstoned, worker sessions can use admission ownership.
// An existing session's ACL denial must not fall back to worker ownership.
func (s *Server) authorizeActivityEvent(p identity.Principal, row sessionruntime.RealtimeOutboxRecord, event activityEvent) (bool, error) {
	if row.Event.EventType == pebblestore.ProjectUpdatedEventType {
		// activityPage admitted it for this account's user; it names no session.
		return event.SessionID == "" && row.AccountScopeID == p.AccountScopeID && p.Type == identity.PrincipalTypeUser, nil
	}
	if event.SessionID != "" {
		_, exists, err := s.sessions.GetSession(event.SessionID)
		if err != nil {
			return false, err
		}
		if exists {
			return s.authorizeReadableSessionV3Access(p, event.SessionID)
		}
		_, tombstoned, err := s.sessions.Store().GetV3SessionTombstone(event.SessionID)
		if err != nil {
			return false, err
		}
		if tombstoned {
			return s.authorizeReadableSessionV3Access(p, event.SessionID)
		}
	}
	if row.Event.EventType != pebblestore.WorkerUpdatedEventType {
		return false, nil
	}
	var payload pebblestore.WorkerRealtimePayload
	if err := json.Unmarshal(row.Event.Payload, &payload); err != nil {
		return false, err
	}
	run, exists, err := s.sessions.Store().WorkerStore().GetWorkerRun(p.AccountScopeID, payload.WorkerID, payload.RunID)
	if err != nil {
		return false, err
	}
	return exists && run.AccountScopeID == p.AccountScopeID && run.UserID == p.UserID && run.SessionID == event.SessionID, nil
}
