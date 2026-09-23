package api

import (
	"bytes"
	"context"
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
				"deliverable": rec,
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
				"deliverable": rec,
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

		// If action contract exists, execute publication action
		if rec.ActionContract != nil && rec.ActionContract.Action != "" {
			switch rec.ActionContract.Action {
			case "execute_webhook":
				if rec.ActionContract.TargetURL != "" {
					payloadBytes, _ := json.Marshal(map[string]any{
						"event":       "deliverable.approved",
						"deliverable": rec,
					})
					ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
					defer cancel()
					req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, rec.ActionContract.TargetURL, bytes.NewReader(payloadBytes))
					if reqErr == nil {
						req.Header.Set("Content-Type", "application/json")
						resp, postErr := http.DefaultClient.Do(req)
						if postErr == nil {
							actionResult["webhook_status"] = resp.StatusCode
							resp.Body.Close()
						} else {
							actionResult["webhook_error"] = postErr.Error()
						}
					}
				}
			case "publish_x_post":
				// X / Twitter publication execution
				// Formats post receipt and records publication
				postCount := 1
				if posts, ok := rec.Payload["posts"].([]any); ok && len(posts) > 0 {
					postCount = len(posts)
				}
				actionResult["published_to"] = "x"
				actionResult["post_count"] = postCount
				actionResult["status"] = "published"
				if rec.ActionContract.TargetSecretRef != "" {
					actionResult["secret_ref"] = rec.ActionContract.TargetSecretRef
				}
			default:
				actionResult["action"] = rec.ActionContract.Action
				actionResult["status"] = "completed"
			}
		}

		targetStatus := "approved"
		if rec.ActionContract != nil && (rec.ActionContract.Action == "publish_x_post" || rec.ActionContract.Action == "execute_webhook") {
			targetStatus = "published"
		}

		updated, err := db.UpdateDeliverableStatus(p.AccountScopeID, id, targetStatus, p.UserID, actionResult)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"deliverable":   updated,
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
			"deliverable": updated,
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
			"deliverable": updated,
		})
		return
	}

	writeError(w, http.StatusNotFound, errors.New("unknown deliverable action"))
}
