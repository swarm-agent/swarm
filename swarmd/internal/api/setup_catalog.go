package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// SetupActionsPath is the route a linked home's manage_setup reaches on this
// machine: POST /v3/setup-actions/{action} runs this machine's ManageSetup,
// the one setup service, with the caller's own scopes. Only the actions that
// reach a linked machine (models, providers, usage, limits, media models and
// update drains) are served; keys, secrets, permissions and provider sign-in
// never cross a link.
const SetupActionsPath = "/v3/setup-actions"

// manageSetupAction is the declared manage_setup action.
func manageSetupAction(name string) (tool.CatalogAction, bool) {
	catalog, ok := tool.CatalogToolNamed(tool.ManageSetupToolName)
	if !ok {
		return tool.CatalogAction{}, false
	}
	return catalog.Action(name)
}

// setupLinkedScopes are the API scopes of the setup actions that reach a
// linked machine: what a linked home's link holds on SetupActionsPath.
func setupLinkedScopes() []string {
	seen := map[string]bool{}
	var out []string
	catalog, _ := tool.CatalogToolNamed(tool.ManageSetupToolName)
	for _, action := range catalog.Actions {
		if action.Reach != tool.CatalogReachAny {
			continue
		}
		for _, scope := range action.APIScopes {
			if !seen[scope] {
				seen[scope] = true
				out = append(out, scope)
			}
		}
	}
	return out
}

// ManageSetup is the one setup service behind the catalog's manage_setup:
// the Orchestrator (who is its session; the run's person-decision gate and
// the tool handler already held a change to the person's decision), Swarm
// Control (who is "key:ID"; its key scopes ride in ctx,
// tool.ControlScopesFromContext) and a linked home (POST
// /v3/setup-actions/{action}) all run it. Each action runs the swarm_* tool
// handler with the same effect, on an internal request that carries the
// caller's own key (Swarm Control), only the scopes the key holds (a linked
// caller), or only the action's own route scopes (a conversation): never
// wider. Before a change, the service refuses any that would widen the
// caller's access (permissions, limits, usage limits) and every key
// creation; after, it drops any secret-shaped field from the answer. With
// machine in args the action runs on that linked machine's own ManageSetup
// and its answer comes back as it came.
func (s *Server) ManageSetup(ctx context.Context, p identity.Principal, who string, args map[string]any) (any, error) {
	if !p.Valid() || s.sessions == nil {
		return nil, errors.New("manage_setup requires a session principal")
	}
	declared, ok := manageSetupAction(controlMCPString(args, "action"))
	if !ok {
		return nil, errors.New("unknown manage_setup action")
	}
	held, control := tool.ControlScopesFromContext(ctx)
	if control {
		// A Swarm Control or linked caller acts within its key: one of the
		// action's route scopes (the catalog gate checked it too).
		if !catalogScopesHeld(func(scope string) bool { return nativeJobScopeHeld(held, scope) }, declared) {
			return nil, controlMCPToolFailure("this connection lacks %s", strings.Join(declared.APIScopes, " or "))
		}
	} else {
		session, found, err := s.sessions.Store().GetSession(who)
		if err != nil {
			return nil, err
		}
		if !found || session.AccountScopeID != p.AccountScopeID || session.UserID != p.UserID {
			return nil, errors.New("manage_setup session principal mismatch")
		}
		if task, _ := session.Metadata["task_id"].(string); task != "" {
			return nil, errors.New("manage_setup is the home Orchestrator's; a task's session cannot change machine setup")
		}
		if task, _ := session.Metadata["project_task_id"].(string); task != "" {
			return nil, errors.New("manage_setup is the home Orchestrator's; a task's session cannot change machine setup")
		}
	}
	clean := map[string]any{}
	for _, key := range declared.Inputs {
		if value, ok := args[key]; ok && key != "machine" {
			clean[key] = value
		}
	}
	// The declared inputs are checked here for every caller (Swarm Control
	// checked them too), before anything is routed or run.
	catalog, _ := tool.CatalogToolNamed(tool.ManageSetupToolName)
	validate := map[string]any{"action": declared.Action}
	for key, value := range clean {
		validate[key] = value
	}
	if err := controlMCPValidateArgs(catalog.InputSchema(declared), validate); err != nil {
		return nil, err
	}
	if machine := controlMCPString(args, "machine"); machine != "" {
		if declared.Reach != tool.CatalogReachAny {
			return nil, fmt.Errorf("manage_setup %s runs on this machine only", declared.Action)
		}
		return s.CallFleetWork(ctx, p, machine, tool.ManageSetupToolName, declared.Action, clean)
	}
	call, err := s.setupCall(ctx, p, who, control, held, declared)
	if err != nil {
		return nil, err
	}
	out, err := s.runSetupAction(call, p, declared.Action, clean)
	if err != nil {
		// One text for both surfaces: the Orchestrator's runtime trims a
		// tool error, so the service does it for everyone.
		var refused controlMCPToolError
		if errors.As(err, &refused) {
			refused.message = strings.TrimSpace(refused.message)
			return nil, refused
		}
		return nil, err
	}
	return setupScrub(out), nil
}

