package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const DeliverablesPath = "/v3/deliverables"

// publicDeliverable keeps the durable fencing token inside the store/service.
// Return a copy: redacting an API response must not erase persistence authority.
func publicDeliverable(rec pebblestore.DeliverableRecord) pebblestore.DeliverableRecord {
	rec.PublicationClaim = ""
	return rec
}

func (s *Server) handleDeliverables(w http.ResponseWriter, r *http.Request) {
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
	path := strings.TrimPrefix(r.URL.Path, DeliverablesPath)
	path = strings.TrimPrefix(path, "/")

	if path == "" {
		// Collection level: GET /v3/deliverables or POST /v3/deliverables
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "automations:read", "sessions:read") {
				return
			}
			q := r.URL.Query()
			limit := 100
			if q.Get("limit") != "" {
				if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
					limit = n
				}
			}
			filter := pebblestore.DeliverableFilter{
				Status:      q.Get("status"),
				WorkerID:    q.Get("worker_id"),
				Kind:        q.Get("kind"),
				WorkspaceID: q.Get("workspace_id"),
				Limit:       limit,
			}
			records, err := db.ListDeliverables(p.AccountScopeID, filter)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if records == nil {
				records = []pebblestore.DeliverableRecord{}
			}
			for i := range records {
				records[i] = publicDeliverable(records[i])
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"deliverables": records,
				"count":        len(records),
			})
			return
		}

		if r.Method == http.MethodPost {
			if !s.requireScopeAny(w, r, "automations:write", "sessions:write") {
				return
			}
			var rec pebblestore.DeliverableRecord
			body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if err := json.Unmarshal(body, &rec); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid json: %w", err))
				return
			}
			if err := db.PutDeliverable(p.AccountScopeID, &rec); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{
				"deliverable": publicDeliverable(rec),
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	// Single deliverable actions: /v3/deliverables/{id} or /v3/deliverables/{id}/(approve|dismiss)
	parts := strings.Split(path, "/")
	id := parts[0]
	subAction := ""
	if len(parts) > 1 {
		subAction = parts[1]
	}

	if subAction == "" {
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "automations:read", "sessions:read") {
				return
			}
			rec, found, err := db.GetDeliverable(p.AccountScopeID, id)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if !found {
				writeError(w, http.StatusNotFound, errors.New("deliverable not found"))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"deliverable": publicDeliverable(rec),
			})
			return
		}

		if r.Method == http.MethodDelete {
			if !s.requireScopeAny(w, r, "automations:write", "sessions:write") {
				return
			}
			if err := db.DeleteDeliverable(p.AccountScopeID, id); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":         true,
				"deleted_id": id,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	if subAction == "approve" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed; POST required"))
			return
		}
		if !s.requireScopeAny(w, r, "automations:write", "sessions:write") {
			return
		}

		rec, found, err := db.GetDeliverable(p.AccountScopeID, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, errors.New("deliverable not found"))
			return
		}

		actionResult := map[string]any{
			"approved_at": time.Now().UnixMilli(),
			"approved_by": p.UserID,
		}

		if rec.ActionContract != nil && (rec.ActionContract.Action == "publish_x_post" || rec.ActionContract.Action == "execute_webhook") {
			updated, err := approveDeliverablePublication(r.Context(), db, p.AccountScopeID, id, p.UserID)
			if err != nil {
				writeError(w, http.StatusConflict, err)
				return
			}
			code := http.StatusOK
			if updated.Status != "published" {
				code = http.StatusBadGateway
			}
			writeJSON(w, code, map[string]any{"deliverable": publicDeliverable(updated), "action_result": updated.ActionResult})
			return
		}

		targetStatus := "approved"

		updated, err := db.UpdateDeliverableStatus(p.AccountScopeID, id, targetStatus, p.UserID, actionResult)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"deliverable":   publicDeliverable(updated),
			"action_result": actionResult,
		})
		return
	}

	if subAction == "dismiss" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed; POST required"))
			return
		}
		if !s.requireScopeAny(w, r, "automations:write", "sessions:write") {
			return
		}

		updated, err := db.UpdateDeliverableStatus(p.AccountScopeID, id, "dismissed", p.UserID, nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deliverable": publicDeliverable(updated),
		})
		return
	}

	if subAction == "request_changes" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed; POST required"))
			return
		}
		if !s.requireScopeAny(w, r, "automations:write", "sessions:write") {
			return
		}

		var reqBody struct {
			Notes string   `json:"notes"`
			Tags  []string `json:"tags"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err == nil && len(body) > 0 {
			_ = json.Unmarshal(body, &reqBody)
		}

		updated, err := db.RequestChangesDeliverable(p.AccountScopeID, id, reqBody.Notes, reqBody.Tags, p.UserID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				writeError(w, http.StatusNotFound, err)
			} else {
				writeError(w, http.StatusInternalServerError, err)
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deliverable": publicDeliverable(updated),
		})
		return
	}

	writeError(w, http.StatusNotFound, errors.New("unknown deliverable action"))
}
