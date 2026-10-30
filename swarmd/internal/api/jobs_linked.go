package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"context"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/tool"
)

// JobsPath is the route a linked home's manage_jobs reaches on this machine:
// POST /v3/jobs/{action} runs this machine's ManageJobs, the one job service,
// with the caller's own scopes. It is not a second job API: every action is
// the catalog's (tool.Catalog manage_jobs) and runs the same handlers.
const JobsPath = "/v3/jobs"

// callFleetJobs runs a manage_jobs action on a linked machine through its
// JobsPath route and returns the machine's answer as it came.
func (s *Server) callFleetJobs(ctx context.Context, p identity.Principal, machine, action string, args map[string]any) (json.RawMessage, error) {
	if !controlMCPSafeID.MatchString(action) {
		return nil, errors.New("a valid job action is required")
	}
	body := map[string]any{}
	for key, value := range args {
		if key != "machine" && key != "action" {
			body[key] = value
		}
	}
	client, linked, _, err := s.fleetMachineClient(p.AccountScopeID, machine)
	if err != nil {
		return nil, errors.New("linked machine connection unavailable")
	}
	var result json.RawMessage
	err = client.JSON(ctx, http.MethodPost, JobsPath+"/"+url.PathEscape(action), body, &result)
	s.noteFleetContact(linked, err)
	if err != nil {
		return nil, fmt.Errorf("linked machine %s refused %s: %w", machine, action, err)
	}
	return result, nil
}

// handleJobs serves POST /v3/jobs/{action}: one manage_jobs action for a
// linked home (or any caller with the route's scopes), run by ManageJobs as
// a caller with no conversation here. A scoped key acts within the scopes it
// holds; an owner session (a linked home's link) holds the job scopes.
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, JobsPath), "/")
	action, ok := manageJobsAction(name)
	if !ok || action.Action != name {
		writeError(w, http.StatusNotFound, fmt.Errorf("no job action %q", name))
		return
	}
	if !s.requireScopeAny(w, r, action.APIScopes...) {
		return
	}
	var args map[string]any
	if err := decodeJSONStrict(w, r, 256*1024, &args); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if args == nil {
		writeError(w, http.StatusBadRequest, errors.New("object body required"))
		return
	}
	if _, exists := args["machine"]; exists {
		writeError(w, http.StatusBadRequest, errors.New("nested machine routing is not allowed"))
		return
	}
	args["action"] = action.Action
	held := append(append([]string(nil), nativeJobReadScopes...), nativeJobWriteScopes...)
	if record, ok := ScopedTokenFromRequest(r); ok && record != nil {
		held = controlCatalogHeldScopes(record)
	}
	ctx := tool.WithControlScopes(identity.ContextWithPrincipal(r.Context(), principal), held)
	out, err := s.ManageJobs(ctx, principal, "", "", args)
	if err != nil {
		status := http.StatusBadRequest
		var refused controlMCPToolError
		if errors.As(err, &refused) && refused.status >= 400 {
			status = refused.status
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