// setupCall is the internal request a setup action's swarm_* handler runs on:
// the principal, and a key record that is the caller's own key narrowed to
// the scopes it holds (Swarm Control), only those scopes (a linked caller),
// or only the action's route scopes and level (a conversation).
func (s *Server) setupCall(ctx context.Context, p identity.Principal, who string, control bool, held []string, declared tool.CatalogAction) (*controlMCPCall, error) {
	record := &store.ScopedTokenRecord{AccountScopeID: p.AccountScopeID}
	if control {
		record.Scopes = append([]string(nil), held...)
		if id, ok := strings.CutPrefix(who, "key:"); ok && s.security != nil {
			// The caller's own key, so its revocation and identity hold
			// (a provider key link dies with the key that asked for it).
			records, err := s.security.ListScopedTokens(p.AccountScopeID)
			if err != nil {
				return nil, controlMCPToolFailure("keys could not be read")
			}
			for _, candidate := range records {
				if candidate.ID == id {
					own := candidate
					own.Scopes = append([]string(nil), held...)
					record = &own
					break
				}
			}
		}
	} else {
		record.Name = "Swarm Orchestrator"
		record.Scopes = append(append([]string(nil), declared.APIScopes...), declared.Level.Scope())
	}
	req, err := http.NewRequestWithContext(identity.ContextWithPrincipal(ctx, p), http.MethodPost, "/mcp", nil)
	if err != nil {
		return nil, err
	}
	req = requestWithScopedToken(req, record)
	return &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}, nil
}

// runSetupAction runs one setup action's swarm_* handler (the same function
// the swarm_* tool calls) with the catalog's inputs under that tool's names.
func (s *Server) runSetupAction(c *controlMCPCall, p identity.Principal, action string, args map[string]any) (any, error) {
	renamed := func(from, to string) map[string]any {
		out := map[string]any{}
		for key, value := range args {
			if key == from {
				key = to
			}
			out[key] = value
		}
		return out
	}
	switch action {
	case "models":
		return controlMCPListModels(c, args)
	case "providers":
		return controlMCPListProviders(c, args)
	case "usage":
		return controlMCPGetUsage(c, args)
	case "plan_usage":
		return controlMCPGetPlanUsage(c, args)
	case "limits":
		return controlMCPGetLimits(c, args)
	case "permissions":
		return controlMCPGetPermissions(c, args)
	case "list_keys":
		return controlMCPListKeys(c, args)
	case "list_fleet_keys":
		return controlMCPListFleetKeys(c, args)
	case "list_secrets":
		return controlMCPListSecrets(c, args)
	case "media_models":
		return controlMCPListMediaModels(c, args)
	case "set_agent_model":
		return controlMCPSetAgentModel(c, args)
	case "set_media_models":
		return controlMCPSetMediaModels(c, args)
	case "set_limits":
		if err := s.setupLimitsNarrow(p, args); err != nil {
			return nil, err
		}
		return controlMCPSetLimits(c, args)
	case "set_usage_limits":
		if err := setupUsageLimitsNarrow(c, args); err != nil {
			return nil, err
		}
		return controlMCPSetUsageLimits(c, args)
	case "set_permissions":
		if err := s.setupPermissionsNarrow(p, args); err != nil {
			return nil, err
		}
		return controlMCPSetPermissions(c, args)
	case "create_key", "create_client_key":
		return nil, controlMCPToolFailure("manage_setup %s makes no key: a new key's secret may reach only the person, and no one-time path hands it to them yet. Nothing was created; the person makes the key in Swarm", action)
	case "revoke_key":
		return controlMCPRevokeKey(c, args)
	case "request_fleet_key":
		return controlMCPRequestFleetKey(c, args)
	case "revoke_fleet_key":
		return controlMCPRevokeFleetKey(c, args)
	case "request_provider_key":
		return controlMCPRequestProviderKey(c, args)
	case "connect_chatgpt":
		return controlMCPConnectChatGPT(c, renamed("step", "action"))
	case "connect_claude_code":
		return controlMCPConnectClaudeCode(c, renamed("step", "action"))
	case "request_update":
		return controlMCPRequestUpdate(c, args)
	case "cancel_update":
		return controlMCPCancelUpdate(c, args)
	}
	return nil, fmt.Errorf("manage_setup %s has no handler", action)
}

