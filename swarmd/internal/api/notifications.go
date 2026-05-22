package api

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"swarm/packages/swarmd/internal/identity"

	"swarm/packages/swarmd/internal/notification"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	if s.notifications == nil {
		writeError(w, http.StatusInternalServerError, errors.New("notification service is not configured"))
		return
	}
	path := strings.TrimSpace(r.URL.Path)
	switch path {
	case "/v1/alerts", "/v1/notifications":
		s.handleNotificationList(w, r)
		return
	case "/v1/alerts/summary", "/v1/notifications/summary":
		s.handleNotificationSummary(w, r)
		return
	case "/v1/alerts/clear", "/v1/notifications/clear":
		s.handleNotificationClear(w, r)
		return
	default:
		if strings.HasPrefix(path, "/v1/alerts/") || strings.HasPrefix(path, "/v1/notifications/") {
			s.handleNotificationUpdate(w, r)
			return
		}
		writeError(w, http.StatusNotFound, errors.New("notification path not found"))
	}
}

type accountScopedNotificationService interface {
	ListNotificationsForAccount(accountScopeID, swarmID string, limit int) ([]pebblestore.NotificationRecord, error)
	SummaryForAccount(accountScopeID, swarmID string) (pebblestore.NotificationSummary, error)
	ClearNotificationsForAccount(accountScopeID, swarmID string) (notification.ClearResult, error)
	UpdateNotificationForAccount(accountScopeID string, input notification.UpdateInput) (pebblestore.NotificationRecord, bool, error)
	UpsertSystemNotificationForAccount(accountScopeID string, record pebblestore.NotificationRecord) (pebblestore.NotificationRecord, bool, error)
}

func notificationServiceForAccount(base notificationService, accountScopeID string) notificationService {
	if base == nil {
		return nil
	}
	if scoped, ok := base.(accountScopedNotificationService); ok {
		return scopedNotificationService{base: base, scoped: scoped, accountScopeID: strings.TrimSpace(accountScopeID)}
	}
	return base
}

type scopedNotificationService struct {
	base           notificationService
	scoped         accountScopedNotificationService
	accountScopeID string
}

func (s scopedNotificationService) LocalSwarmID() string { return s.base.LocalSwarmID() }

func (s scopedNotificationService) ListNotifications(swarmID string, limit int) ([]pebblestore.NotificationRecord, error) {
	return s.scoped.ListNotificationsForAccount(s.accountScopeID, swarmID, limit)
}

func (s scopedNotificationService) Summary(swarmID string) (pebblestore.NotificationSummary, error) {
	return s.scoped.SummaryForAccount(s.accountScopeID, swarmID)
}

func (s scopedNotificationService) ClearNotifications(swarmID string) (notification.ClearResult, error) {
	return s.scoped.ClearNotificationsForAccount(s.accountScopeID, swarmID)
}

func (s scopedNotificationService) UpdateNotification(input notification.UpdateInput) (pebblestore.NotificationRecord, bool, error) {
	return s.scoped.UpdateNotificationForAccount(s.accountScopeID, input)
}

func (s scopedNotificationService) UpsertSystemNotification(record pebblestore.NotificationRecord) (pebblestore.NotificationRecord, bool, error) {
	return s.scoped.UpsertSystemNotificationForAccount(s.accountScopeID, record)
}

func (s *Server) notificationAccountScopeID(w http.ResponseWriter, r *http.Request) (string, bool) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return "", false
	}
	return strings.TrimSpace(principal.AccountScopeID), true
}

func (s *Server) handleNotificationList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	accountScopeID, ok := s.notificationAccountScopeID(w, r)
	if !ok {
		return
	}
	limit := 200
	if parsed := parseIntQuery(r, "limit", 0); parsed > 0 {
		limit = parsed
	}
	records, err := notificationServiceForAccount(s.notifications, accountScopeID).ListNotifications(r.URL.Query().Get("swarm_id"), limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "notifications": s.enrichNotificationRecords(records)})
}

