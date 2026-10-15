package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

const activityStreamLimit = 32
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
	case "session.delivery.updated", "session.landing.updated", "session.run_intent.updated", "session.run_intent.recorded":
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

func (s *Server) activityPage(principal identity.Principal, cursor uint64, project, worker string) ([]activityEvent, uint64, bool, error) {
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
		if row.AccountScopeID != principal.AccountScopeID || row.UserID != principal.UserID {
			continue
		}
		allowed, err := s.authorizeReadableSessionV3Access(principal, row.SessionID)
		if err != nil {
			return nil, cursor, false, err
		}
		if !allowed {
			continue
		}
		event, ok := activityMetadata(row)
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
	if r.URL.Path == "/v3/activity" {
		events, next, gap, err := s.activityPage(principal, cursor, project, worker)
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
	gap := func(end uint64) {
		send("gap", map[string]any{"from": cursor + 1, "to": end, "cursor": strconv.FormatUint(cursor, 10)})
	}
	ticker := time.NewTicker(activityHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case slow := <-sub.slow:
			gap(slow.EndpointSeq)
			return
		default:
		}
		events, next, expired, err := s.activityPage(principal, cursor, project, worker)
		if err != nil {
			return
		}
		if expired {
			send("gap", map[string]any{"from": cursor + 1, "to": next, "cursor": strconv.FormatUint(next, 10)})
			return
		}
		for _, event := range events {
			if !send("activity", event) {
				return
			}
		}
		advanced := next != cursor
		cursor = next
		if advanced {
			if !send("cursor", map[string]string{"cursor": strconv.FormatUint(cursor, 10)}) {
				return
			}
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case slow := <-sub.slow:
			gap(slow.EndpointSeq)
			return
		case <-sub.send: // A wakeup only: always replay committed rows in durable sequence order.
		case <-ticker.C:
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
		}
	}
}
