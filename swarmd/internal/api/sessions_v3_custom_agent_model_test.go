package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

// Purpose: a custom (sealed) agent defined without a provider/model must run
// on the account's default Swarm model, the same Action assignment a plain
// session gets, instead of failing its first run with an empty provider/model.
// A model set on the agent profile still wins, and when no model was ever set
// up the run fails with a plain "no model is set up yet" error.
// Owner: createSessionsV3Primary (server-resolved default kept out of the
// request hash) and resolveSessionV3Capabilities (effective preference). The
// authenticated /v3/sessions handler plus the V3 executor is the narrowest
// layer where the stored agent profile, the account settings and the provider
// request meet.
func TestSessionsV3CustomAgentWithoutModelUsesAccountDefault(t *testing.T) {
	setup := func(t *testing.T, agentProvider, agentModel string) (*Server, *sessionruntime.Service, *[]provideriface.Request) {
		t.Helper()
		server, sessions, permissions, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		var mu sync.Mutex
		requests := []provideriface.Request{}
		runner := &sessionsV3RecordingProviderRunner{handler: func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
			mu.Lock()
			requests = append(requests, req)
			mu.Unlock()
			return provideriface.Response{Text: "ok", StopReason: "stop"}, nil
		}}
		providers := registry.New()
		providers.RegisterRunner(runner)
		server.providers = providers
		server.runner = runruntime.NewService(sessions, server.model, providers, tool.NewRuntime(1), permissions, server.agents, nil, nil)
		account := testPrincipal().AccountScopeID
		ctx := identity.ContextWithPrincipal(context.Background(), testPrincipal())
		if _, err := server.agentModelSettings.ReplaceSwarm(ctx, agentmodelsettings.SwarmInput{
			Action: pebblestore.AgentModelAssignment{Provider: "test-provider", Model: "account-default", Thinking: "low"},
			Plan:   pebblestore.AgentModelAssignment{Provider: "test-provider", Model: "account-plan", Thinking: "low"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := server.agents.PutCustomToolForAccount(account, pebblestore.AgentCustomToolDefinition{Name: "lookup_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "x", InputSchema: lookupOrderSchema, TimeoutMS: 2000}); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := server.agents.UpsertForAccount(account, agentruntime.UpsertInput{
			Name: "frontdesk", Mode: agentruntime.ModeSubagent, Provider: agentProvider, Model: agentModel,
			Enabled: pebblestore.BoolPtr(true), Prompt: "Answer order questions.",
			ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}}},
		}); err != nil {
			t.Fatal(err)
		}
		exec := newSessionV3Executor(server)
		exec.startDelay = 0
		server.v3SessionExecutor = exec
		return server, sessions, &requests
	}
	// The SDK sends no preference for a sealed agent session.
	create := func(t *testing.T, server *Server) pebblestore.SessionSnapshot {
		t.Helper()
		workspace := t.TempDir()
		bindingID := seedSessionsV3PrimaryAuthority(t, server, workspace)
		raw, err := json.Marshal(map[string]any{
			"client_request_id": "create-frontdesk", "workspace_path": workspace, "workspace_name": filepath.Base(workspace),
			"swarm_id": "host-swarm-id", "workspace_binding_id": bindingID, "target_kind": "host", "target_relationship": "self",
			"agent_name": "frontdesk",
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var created struct {
			Session pebblestore.SessionSnapshot `json:"session"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		return created.Session
	}

	t.Run("agent without a model runs on the account default", func(t *testing.T) {
		server, sessions, requests := setup(t, "", "")
		created := create(t, server)
		if created.Preference.Provider != "test-provider" || created.Preference.Model != "account-default" || created.Preference.Thinking != "low" {
			t.Fatalf("session preference = %+v, want the account Swarm Action model", created.Preference)
		}
		postSessionsV3PrimaryTestMessage(t, server, created.ID, "hello", "where is my order?")
		waitForSessionsV3RunIntentStatus(t, sessions, created.ID, sessionruntime.RunIntentCompleted)
		if len(*requests) == 0 || (*requests)[0].Model != "account-default" {
			t.Fatalf("provider requests = %+v, want a call on account-default", *requests)
		}
	})

	t.Run("a model on the agent profile wins", func(t *testing.T) {
		server, _, _ := setup(t, "test-provider", "agent-own")
		created := create(t, server)
		resolved, err := newSessionV3Executor(server).resolveSessionV3Capabilities(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID})
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Preference.Model != "agent-own" {
			t.Fatalf("resolved model = %q, want the agent's own model", resolved.Preference.Model)
		}
	})

	t.Run("no model set up at all fails plainly", func(t *testing.T) {
		server, _, _ := setup(t, "", "")
		empty, err := pebblestore.Open(filepath.Join(t.TempDir(), "empty-settings.pebble"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = empty.Close() })
		server.SetAgentModelSettingsService(agentmodelsettings.NewService(pebblestore.NewAgentModelSettingsStore(empty)))
		created := create(t, server)
		if created.Preference.Model != "" {
			t.Fatalf("session preference = %+v, want none when no model was set up", created.Preference)
		}
		_, err = newSessionV3Executor(server).resolveSessionV3Capabilities(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID})
		if !errors.Is(err, errSessionV3NoModel) {
			t.Fatalf("resolve error = %v, want %v", err, errSessionV3NoModel)
		}
	})
}

// Purpose: a session, including a sealed agent's, is always bound to a
// workspace (its grants, runtime binding and scope all come from it), so a
// create call without workspace_path on a Swarm that has no current workspace
// must fail with a plain, actionable error and create nothing. Owner:
// resolveSessionsV3PrimaryBinding through the authenticated /v3/sessions
// handler, the narrowest layer that shows the caller's error and the store.
func TestSessionsV3CreateWithoutWorkspaceFailsPlainly(t *testing.T) {
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspaces.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server.workspace = workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	account := testPrincipal().AccountScopeID
	if _, err := server.agents.PutCustomToolForAccount(account, pebblestore.AgentCustomToolDefinition{Name: "lookup_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "x", InputSchema: lookupOrderSchema, TimeoutMS: 2000}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := server.agents.UpsertForAccount(account, agentruntime.UpsertInput{
		Name: "frontdesk", Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(true), Prompt: "Answer order questions.",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}}},
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"no-workspace","agent_name":"frontdesk"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("pass workspace_path of a registered workspace")) {
		t.Fatalf("create = %d %s, want 400 with the plain workspace error", rec.Code, rec.Body.String())
	}
	if _, found, err := sessions.GetSession(stableSessionsV3PrimarySessionID(testPrincipal(), "no-workspace")); err != nil || found {
		t.Fatalf("session created without a workspace: found=%v err=%v", found, err)
	}
}
