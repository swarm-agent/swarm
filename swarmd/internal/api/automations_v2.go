package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/automation"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/webhook"
)

const AutomationsV2Path = "/v3/automations/v2"

type automationV2Request struct {
	Generation  uint64                     `json:"generation,omitempty"`
	Action      string                     `json:"action"`
	WorkspaceID string                     `json:"workspace_id"`
	SessionID   string                     `json:"session_id"`
	Review      store.AutomationV2Review   `json:"review"`
	Document    *store.SessionPlanDocument `json:"document,omitempty"`
}

type automationV2TriggerRequest struct {
	WorkspaceID  string         `json:"workspace_id"`
	SessionID    string         `json:"session_id,omitempty"`
	AutomationID string         `json:"automation_id,omitempty"`
	WorkerID     string         `json:"worker_id,omitempty"`
	Context      map[string]any `json:"context,omitempty"`
}

func automationV2Error(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrAutomationV2Conflict) {
		writeError(w, http.StatusConflict, errors.New("automation ownership or review conflict"))
		return
	}
	writeError(w, http.StatusBadRequest, errors.New("automation v2 operation rejected"))
}

func (s *Server) handleAutomationsV2(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := PrincipalFromRequest(r)
	if !ok || !p.Valid() {
		writeError(w, http.StatusUnauthorized, errors.New("trusted user required"))
		return
	}
	want := automation.Principal{AccountID: p.AccountScopeID, SubjectID: p.UserID, Role: "user"}
	if p.Type != "user" {
		writeError(w, http.StatusForbidden, errors.New("explicit user required"))
		return
	}
	if bound, err := automation.RuntimePrincipal(r.Context()); err == nil && bound != want {
		writeError(w, http.StatusForbidden, errors.New("explicit user required"))
		return
	}
	if _, err := automation.BindRuntimeIdentity(r.Context(), p, "user", ""); err != nil {
		writeError(w, http.StatusForbidden, errors.New("explicit user required"))
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session service unavailable"))
		return
	}
	if r.Method == http.MethodGet && (r.URL.Path == AutomationsV2Path || r.URL.Path == AutomationsV2Path+"/review" || r.URL.Path == AutomationsV2Path+"/progress") {
		if !s.requireScope(w, r, "automations:read") {
			return
		}
		q := r.URL.Query()
		for key, values := range q {
			if len(values) != 1 || (key != "workspace_id" && key != "session_id" && key != "automation_id" && key != "worker_id" && key != "limit" && key != "cursor" && key != "timezone" && key != "archived_mode") {
				automationV2Error(w, errors.New("invalid query"))
				return
			}
		}
		if q.Get("workspace_id") == "" {
			automationV2Error(w, errors.New("workspace required"))
			return
		}
		if r.URL.Path == AutomationsV2Path+"/progress" {
			targetID := q.Get("session_id")
			if targetID == "" {
				targetID = q.Get("automation_id")
			}
			if targetID == "" {
				targetID = q.Get("worker_id")
			}
			if targetID == "" || q.Has("limit") || len(q.Get("cursor")) > 1024 {
				automationV2Error(w, errors.New("invalid progress query"))
				return
			}
			progress, err := s.sessions.AutomationV2Progress(p.AccountScopeID, p.UserID, q.Get("workspace_id"), targetID, q.Get("timezone"), q.Get("cursor"), time.Now().UnixMilli())
			if err != nil {
				automationV2Error(w, err)
				return
			}
			writeJSON(w, http.StatusOK, progress)
			return
		}
		if q.Has("timezone") {
			automationV2Error(w, errors.New("timezone only applies to progress"))
			return
		}
		if r.URL.Path != AutomationsV2Path && q.Has("archived_mode") {
			automationV2Error(w, errors.New("archived_mode only applies to list"))
			return
		}
		if r.URL.Path == AutomationsV2Path+"/review" {
			targetID := q.Get("session_id")
			if targetID == "" {
				targetID = q.Get("automation_id")
			}
			if targetID == "" {
				targetID = q.Get("worker_id")
			}
			if targetID == "" || q.Has("limit") || q.Has("cursor") {
				automationV2Error(w, errors.New("invalid review query"))
				return
			}
			proposal, found, err := s.sessions.GetAutomationV2Proposal(p.AccountScopeID, p.UserID, q.Get("workspace_id"), targetID)
			if err != nil {
				automationV2Error(w, err)
				return
			}
			if !found {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"proposal": proposal})
			return
		}
		limit := 20
		var err error
		if q.Has("limit") {
			limit, err = strconv.Atoi(q.Get("limit"))
		}
		if err != nil || limit < 1 || limit > 100 || len(q.Get("cursor")) > 1024 || q.Has("session_id") || q.Has("automation_id") || q.Has("worker_id") {
			automationV2Error(w, errors.New("invalid discovery query"))
			return
		}
		archivedMode := q.Get("archived_mode")
		if archivedMode == "" {
			archivedMode = "exclude"
		}
		if archivedMode != "exclude" && archivedMode != "include" && archivedMode != "only" {
			automationV2Error(w, errors.New("invalid discovery query"))
			return
		}
		records, next, err := s.sessions.ListAutomationV2Records(p.AccountScopeID, p.UserID, q.Get("workspace_id"), q.Get("cursor"), limit, archivedMode)
		if err != nil {
			automationV2Error(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"records": records, "next_cursor": next})
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if r.URL.Path != AutomationsV2Path+"/proposal" && r.URL.Path != AutomationsV2Path+"/accept" && r.URL.Path != AutomationsV2Path+"/decline" && r.URL.Path != AutomationsV2Path+"/control" && r.URL.Path != AutomationsV2Path+"/trigger" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path != AutomationsV2Path+"/trigger" {
		if !s.requireScope(w, r, "automations:write") {
			return
		}
	}
	if r.URL.Path == AutomationsV2Path+"/trigger" {
		if !s.requireScope(w, r, "automations:trigger") {
			return
		}
		var req automationV2TriggerRequest
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 300*1024))
		d.DisallowUnknownFields()
		if err := d.Decode(&req); err != nil {
			automationV2Error(w, err)
			return
		}
		if err := d.Decode(new(any)); err != io.EOF {
			automationV2Error(w, errors.New("trailing payload"))
			return
		}
		if req.WorkspaceID == "" || len(req.WorkspaceID) > 256 {
			automationV2Error(w, errors.New("workspace required"))
			return
		}
		targetID := req.SessionID
		if targetID == "" {
			targetID = req.WorkerID
		}
		if targetID == "" {
			targetID = req.AutomationID
		}
		if targetID == "" || len(targetID) > 256 {
			automationV2Error(w, errors.New("target worker or session required"))
			return
		}
		occurrence, err := s.sessions.TriggerAutomationV2(p.AccountScopeID, p.UserID, req.WorkspaceID, targetID, req.Context)
		if err != nil {
			automationV2Error(w, err)
			return
		}
		if s.automationV2Scheduler != nil {
			go func() {
				_ = s.automationV2Scheduler.Tick(context.Background(), occurrence.Record, time.Now().UnixMilli())
			}()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "occurrence": occurrence})
		return
	}
	var req automationV2Request
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 300*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		automationV2Error(w, err)
		return
	}
	if err := d.Decode(new(any)); err != io.EOF {
		automationV2Error(w, errors.New("trailing payload"))
		return
	}
	if req.WorkspaceID == "" || req.SessionID == "" || len(req.WorkspaceID) > 256 || len(req.SessionID) > 256 {
		automationV2Error(w, errors.New("scope required"))
		return
	}
	var response any
	if r.URL.Path == AutomationsV2Path+"/control" {
		if req.Generation == 0 || req.Document != nil || req.Review != (store.AutomationV2Review{}) {
			automationV2Error(w, errors.New("exact generation required"))
			return
		}
		record, err := s.sessions.ControlAutomationV2(p.AccountScopeID, p.UserID, req.WorkspaceID, req.SessionID, req.Generation, req.Action)
		if err != nil {
			automationV2Error(w, err)
			return
		}
		response = map[string]any{"record": record}
	} else if r.URL.Path == AutomationsV2Path+"/decline" {
		if req.Action != "decline_automation" || req.Document != nil || req.Review.ProposalID == "" || req.Review.Revision == 0 || req.Review.Digest == "" {
			automationV2Error(w, errors.New("exact decline required"))
			return
		}
		if err := s.sessions.DeclineAutomationV2(p.AccountScopeID, p.UserID, req.WorkspaceID, req.SessionID, req.Review); err != nil {
			automationV2Error(w, err)
			return
		}
		response = map[string]any{"ok": true, "declined": true}
	} else if r.URL.Path == AutomationsV2Path+"/proposal" {
		if req.Action != "propose_automation" || req.Document == nil {
			automationV2Error(w, errors.New("proposal required"))
			return
		}
		proposal, err := s.sessions.ProposeAutomationV2(p.AccountScopeID, p.UserID, req.WorkspaceID, req.SessionID, req.Document, req.Review)
		if err != nil {
			automationV2Error(w, err)
			return
		}
		response = map[string]any{"proposal": proposal}
	} else {
		if req.Action != "accept_automation" || req.Document != nil || req.Review.ProposalID == "" || req.Review.Revision == 0 || req.Review.Digest == "" {
			automationV2Error(w, errors.New("exact acceptance required"))
			return
		}
		record, err := s.sessions.AcceptAutomationV2(p.AccountScopeID, p.UserID, req.WorkspaceID, req.SessionID, req.Review)
		if err != nil {
			automationV2Error(w, err)
			return
		}
		response = map[string]any{"record": record}
	}
	// The foundation committed its durable outbox before returning. Wake the
	// canonical scoped stream; a delivery error never rolls back acceptance.
	seq, err := s.sessions.CurrentRealtimeOutboxRevision()
	if err == nil {
		var found bool
		var record store.V3RealtimeOutboxRecord
		record, found, err = s.sessions.LastRealtimeOutboxForSessionAtOrBeforeEndpoint(req.SessionID, seq)
		if err == nil && found {
			err = s.publishCommittedV3RealtimeOutbox(record)
		}
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("automation committed; refresh review before retrying"))
		return
	}
	writeJSON(w, http.StatusOK, response)
}