// setupPermissionsNarrow refuses a permissions change that would widen what
// agents may do: Start tasks may only go to approve, and a linked machine's
// work only down (auto, ask, off). Raising either is the person's, in Swarm.
func (s *Server) setupPermissionsNarrow(p identity.Principal, args map[string]any) error {
	current, err := s.sessions.Store().GetAccountPermissions(p.AccountScopeID)
	if err != nil {
		return err
	}
	if raw, ok := args["tasks"]; ok {
		tasks, _ := raw.(string)
		if tasks != "approve" && tasks != current.Tasks {
			return controlMCPToolFailure("manage_setup set_permissions can only set Start tasks to approve; anything wider is the person's to set in Swarm. Nothing changed")
		}
	}
	rank := map[string]int{store.RuleOff: 0, store.RuleAsk: 1, store.RuleAuto: 2}
	for name, raw := range controlMCPMap(args["linked"]) {
		work, _ := controlMCPMap(raw)["work"].(string)
		want, known := rank[store.NormalizeLinkedRule(work)]
		if !known {
			return controlMCPToolFailure("linked %s work must be off or ask", name)
		}
		if want > rank[current.Link(name).Work] {
			return controlMCPToolFailure("manage_setup set_permissions can only lower linked %s work (now %s); raising it is the person's to do in Swarm. Nothing changed", name, current.Link(name).Work)
		}
	}
	return nil
}

// setupLimitsNarrow refuses a box limits change that raises any capacity or
// budget (or removes a job budget): the limits bound what agents may spend
// and start, so only the person raises them, in Swarm.
func (s *Server) setupLimitsNarrow(p identity.Principal, args map[string]any) error {
	current, err := s.sessions.Store().GetLimits(p.AccountScopeID)
	if err != nil {
		return err
	}
	// Decode the request as PUT /v3/limits does: helper fields default,
	// everything else is zero when left out.
	defaults := store.DefaultLimits()
	next := store.Limits{HelperConcurrency: defaults.HelperConcurrency, HelpersPerJob: defaults.HelpersPerJob, HelperTimeoutSeconds: defaults.HelperTimeoutSeconds, HelperTokens: defaults.HelperTokens}
	raw, err := json.Marshal(args["limits"])
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		return controlMCPToolFailure("limits: %s", err.Error())
	}
	cacheBytes := func(v uint64) uint64 {
		if v == 0 {
			return defaults.ProjectCacheBytes
		}
		return v
	}
	var wider []string
	check := func(name string, now, want float64) {
		if want > now {
			wider = append(wider, name)
		}
	}
	// A zero job budget is unset: no budget at all.
	budget := func(name string, now, want float64) {
		if now > 0 && (want == 0 || want > now) {
			wider = append(wider, name)
		}
	}
	check("project_cache_bytes", float64(cacheBytes(current.ProjectCacheBytes)), float64(cacheBytes(next.ProjectCacheBytes)))
	check("helper_concurrency", float64(current.HelperConcurrency), float64(next.HelperConcurrency))
	check("helpers_per_job", float64(current.HelpersPerJob), float64(next.HelpersPerJob))
	check("helper_timeout_seconds", float64(current.HelperTimeoutSeconds), float64(next.HelperTimeoutSeconds))
	check("helper_tokens", float64(current.HelperTokens), float64(next.HelperTokens))
	check("worker_job_concurrency", float64(current.WorkerJobConcurrency), float64(next.WorkerJobConcurrency))
	check("delegation_waves", float64(current.DelegationWaves), float64(next.DelegationWaves))
	check("active_children", float64(current.ActiveChildren), float64(next.ActiveChildren))
	check("swarm_children", float64(current.SwarmChildren), float64(next.SwarmChildren))
	budget("job_tokens", float64(current.JobTokens), float64(next.JobTokens))
	budget("job_cost_usd", current.JobCostUSD, next.JobCostUSD)
	if len(wider) > 0 {
		return controlMCPToolFailure("manage_setup set_limits can only lower limits; %s would rise (or lose its budget). Raising limits is the person's to do in Swarm. Nothing changed", strings.Join(wider, ", "))
	}
	return nil
}