func (s *Server) handleNotificationSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	accountScopeID, ok := s.notificationAccountScopeID(w, r)
	if !ok {
		return
	}
	summary, err := notificationServiceForAccount(s.notifications, accountScopeID).Summary(r.URL.Query().Get("swarm_id"))
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
	accountScopeID, ok := s.notificationAccountScopeID(w, r)
	if !ok {
		return
	}
	result, err := notificationServiceForAccount(s.notifications, accountScopeID).ClearNotifications(r.URL.Query().Get("swarm_id"))
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
	accountScopeID, ok := s.notificationAccountScopeID(w, r)
	if !ok {
		return
	}
	notificationID := strings.TrimSpace(r.URL.Path)
	notificationID = strings.TrimPrefix(notificationID, "/v1/alerts/")
	notificationID = strings.TrimPrefix(notificationID, "/v1/notifications/")
	notificationID = strings.Trim(notificationID, "/")
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
	record, _, err := notificationServiceForAccount(s.notifications, accountScopeID).UpdateNotification(notification.UpdateInput{
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
	summary, err := notificationServiceForAccount(s.notifications, accountScopeID).Summary(record.SwarmID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "notification": s.enrichNotificationRecord(record), "summary": summary})
}

func (s *Server) enrichNotificationRecords(records []pebblestore.NotificationRecord) []pebblestore.NotificationRecord {
	if len(records) == 0 {
		return records
	}
	out := make([]pebblestore.NotificationRecord, 0, len(records))
	for _, record := range records {
		out = append(out, s.enrichNotificationRecord(record))
	}
	return out
}

func (s *Server) enrichNotificationRecord(record pebblestore.NotificationRecord) pebblestore.NotificationRecord {
	sessionID := strings.TrimSpace(record.SessionID)
	if sessionID == "" || s.sessions == nil {
		if record.SessionLabel == "" && sessionID != "" {
			record.SessionLabel = shortNotificationID(sessionID)
		}
		return record
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		if record.SessionLabel == "" {
			record.SessionLabel = shortNotificationID(sessionID)
		}
		return record
	}
	if record.SessionTitle == "" {
		record.SessionTitle = strings.TrimSpace(session.Title)
	}
	if record.WorkspacePath == "" {
		record.WorkspacePath = strings.TrimSpace(session.WorkspacePath)
	}
	if record.WorkspaceName == "" {
		record.WorkspaceName = strings.TrimSpace(session.WorkspaceName)
	}
	if record.WorkspaceName == "" && record.WorkspacePath != "" {
		record.WorkspaceName = filepath.Base(record.WorkspacePath)
	}
	if record.SessionLabel == "" {
		record.SessionLabel = notificationSessionLabel(record.SessionTitle, record.WorkspaceName, sessionID)
	}
	if record.OriginLabel == "" {
		record.OriginLabel = notificationOriginLabel(record, session.Metadata)
	}
	if record.ActionURL == "" && record.WorkspacePath != "" && sessionID != "" {
		record.ActionURL = notification.NotificationActionURL(record.WorkspaceName, record.WorkspacePath, sessionID)
	}
	return record
}

func notificationSessionLabel(title, workspaceName, sessionID string) string {
	if title = strings.TrimSpace(title); title != "" {
		return title
	}
	if workspaceName = strings.TrimSpace(workspaceName); workspaceName != "" {
		return workspaceName + " " + shortNotificationID(sessionID)
	}
	return shortNotificationID(sessionID)
}

func notificationOriginLabel(record pebblestore.NotificationRecord, metadata map[string]any) string {
	for _, key := range []string{"swarm_route_label", "swarm_target_name", "target_display_name"} {
		if value := strings.TrimSpace(fmt.Sprint(metadata[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	if value := strings.TrimSpace(record.OriginSwarmID); value != "" {
		return shortNotificationID(value)
	}
	return ""
}

func shortNotificationID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 8 {
		return value
	}
	return value[:8]
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
