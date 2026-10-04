package api

import (
	"errors"
	"net/http"
	"strconv"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// All routes are nested under authenticated worker ingress and use the same
// worker execution authority. Bodies cannot choose an account or agent role.
func (s *Server) handleWorkerControl(w http.ResponseWriter, r *http.Request, p identity.Principal, worker string, parts []string) {
	if token, ok := ScopedTokenFromRequest(r); ok && token != nil && token.WorkerID != "" {
		writeError(w, http.StatusForbidden, errors.New("worker-scoped credentials cannot manage deployment control"))
		return
	}
	scope := "automations:read"
	if r.Method != http.MethodGet {
		scope = "automations:write"
	}
	if !s.requireScope(w, r, scope) {
		return
	}
	ctx, err := automation.BindRuntimeIdentity(r.Context(), p, "user", "")
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	r = r.WithContext(ctx)
	ws := s.sessions.Store().WorkerStore()
	if _, found, err := ws.GetWorker(p.AccountScopeID, worker); err != nil {
		workerHTTPError(w, err)
		return
	} else if !found {
		workerHTTPError(w, store.ErrWorkerNotFound)
		return
	}
	if len(parts) == 1 && parts[0] == "context" && r.Method == http.MethodGet {
		q, err := parseAndValidateQuery(r, "revision")
		if err != nil {
			workerHTTPError(w, err)
			return
		}
		var revision uint64
		if q.Has("revision") {
			revision, err = strconv.ParseUint(q.Get("revision"), 10, 64)
			if err != nil || revision == 0 {
				workerHTTPError(w, store.ErrWorkerInvalid)
				return
			}
		}
		c, err := ws.GetWorkerContext(p.AccountScopeID, worker, revision)
		if err != nil {
			workerHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"context": c})
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		workerHTTPError(w, err)
		return
	}
	if parts[0] == "deployments" && r.Method == http.MethodGet {
		if len(parts) == 1 {
			d, err := ws.ListWorkerDeployments(p.AccountScopeID, worker)
			if err != nil {
				workerHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"deployments": d})
			return
		}
		if len(parts) == 2 {
			d, err := ws.GetWorkerDeployment(p.AccountScopeID, worker, parts[1])
			if err != nil {
				workerHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"deployment": d})
			return
		}
		if len(parts) == 3 && parts[2] == "commands" {
			c, err := ws.ListWorkerCommands(p.AccountScopeID, worker, parts[1])
			if err != nil {
				workerHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"commands": c})
			return
		}
	}
	execution, err := s.workerExecutionService()
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if len(parts) == 1 && parts[0] == "context" && r.Method == http.MethodPut {
		var req store.WorkerContextUpdate
		if err = decodeJSONStrict(w, r, 96*1024, &req); err != nil {
			workerHTTPError(w, err)
			return
		}
		c, err := execution.UpdateContext(ctx, worker, req)
		if err != nil {
			workerHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"context": c})
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if len(parts) == 1 && parts[0] == "ssh-targets" {
		var req store.WorkerSSHRegistration
		if err = decodeJSONStrict(w, r, 8192, &req); err != nil {
			workerHTTPError(w, err)
			return
		}
		target, err := execution.RegisterSSHTarget(ctx, req)
		if err != nil {
			workerHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"target": target, "execution_available": false})
		return
	}
	if len(parts) == 1 && parts[0] == "target-reference" {
		var req store.WorkerTargetReference
		if err = decodeJSONStrict(w, r, 8192, &req); err != nil {
			workerHTTPError(w, err)
			return
		}
		target, err := execution.ResolveWorkerTarget(ctx, req)
		if err != nil {
			workerHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"target": target, "execution_available": false})
		return
	}
	if len(parts) == 1 && parts[0] == "deployments" {
		var req store.WorkerDeploymentRequest
		if err = decodeJSONStrict(w, r, 16384, &req); err != nil {
			workerHTTPError(w, err)
			return
		}
		d, err := execution.ProposeDeployment(ctx, worker, req)
		if err != nil {
			workerHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"deployment": d})
		return
	}
	if len(parts) == 3 && parts[0] == "deployments" {
		id := parts[1]
		switch parts[2] {
		case "approve":
			var req struct {
				ExpectedRevision uint64 `json:"expected_revision"`
				ApprovalDigest   string `json:"approval_digest"`
			}
			if err = decodeJSONStrict(w, r, 8192, &req); err != nil {
				workerHTTPError(w, err)
				return
			}
			d, err := execution.ApproveDeployment(ctx, worker, id, req.ExpectedRevision, req.ApprovalDigest)
			if err != nil {
				workerHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"deployment": d})
			return
		case "commands":
			var req store.WorkerCommandRequest
			if err = decodeJSONStrict(w, r, 8192, &req); err != nil {
				workerHTTPError(w, err)
				return
			}
			c, err := execution.DeploymentCommand(ctx, worker, id, req)
			if err != nil {
				workerHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"command": c})
			return
		case "jobs":
			var req struct {
				WorkerRevision     uint64         `json:"worker_revision"`
				DeploymentRevision uint64         `json:"deployment_revision"`
				ContextRevision    uint64         `json:"context_revision"`
				IdempotencyKey     string         `json:"idempotency_key"`
				Input              map[string]any `json:"input"`
			}
			if err = decodeJSONStrict(w, r, 96*1024, &req); err != nil {
				workerHTTPError(w, err)
				return
			}
			run, err := execution.QueueDeploymentJob(ctx, store.WorkerRunAdmission{WorkerID: worker, ExpectedWorkerRevision: req.WorkerRevision, Placement: &store.WorkerPlacementAdmission{DeploymentID: id, DeploymentRevision: req.DeploymentRevision, ContextRevision: req.ContextRevision}, IdempotencyKey: req.IdempotencyKey, Input: req.Input, RequestSource: "direct"})
			if err != nil {
				workerHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"run": run, "execution_available": false})
			return
		}
	}
	writeError(w, http.StatusNotFound, errors.New("worker control route not found"))
}