// setupUsageLimitsNarrow refuses a daily usage limits change that raises or
// removes a limit or turns the limits off.
func setupUsageLimitsNarrow(c *controlMCPCall, args map[string]any) error {
	response, err := c.dispatch(http.MethodGet, "/v3/usage/limits", nil, nil)
	if err != nil {
		return err
	}
	current := controlMCPMap(response["limits"])
	if enabled, ok := args["enabled"].(bool); ok && !enabled {
		if on, _ := current["enabled"].(bool); on {
			return controlMCPToolFailure("manage_setup set_usage_limits cannot turn the limits off; that is the person's to do in Swarm. Nothing changed")
		}
	}
	for _, key := range []string{"daily_cost_limit_usd", "daily_tokens_limit"} {
		raw, ok := args[key]
		if !ok {
			continue
		}
		want, isNumber := raw.(float64)
		if !isNumber {
			return controlMCPToolFailure("%s must be a number", key)
		}
		now, _ := current[key].(float64)
		if now > 0 && (want == 0 || want > now) {
			return controlMCPToolFailure("manage_setup set_usage_limits can only lower %s (now %v); raising or removing it is the person's to do in Swarm. Nothing changed", key, now)
		}
	}
	return nil
}

// setupSecretFields are answer fields that would hold a secret or key value.
// No setup answer carries one (the handlers return names, ids, scopes and
// expiry); setupScrub drops them anyway, so no future handler change can
// hand one to an AI through manage_setup.
var setupSecretFields = map[string]bool{"key": true, "token": true, "api_key": true, "secret": true, "secret_value": true, "password": true, "access_token": true, "refresh_token": true, "private_key": true}

// setupScrub drops secret-shaped string fields from an answer. A field such
// as revoke_key's key (the key's record, an object) is not a string and
// stays.
func setupScrub(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return value
	}
	var walk func(any) any
	walk = func(v any) any {
		switch typed := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(typed))
			for key, item := range typed {
				if _, isText := item.(string); isText && setupSecretFields[strings.ToLower(key)] {
					continue
				}
				out[key] = walk(item)
			}
			return out
		case []any:
			out := make([]any, len(typed))
			for i, item := range typed {
				out[i] = walk(item)
			}
			return out
		}
		return v
	}
	return walk(decoded)
}

// handleSetupActions serves POST /v3/setup-actions/{action}: one manage_setup
// action that reaches a linked machine, run by ManageSetup. Keys, secrets,
// permissions and provider sign-in are refused here: they never cross a link.
func (s *Server) handleSetupActions(w http.ResponseWriter, r *http.Request) {
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, SetupActionsPath), "/")
	if action, ok := manageSetupAction(name); ok && action.Reach != tool.CatalogReachAny {
		writeError(w, http.StatusForbidden, fmt.Errorf("manage_setup %s is never managed over a link", action.Action))
		return
	}
	s.serveCatalogLinked(w, r, SetupActionsPath, tool.ManageSetupToolName, setupLinkedScopes(), func(ctx context.Context, p identity.Principal, args map[string]any) (any, error) {
		return s.ManageSetup(ctx, p, "", args)
	})
}
