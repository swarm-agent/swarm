package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/notification"
)

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	if s.notifications == nil {
		writeError(w, http.StatusInternalServerError, errors.New("notification service is not configured"))
		return
	}
	path := strings.TrimSpace(r.URL.Path)
	switch path {
	case "/v1/notifications":
		s.handleNotificationList(w, r)
		return
	case "/v1/notifications/summary":
		s.handleNotificationSummary(w, r)
		return
	case "/v1/notifications/clear":
		s.handleNotificationClear(w, r)
		return
	default:
		if strings.HasPrefix(path, "/v1/notifications/") {
			s.handleNotificationUpdate(w, r)
			return
		}
		writeError(w, http.StatusNotFound, errors.New("notification path not found"))
	}
}

func (s *Server) handleNotificationList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	limit := 200
	if parsed := parseIntQuery(r, "limit", 0); parsed > 0 {
		limit = parsed
	}
	records, err := s.notifications.ListNotifications(r.URL.Query().Get("swarm_id"), limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "notifications": records})
}

func (s *Server) handleNotificationSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	summary, err := s.notifications.Summary(r.URL.Query().Get("swarm_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "summary": summary})
}

func (s *Server) handleNotificationClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	result, err := s.notifications.ClearNotifications(r.URL.Query().Get("swarm_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleNotificationUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	notificationID := strings.Trim(strings.TrimPrefix(strings.TrimSpace(r.URL.Path), "/v1/notifications/"), "/")
	if notificationID == "" {
		writeError(w, http.StatusBadRequest, errors.New("notification id is required"))
		return
	}
	var req struct {
		SwarmID        string `json:"swarm_id"`
		Read           *bool  `json:"read"`
		Acked          *bool  `json:"acked"`
		Muted          *bool  `json:"muted"`
		ResolvedStatus string `json:"status"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, _, err := s.notifications.UpdateNotification(notification.UpdateInput{
		SwarmID:        req.SwarmID,
		NotificationID: notificationID,
		MarkRead:       req.Read,
		MarkAcked:      req.Acked,
		MarkMuted:      req.Muted,
		ResolvedStatus: req.ResolvedStatus,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	summary, err := s.notifications.Summary(record.SwarmID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "notification": record, "summary": summary})
}

func parseIntQuery(r *http.Request, key string, fallback int) int {
	if r == nil {
		return fallback
	}
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}
