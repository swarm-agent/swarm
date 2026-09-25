package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const ProjectsPath = "/v3/projects"

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
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
	path := strings.TrimPrefix(r.URL.Path, ProjectsPath)
	path = strings.TrimPrefix(path, "/")

	if path == "" {
		// Collection level: GET /v3/projects or POST /v3/projects
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			q := r.URL.Query()
			limit := 100
			if q.Get("limit") != "" {
				if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
					limit = n
				}
			}
			records, err := db.ListProjects(p.AccountScopeID, limit)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if records == nil {
				records = []pebblestore.ProjectRecord{}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"projects": records,
				"count":    len(records),
			})
			return
		}

		if r.Method == http.MethodPost {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			var rec pebblestore.ProjectRecord
			body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
				return
			}
			if err := json.Unmarshal(body, &rec); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}

			if err := db.PutProject(p.AccountScopeID, &rec); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}

			writeJSON(w, http.StatusCreated, map[string]any{
				"project": rec,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	// Single project resource: /v3/projects/{id}
	id := path
	if slashIdx := strings.Index(id, "/"); slashIdx >= 0 {
		id = id[:slashIdx]
	}

	if r.Method == http.MethodGet {
		if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
			return
		}
		rec, found, err := db.GetProject(p.AccountScopeID, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || rec == nil {
			writeError(w, http.StatusNotFound, errors.New("project not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"project": rec,
		})
		return
	}

	if r.Method == http.MethodPatch {
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
			return
		}
		var patch map[string]any
		if err := json.Unmarshal(body, &patch); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
			return
		}

		updated, err := db.UpdateProject(p.AccountScopeID, id, func(p *pebblestore.ProjectRecord) error {
			if v, ok := patch["name"].(string); ok && strings.TrimSpace(v) != "" {
				p.Name = strings.TrimSpace(v)
			}
			if v, ok := patch["description"].(string); ok {
				p.Description = strings.TrimSpace(v)
			}
			if v, ok := patch["project_context"].(string); ok {
				p.ProjectContext = v
			}
			if v, ok := patch["primary_session_id"].(string); ok {
				p.PrimarySessionID = strings.TrimSpace(v)
			}
			if workspacesRaw, ok := patch["workspaces"]; ok {
				rawBytes, err := json.Marshal(workspacesRaw)
				if err == nil {
					var ws []pebblestore.ProjectWorkspaceRef
					if err := json.Unmarshal(rawBytes, &ws); err == nil {
						p.Workspaces = ws
					}
				}
			}
			if tasksRaw, ok := patch["active_task_ids"]; ok {
				rawBytes, err := json.Marshal(tasksRaw)
				if err == nil {
					var tasks []string
					if err := json.Unmarshal(rawBytes, &tasks); err == nil {
						p.ActiveTaskIDs = tasks
					}
				}
			}
			if autosRaw, ok := patch["automation_ids"]; ok {
				rawBytes, err := json.Marshal(autosRaw)
				if err == nil {
					var autos []string
					if err := json.Unmarshal(rawBytes, &autos); err == nil {
						p.AutomationIDs = autos
					}
				}
			}
			return nil
		})
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				writeError(w, http.StatusNotFound, err)
				return
			}
			writeError(w, http.StatusBadRequest, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"project": updated,
		})
		return
	}

	if r.Method == http.MethodDelete {
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		if err := db.DeleteProject(p.AccountScopeID, id); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "deleted",
			"id":     id,
		})
		return
	}

	writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}
