package api

import (
	"net/http"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func controlMCPWorkerModelSchema(nullable bool) map[string]any {
	schema := controlMCPObject([]string{"provider", "model", "thinking"}, map[string]any{
		"provider": controlMCPStringProp("Connected provider from swarm_list_models.", 1, 100),
		"model":    controlMCPStringProp("Model from swarm_list_models.", 1, 256),
		"thinking": controlMCPStringProp("Supported thinking option.", 0, 100),
		"fast":     map[string]any{"type": "boolean", "description": "Only where swarm_list_models shows fast: true."},
	})
	if nullable {
		return map[string]any{"anyOf": []any{schema, map[string]any{"type": "null"}}}
	}
	return schema
}

// workerModel validates through the same provider/catalog authority as list_models,
// without requiring its sessions:read grant: these tools retain their write scopes.
func (c *controlMCPCall) workerModel(raw any) (*store.SessionModelProfileSnapshot, error) {
	profile, err := runruntime.ValidateWorkerModelPin(c.request.Context(), c.server.providers, c.server.model, raw)
	if err != nil {
		return nil, controlMCPToolFailure("%s", err)
	}
	return profile, nil
}

func (s *Server) handleWorkerModelPin(w http.ResponseWriter, r *http.Request, p identity.Principal, id string) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		ExpectedRevision uint64         `json:"expected_revision"`
		Model            map[string]any `json:"model"`
	}
	if err := decodeJSONStrict(w, r, 16*1024, &req); err != nil {
		workerHTTPError(w, err)
		return
	}
	profile := &store.SessionModelProfileSnapshot{Source: store.SessionModelProfileSourceSwarmSettings, UseAccountDefault: true}
	if req.Model != nil {
		var err error
		profile, err = runruntime.ValidateWorkerModelPin(identity.ContextWithPrincipal(r.Context(), p), s.providers, s.model, req.Model)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	ws := store.NewWorkerStore(s.sessions.Store().Underlying())
	worker, err := ws.SetWorkerModelProfile(p.AccountScopeID, p.UserID, id, req.ExpectedRevision, profile)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"worker": worker})
}

func controlMCPRunModel(run map[string]any) map[string]any {
	profile := controlMCPMap(run["model_profile"])
	if profile == nil {
		return nil
	}
	model := controlMCPRoleModel(controlMCPMap(profile["action"]))
	if len(model) == 0 {
		return nil
	}
	model["source"] = run["model_source"]
	return model
}

func (c *controlMCPCall) deployWorkerModel(worker map[string]any, args map[string]any) (any, error) {
	if role := controlMCPString(args, "role"); role != "" && role != worker["role"] {
		return nil, controlMCPToolFailure("changing an existing worker's role is not supported; create a new worker")
	}
	body := map[string]any{"expected_revision": controlMCPRevision(worker)}
	for _, key := range []string{"instructions", "description", "agent"} {
		if value, present := args[key]; present {
			body[key] = value
		}
	}
	if len(body) > 1 {
		id, _ := worker["id"].(string)
		response, err := c.dispatch(http.MethodPut, "/v3/workers/"+id, nil, body)
		if err != nil {
			return nil, err
		}
		worker = controlMCPMap(response["worker"])
	}
	if raw, exists := args["model"]; exists {
		id, _ := worker["id"].(string)
		response, err := c.dispatch(http.MethodPut, "/v3/workers/"+id+"/model", nil, map[string]any{"expected_revision": controlMCPRevision(worker), "model": raw})
		if err != nil {
			return nil, err
		}
		worker = controlMCPMap(response["worker"])
	}
	view := controlMCPWorkerView(worker)
	view["model"] = c.workerModelSummary(worker)
	return map[string]any{"worker": view, "deployed": true}, nil
}
