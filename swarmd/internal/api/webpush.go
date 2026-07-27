package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/webpush"
)

const webPushRoutePrefix = "/v1/notifications/push"

func (s *Server) handleWebPush(w http.ResponseWriter, r *http.Request) {
	if s.webPush == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("web push service is not configured"))
		return
	}
	accountScopeID, ok := s.notificationAccountScopeID(w, r)
	if !ok {
		return
	}
	path := strings.TrimSuffix(strings.TrimSpace(r.URL.Path), "/")
	switch {
	case path == webPushRoutePrefix && r.Method == http.MethodGet:
		status, err := s.webPush.Status(r.Context(), accountScopeID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
	case path == webPushRoutePrefix+"/subscriptions" && r.Method == http.MethodGet:
		records, err := s.webPush.List(r.Context(), accountScopeID, parseIntQuery(r, "limit", 200))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "subscriptions": records})
	case path == webPushRoutePrefix+"/subscriptions" && r.Method == http.MethodPost:
		var input webpush.SubscriptionInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		record, changed, err := s.webPush.Upsert(r.Context(), accountScopeID, input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "subscription": record, "changed": changed})
	case strings.HasPrefix(path, webPushRoutePrefix+"/subscriptions/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(path, webPushRoutePrefix+"/subscriptions/")
		removed, err := s.webPush.Delete(r.Context(), accountScopeID, id)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
	case path == webPushRoutePrefix+"/test" && r.Method == http.MethodPost:
		result, err := s.webPush.Test(r.Context(), accountScopeID)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "result": result, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
	default:
		if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
			methodNotAllowed(w)
			return
		}
		writeError(w, http.StatusNotFound, errors.New("web push path not found"))
	}
}
