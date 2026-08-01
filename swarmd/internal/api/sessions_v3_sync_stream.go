package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

type sessionsV3SyncStreamRequest struct {
	Surface        string                     `json:"surface,omitempty"`
	SelectorKind   string                     `json:"selector_kind,omitempty"`
	Selector       sessionsV3SyncSelector     `json:"selector,omitempty"`
	SessionIDs     []string                   `json:"session_ids,omitempty"`
	Global         bool                       `json:"global,omitempty"`
	Workspace      sessionsV3WorksetWorkspace `json:"workspace,omitempty"`
	Recent         sessionsV3WorksetRecent    `json:"recent,omitempty"`
	History        sessionsV3WorksetHistory   `json:"history,omitempty"`
	Resources      sessionsV3WorksetResources `json:"resources,omitempty"`
	IncludeActive  bool                       `json:"include_active,omitempty"`
	EndpointCursor string                     `json:"endpoint_cursor"`
	Limit          int                        `json:"limit,omitempty"`
}

type sessionsV3SyncStreamResponse struct {
	OK                 bool                        `json:"ok"`
	EndpointCursor     string                      `json:"endpoint_cursor"`
	Events             []sessionsV3SyncStreamEvent `json:"events"`
	HasMore            bool                        `json:"has_more"`
	Selector           sessionsV3SyncSelector      `json:"selector"`
	ReplayInstructions map[string]any              `json:"replay_instructions"`
}

type sessionsV3SyncStreamEvent struct {
	SessionID           string                            `json:"session_id"`
	EventType           string                            `json:"event_type"`
	Event               sessionruntime.SessionEvent       `json:"event"`
	Projection          sessionruntime.SessionProjection  `json:"projection"`
	Notification        *pebblestore.NotificationRecord   `json:"notification,omitempty"`
	NotificationSummary *pebblestore.NotificationSummary  `json:"notification_summary,omitempty"`
	AITask              *sessionsV3AITaskLifecyclePayload `json:"task,omitempty"`
}

func newSessionsV3SyncStreamEvent(record sessionruntime.RealtimeOutboxRecord) sessionsV3SyncStreamEvent {
	event := record.Event
	event.Payload = sanitizeV3SyncStreamEventPayload(event.EventType, event.Payload)
	return sessionsV3SyncStreamEvent{
		SessionID:  record.SessionID,
		EventType:  record.Event.EventType,
		Event:      event,
		Projection: record.Projection,
	}
}

func sessionsV3SyncStreamNotificationEvent(record sessionruntime.RealtimeOutboxRecord, resources sessionsV3WorksetResources) (sessionsV3SyncStreamEvent, bool) {
	payload, ok := sessionsV3NotificationResourcePayloadFromRecord(record)
	if !ok {
		return sessionsV3SyncStreamEvent{}, false
	}
	event := newSessionsV3SyncStreamEvent(record)
	if resources.Notifications && payload.Notification != nil {
		notification := *payload.Notification
		event.Notification = &notification
	}
	if payload.Summary != nil && (resources.NotificationSummary || payload.Notification == nil) {
		summary := *payload.Summary
		event.NotificationSummary = &summary
	}
	return event, event.Notification != nil || event.NotificationSummary != nil
}

func sanitizeV3SyncStreamEventPayload(eventType string, raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	sanitizeV3SyncStreamValue(eventType, value)
	out, err := json.Marshal(value)
	if err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	return out
}

func sanitizeV3SyncStreamValue(eventType string, value any) {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"endpoint_seq", "endpoint_cursor", "user_id", "account_scope_id", "created_at"} {
			delete(typed, key)
		}
		if session, ok := typed["session"].(map[string]any); ok {
			sanitizeV3SyncStreamSessionPayload(session)
		}
		if strings.TrimSpace(eventType) == "session.metadata.updated" {
			if metadata, ok := typed["metadata"].(map[string]any); ok {
				typed["metadata"] = sessionsV3SyncShellMetadata(metadata)
			}
		}
		for _, child := range typed {
			sanitizeV3SyncStreamValue(eventType, child)
		}
	case []any:
		for _, child := range typed {
			sanitizeV3SyncStreamValue(eventType, child)
		}
	}
}

