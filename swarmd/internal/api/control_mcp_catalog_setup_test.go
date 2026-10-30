package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/provider/registry"
	"swarm/packages/swarmd/internal/remote"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// catalogParitySetupKey is a key at every level with the API scopes behind
// every manage_setup action (admin among them: drains and key routes).
var catalogParitySetupKey = append(append([]string(nil), catalogParityFullKey...), "usage:read", "usage:write", "settings:write", "secrets:read", "admin")

// manageSetupParityCases holds a case for every manage_setup action, here
// and, for the actions that reach linked machines, on the linked machine
// box. Reads run as they are; changes run on inputs both surfaces refuse
// the same way (a stale revision, an unknown role or provider, an input of
// the wrong type, a key or fleet key that does not exist) or are the same
// on both (cancel_update with nothing held), so neither call changes what
// the other reads. The successful flows are TestManageSetupFromBothSurfaces.
func manageSetupParityCases() map[string][]catalogParityCase {
	on := func(machines []string, args ...any) []catalogParityCase {
		var out []catalogParityCase
		for _, machine := range machines {
			call := parityArgs(args...)
			if machine != "" {
				call["machine"] = machine
			}
			out = append(out, catalogParityCase{orchestrator: call, control: call, compare: parityExact})
		}
		return out
	}
	everywhere := func(args ...any) []catalogParityCase { return on([]string{"", "box"}, args...) }
	home := func(args ...any) []catalogParityCase { return on([]string{""}, args...) }
	return map[string][]catalogParityCase{
		"models":               everywhere("action", "models"),
		"providers":            everywhere("action", "providers"),
		"usage":                everywhere("action", "usage", "period", "today"),
		"plan_usage":           everywhere("action", "plan_usage"),
		"limits":               everywhere("action", "limits"),
		"media_models":         everywhere("action", "media_models"),
		"permissions":          home("action", "permissions"),
		"list_keys":            home("action", "list_keys"),
		"list_fleet_keys":      home("action", "list_fleet_keys"),
		"list_secrets":         home("action", "list_secrets"),
		"set_agent_model":      everywhere("action", "set_agent_model", "role", "no-such-role", "provider", "fixture-provider", "model", "fixture-model"),
		"set_media_models":     everywhere("action", "set_media_models", "image", float64(5)),
		"set_limits":           everywhere("action", "set_limits", "expected_revision", float64(999), "limits", map[string]any{}),
		"set_usage_limits":     everywhere("action", "set_usage_limits", "daily_cost_limit_usd", float64(-1)),
		"set_permissions":      home("action", "set_permissions", "expected_revision", float64(999), "client_request_id", "parity", "tasks", "approve"),
		"create_key":           home("action", "create_key", "key_kind", "app", "name", "parity"),
		"create_client_key":    home("action", "create_client_key", "name", "parity"),
		"revoke_key":           home("action", "revoke_key", "key_id", "tok_missing"),
		"request_fleet_key":    home("action", "request_fleet_key", "provider", "no-such-provider"),
		"revoke_fleet_key":     home("action", "revoke_fleet_key", "id", "fk_missing"),
		"request_provider_key": home("action", "request_provider_key", "provider", "no-such-provider"),
		"connect_chatgpt":      home("action", "connect_chatgpt", "step", "status", "login_id", "no-such-login"),
		"connect_claude_code":  home("action", "connect_claude_code", "step", "status"),
		"request_update":       everywhere("action", "request_update", "max_seconds", float64(-1)),
		"cancel_update":        everywhere("action", "cancel_update"),
	}
}

// seedSetup gives the home and the linked machine a provider registry (no
// adapters, so no provider is ever reached), as the daemon always has one:
// the models and providers routes read it.
func (h *catalogParityHarness) seedSetup(t *testing.T) {
	t.Helper()
	for _, s := range []*Server{h.home.s, h.remote.s} {
		if s.providers == nil {
			s.providers = registry.New()
		}
	}
}

