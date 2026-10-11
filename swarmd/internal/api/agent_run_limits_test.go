package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: a public sealed agent must never let one message run unbounded,
// whatever the model does. A model that calls tools forever stops after
// MaxSteps model calls (the last one offered no tools); every call carries the
// output-token cap; a model that hangs is cut off by the run deadline; and only
// the newest MaxHistoryMessages are sent, as a fresh provider context.
func TestSealedAgentRunLimits(t *testing.T) {
	setup := func(t *testing.T, limits *pebblestore.AgentRunLimits, handler func(context.Context, provideriface.Request) (provideriface.Response, error)) (*Server, *sessionruntime.Service, pebblestore.SessionSnapshot, *[]provideriface.Request) {
		server, sessions, permissions, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		var mu sync.Mutex
		requests := []provideriface.Request{}
		runner := &sessionsV3RecordingProviderRunner{handler: func(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
			mu.Lock()
			requests = append(requests, req)
			mu.Unlock()
			return handler(ctx, req)
		}}
		providers := registry.New()
		providers.RegisterRunner(runner)
		server.providers = providers
		server.runner = runruntime.NewService(sessions, server.model, providers, tool.NewRuntime(1), permissions, server.agents, nil, nil)
		account := testPrincipal().AccountScopeID
		if _, err := server.agents.PutCustomToolForAccount(account, pebblestore.AgentCustomToolDefinition{Name: "lookup_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "x", InputSchema: lookupOrderSchema, TimeoutMS: 2000}); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := server.agents.UpsertForAccount(account, agentruntime.UpsertInput{
			Name: "frontdesk", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "test-model",
			Enabled: pebblestore.BoolPtr(true), Prompt: "Answer order questions.", Limits: limits,
			ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}}},
		}); err != nil {
			t.Fatal(err)
		}
		exec := newSessionV3Executor(server)
		exec.startDelay = 0
		server.v3SessionExecutor = exec
		created := createSessionsV3TestSessionForAgent(t, server, "frontdesk", t.TempDir())
		// Answer every client tool call so the model can keep looping.
		go func() {
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				pending, _ := permissions.ListPending(created.ID, 10)
				for _, record := range pending {
					_, _ = permissions.ResolveWithArguments(created.ID, record.ID, "allow_once", "", `{"result":{"status":"shipped"}}`)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
		return server, sessions, created, &requests
	}

	t.Run("defaults stop a model that never stops calling tools", func(t *testing.T) {
		server, sessions, created, requests := setup(t, nil, func(_ context.Context, req provideriface.Request) (provideriface.Response, error) {
			return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "c", Name: "lookup_order", Arguments: `{"order_id":"AB12CD"}`}}}, nil
		})
		postSessionsV3PrimaryTestMessage(t, server, created.ID, "loop", "where is my order?")
		waitForSessionsV3RunIntentStatus(t, sessions, created.ID, sessionruntime.RunIntentFailed)
		if got := len(*requests); got != sealedAgentDefaultLimits.MaxSteps {
			t.Fatalf("model calls = %d, want %d", got, sealedAgentDefaultLimits.MaxSteps)
		}
		for i, req := range *requests {
			if req.MaxOutputTokens != sealedAgentDefaultLimits.MaxOutputTokens {
				t.Fatalf("call %d MaxOutputTokens = %d", i, req.MaxOutputTokens)
			}
		}
		if last := (*requests)[len(*requests)-1]; len(last.Tools) != 0 || last.ToolChoice != "none" {
			t.Fatalf("last allowed call still offered tools: %d %q", len(last.Tools), last.ToolChoice)
		}
	})

	t.Run("run deadline cuts off a hanging model", func(t *testing.T) {
		server, sessions, created, _ := setup(t, &pebblestore.AgentRunLimits{RunTimeoutMS: 300}, func(ctx context.Context, _ provideriface.Request) (provideriface.Response, error) {
			<-ctx.Done()
			return provideriface.Response{}, ctx.Err()
		})
		started := time.Now()
		postSessionsV3PrimaryTestMessage(t, server, created.ID, "hang", "hello")
		waitForSessionsV3RunIntentStatus(t, sessions, created.ID, sessionruntime.RunIntentFailed)
		if elapsed := time.Since(started); elapsed > 10*time.Second {
			t.Fatalf("hanging run lasted %s", elapsed)
		}
	})

	t.Run("only recent history is sent, as a fresh context", func(t *testing.T) {
		server, sessions, created, requests := setup(t, &pebblestore.AgentRunLimits{MaxHistoryMessages: 4}, func(_ context.Context, req provideriface.Request) (provideriface.Response, error) {
			return provideriface.Response{Text: "ok", StopReason: "stop"}, nil
		})
		for i, text := range []string{"first secret-alpha", "second", "third", "fourth"} {
			postSessionsV3PrimaryTestMessage(t, server, created.ID, "h"+string(rune('a'+i)), text)
			waitForSessionsV3MessageCount(t, sessions, created.ID, 2*(i+1))
		}
		last := (*requests)[len(*requests)-1]
		raw, _ := json.Marshal(last.Input)
		if strings.Contains(string(raw), "secret-alpha") || !strings.Contains(string(raw), "fourth") {
			t.Fatalf("history not trimmed: %s", raw)
		}
		if !last.ForceFreshProviderContext || last.AllowContinuation {
			t.Fatalf("bounded history must not continue a stored provider chain: %+v", last)
		}
	})
}
