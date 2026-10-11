package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: the box-setup tools let an AI that runs this box define custom
// agents, list them, start sessions as them, and mint keys for the client apps
// it builds, without widening what those things may do. A defined agent is
// always an enabled sub-agent with exactly the chosen tool set and no model of
// its own (it runs on the account default); built-in and system names are
// refused with nothing written; start_session admits only existing, enabled
// custom sub-agents; a client key carries exactly sessions:read and
// sessions:write and never outlives the key that minted it (lifetime capped,
// refused once that key is revoked). Threats: an AI overwriting a system agent
// or the Swarm agent, a custom agent pinned to a hardcoded model, a client key
// minted with broader scopes or an odd lifetime, a client key that keeps
// working after the owner revokes the AI key, a session started as an unknown
// agent.
// Owners: controlMCPDefineAgent, controlMCPListAgents,
// controlMCPCall.customAgent, controlMCPStartSession and
// controlMCPCreateClientKey over the PUT/GET /v2/agents and POST
// /v3/auth/tokens handlers. In-process HTTP through ContainerSDKHandler with
// real temporary agent and token stores is the narrowest layer that runs the
// tool, route allowlist, handler scope checks and storage together; it runs
// no provider and no ChatGPT sign-in.
func TestControlMCPBoxSetupTools(t *testing.T) {
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "agents"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	// Compiled defaults (swarm and the system agents) exist, as on a real box.
	s.agents = agentruntime.NewService(pebblestore.NewAgentStore(db), events)
	if err := s.agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	settingsStore := pebblestore.NewAgentModelSettingsStore(db)
	if _, err := settingsStore.PutForAccount(testAgentModelSettingsRecord(actor.AccountScopeID)); err != nil {
		t.Fatal(err)
	}
	s.SetAgentModelSettingsService(agentmodelsettings.NewService(settingsStore), settingsStore)
	profiles := func() map[string]pebblestore.AgentProfile {
		t.Helper()
		state, err := s.agents.ListStateForAccount(actor.AccountScopeID, 200)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]pebblestore.AgentProfile{}
		for _, profile := range state.Profiles {
			out[profile.Name] = profile
		}
		return out
	}
	mintFor := func(lifetime time.Duration, scopes ...string) (string, string) {
		t.Helper()
		token, record, err := sec.CreateScopedToken("ai", scopes, actor.AccountScopeID, actor.UserID, lifetime, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return token, record.ID
	}
	mint := func(scopes ...string) string { token, _ := mintFor(time.Hour, scopes...); return token }
	call := controlMCPTestCaller(t, s, mint("admin"))

	// Built-in and system names are refused before anything is written.
	before := profiles()
	for _, name := range []string{"swarm", "system-coder", "system-router", "system-orchestrator"} {
		if text, isErr := call("swarm_define_agent", map[string]any{"name": name, "instructions": "take over", "tools": "build"}); !isErr || !strings.Contains(text, "built-in agent") {
			t.Fatalf("defined built-in %s: %v %s", name, isErr, text)
		}
	}
	for _, args := range []map[string]any{
		{"name": "Bad Name", "instructions": "x", "tools": "build"},
		{"name": "token", "instructions": "x", "tools": "build"},
		{"name": "helper", "instructions": "x", "tools": "everything"},
	} {
		if text, isErr := call("swarm_define_agent", args); !isErr {
			t.Fatalf("accepted %v: %s", args, text)
		}
	}
	if after := profiles(); len(after) != len(before) {
		t.Fatalf("refused definitions wrote profiles: %d -> %d", len(before), len(after))
	}

	// A build agent: enabled sub-agent, no model, exactly the build tools.
	if text, isErr := call("swarm_define_agent", map[string]any{"name": "builder", "description": "Builds apps", "instructions": "Build what is asked.", "tools": "build"}); isErr {
		t.Fatalf("define builder: %s", text)
	}
	builder := profiles()["builder"]
	if builder.Mode != agentruntime.ModeSubagent || !builder.Enabled || builder.Provider != "" || builder.Model != "" || builder.Prompt != "Build what is asked." {
		t.Fatalf("builder profile = %+v", builder)
	}
	if builder.ToolContract == nil || builder.ToolContract.Preset != "custom" || len(builder.ToolContract.Tools) != len(controlMCPBuildAgentTools) {
		t.Fatalf("builder tools = %+v", builder.ToolContract)
	}
	for _, name := range controlMCPBuildAgentTools {
		if cfg, ok := builder.ToolContract.Tools[name]; !ok || cfg.Enabled == nil || !*cfg.Enabled {
			t.Fatalf("builder misses %s", name)
		}
	}
	for _, name := range []string{"task", "manage_agent", "manage_workers", "manage_sessions"} {
		if _, ok := builder.ToolContract.Tools[name]; ok {
			t.Fatalf("builder got %s", name)
		}
	}
	if text, isErr := call("swarm_define_agent", map[string]any{"name": "reviewer", "instructions": "Review.", "tools": "read_only"}); isErr {
		t.Fatalf("define reviewer: %s", text)
	}
	if reviewer := profiles()["reviewer"]; reviewer.ToolContract == nil || reviewer.ToolContract.Preset != "read_only" || len(reviewer.ToolContract.Tools) != 0 {
		t.Fatalf("reviewer tools = %+v", reviewer.ToolContract)
	}

	// list_agents shows custom sub-agents next to the built-in names only.
	text, isErr := call("swarm_list_agents", map[string]any{})
	var listed struct {
		BuiltIn []string `json:"built_in"`
		Custom  []struct {
			Name string `json:"name"`
		} `json:"custom"`
	}
	if isErr || json.Unmarshal([]byte(text), &listed) != nil {
		t.Fatalf("list_agents: %v %s", isErr, text)
	}
	var custom []string
	for _, agent := range listed.Custom {
		custom = append(custom, agent.Name)
	}
	slices.Sort(custom)
	if !slices.Equal(custom, []string{"builder", "reviewer"}) || !slices.Equal(listed.BuiltIn, controlMCPSessionAgents) {
		t.Fatalf("list_agents = %s", text)
	}

	// start_session admits a custom agent (admission runs before the
	// workspace check) and refuses unknown, system and disabled agents.
	if text, _ := call("swarm_start_session", map[string]any{"agent": "builder", "prompt": "hi"}); !strings.Contains(text, "workspace_path is required") {
		t.Fatalf("custom agent not admitted: %s", text)
	}
	if _, _, _, err := s.agents.UpsertForAccount(actor.AccountScopeID, agentruntime.UpsertInput{Name: "reviewer", Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(false), Prompt: "Review."}); err != nil {
		t.Fatal(err)
	}
	for agent, want := range map[string]string{"nobody": "not available", "system-router": "not a session agent", "system-compact": "not a session agent", "reviewer": "disabled", "../x": "not a session agent"} {
		if text, isErr := call("swarm_start_session", map[string]any{"agent": agent, "workspace_path": "/project", "prompt": "hi"}); !isErr || !strings.Contains(text, want) {
			t.Fatalf("agent %s: want %q, got %v %s", agent, want, isErr, text)
		}
	}

	// Client keys: sessions scopes only, chosen lifetime, shown once, bound to
	// the minting key.
	for _, args := range []map[string]any{{"name": "ui", "days": 2}, {"name": ""}} {
		if text, isErr := call("swarm_create_client_key", args); !isErr {
			t.Fatalf("minted with %v: %s", args, text)
		}
	}
	createKey := func(caller func(string, map[string]any) (string, bool)) (string, *pebblestore.ScopedTokenRecord) {
		t.Helper()
		text, isErr := caller("swarm_create_client_key", map[string]any{"name": "my-ui", "days": 7})
		var created struct {
			Key    string `json:"key"`
			Record struct {
				Scopes []string `json:"scopes"`
			} `json:"record"`
		}
		if isErr || json.Unmarshal([]byte(text), &created) != nil || !strings.HasPrefix(created.Key, "swk_") {
			t.Fatalf("create_client_key: %v %s", isErr, text)
		}
		record, err := sec.ValidateScopedToken(created.Key)
		if err != nil || record == nil {
			t.Fatalf("minted key does not validate: %v", err)
		}
		if !slices.Equal(record.Scopes, controlMCPClientKeyScopes) || !slices.Equal(created.Record.Scopes, controlMCPClientKeyScopes) || record.AccountScopeID != actor.AccountScopeID {
			t.Fatalf("client key record = %+v", record)
		}
		return created.Key, record
	}
	// A key minted by a one-hour key lasts at most that hour.
	if _, short := createKey(call); time.Until(time.UnixMilli(short.ExpiresAt)) > time.Hour {
		t.Fatalf("client key outlives its one-hour minting key: expires %d", short.ExpiresAt)
	}
	longKey, longID := mintFor(30*24*time.Hour, "admin")
	clientKey, record := createKey(controlMCPTestCaller(t, s, longKey))
	if left := time.Until(time.UnixMilli(record.ExpiresAt)); left < 6*24*time.Hour || left > 8*24*time.Hour || record.ParentTokenID != longID {
		t.Fatalf("client key lifetime = %s, parent %q; want 7 days under %q", left, record.ParentTokenID, longID)
	}
	// The key reaches the session routes only, on every listener: a full AI
	// key must not escape /mcp by minting it. Regression found in review: an
	// unmarked sessions key reached principal-only handlers (host actions,
	// model settings, vault) and every /mcp tool.
	for _, h := range []http.Handler{s.Handler(), s.ContainerSDKHandler()} {
		for _, route := range []struct{ method, path, body string }{
			{"POST", "/v1/workspace/actions/run", `{"action_id":"x"}`},
			{"POST", "/v1/model", `{"provider":"codex","model":"x"}`},
			{"PATCH", "/v1/agent-model-settings", `{}`},
			{"POST", "/v1/vault/export", `{"password":"attacker-password"}`},
			{"POST", "/v3/auth/tokens", `{"name":"x","scopes":["admin"]}`},
			{"PUT", "/v2/agents/evil", `{"mode":"subagent","prompt":"x"}`},
			{"GET", "/v1/workspace/list", ""},
			{"POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		} {
			r := httptest.NewRequest(route.method, "http://127.0.0.1:7783"+route.path, strings.NewReader(route.body))
			r.RemoteAddr = "192.0.2.1:40000"
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+clientKey)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "client app keys reach only session routes") {
				t.Fatalf("client key reached %s %s: %d %s", route.method, route.path, w.Code, w.Body.String())
			}
		}
		r := httptest.NewRequest("GET", "http://127.0.0.1:7783/v3/sessions", nil)
		r.RemoteAddr = "192.0.2.1:40000"
		r.Header.Set("Authorization", "Bearer "+clientKey)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
			t.Fatalf("client key refused on its own session route: %d %s", w.Code, w.Body.String())
		}
	}
	if _, ok := profiles()["evil"]; ok {
		t.Fatal("client key defined an agent")
	}
	// Revoking the minting key ends the client key on every listener.
	if _, err := sec.RevokeScopedToken(actor.AccountScopeID, longID); err != nil {
		t.Fatal(err)
	}
	if record, err := sec.ValidateScopedToken(clientKey); err == nil || record != nil {
		t.Fatalf("client key outlived its revoked minting key: %+v", record)
	}
	for _, h := range []http.Handler{s.Handler(), s.ContainerSDKHandler()} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:7783/v3/sessions", nil)
		r.RemoteAddr = "192.0.2.1:40000"
		r.Header.Set("Authorization", "Bearer "+clientKey)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("client key of a revoked key reached sessions: %d %s", w.Code, w.Body.String())
		}
	}

	// Redefining an agent clears a model pinned on an earlier profile, so
	// custom agents always run on the account default model.
	if _, _, _, err := s.agents.UpsertForAccount(actor.AccountScopeID, agentruntime.UpsertInput{Name: "pinned", Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(true), Prompt: "x",
		Provider: "codex", Model: "pinned-model", Thinking: "high", ProviderSet: true, ModelSet: true, ThinkingSet: true, ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}}); err != nil {
		t.Fatal(err)
	}
	if text, isErr := call("swarm_define_agent", map[string]any{"name": "pinned", "instructions": "Now unpinned.", "tools": "read_only"}); isErr {
		t.Fatalf("redefine pinned: %s", text)
	}
	if pinned := profiles()["pinned"]; pinned.Provider != "" || pinned.Model != "" || pinned.Thinking != "" || pinned.Prompt != "Now unpinned." {
		t.Fatalf("pin survived redefinition: %+v", pinned)
	}

	// Without admin the handlers refuse agent writes and minting.
	plain := controlMCPTestCaller(t, s, mint("sessions:read", "sessions:write", "agents:read"))
	if text, isErr := plain("swarm_define_agent", map[string]any{"name": "sneaky", "instructions": "x", "tools": "build"}); !isErr || !strings.Contains(text, "HTTP 403") {
		t.Fatalf("plain token defined an agent: %s", text)
	}
	if text, isErr := plain("swarm_create_client_key", map[string]any{"name": "sneaky"}); !isErr || !strings.Contains(text, "HTTP 403") {
		t.Fatalf("plain token minted a key: %s", text)
	}
	if _, ok := profiles()["sneaky"]; ok {
		t.Fatal("refused definition was written")
	}
	if text, isErr := call("swarm_connect_chatgpt", map[string]any{"action": "status"}); !isErr || !strings.Contains(text, "needs login_id") {
		t.Fatalf("status without login_id: %s", text)
	}
}
