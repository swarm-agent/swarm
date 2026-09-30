package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// Point reads of canonical projections; callers hydrate only visible scopes and
// invalidate from durable run.usage.updated turn_usage.scope_totals events.
func (s *Server) handleUsageScope(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromRequest(r)
	if !ok { writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired); return }
	if r.Method != http.MethodGet { writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed")); return }
	if _, scoped := ScopedTokenFromRequest(r); scoped { writeError(w, http.StatusForbidden, errors.New("scoped tokens cannot read account usage scopes")); return }
	kind, project, id := r.URL.Query().Get("kind"), r.URL.Query().Get("project_id"), r.URL.Query().Get("id")
	if strings.TrimSpace(id) == "" || len(id) > 256 || len(project) > 256 { writeError(w, http.StatusBadRequest, errors.New("invalid usage scope identity")); return }
	switch kind {
	case "worker":
		if project != "" { writeError(w, http.StatusBadRequest, errors.New("worker scope has no project")); return }
		_, found, err := s.sessions.GetWorker(p.AccountScopeID, id)
		if err != nil { writeError(w, http.StatusInternalServerError, err); return }
		if !found { writeError(w, http.StatusNotFound, errors.New("worker not found")); return }
	case "worker_run":
		_, found, err := s.sessions.GetWorkerRun(p.AccountScopeID, project, id)
		if err != nil { writeError(w, http.StatusInternalServerError, err); return }
		if !found { writeError(w, http.StatusNotFound, errors.New("worker run not found")); return }
	case "task":
		_, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, project, id)
		if err != nil { writeError(w, http.StatusInternalServerError, err); return }
		if !found { writeError(w, http.StatusNotFound, errors.New("task not found")); return }
	default:
		writeError(w, http.StatusBadRequest, errors.New("unknown usage scope kind")); return
	}
	total, recorded, err := s.sessions.Store().GetUsageScope(p.AccountScopeID, kind, project, id)
	if err != nil { writeError(w, http.StatusInternalServerError, err); return }
	writeJSON(w, http.StatusOK, map[string]any{"usage": total, "recorded": recorded})
}
