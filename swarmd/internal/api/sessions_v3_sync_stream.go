package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

type sessionsV3SyncStreamRequest struct {
	Surface        string                     `json:"surface,omitempty"`
	SelectorKind   string                     `json:"selector_kind,omitempty"`
	Selector       sessionsV3SyncSelector     `json:"selector,omitempty"`
	History        sessionsV3WorksetHistory   `json:"history,omitempty"`
	Resources      sessionsV3WorksetResources `json:"resources,omitempty"`
	IncludeActive  bool                       `json:"include_active,omitempty"`
	EndpointCursor string                     `json:"endpoint_cursor"`
	Limit          int                        `json:"limit,omitempty"`
}

func (s *Server) handleSessionsV3SyncStream(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.handleV3RealtimeStream(w, r)
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
	selector := normalizeSessionsV3SyncSelector(req.SelectorKind, req.Selector, nil, false, sessionsV3WorksetWorkspace{}, sessionsV3WorksetRecent{})
	resources := sessionsV3SyncResourceSet(req.Resources, req.History, req.IncludeActive)
	scope := v3SyncCursorScopeForSnapshot(principal, normalizeV3SyncSurface(req.Surface), "v3.sync.snapshot", selector, resources)
	afterEndpointSeq, _, err := s.parseV3SyncEndpointCursor(req.EndpointCursor, scope)
	if err != nil {
		writeV3SyncCursorHTTPError(w, err)
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > v3RealtimeReplayLimit {
		limit = v3RealtimeReplayLimit
	}
	records, err := s.sessions.ListRealtimeOutboxForAuthScopeAfter(principal.AccountScopeID, principal.UserID, afterEndpointSeq, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	visible := make([]sessionruntime.RealtimeOutboxRecord, 0, len(records))
	advanced := afterEndpointSeq
	for _, record := range records {
		if record.EndpointSeq > advanced {
			advanced = record.EndpointSeq
		}
		matches, err := s.sessionsV3SyncOutboxRecordMatchesSelector(principal.AccountScopeID, record, selector)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if matches {
			visible = append(visible, record)
		}
	}
	nextCursor, err := s.signV3SyncEndpointCursor(scope, advanced)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"endpoint_cursor":     nextCursor,
		"after_endpoint_seq":  afterEndpointSeq,
		"high_watermark_seq":  advanced,
		"events":              visible,
		"has_more":            len(records) == limit,
		"selector":            selector,
		"replay_instructions": map[string]any{"stream_path": V3SyncStreamPath, "after_endpoint_cursor": nextCursor, "bootstrap_required_on_cursor_error": true},
	})
}

func (s *Server) sessionsV3SyncOutboxRecordMatchesSelector(accountScopeID string, record sessionruntime.RealtimeOutboxRecord, selector sessionsV3SyncSelector) (bool, error) {
	kind := strings.TrimSpace(selector.Kind)
	switch kind {
	case "", "global", "recent":
		return true, nil
	case "session_ids":
		for _, id := range selector.SessionIDs {
			if strings.TrimSpace(id) == record.SessionID {
				return true, nil
			}
		}
		return false, nil
	case "workspace", "tui":
		if strings.TrimSpace(selector.WorkspacePath) == "" && len(selector.WorkspacePaths) == 0 && strings.TrimSpace(selector.CWDPath) == "" {
			return true, nil
		}
		session, found, err := s.sessions.GetSession(record.SessionID)
		if err != nil || !found {
			return false, err
		}
		paths, err := canonicalSessionsV3TUIWorksetPaths(sessionsV3TUIWorksetScope{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths, CWDPath: selector.CWDPath})
		if err != nil {
			paths, err = canonicalSessionsV3WorksetWorkspacePaths(sessionsV3WorksetWorkspace{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths})
			if err != nil {
				return false, err
			}
		}
		return sessionsV3TUISessionVisibleForPaths(session, identity.Principal{AccountScopeID: accountScopeID}, paths), nil
	default:
		return true, nil
	}
}

func writeV3SyncCursorHTTPError(w http.ResponseWriter, err error) {
	var cursorErr *v3SyncCursorError
	if errors.As(err, &cursorErr) {
		status := http.StatusBadRequest
		if cursorErr.BootstrapRequired {
			status = http.StatusGone
		}
		writeJSON(w, status, map[string]any{"ok": false, "error": cursorErr.Error(), "error_code": cursorErr.Code, "bootstrap_required": cursorErr.BootstrapRequired, "oldest_available": cursorErr.OldestAvailable, "latest": cursorErr.Latest})
		return
	}
	writeError(w, http.StatusBadRequest, err)
}