type automationV2WebhookTestRequest struct {
	ID          string   `json:"id,omitempty"`
	URL         string   `json:"url,omitempty"`
	Secret      string   `json:"secret,omitempty"`
	Format      string   `json:"format,omitempty"`
	Events      []string `json:"events,omitempty"`
	WorkerID    string   `json:"worker_id,omitempty"`
	WorkerTitle string   `json:"worker_title,omitempty"`
}

func (s *Server) handleAutomationsV2Webhooks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := PrincipalFromRequest(r)
	if !ok || !p.Valid() {
		writeError(w, http.StatusUnauthorized, errors.New("trusted user required"))
		return
	}
	if p.Type != "user" {
		writeError(w, http.StatusForbidden, errors.New("explicit user required"))
		return
	}
	if s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session service unavailable"))
		return
	}

	db := s.sessions.Store()
	path := strings.TrimPrefix(r.URL.Path, AutomationsV2Path+"/webhooks")
	path = strings.TrimPrefix(path, "/")

	// GET /v3/automations/v2/webhooks
	if r.Method == http.MethodGet && path == "" {
		if !s.requireScope(w, r, "automations:read") {
			return
		}
		webhooks, err := db.ListAutomationV2Webhooks(p.AccountScopeID)
		if err != nil {
			automationV2Error(w, err)
			return
		}
		if webhooks == nil {
			webhooks = []store.AutomationV2GlobalWebhook{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "webhooks": webhooks})
		return
	}

	// POST /v3/automations/v2/webhooks/test or POST /v3/automations/v2/webhooks/{id}/test
	if r.Method == http.MethodPost && (path == "test" || strings.HasSuffix(path, "/test")) {
		if !s.requireScope(w, r, "automations:write") {
			return
		}
		if s.webhookDispatcher == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("webhook dispatcher unavailable"))
			return
		}
		var req automationV2WebhookTestRequest
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 100*1024)).Decode(&req)

		targetID := strings.TrimSuffix(path, "/test")
		if targetID != "" && targetID != "test" && req.ID == "" {
			req.ID = targetID
		}

		dest := webhook.Destination{
			ID:      req.ID,
			URL:     req.URL,
			Secret:  req.Secret,
			Format:  req.Format,
			Events:  req.Events,
			Enabled: true,
		}
		if req.ID != "" && dest.URL == "" {
			wh, found, err := db.GetAutomationV2Webhook(p.AccountScopeID, req.ID)
			if err != nil || !found {
				writeError(w, http.StatusNotFound, errors.New("webhook destination not found"))
				return
			}
			dest = webhook.Destination{
				ID:      wh.ID,
				URL:     wh.URL,
				Secret:  wh.Secret,
				Format:  wh.Format,
				Events:  wh.Events,
				Enabled: wh.Enabled,
			}
		}
		if dest.URL == "" {
			writeError(w, http.StatusBadRequest, errors.New("webhook url or valid webhook id required"))
			return
		}

		workerTitle := req.WorkerTitle
		if workerTitle == "" {
			workerTitle = "Test Ping Worker"
		}
		workerID := req.WorkerID
		if workerID == "" {
			workerID = "test-worker"
		}

		event := webhook.WebhookEvent{
			Type:        webhook.EventTestPing,
			EventID:     "whk_test_" + strconv.FormatInt(time.Now().UnixNano(), 36),
			Timestamp:   time.Now().UnixMilli(),
			AccountID:   p.AccountScopeID,
			WorkerID:    workerID,
			WorkerTitle: workerTitle,
			State:       "succeeded",
			Detail:      "This is a test notification from Swarm daemon.",
		}

		result, err := s.webhookDispatcher.DeliverSync(r.Context(), event, dest)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":     false,
				"error":  err.Error(),
				"result": result,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"result": result,
		})
		return
	}

	// POST /v3/automations/v2/webhooks (Create or Update)
	if r.Method == http.MethodPost && path == "" {
		if !s.requireScope(w, r, "automations:write") {
			return
		}
		var whk store.AutomationV2GlobalWebhook
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 100*1024))
		d.DisallowUnknownFields()
		if err := d.Decode(&whk); err != nil {
			automationV2Error(w, err)
			return
		}
		if err := db.PutAutomationV2Webhook(p.AccountScopeID, &whk); err != nil {
			automationV2Error(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "webhook": whk})
		return
	}

	// DELETE /v3/automations/v2/webhooks/{id}
	if r.Method == http.MethodDelete && path != "" {
		if !s.requireScope(w, r, "automations:write") {
			return
		}
		if err := db.DeleteAutomationV2Webhook(p.AccountScopeID, path); err != nil {
			automationV2Error(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	writeError(w, http.StatusNotFound, errors.New("not found"))
}
