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
	"swarm/packages/swarmd/internal/remote"
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

// Requirement: the AI the installer connects (a write AI key) can offer the
// person ChatGPT sign-in: swarm_connect_chatgpt is in its default tool list
// and callable, and status reads only the caller's own sign-in, never another
// principal's. A read key cannot call it, and a scoped token without Swarm
// Control levels still needs admin as the owner-only routes do. status
// wait_seconds returns as soon as the sign-in settles, woken by the sign-in
// store rather than polling. The default list for a key never names a tool its
// level cannot call (swarm_manage_worker and swarm_request_provider_key stay
// full-key tools). Threats: the box's own AI unable to connect a ChatGPT plan
// (it falls back to asking for an API key), a code leaked across principals
// or to a connection without authority, a waiting AI spinning on status, or a
// tool listed that always fails. Owners: controlMCPConnectChatGPT,
// controlMCPCall.chatGPTStatus, codexOAuthChanges, remote.ToolScope and
// controlMCPCall.visibleTools over handleCodexOAuthStatus. In-process HTTP
// through ContainerSDKHandler with real temporary token stores and seeded
// sign-in records is the narrowest layer that runs the level check, the tool's
// own authorization and the handler together; action start would contact
// OpenAI, so it is not run here.
func TestControlMCPWriteKeyConnectsChatGPT(t *testing.T) {
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	mint := func(scopes ...string) string {
		t.Helper()
		token, _, err := sec.CreateScopedToken("key", scopes, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	writeScopes, _ := aiKeyScopes("write")
	readScopes, _ := aiKeyScopes("read")
	fullScopes, _ := aiKeyScopes("full")
	writeKey, readKey, fullKey := mint(writeScopes...), mint(readScopes...), mint(fullScopes...)
	plain := mint("sessions:read", "sessions:write")

	now := time.Now()
	seed := func(id, userID, account string) {
		s.codexOAuthSet(id, &codexOAuthSession{UserID: userID, AccountScopeID: account, Provider: "codex", Method: "device", Active: true,
			Status: "waiting", VerificationURL: "https://example.test/device", UserCode: "ABCD-EFGH", ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now, UpdatedAt: now})
	}
	seed("login_mine", actor.UserID, actor.AccountScopeID)
	seed("login_other", "user-other", "account-other")

	write := controlMCPTestCaller(t, s, writeKey)
	if text, isErr := write("swarm_connect_chatgpt", map[string]any{"action": "status", "login_id": "login_mine"}); isErr || !strings.Contains(text, "ABCD-EFGH") || !strings.Contains(text, "tell_the_person") {
		t.Fatalf("write key status: %v %s", isErr, text)
	}
	if text, isErr := write("swarm_connect_chatgpt", map[string]any{"action": "status", "login_id": "login_other"}); !isErr || !strings.Contains(text, "HTTP 404") || strings.Contains(text, "ABCD") {
		t.Fatalf("write key read another principal's sign-in: %v %s", isErr, text)
	}
	if text, isErr := controlMCPTestCaller(t, s, readKey)("swarm_connect_chatgpt", map[string]any{"action": "status", "login_id": "login_mine"}); !isErr || !strings.Contains(text, "limited to") || strings.Contains(text, "ABCD") {
		t.Fatalf("read key reached sign-in: %v %s", isErr, text)
	}
	if text, isErr := controlMCPTestCaller(t, s, plain)("swarm_connect_chatgpt", map[string]any{"action": "status", "login_id": "login_mine"}); !isErr || !strings.Contains(text, "admin") || strings.Contains(text, "ABCD") {
		t.Fatalf("token without admin reached sign-in: %v %s", isErr, text)
	}
	if text, isErr := write("swarm_connect_chatgpt", map[string]any{"action": "status", "login_id": "login_mine", "wait_seconds": controlMCPChatGPTMaxWaitSeconds + 1}); !isErr {
		t.Fatalf("wait above the cap accepted: %s", text)
	}

	// status with wait_seconds returns when the sign-in settles.
	type result struct {
		text    string
		isErr   bool
		elapsed time.Duration
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		text, isErr := write("swarm_connect_chatgpt", map[string]any{"action": "status", "login_id": "login_mine", "wait_seconds": 20})
		done <- result{text, isErr, time.Since(start)}
	}()
	select {
	case got := <-done:
		t.Fatalf("wait returned while the sign-in was waiting: %s", got.text)
	case <-time.After(200 * time.Millisecond):
	}
	s.codexOAuthUpdate("login_mine", func(next *codexOAuthSession) { next.Status = "success" })
	select {
	case got := <-done:
		if got.isErr || !strings.Contains(got.text, `"status":"success"`) || got.elapsed > 5*time.Second {
			t.Fatalf("wait after success: %v %s %s", got.isErr, got.elapsed, got.text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not wake on the finished sign-in")
	}

	// The default list names only tools the key's level can call.
	listDefault := func(token string, levels []string) map[string]bool {
		t.Helper()
		r := httptest.NewRequest("POST", "http://127.0.0.1:7783/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		r.RemoteAddr = "192.0.2.1:40000"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ContainerSDKHandler().ServeHTTP(w, r)
		var out struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Result.Tools) == 0 {
			t.Fatalf("tools/list: %d %s", w.Code, w.Body.String())
		}
		names := map[string]bool{}
		for _, tool := range out.Result.Tools {
			if !controlMCPCoreTools[tool.Name] || !slices.Contains(levels, remote.ToolScope(tool.Name)) {
				t.Fatalf("default list for %v names %s", levels, tool.Name)
			}
			names[tool.Name] = true
		}
		return names
	}
	writeTools := listDefault(writeKey, writeScopes)
	if !writeTools["swarm_connect_chatgpt"] || writeTools["swarm_manage_worker"] || writeTools["swarm_request_provider_key"] {
		t.Fatalf("write key default list: %v", writeTools)
	}
	if listDefault(readKey, readScopes)["swarm_connect_chatgpt"] {
		t.Fatal("read key lists swarm_connect_chatgpt")
	}
	if fullTools := listDefault(fullKey, fullScopes); len(fullTools) != len(controlMCPCoreTools) {
		t.Fatalf("full key default list has %d of %d tools", len(fullTools), len(controlMCPCoreTools))
	}
}
