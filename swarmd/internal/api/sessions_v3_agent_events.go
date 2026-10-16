package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The agent event feed is what a console (a support hub, a gateway's own
// dashboard) follows to see one agent's conversations as they happen:
//
//	GET /v3/sessions:agent-events?agent=&after=N&limit=N&kind=message,tool&wait=S
//
// It is not a second store. Each entry is read from the durable V3 realtime
// outbox (ordered by its endpoint position, which is the cursor) and the
// session events it carries, for the caller's account and user and the one
// agent's sessions. So the feed is as durable as the sessions, needs no extra
// writes, and keeps nothing the sessions do not: once a session is deleted its
// entries disappear from the feed, and the feed's history is the outbox's
// bounded replay window (pebblestore.DefaultV3RealtimeReplayRetention, at
// least DefaultV3RealtimeMinimumRecords rows). A cursor older than that window
// is reported as a gap, like the signal feed.
//
// Entries are bounded: message text and tool arguments, results and reasons
// are cut to agentEventTextMaxRunes with truncated set.

const (
	agentEventsDefaultLimit = 100
	agentEventsMaxLimit     = 500
	agentEventsScanPage     = 500
	// agentEventsMaxScan bounds the outbox rows one request examines.
	agentEventsMaxScan     = 5_000
	agentEventsMaxWait     = 30
	agentEventTextMaxRunes = 2_000
)

// Feed entry kinds.
const (
	agentEventSessionCreated   = "session.created"
	agentEventMessageUser      = "message.user"
	agentEventMessageAssistant = "message.assistant"
	agentEventToolCalled       = "tool.called"
	agentEventToolAnswered     = "tool.answered"
	agentEventToolFailed       = "tool.failed"
	agentEventSessionEnded     = "session.ended"
	agentEventRunFailed        = "run.failed"
	agentEventTakeoverStarted  = "takeover.started"
	agentEventTakeoverEnded    = "takeover.ended"
)

type agentEvent struct {
	// Seq is the feed position; pass the last next_after back as after.
	Seq       uint64         `json:"seq"`
	ID        string         `json:"id"`
	At        int64          `json:"at"`
	Kind      string         `json:"kind"`
	SessionID string         `json:"session_id"`
	Data      map[string]any `json:"data,omitempty"`
}

type agentEventsPage struct {
	Events    []agentEvent `json:"events"`
	NextAfter uint64       `json:"next_after"`
	LatestSeq uint64       `json:"latest_seq"`
	OldestSeq uint64       `json:"oldest_seq"`
	Gap       bool         `json:"gap"`
}

type agentEventPermission struct {
	ID                string `json:"id"`
	RunID             string `json:"run_id"`
	ToolName          string `json:"tool_name"`
	ToolArguments     string `json:"tool_arguments"`
	ToolCallArguments string `json:"tool_call_arguments"`
	ApprovedArguments string `json:"approved_arguments"`
	Status            string `json:"status"`
	Reason            string `json:"reason"`
}

type agentEventPayload struct {
	Change   string `json:"change"`
	RunID    string `json:"run_id"`
	ToolName string `json:"tool_name"`
	Args     string `json:"arguments"`
	Session  *struct {
		Title string `json:"title"`
	} `json:"session"`
	Message *struct {
		ID       string         `json:"id"`
		Role     string         `json:"role"`
		Content  string         `json:"content"`
		Metadata map[string]any `json:"metadata"`
	} `json:"message"`
	RunIntent *struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	} `json:"run_intent"`
	Permission *agentEventPermission `json:"permission"`
}

// boundAgentEventText cuts text to agentEventTextMaxRunes and reports whether it did.
func boundAgentEventText(text string) (string, bool) {
	if utf8.RuneCountInString(text) <= agentEventTextMaxRunes {
		return text, false
	}
	return string([]rune(text)[:agentEventTextMaxRunes]), true
}

func setBoundedAgentEventText(data map[string]any, key, text string) {
	bounded, truncated := boundAgentEventText(text)
	data[key] = bounded
	if truncated {
		data["truncated"] = true
	}
}

