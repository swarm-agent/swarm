package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/tool"
)

// JobsPath is the route a linked home's manage_jobs reaches on this machine:
// POST /v3/jobs/{action} runs this machine's ManageJobs, the one job service,
// with the caller's own scopes. It is not a second job API: every action is
// the catalog's (tool.Catalog manage_jobs) and runs the same handlers.
const JobsPath = "/v3/jobs"

// WorkerActionsPath is the route a linked home's manage_workers reaches on
// this machine: POST /v3/worker-actions/{action} runs this machine's
// ManageWorkers, the one worker service, with the caller's own scopes. Every
// action is the catalog's (tool.Catalog manage_workers).
const WorkerActionsPath = "/v3/worker-actions"

// callFleetCatalog runs a catalog action on a linked machine through its
// catalog route (JobsPath or WorkerActionsPath) and returns the machine's
// answer as it came.
func (s *Server) callFleetCatalog(ctx context.Context, p identity.Principal, machine, path, action string, args map[string]any) (json.RawMessage, error) {
	if !controlMCPSafeID.MatchString(action) {
		return nil, errors.New("a valid action is required")
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
	err = client.JSON(ctx, http.MethodPost, path+"/"+url.PathEscape(action), body, &result)
	s.noteFleetContact(linked, err)
	if err != nil {
		return nil, fmt.Errorf("linked machine %s refused %s: %w", machine, action, err)
	}
	return result, nil
}

// callFleetJobs runs a manage_jobs action on a linked machine.
func (s *Server) callFleetJobs(ctx context.Context, p identity.Principal, machine, action string, args map[string]any) (json.RawMessage, error) {
	return s.callFleetCatalog(ctx, p, machine, JobsPath, action, args)
}

// serveCatalogLinked serves POST {path}/{action}: one action of a catalog
// tool for a linked home (or any caller with the route's scopes), run by the
// tool's service as a caller with no conversation here. A scoped key acts
// within the scopes it holds; an owner session (a linked home's link) holds
// owned. A key bound to one worker, agent or trigger is refused: the
// service runs the action on internal requests that carry only scopes, so
// the routes behind it could not hold such a key to its binding.
func (s *Server) serveCatalogLinked(w http.ResponseWriter, r *http.Request, path, toolName string, owned []string, run func(context.Context, identity.Principal, map[string]any) (any, error)) {
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
	catalog, ok := tool.CatalogToolNamed(toolName)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no catalog tool %s", toolName))
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, path), "/")
	action, ok := catalog.Action(name)
	if !ok || action.Action != name {
		writeError(w, http.StatusNotFound, fmt.Errorf("no %s action %q", toolName, name))
		return
	}
	if record, ok := ScopedTokenFromRequest(r); ok && catalogBoundKey(record) {
		writeError(w, http.StatusForbidden, fmt.Errorf("a key bound to one worker, agent or trigger cannot use %s", path))
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
	held := append([]string(nil), owned...)
	if record, ok := ScopedTokenFromRequest(r); ok && record != nil {
		held = controlCatalogHeldScopes(record)
	}
	ctx := tool.WithControlScopes(identity.ContextWithPrincipal(r.Context(), principal), held)
	out, err := run(ctx, principal, args)
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

// handleJobs serves POST /v3/jobs/{action}: one manage_jobs action, run by
// ManageJobs.
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	owned := append(append([]string(nil), nativeJobReadScopes...), nativeJobWriteScopes...)
	s.serveCatalogLinked(w, r, JobsPath, tool.ManageJobsToolName, owned, func(ctx context.Context, p identity.Principal, args map[string]any) (any, error) {
		return s.ManageJobs(ctx, p, "", "", args)
	})
}

// handleWorkerActions serves POST /v3/worker-actions/{action}: one
// manage_workers action, run by ManageWorkers.
func (s *Server) handleWorkerActions(w http.ResponseWriter, r *http.Request) {
	s.serveCatalogLinked(w, r, WorkerActionsPath, tool.ManageWorkersToolName, workerCatalogScopes, func(ctx context.Context, p identity.Principal, args map[string]any) (any, error) {
		return s.ManageWorkers(ctx, p, "", "", args)
	})
}
