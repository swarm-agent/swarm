package api

import (
	"net/http"

	"swarm/packages/swarmd/internal/identity"
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
	args := controlMCPMap(raw)
	fail := func() (*store.SessionModelProfileSnapshot, error) {
		return nil, controlMCPToolFailure("invalid worker model; choose a connected provider, model and supported options from swarm_list_models")
	}
	if args == nil {
		return fail()
	}
	for key := range args {
		if key != "provider" && key != "model" && key != "thinking" && key != "fast" {
			return fail()
		}
	}
	for _, key := range []string{"provider", "model", "thinking"} {
		if _, ok := args[key].(string); !ok {
			return fail()
		}
	}
	if fast, exists := args["fast"]; exists {
		if _, ok := fast.(bool); !ok {
			return fail()
		}
	}
	providers, err := c.dispatch(http.MethodGet, "/v1/providers", nil, nil)
	if err != nil {
		return nil, err
	}
	connected := false
	for _, raw := range controlMCPList(providers["providers"]) {
		p := controlMCPMap(raw)
		runnable, _ := p["runnable"].(bool)
		if p["id"] == args["provider"] && runnable {
			connected = true
		}
	}
	if !connected {
		return fail()
	}
	preference, err := c.modelPreference(args)
	if err != nil || preference == nil || controlMCPString(preference, "thinking") != args["thinking"] {
		return fail()
	}
	selection := store.ModelProfileSelection{Provider: controlMCPString(preference, "provider"), Model: controlMCPString(preference, "model"), Thinking: controlMCPString(preference, "thinking"), ServiceTier: controlMCPString(preference, "service_tier")}
	return &store.SessionModelProfileSnapshot{Source: store.SessionModelProfileSourceTemporary, Action: selection, Plan: &selection}, nil
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
		c := &controlMCPCall{request: r, next: s.Handler(), server: s}
		var err error
		profile, err = c.workerModel(req.Model)
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
	if raw, exists := args["model"]; exists {
		id, _ := worker["id"].(string)
		response, err := c.dispatch(http.MethodPut, "/v3/workers/"+id+"/model", nil, map[string]any{"expected_revision": controlMCPRevision(worker), "model": raw})
		if err != nil {
			return nil, err
		}
		worker = controlMCPMap(response["worker"])
	}
	view := controlMCPWorkerView(worker)
	roleModels, err := c.roleModels()
	if err != nil {
		return nil, err
	}
	view["model"] = controlMCPWorkerModel(worker, roleModels)
	return map[string]any{"worker": view, "deployed": true}, nil
}
