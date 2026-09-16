package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"swarm/packages/swarmd/internal/automation"
	store "swarm/packages/swarmd/internal/store/pebble"
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
		q := r.URL.Query()
		for key, values := range q {
			if len(values) != 1 || (key != "workspace_id" && key != "session_id" && key != "limit" && key != "cursor" && key != "timezone" && key != "archived_mode") {
				automationV2Error(w, errors.New("invalid query"))
				return
			}
		}
		if q.Get("workspace_id") == "" {
			automationV2Error(w, errors.New("workspace required"))
			return
		}
		if r.URL.Path == AutomationsV2Path+"/progress" {
			if q.Get("session_id") == "" || q.Has("limit") || len(q.Get("cursor")) > 1024 {
				automationV2Error(w, errors.New("invalid progress query"))
				return
			}
			progress, err := s.sessions.AutomationV2Progress(p.AccountScopeID, p.UserID, q.Get("workspace_id"), q.Get("session_id"), q.Get("timezone"), q.Get("cursor"), time.Now().UnixMilli())
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
			if q.Get("session_id") == "" || q.Has("limit") || q.Has("cursor") {
				automationV2Error(w, errors.New("invalid review query"))
				return
			}
			proposal, found, err := s.sessions.GetAutomationV2Proposal(p.AccountScopeID, p.UserID, q.Get("workspace_id"), q.Get("session_id"))
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
		if err != nil || limit < 1 || limit > 100 || len(q.Get("cursor")) > 1024 || q.Has("session_id") {
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
	if r.URL.Path != AutomationsV2Path+"/proposal" && r.URL.Path != AutomationsV2Path+"/accept" && r.URL.Path != AutomationsV2Path+"/decline" && r.URL.Path != AutomationsV2Path+"/control" {
		http.NotFound(w, r)
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
