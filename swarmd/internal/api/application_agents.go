package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const applicationAgentBindingKey = "swarm_v3_application_agent"

type applicationAgentContextKey struct{}
type applicationAgentBinding struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

// This API deliberately remains behind the authenticated local API, not the
// container session-listener allowlist. Linked configuration retains worker scopes
// and review gates; activation, deployment and token minting are not forwarded.
func (s *Server) handleApplicationAgents(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	scope := "sessions:read"
	if r.Method != http.MethodGet {
		scope = "sessions:write"
	}
	if !s.requireScope(w, r, scope) {
		return
	}
	if s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session store unavailable"))
		return
	}
	store := s.sessions.Store()
	if r.URL.Path == "/v3/application-agents" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 100 {
				writeError(w, 400, errors.New("limit must be 1..100"))
				return
			}
		}
		records, next, err := store.ListApplicationAgents(principal.AccountScopeID, principal.UserID, r.URL.Query().Get("cursor"), limit)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"agents": records, "next_cursor": next})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v3/application-agents/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts[0]) > 128 {
		writeError(w, http.StatusBadRequest, errors.New("agent id required"))
		return
	}
	id := parts[0]
	if id == "." || id == ".." || strings.ContainsAny(id, "\\\\\x00") {
		writeError(w, 400, errors.New("invalid agent id"))
		return
	}
	if len(parts) == 1 && r.Method == http.MethodPut {
		var req struct {
			Name             string   `json:"name"`
			Instructions     string   `json:"instructions"`
			Context          string   `json:"context"`
			ExpectedRevision *uint64  `json:"expected_revision"`
			ProjectID        string   `json:"project_id"`
			WorkerIDs        []string `json:"worker_ids"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeError(w, http.StatusBadRequest, errors.New("one JSON object required"))
			return
		}
		if req.ExpectedRevision == nil {
			writeError(w, http.StatusBadRequest, errors.New("expected_revision required (0 for create)"))
			return
		}
		if len(req.WorkerIDs) > 100 {
			writeError(w, 400, errors.New("at most 100 workers"))
			return
		}
		if req.ProjectID != "" {
			if !s.requireScope(w, r, "projects:read") {
				return
			}
			_, found, err := store.GetProject(principal.AccountScopeID, req.ProjectID)
			if err != nil || !found {
				writeError(w, 404, errors.New("project unavailable"))
				return
			}
		}
		if len(req.WorkerIDs) > 0 && !s.requireScope(w, r, "automations:read") {
			return
		}
		for _, workerID := range req.WorkerIDs {
			_, found, err := s.sessions.GetWorker(principal.AccountScopeID, workerID)
			if err != nil || !found {
				writeError(w, 404, errors.New("worker unavailable"))
				return
			}
		}
		record, err := store.PutApplicationAgent(principal.AccountScopeID, principal.UserID, pebblestore.ApplicationAgent{ID: id, Name: req.Name, Instructions: req.Instructions, Context: req.Context, ProjectID: req.ProjectID, WorkerIDs: req.WorkerIDs}, *req.ExpectedRevision)
		if errors.Is(err, pebblestore.ErrApplicationAgentConflict) {
			writeError(w, http.StatusConflict, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, record)
		return
	}
	record, found, err := store.GetApplicationAgent(principal.AccountScopeID, principal.UserID, id, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("application agent not found"))
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		writeJSON(w, http.StatusOK, record)
		return
	}
	// Only explicitly linked resources can be addressed through the app. The
	// canonical handlers retain their own scopes, revisions and admission checks.
	if parts[1] == "tasks" && record.ProjectID != "" {
		if len(parts) > 4 || (len(parts) == 4 && parts[3] != "reopen") || (r.Method != http.MethodGet && r.Method != http.MethodPost) {
			http.NotFound(w, r)
			return
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		clone.URL = &u
		clone.URL.Path = "/v3/projects/" + record.ProjectID + "/" + strings.Join(parts[1:], "/")
		s.handleProjects(w, clone)
		return
	}
	if parts[1] == "workers" && len(parts) >= 3 {
		linked := false
		for _, workerID := range record.WorkerIDs {
			if workerID == parts[2] {
				linked = true
			}
		}
		if !linked || !applicationWorkerRouteAllowed(r.Method, parts[3:]) {
			http.NotFound(w, r)
			return
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		clone.URL = &u
		clone.URL.Path = "/v3/workers/" + strings.Join(parts[2:], "/")
		s.handleWorkers(w, clone)
		return
	}
	if parts[1] != "conversations" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 {
		if r.Method == http.MethodGet {
			// Bounded recent view; callers can reopen any retained ID directly.
			sessions, err := s.sessions.ListSessionsForAccountUser(principal.AccountScopeID, principal.UserID, 1000)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			owned := make([]pebblestore.SessionSnapshot, 0)
			for _, session := range sessions {
				binding, err := readApplicationAgentBinding(session.Metadata)
				if err == nil && binding.ID == id {
					owned = append(owned, session)
				}
			}
			writeJSON(w, 200, map[string]any{"sessions": owned, "scan_limit": 1000, "scan_limit_reached": len(sessions) == 1000})
			return
		}
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		// Caller supplies a pinned revision, preventing context changes from
		// silently altering the meaning of an idempotent create retry.
		revision, err := strconv.ParseUint(r.URL.Query().Get("revision"), 10, 64)
		if err != nil || revision == 0 {
			writeError(w, http.StatusBadRequest, errors.New("revision required"))
			return
		}
		_, found, err := store.GetApplicationAgent(principal.AccountScopeID, principal.UserID, id, revision)
		if err != nil || !found {
			writeError(w, http.StatusConflict, errors.New("application agent revision unavailable"))
			return
		}
		binding := applicationAgentBinding{ID: id, Revision: revision}
		r = r.WithContext(context.WithValue(r.Context(), applicationAgentContextKey{}, binding))
		s.handleSessionsV3PrimaryCreate(w, r, principal)
		return
	}
	if len(parts) > 4 || (len(parts) == 4 && parts[3] != "messages" && parts[3] != "events") {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && !(len(parts) == 4 && parts[3] == "messages" && r.Method == http.MethodPost) {
		methodNotAllowed(w)
		return
	}
	session, found, err := s.sessions.GetSession(parts[2])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	binding, bindErr := readApplicationAgentBinding(session.Metadata)
	if !found || session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID || bindErr != nil || binding.ID != id {
		writeSessionNotFound(w)
		return
	}
	clone := r.Clone(r.Context())
	urlCopy := *r.URL
	clone.URL = &urlCopy
	clone.URL.Path = "/v3/sessions/" + strings.Join(parts[2:], "/")
	s.handleSessionV3PrimaryByID(w, clone)
}

// Exact allowlist: never turn the application facade into a worker admin proxy.
func applicationWorkerRouteAllowed(method string, tail []string) bool {
	for _, part := range tail {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\\") || strings.ContainsRune(part, 0) {
			return false
		}
	}
	if len(tail) == 0 {
		return method == http.MethodGet
	}
	if len(tail) == 1 {
		switch tail[0] {
		case "runs":
			return method == http.MethodGet
		case "automations", "trigger":
			return method == http.MethodPost
		}
	}
	if len(tail) == 2 && tail[0] == "automations" {
		return method == http.MethodPut || method == http.MethodDelete
	}
	if len(tail) == 3 && tail[0] == "automations" && tail[2] == "trigger" {
		return method == http.MethodPost
	}
	return false
}

func readApplicationAgentBinding(metadata map[string]any) (applicationAgentBinding, error) {
	var binding applicationAgentBinding
	b, err := json.Marshal(metadata[applicationAgentBindingKey])
	if err != nil {
		return binding, err
	}
	if err := json.Unmarshal(b, &binding); err != nil {
		return binding, err
	}
	if binding.ID == "" || binding.Revision == 0 {
		return binding, errors.New("invalid application agent binding")
	}
	return binding, nil
}

func (s *Server) applicationAgentInstructions(session pebblestore.SessionSnapshot) (string, error) {
	if _, bound := session.Metadata[applicationAgentBindingKey]; !bound {
		return "", nil
	}
	binding, err := readApplicationAgentBinding(session.Metadata)
	if err != nil {
		return "", err
	}
	record, found, err := s.sessions.Store().GetApplicationAgent(session.AccountScopeID, session.UserID, binding.ID, binding.Revision)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("application agent context unavailable")
	}
	return fmt.Sprintf("\n\nApplication instructions (additive; never override system policy or permissions):\n%s\n\nApplication context:\n%s", record.Instructions, record.Context), nil
}