// agentEventFromSessionEvent translates one durable session event into a feed
// entry, or reports that it is not one a console follows.
func agentEventFromSessionEvent(event pebblestore.V3SessionEvent) (string, map[string]any, bool) {
	eventType := strings.TrimSpace(event.EventType)
	if eventType == "session.assistant.delta" || len(event.Payload) == 0 {
		return "", nil, false
	}
	var payload agentEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return "", nil, false
	}
	switch eventType {
	case "session.created":
		data := map[string]any{}
		if payload.Session != nil && payload.Session.Title != "" {
			data["title"] = payload.Session.Title
		}
		return agentEventSessionCreated, data, true
	case "permission.requested", "permission.updated":
		permission := payload.Permission
		if permission == nil || strings.TrimSpace(permission.ID) == "" {
			return "", nil, false
		}
		data := map[string]any{"call_id": permission.ID, "run_id": firstNonEmpty(permission.RunID, payload.RunID), "tool": firstNonEmpty(permission.ToolName, payload.ToolName)}
		if eventType == "permission.requested" {
			setBoundedAgentEventText(data, "arguments", firstNonEmpty(permission.ToolCallArguments, permission.ToolArguments, payload.Args))
			return agentEventToolCalled, data, true
		}
		switch strings.TrimSpace(permission.Status) {
		case pebblestore.PermissionStatusApproved:
			setBoundedAgentEventText(data, "result", permission.ApprovedArguments)
			return agentEventToolAnswered, data, true
		case pebblestore.PermissionStatusDenied, pebblestore.PermissionStatusCancelled:
			setBoundedAgentEventText(data, "reason", permission.Reason)
			return agentEventToolFailed, data, true
		}
		return "", nil, false
	}
	switch payload.Change {
	case sessionChangeEnded:
		return agentEventSessionEnded, map[string]any{}, true
	case sessionChangeTakeoverStarted:
		return agentEventTakeoverStarted, map[string]any{}, true
	case sessionChangeTakeoverEnded:
		return agentEventTakeoverEnded, map[string]any{}, true
	}
	if message := payload.Message; message != nil && strings.TrimSpace(message.Content) != "" {
		data := map[string]any{"message_id": message.ID}
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "user":
			setBoundedAgentEventText(data, "text", message.Content)
			return agentEventMessageUser, data, true
		case "assistant":
			setBoundedAgentEventText(data, "text", message.Content)
			if source, _ := message.Metadata["source"].(string); source == "operator" {
				data["source"] = "operator"
			}
			return agentEventMessageAssistant, data, true
		}
	}
	if intent := payload.RunIntent; intent != nil {
		switch strings.TrimSpace(intent.Status) {
		case pebblestore.V3RunIntentFailed, pebblestore.V3RunIntentInterrupted:
			// The failure reason stays on the machine, as in the signal feed.
			return agentEventRunFailed, map[string]any{"run_id": intent.RunID, "status": intent.Status}, true
		}
	}
	return "", nil, false
}

func agentEventKindVisible(kind string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		if kind == prefix || strings.HasPrefix(kind, prefix+".") {
			return true
		}
	}
	return false
}