func sanitizeV3SyncStreamSessionPayload(session map[string]any) {
	// Durable replay carries the session preference/model snapshot as the
	// authoritative current-mode policy. Keep those top-level fields and only
	// trim metadata that belongs to settings or execution internals.
	metadata, ok := session["metadata"].(map[string]any)
	if !ok {
		return
	}
	if sanitized := sessionsV3SyncShellMetadata(metadata); len(sanitized) > 0 {
		session["metadata"] = sanitized
	} else {
		delete(session, "metadata")
	}
}

func (s *Server) handleSessionsV3SyncStream(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		if s == nil || s.sessions == nil {
			writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
			return
		}
		principal, principalOK := PrincipalFromRequest(r)
		if !principalOK || !principal.Valid() {
			writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
			return
		}
		s.writeV3SyncStreamWebsocketUnsupported(w, r)
		return
	}
	principal, ok := s.sessionsV3SyncPrincipal(w, r, http.MethodPost)
	if !ok {
		return
	}
	var req sessionsV3SyncStreamRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	selector, _, err := canonicalSessionsV3SyncBootstrapSelector(sessionsV3SyncBootstrapRequest{
		SelectorKind: req.SelectorKind,
		Selector:     req.Selector,
		SessionIDs:   req.SessionIDs,
		Global:       req.Global,
		Workspace:    req.Workspace,
		Recent:       req.Recent,
		History:      req.History,
		Resources:    req.Resources,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resources := sessionsV3SyncResourceSet(req.Resources, req.History, req.IncludeActive)
	scope := v3SyncCursorScopeForSnapshot(principal, normalizeV3SyncSurface(req.Surface), "v3.sync.snapshot", selector, resources)
	afterEndpointSeq, legacy, err := s.parseV3SyncEndpointCursor(req.EndpointCursor, scope)
	if err != nil {
		writeV3SyncCursorHTTPError(w, err)
		return
	}
	if strings.TrimSpace(req.EndpointCursor) == "" {
		writeV3SyncCursorHTTPError(w, newV3SyncCursorError("endpoint_cursor_required", errors.New("sync stream requires endpoint_cursor from bootstrap or hydrate")))
		return
	}
	if legacy {
		writeV3SyncCursorHTTPError(w, newV3SyncCursorError("endpoint_cursor_legacy_unsupported", errors.New("sync stream requires a signed scoped endpoint_cursor from bootstrap or hydrate")))
		return
	}
	currentHead, err := s.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if afterEndpointSeq > currentHead {
		cursorErr := newV3SyncCursorError("endpoint_cursor_ahead", errors.New("endpoint cursor is ahead of committed realtime outbox; bootstrap required"))
		cursorErr.BootstrapRequired = true
		cursorErr.Latest = currentHead
		writeV3SyncCursorHTTPError(w, cursorErr)
		return
	}
	oldestAvailable, err := s.v3RealtimeOldestAvailableEndpointSeq()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if oldestAvailable > 0 && afterEndpointSeq+1 < oldestAvailable {
		cursorErr := newV3SyncCursorError("endpoint_cursor_too_old", errors.New("endpoint cursor is no longer available; bootstrap required"))
		cursorErr.BootstrapRequired = true
		cursorErr.OldestAvailable = oldestAvailable
		cursorErr.Latest = currentHead
		writeV3SyncCursorHTTPError(w, cursorErr)
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > v3RealtimeReplayLimit {
		limit = v3RealtimeReplayLimit
	}
	records, err := s.listV3SyncStreamRealtimeOutboxAfter(afterEndpointSeq, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	visible := make([]sessionsV3SyncStreamEvent, 0, len(records))
	advanced := afterEndpointSeq
	if len(records) == 0 && afterEndpointSeq < currentHead {
		writeV3SyncStreamGapError(w, afterEndpointSeq+1, oldestAvailable, currentHead)
		return
	}
	expectedEndpointSeq := afterEndpointSeq + 1
	if afterEndpointSeq == ^uint64(0) {
		expectedEndpointSeq = afterEndpointSeq
	}
	for _, record := range records {
		if record.EndpointSeq != expectedEndpointSeq {
			writeV3SyncStreamGapError(w, expectedEndpointSeq, oldestAvailable, currentHead)
			return
		}
		advanced = record.EndpointSeq
		expectedEndpointSeq = record.EndpointSeq + 1
		if !v3RealtimeRecordVisibleToPrincipal(principal, record) {
			continue
		}
		if strings.TrimSpace(record.Event.EventType) == v3NotificationResourceEventType {
			if event, ok := sessionsV3SyncStreamNotificationEvent(record, req.Resources); ok {
				visible = append(visible, event)
			}
			continue
		}
		if strings.TrimSpace(record.Event.EventType) == v3AITaskLifecycleEventType {
			if payload, ok := sessionsV3AITaskLifecyclePayloadFromRecord(record); ok && req.Resources.Tasks && payload.UserID == principal.UserID {
				item := pebblestore.WorkspaceTodoItem{WorkspacePath: payload.WorkspacePath, ManagedSessionID: payload.ManagedSessionID}
				if sessionsV3AITaskMatchesSelector(item, selector) {
					event := newSessionsV3SyncStreamEvent(record)
					event.AITask = &payload
					visible = append(visible, event)
				}
			}
			continue
		}
		matches, err := s.sessionsV3SyncOutboxRecordMatchesSelector(principal, record, selector)
		if err != nil {
			writeV3SyncCursorHTTPError(w, err)
			return
		}
		if matches {
			visible = append(visible, newSessionsV3SyncStreamEvent(record))
		}
	}
	if len(records) < limit && advanced < currentHead {
		writeV3SyncStreamGapError(w, advanced+1, oldestAvailable, currentHead)
		return
	}
	nextCursor, err := s.signV3SyncEndpointCursor(scope, advanced)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionsV3SyncStreamResponse{
		OK:             true,
		EndpointCursor: nextCursor,
		Events:         visible,
		HasMore:        len(records) == limit,
		Selector:       selector,
		ReplayInstructions: map[string]any{
			"stream_path":                        V3SyncStreamPath,
			"transport":                          "http_post",
			"after_endpoint_cursor":              nextCursor,
			"bootstrap_required_on_cursor_error": true,
		},
	})
}

func (s *Server) writeV3SyncStreamWebsocketUnsupported(w http.ResponseWriter, r *http.Request) {
	conn, err := transportws.Accept(w, r)
	if err != nil {
		if errors.Is(err, transportws.ErrUpgradeRequired) {
			writeError(w, http.StatusUpgradeRequired, errors.New("websocket upgrade required"))
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer conn.Close()
	raw, err := json.Marshal(map[string]any{
		"protocol":           V3RealtimeProtocol,
		"protocol_version":   V3RealtimeProtocolVersion,
		"kind":               "cursor.error",
		"code":               "sync_websocket_unsupported",
		"message":            "/v3/sync/stream is HTTP POST only until snapshot-scope websocket sync is implemented",
		"bootstrap_required": false,
	})
	if err == nil {
		_ = conn.WriteText(raw)
	}
}

func (s *Server) sessionsV3SyncOutboxRecordMatchesSelector(principal identity.Principal, record sessionruntime.RealtimeOutboxRecord, selector sessionsV3SyncSelector) (bool, error) {
	kind := strings.TrimSpace(selector.Kind)
	switch kind {
	case "", "global":
		return true, nil
	case "recent":
		if selector.Global {
			return true, nil
		}
		return sessionsV3SyncOutboxRecordMatchesWorkspacePaths(principal, record, selector)
	case "session_ids":
		for _, id := range selector.SessionIDs {
			if strings.TrimSpace(id) == record.SessionID {
				return true, nil
			}
		}
		return false, nil
	case "workspace", "tui":
		return sessionsV3SyncOutboxRecordMatchesWorkspacePaths(principal, record, selector)
	default:
		return false, errors.New("unsupported sync selector kind " + kind)
	}
}

func sessionsV3SyncOutboxRecordMatchesWorkspacePaths(principal identity.Principal, record sessionruntime.RealtimeOutboxRecord, selector sessionsV3SyncSelector) (bool, error) {
	var (
		paths []string
		err   error
	)
	if strings.TrimSpace(selector.CWDPath) != "" || strings.TrimSpace(selector.Kind) == "tui" {
		paths, err = canonicalSessionsV3TUIWorksetPaths(sessionsV3TUIWorksetScope{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths, CWDPath: selector.CWDPath})
	} else {
		paths, err = canonicalSessionsV3WorksetWorkspacePaths(sessionsV3WorksetWorkspace{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths})
	}
	if err != nil {
		return false, err
	}
	if len(paths) == 0 {
		return true, nil
	}
	session, ok := v3RealtimeSessionSnapshotFromRecord(record)
	if !ok {
		return false, newV3SyncMembershipUnavailableError(record.EndpointSeq)
	}
	return v3RealtimeSessionMatchesWorksetSelector(principal, session, V3RealtimeWorksetSelector{Kind: "workspace", WorkspacePaths: paths}), nil
}

var v3SyncStreamListRealtimeOutboxAfter = func(s *Server, afterEndpointSeq uint64, limit int) ([]sessionruntime.RealtimeOutboxRecord, error) {
	return s.sessions.ListRealtimeOutboxAfter(afterEndpointSeq, limit)
}

func (s *Server) listV3SyncStreamRealtimeOutboxAfter(afterEndpointSeq uint64, limit int) ([]sessionruntime.RealtimeOutboxRecord, error) {
	return v3SyncStreamListRealtimeOutboxAfter(s, afterEndpointSeq, limit)
}

func writeV3SyncStreamGapError(w http.ResponseWriter, missingEndpointSeq, oldestAvailable, latest uint64) {
	cursorErr := newV3SyncCursorError("endpoint_cursor_gap", errors.New("endpoint cursor cannot be replayed continuously from durable realtime outbox; bootstrap required"))
	cursorErr.BootstrapRequired = true
	cursorErr.OldestAvailable = oldestAvailable
	cursorErr.MissingEndpointSeq = missingEndpointSeq
	cursorErr.Latest = latest
	writeV3SyncCursorHTTPError(w, cursorErr)
}

func newV3SyncMembershipUnavailableError(endpointSeq uint64) *v3SyncCursorError {
	cursorErr := newV3SyncCursorError("endpoint_membership_unavailable", errors.New("endpoint membership unavailable for scoped replay; bootstrap required"))
	cursorErr.BootstrapRequired = true
	cursorErr.MissingEndpointSeq = endpointSeq
	return cursorErr
}

func writeV3SyncCursorHTTPError(w http.ResponseWriter, err error) {
	var cursorErr *v3SyncCursorError
	if errors.As(err, &cursorErr) {
		status := http.StatusBadRequest
		if cursorErr.BootstrapRequired {
			status = http.StatusGone
		}
		body := map[string]any{"ok": false, "error": cursorErr.Error(), "error_code": cursorErr.Code, "bootstrap_required": cursorErr.BootstrapRequired, "oldest_available": cursorErr.OldestAvailable, "latest": cursorErr.Latest}
		if cursorErr.MissingEndpointSeq > 0 {
			body["missing_endpoint_seq"] = cursorErr.MissingEndpointSeq
		}
		writeJSON(w, status, body)
		return
	}
	writeError(w, http.StatusBadRequest, err)
}