// setupEnvelope checks the catalog envelope and returns its data.
func setupEnvelope(t *testing.T, out string) map[string]any {
	t.Helper()
	if err := tool.CheckEnvelope(out); err != nil {
		t.Fatalf("not a catalog envelope: %v\n%s", err, out)
	}
	var envelope struct {
		Tool string         `json:"tool"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || envelope.Tool != "manage_setup" {
		t.Fatalf("envelope %s: %v", out, err)
	}
	return envelope.Data
}

// setupSurfaces runs manage_setup calls through Swarm Control (a key at
// every level) and as the Orchestrator with the person's decision (what the
// run's gate gives a change the person approved).
func setupSurfaces(t *testing.T, h *catalogParityHarness) (control, orchestrator func(map[string]any) (map[string]any, string)) {
	control = func(args map[string]any) (map[string]any, string) {
		t.Helper()
		out, refused := h.controlTool(t, catalogParitySetupKey, "manage_setup", args)
		if refused != "" {
			return nil, refused
		}
		return setupEnvelope(t, out), ""
	}
	orchestrator = func(args map[string]any) (map[string]any, string) {
		t.Helper()
		out, err := h.orchestratorTool(t, "manage_setup", args)
		if err != nil {
			return nil, err.Error()
		}
		return setupEnvelope(t, out), ""
	}
	return control, orchestrator
}

// setupLimitsArgs is the machine's current box limits as set_limits takes
// them, with changes applied.
func setupLimitsArgs(t *testing.T, s *Server, account string, change func(*pebblestore.Limits)) map[string]any {
	t.Helper()
	current, err := s.sessions.Store().GetLimits(account)
	if err != nil {
		t.Fatal(err)
	}
	revision := current.Revision
	change(&current)
	raw, _ := json.Marshal(current)
	var limits map[string]any
	_ = json.Unmarshal(raw, &limits)
	delete(limits, "revision")
	return map[string]any{"action": "set_limits", "expected_revision": float64(revision), "limits": limits}
}

// Purpose:
//   - Requirement: the Orchestrator and an MCP client change and read this
//     machine's setup through the catalog's manage_setup with the same
//     actions and answers, here and on a linked machine: the Orchestrator
//     narrows Start tasks and Swarm Control reads it; Swarm Control lowers a
//     linked machine's work rule and the Orchestrator reads it; the
//     Orchestrator lowers a box limit and Swarm Control reads it; Swarm
//     Control lowers the linked machine's limit and the Orchestrator reads it
//     there; both cancel an update drain.
//   - Threat/regression: a setup action one surface cannot reach, a change
//     not stored, or a linked action that does not reach the machine's own
//     record.
//   - Boundary/authority: tool.Runtime.executeManageSetup (both surfaces)
//     -> api.Server.ManageSetup -> the swarm_* setup handlers and their
//     routes, and with machine CallFleetWork -> the machine's POST
//     /v3/setup-actions/{action}. Real stores on two linked in-process
//     daemons; no provider is reached.
//   - Narrowest layer: the flow crosses both surfaces, the service, the
//     routes and the link, so only routed calls prove it.
func TestManageSetupFromBothSurfaces(t *testing.T) {
	h := newCatalogParityHarness(t)
	control, orchestrator := setupSurfaces(t, h)
	store := h.home.s.sessions.Store()

	before, _ := store.GetAccountPermissions(h.home.account)
	if _, refused := orchestrator(map[string]any{"action": "set_permissions", "expected_revision": float64(before.Revision), "client_request_id": "e2e-tasks", "tasks": "approve"}); refused != "" {
		t.Fatalf("Orchestrator set_permissions: %s", refused)
	}
	if got, _ := store.GetAccountPermissions(h.home.account); got.Tasks != "approve" {
		t.Fatalf("Start tasks not stored: %+v", got)
	}
	if read, refused := control(map[string]any{"action": "permissions"}); refused != "" || !strings.Contains(mustJSON(t, read["home"]), `"tasks":"approve"`) {
		t.Fatalf("Swarm Control permissions: %v %s", read, refused)
	}
	current, _ := store.GetAccountPermissions(h.home.account)
	if _, refused := control(map[string]any{"action": "set_permissions", "expected_revision": float64(current.Revision), "client_request_id": "e2e-linked", "linked": map[string]any{"box": map[string]any{"work": "ask"}}}); refused != "" {
		t.Fatalf("Swarm Control set_permissions: %s", refused)
	}
	if got, _ := store.GetAccountPermissions(h.home.account); got.Link("box").Work != pebblestore.RuleAsk {
		t.Fatalf("linked work not stored: %+v", got)
	}
	if read, refused := orchestrator(map[string]any{"action": "permissions"}); refused != "" || !strings.Contains(mustJSON(t, read["linked"]), `"name":"box"`) || !strings.Contains(mustJSON(t, read["linked"]), `"work":"ask"`) {
		t.Fatalf("Orchestrator permissions: %v %s", read, refused)
	}
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleAuto)

	lower := setupLimitsArgs(t, h.home.s, h.home.account, func(l *pebblestore.Limits) { l.ActiveChildren = 3 })
	if _, refused := orchestrator(lower); refused != "" {
		t.Fatalf("Orchestrator set_limits: %s", refused)
	}
	if read, refused := control(map[string]any{"action": "limits"}); refused != "" || read["active_children"] != float64(3) {
		t.Fatalf("Swarm Control limits: %v %s", read, refused)
	}
	remoteLower := setupLimitsArgs(t, h.remote.s, h.remote.account, func(l *pebblestore.Limits) { l.SwarmChildren = 2 })
	remoteLower["machine"] = "box"
	if _, refused := control(remoteLower); refused != "" {
		t.Fatalf("Swarm Control set_limits on box: %s", refused)
	}
	if got, _ := h.remote.s.sessions.Store().GetLimits(h.remote.account); got.SwarmChildren != 2 {
		t.Fatalf("linked limits not stored: %+v", got)
	}
	if read, refused := orchestrator(map[string]any{"action": "limits", "machine": "box"}); refused != "" || read["swarm_children"] != float64(2) {
		t.Fatalf("Orchestrator limits on box: %v %s", read, refused)
	}
	for _, surface := range []func(map[string]any) (map[string]any, string){control, orchestrator} {
		if _, refused := surface(map[string]any{"action": "cancel_update"}); refused != "" {
			t.Fatalf("cancel_update: %s", refused)
		}
	}
}

// Purpose:
//   - Requirement: no AI widens its own access through manage_setup, on
//     either surface, even at the manage level and with the person's
//     decision: set_permissions only narrows (Start tasks to approve, linked
//     work down), set_limits only lowers (no capacity raised, no job budget
//     removed), set_usage_limits only lowers or turns limits on, and
//     create_key and create_client_key make no key. Each refusal says so and
//     leaves the record as it was.
//   - Threat/regression: an AI raising its own approval rules, spend or
//     concurrency, or minting a key for itself, through the catalog.
//   - Boundary/authority: Server.ManageSetup's setupPermissionsNarrow,
//     setupLimitsNarrow, setupUsageLimitsNarrow and the key refusal, through
//     tools/call and the Orchestrator's handler on the parity daemons.
//   - Narrowest layer: the service, called as both surfaces call it, with
//     the stores read before and after.
func TestManageSetupNeverWidensTheCallersAccess(t *testing.T) {
	h := newCatalogParityHarness(t)
	control, orchestrator := setupSurfaces(t, h)
	store := h.home.s.sessions.Store()
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleAsk)
	if _, err := store.SetLimits(h.home.account, mustLimits(t, store, h.home.account).Revision, func() pebblestore.Limits {
		l := mustLimits(t, store, h.home.account)
		l.JobTokens = 1000
		return l
	}()); err != nil {
		t.Fatal(err)
	}
	// A spend limit to protect, set by the person on the usage limits route.
	seed := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/usage/limits", strings.NewReader(`{"daily_cost_limit_usd":5,"enabled":true}`)), h.home.user, h.home.account)
	seed.Header.Set("Content-Type", "application/json")
	seeded := httptest.NewRecorder()
	h.home.s.handleSessionsV3UsageLimits(seeded, seed)
	if seeded.Code != http.StatusOK {
		t.Fatalf("seed usage limit: %d %s", seeded.Code, seeded.Body.String())
	}
	permissions, _ := store.GetAccountPermissions(h.home.account)
	limits := mustLimits(t, store, h.home.account)
	refusals := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "set_permissions", "expected_revision": float64(permissions.Revision), "client_request_id": "w1", "tasks": "auto"}, "tasks"},
		{map[string]any{"action": "set_permissions", "expected_revision": float64(permissions.Revision), "client_request_id": "w2", "linked": map[string]any{"box": map[string]any{"work": "auto"}}}, "work"},
		{setupLimitsArgs(t, h.home.s, h.home.account, func(l *pebblestore.Limits) { l.ActiveChildren = l.ActiveChildren + 1 }), "active_children"},
		{setupLimitsArgs(t, h.home.s, h.home.account, func(l *pebblestore.Limits) { l.WorkerJobConcurrency = l.WorkerJobConcurrency * 2 }), "worker_job_concurrency"},
		{setupLimitsArgs(t, h.home.s, h.home.account, func(l *pebblestore.Limits) { l.JobTokens = 0 }), "job_tokens"},
		{map[string]any{"action": "set_usage_limits", "daily_cost_limit_usd": float64(10)}, "daily_cost_limit_usd"},
		{map[string]any{"action": "set_usage_limits", "daily_cost_limit_usd": float64(0)}, "daily_cost_limit_usd"},
		{map[string]any{"action": "set_usage_limits", "enabled": false}, "cannot turn the limits off"},
		{map[string]any{"action": "create_key", "key_kind": "app", "name": "mine"}, "makes no key"},
		{map[string]any{"action": "create_client_key", "name": "mine"}, "makes no key"},
	}
	keysBefore, err := h.home.s.security.ListScopedTokens(h.home.account)
	if err != nil {
		t.Fatal(err)
	}
	for _, refusal := range refusals {
		for name, surface := range map[string]func(map[string]any) (map[string]any, string){"Swarm Control": control, "Orchestrator": orchestrator} {
			if out, refused := surface(refusal.args); refused == "" || !strings.Contains(refused, refusal.want) {
				t.Fatalf("%s %v: %v %q", name, refusal.args, out, refused)
			}
		}
	}
	// A linked machine's work cannot be raised from off either.
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleOff)
	off, _ := store.GetAccountPermissions(h.home.account)
	if _, refused := orchestrator(map[string]any{"action": "set_permissions", "expected_revision": float64(off.Revision), "client_request_id": "w3", "linked": map[string]any{"box": map[string]any{"work": "ask"}}}); !strings.Contains(refused, "only lower linked box work") {
		t.Fatalf("raise linked work from off: %q", refused)
	}
	if got, _ := store.GetAccountPermissions(h.home.account); got.Revision != off.Revision || got.Tasks != permissions.Tasks || got.Link("box").Work != pebblestore.RuleOff {
		t.Fatalf("a refused permissions change was stored: %+v -> %+v", permissions, got)
	}
	if got := mustLimits(t, store, h.home.account); got != limits {
		t.Fatalf("a refused limits change was stored: %+v -> %+v", limits, got)
	}
	if usage, refused := control(map[string]any{"action": "usage"}); refused != "" || !strings.Contains(mustJSON(t, usage), `"daily_cost_limit_usd":5`) {
		t.Fatalf("a refused usage limit change was stored: %v %s", usage, refused)
	}
	keysAfter, _ := h.home.s.security.ListScopedTokens(h.home.account)
	if len(keysAfter) != len(keysBefore) {
		t.Fatalf("a key was minted: %d -> %d", len(keysBefore), len(keysAfter))
	}
	// Narrowing still works: lower the spend limit.
	if _, refused := orchestrator(map[string]any{"action": "set_usage_limits", "daily_cost_limit_usd": float64(3)}); refused != "" {
		t.Fatalf("lower the spend limit: %s", refused)
	}
}

// Purpose:
//   - Requirement: Swarm Control lists manage_setup from the catalog: the
//     default listing has every read action and the change actions whose
//     swarm_* tool was in the default listing, tools=all adds the rest; each
//     action only for a key holding its level and route scopes (a read key
//     sees reads only and is refused a change before anything runs); a
//     relay client never reaches local-only setup; a key bound to an agent
//     sees none; the machine's POST /v3/setup-actions/{action} refuses keys,
//     secrets and permissions (never managed over a link).
//   - Threat/regression: a read key changing setup, a relayed client
//     reaching sign-in or key creation, or key or secret management over a
//     link.
//   - Boundary/authority: controlMCPCall.visibleTools/catalogTools/
//     callCatalogTool, catalogLevels, handleSetupActions.
//   - Narrowest layer: listing, the call gate and the linked route are
//     separate entries; each is called directly.
func TestManageSetupListingLevelsAndLinkedRoute(t *testing.T) {
	h := newCatalogParityHarness(t)
	listed := func(target string, scopes []string, relayLevels string, agent string) map[string]bool {
		t.Helper()
		req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, target, nil), h.home.user, h.home.account)
		req = requestWithScopedToken(req, &pebblestore.ScopedTokenRecord{ID: "k", Scopes: scopes, AgentName: agent})
		if relayLevels != "" {
			req.Header.Set(remote.ClientLevelsHeader, relayLevels)
		}
		c := &controlMCPCall{request: req, server: h.home.s}
		for _, candidate := range c.visibleTools() {
			if candidate.Name == "manage_setup" {
				out := map[string]bool{}
				for _, action := range candidate.InputSchema["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string) {
					out[action] = true
				}
				return out
			}
		}
		return nil
	}
	catalog, _ := tool.CatalogToolNamed("manage_setup")
	core := listed("/mcp", catalogParitySetupKey, "", "")
	all := listed("/mcp?tools=all", catalogParitySetupKey, "", "")
	for _, action := range catalog.Actions {
		if !all[action.Action] {
			t.Fatalf("tools=all lacks %s", action.Action)
		}
		if core[action.Action] != action.Listed(tool.CatalogProfileEssentials) {
			t.Fatalf("default listing %s = %v", action.Action, core[action.Action])
		}
		if action.Level == tool.CatalogLevelRead && !core[action.Action] {
			t.Fatalf("read %s is not in the default listing", action.Action)
		}
	}
	if core["set_usage_limits"] || core["set_permissions"] || core["request_update"] || !core["set_agent_model"] {
		t.Fatalf("default listing: %v", core)
	}
	read := listed("/mcp?tools=all", []string{"swarm:read", "sessions:read", "usage:read", "secrets:read"}, "", "")
	for action := range read {
		if a, _ := catalog.Action(action); a.Level != tool.CatalogLevelRead {
			t.Fatalf("a read key lists %s", action)
		}
	}
	if !read["models"] || !read["limits"] || !read["list_secrets"] {
		t.Fatalf("read key listing: %v", read)
	}
	if _, refused := h.controlTool(t, []string{"swarm:read", "sessions:read", "settings:write"}, "manage_setup", map[string]any{"action": "set_agent_model", "role": "all", "provider": "p", "model": "m"}); !strings.Contains(refused, "needs swarm:write") {
		t.Fatalf("read key set_agent_model: %q", refused)
	}
	if _, refused := h.controlTool(t, []string{"swarm:read", "swarm:write", "sessions:read"}, "manage_setup", map[string]any{"action": "set_agent_model", "role": "all", "provider": "p", "model": "m"}); !strings.Contains(refused, "settings:write") {
		t.Fatalf("key without settings:write: %q", refused)
	}
	relayed := listed("/mcp?tools=all", relayDeviceScopes, "swarm:read,swarm:write,swarm:approve,swarm:manage", "")
	for _, action := range catalog.Actions {
		if action.LocalOnly && relayed[action.Action] {
			t.Fatalf("a relay client lists local-only %s", action.Action)
		}
	}
	if bound := listed("/mcp?tools=all", catalogParitySetupKey, "", "support"); bound != nil {
		t.Fatalf("an agent-bound key lists manage_setup: %v", bound)
	}
	for _, action := range []string{"list_keys", "list_secrets", "permissions", "set_permissions", "revoke_key", "create_key"} {
		req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, SetupActionsPath+"/"+action, strings.NewReader(`{}`)), h.remote.user, h.remote.account)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.remote.s.handleSetupActions(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("linked %s: %d %s", action, rec.Code, rec.Body.String())
		}
	}
	req := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, SetupActionsPath+"/limits", strings.NewReader(`{}`)), h.remote.user, h.remote.account), &pebblestore.ScopedTokenRecord{ID: "s", Scopes: []string{"sessions:read"}})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.remote.s.handleSetupActions(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a sessions:read key read limits on the machine's route: %d %s", rec.Code, rec.Body.String())
	}
}

// Purpose:
//   - Requirement: no manage_setup answer holds a secret or key value, on
//     either surface: a stored provider API key, an AI key's token and a
//     sign-in token never appear in any read action's answer (names, ids,
//     scopes and expiry only), and setupScrub drops secret-shaped string
//     fields from any answer while keeping records.
//   - Threat/regression: a key or credential handed to an AI through the
//     catalog, or a future handler change leaking one.
//   - Boundary/authority: Server.ManageSetup over the swarm_* read
//     handlers with a real auth and key store, and setupScrub.
//   - Narrowest layer: every read action's answer, through both surfaces.
func TestManageSetupAnswersHoldNoSecret(t *testing.T) {
	h := newCatalogParityHarness(t)
	control, orchestrator := setupSurfaces(t, h)
	const apiKey = "fixture-provider-key-0123456789abcdef"
	const accessToken = "fixture-access-token-0123456789abcdef"
	if _, _, err := h.home.s.auth.UpsertCredential(auth.CredentialUpsertInput{AccountScopeID: h.home.account, Provider: "fixture-provider", Type: "api", APIKey: apiKey, Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.home.s.auth.UpsertCredential(auth.CredentialUpsertInput{AccountScopeID: h.home.account, Provider: "codex", Type: "oauth", AccessToken: accessToken, RefreshToken: accessToken + "-refresh", Active: true}); err != nil {
		t.Fatal(err)
	}
	token, _, err := h.home.s.security.CreateScopedToken("fixture-app", []string{"sessions:read"}, h.home.account, h.home.user, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{apiKey, accessToken, token}
	catalog, _ := tool.CatalogToolNamed("manage_setup")
	checked := 0
	for _, action := range catalog.Actions {
		if action.Level != tool.CatalogLevelRead {
			continue
		}
		for name, surface := range map[string]func(map[string]any) (map[string]any, string){"Swarm Control": control, "Orchestrator": orchestrator} {
			out, refused := surface(map[string]any{"action": action.Action, "include_revoked": true, "include_unready": true})
			text := mustJSON(t, out) + refused
			for _, secret := range secrets {
				if strings.Contains(text, secret) {
					t.Fatalf("%s %s answers a secret: %s", name, action.Action, text)
				}
			}
			checked++
		}
	}
	if listedKeys, refused := control(map[string]any{"action": "list_keys"}); refused != "" || !strings.Contains(mustJSON(t, listedKeys), "fixture-app") {
		t.Fatalf("list_keys lists no names: %v %s", listedKeys, refused)
	}
	if checked < 20 {
		t.Fatalf("checked %d answers", checked)
	}
	scrubbed := mustJSON(t, setupScrub(map[string]any{"key": "sk-live", "token": "t", "nested": []any{map[string]any{"api_key": "x", "name": "kept"}}, "revoked": true, "record": map[string]any{"key": map[string]any{"id": "tok_1"}}}))
	if strings.Contains(scrubbed, "sk-live") || strings.Contains(scrubbed, `"token"`) || strings.Contains(scrubbed, `"api_key"`) || !strings.Contains(scrubbed, "kept") || !strings.Contains(scrubbed, "tok_1") {
		t.Fatalf("setupScrub: %s", scrubbed)
	}
}

func mustLimits(t *testing.T, store *pebblestore.SessionStore, account string) pebblestore.Limits {
	t.Helper()
	limits, err := store.GetLimits(account)
	if err != nil {
		t.Fatal(err)
	}
	return limits
}