// listAgentEvents reads the feed after a cursor for one account, user and agent.
func (s *Server) listAgentEvents(account, user, agent string, after uint64, limit int, kinds []string) (agentEventsPage, error) {
	store := s.sessions.Store()
	if store == nil {
		return agentEventsPage{}, errors.New("session store is not configured")
	}
	latest, err := store.CurrentV3RealtimeOutboxRevision()
	if err != nil {
		return agentEventsPage{}, err
	}
	page := agentEventsPage{Events: []agentEvent{}, NextAfter: after, LatestSeq: latest}
	if state, ok, err := store.GetV3SessionMaintenanceState(); err != nil {
		return agentEventsPage{}, err
	} else if ok {
		page.OldestSeq = state.OldestRetainedRealtimeEndpointSeq
	}
	if after > latest {
		// A cursor from another machine or a reset store never silently resumes.
		page.NextAfter, page.Gap = latest, true
		return page, nil
	}
	if page.OldestSeq > 0 && after+1 < page.OldestSeq {
		page.Gap = true
	}
	agents := map[string]string{} // session ID -> agent name ("" when gone)
	agentOf := func(sessionID string) (string, error) {
		if name, ok := agents[sessionID]; ok {
			return name, nil
		}
		session, found, err := s.sessions.GetSession(sessionID)
		if err != nil {
			return "", err
		}
		name := ""
		if found && session.AccountScopeID == account && session.UserID == user {
			name = sessionsV3MetadataString(session.Metadata, "agent_name")
		}
		agents[sessionID] = name
		return name, nil
	}
	scanned := 0
	cursor := after
	for len(page.Events) < limit && scanned < agentEventsMaxScan {
		records, err := store.ListV3RealtimeOutboxForAuthScopeAfter(account, user, cursor, agentEventsScanPage)
		if err != nil {
			return agentEventsPage{}, err
		}
		for _, record := range records {
			scanned++
			cursor = record.EndpointSeq
			page.NextAfter = record.EndpointSeq
			name, err := agentOf(record.SessionID)
			if err != nil {
				return agentEventsPage{}, err
			}
			if name != agent {
				continue
			}
			kind, data, ok := agentEventFromSessionEvent(record.Event)
			if !ok || !agentEventKindVisible(kind, kinds) {
				continue
			}
			page.Events = append(page.Events, agentEvent{Seq: record.EndpointSeq, ID: record.Event.ID, At: record.Event.TsUnixMs, Kind: kind, SessionID: record.SessionID, Data: data})
			if len(page.Events) >= limit || scanned >= agentEventsMaxScan {
				break
			}
		}
		if len(records) < agentEventsScanPage {
			// Caught up: nothing more for this caller up to the head read above.
			if len(page.Events) < limit && scanned < agentEventsMaxScan && page.NextAfter < latest {
				page.NextAfter = latest
			}
			break
		}
	}
	return page, nil
}

func (s *Server) handleSessionsV3AgentEvents(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "sessions:read") {
		return
	}
	agent, err := agentForSessionsRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	query := r.URL.Query()
	var after uint64
	if raw := strings.TrimSpace(query.Get("after")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("after must be a feed position"))
			return
		}
		after = parsed
	}
	limit := agentEventsDefaultLimit
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > agentEventsMaxLimit {
			writeError(w, http.StatusBadRequest, errors.New("limit must be 1 to 500"))
			return
		}
		limit = parsed
	}
	var kinds []string
	for _, kind := range strings.Split(query.Get("kind"), ",") {
		if kind = strings.TrimSpace(kind); kind != "" {
			kinds = append(kinds, kind)
		}
	}
	wait, err := parseLongPollSeconds(query.Get("wait"), "wait", agentEventsMaxWait)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var deadline <-chan time.Time
	if wait > 0 {
		release, ok := acquireLongPoll(w, r)
		if !ok {
			return
		}
		defer release()
		timer := time.NewTimer(time.Duration(wait) * time.Second)
		defer timer.Stop()
		deadline = timer.C
	}
	// With wait, a read that finds nothing blocks until a session change in
	// the account commits or the wait ends, then re-reads from where it got to.
	var page agentEventsPage
	for {
		changed, release := s.sessionChanges.watch(accountChangeKey(principal.AccountScopeID))
		page, err = s.listAgentEvents(principal.AccountScopeID, principal.UserID, agent, after, limit, kinds)
		if err != nil {
			release()
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if deadline == nil || len(page.Events) > 0 || page.Gap || page.NextAfter < page.LatestSeq {
			release()
			break
		}
		after = page.NextAfter
		select {
		case <-changed:
			release()
			continue
		case <-deadline:
			release()
		case <-r.Context().Done():
			release()
			return
		}
		break
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agent": agent, "events": page.Events, "next_after": page.NextAfter, "latest_seq": page.LatestSeq, "oldest_seq": page.OldestSeq, "gap": page.Gap})
}
