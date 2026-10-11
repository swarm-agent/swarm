package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/notification"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

var lookupOrderSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"order_id": map[string]any{"type": "string", "pattern": "^[A-Z0-9]{6}$"},
	},
	"required":             []any{"order_id"},
	"additionalProperties": false,
}

// Purpose: a sealed agent's only capability is a client tool its gateway
// answers. Even with approvals bypassed, Swarm must (a) refuse arguments that
// break the tool's schema before the gateway sees them, (b) wait for the
// gateway instead of executing anything, (c) hand the gateway's result, its
// failure reason, or a timeout back to the model. (d) A gateway failure or
// timeout is a tool failure carrying the gateway's message, never a permission
// denial: the model sees a tool error, the durable session.tool.failed event
// carries the message, and no permission notification ("Permission denied: …")
// is raised, because the call waited on the app, not on a person.
func TestSessionsV3ClientToolIsAnsweredByTheClient(t *testing.T) {
	for _, outcome := range []string{"result", "failure", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			server, sessions, permissions, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
			permissions.SetBypassPermissions(true)
			notes := notification.NewService(pebblestore.NewNotificationStore(sessions.Store().Underlying()), nil, nil)
			notes.SetLocalSwarmIDResolver(func() string { return "swarm-local" })
			permissions.SetLocalSwarmIDResolver(func() string { return "swarm-local" })
			permissions.SetNotificationService(notes)
			runner := &sessionsV3RecordingProviderRunner{handler: func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
				if hasFunctionCallOutput(req.Input) {
					return provideriface.Response{Text: "done", StopReason: "stop"}, nil
				}
				return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{
					{CallID: "bad", Name: "lookup_order", Arguments: `{"order_id":"../../etc/passwd"}`},
					{CallID: "extra", Name: "lookup_order", Arguments: `{"order_id":"AB12CD","user_id":"someone-else"}`},
					{CallID: "good", Name: "lookup_order", Arguments: `{"order_id":"AB12CD"}`},
				}}, nil
			}}
			providers := registry.New()
			providers.RegisterRunner(runner)
			server.providers = providers
			server.runner = runruntime.NewService(sessions, server.model, providers, tool.NewRuntime(1), permissions, server.agents, nil, nil)
			account := testPrincipal().AccountScopeID
			timeout := 0
			if outcome == "timeout" {
				timeout = 300
			}
			if _, err := server.agents.PutCustomToolForAccount(account, pebblestore.AgentCustomToolDefinition{
				Name: "lookup_order", Kind: pebblestore.AgentCustomToolKindClient,
				Description: "Look up the visitor's order.", InputSchema: lookupOrderSchema, TimeoutMS: timeout,
			}); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := server.agents.UpsertForAccount(account, agentruntime.UpsertInput{
				Name: "frontdesk", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "test-model",
				Enabled: pebblestore.BoolPtr(true), Prompt: "Answer order questions.",
				ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}}},
			}); err != nil {
				t.Fatal(err)
			}
			exec := newSessionV3Executor(server)
			exec.startDelay = 0
			server.v3SessionExecutor = exec
			created := createSessionsV3TestSessionForAgent(t, server, "frontdesk", t.TempDir())
			postSessionsV3PrimaryTestMessage(t, server, created.ID, "client-tool-message", "where is my order AB12CD?")

			if outcome != "timeout" {
				record := waitForPendingClientToolCall(t, permissions, created.ID)
				if record.ToolName != "lookup_order" || !strings.Contains(record.ToolArguments, "AB12CD") {
					t.Fatalf("gateway saw the wrong call: %+v", record)
				}
				body := `{"action":"allow_once","approved_arguments":{"result":{"status":"shipped","eta":"Friday"}}}`
				if outcome == "failure" {
					body = `{"action":"deny","reason":"order not found for this visitor"}`
				}
				req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/permissions/"+record.ID+"/resolve", bytes.NewBufferString(body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				server.Handler().ServeHTTP(rec, withTestPrincipal(req))
				if rec.Code != http.StatusOK {
					t.Fatalf("resolve: %d %s", rec.Code, rec.Body.String())
				}
			}
			waitForSessionsV3RunIntentStatus(t, sessions, created.ID, sessionruntime.RunIntentCompleted)

			outputs := map[string]string{}
			for _, item := range runner.lastRequest.Input {
				if stringMapValue(item, "type") == "function_call_output" {
					outputs[stringMapValue(item, "call_id")] = stringMapValue(item, "output")
				}
			}
			for _, id := range []string{"bad", "extra"} {
				if !strings.Contains(outputs[id], "do not match the tool's schema") {
					t.Fatalf("%s: schema violation not refused: %q", id, outputs[id])
				}
			}
			want := map[string]string{"result": `"status":"shipped"`, "failure": "order not found for this visitor", "timeout": "client tool timed out"}[outcome]
			if !strings.Contains(outputs["good"], want) {
				t.Fatalf("model got %q, want it to contain %q", outputs["good"], want)
			}
			if outcome == "result" && !strings.Contains(outputs["good"], `"untrusted_content":true`) {
				t.Fatalf("client result not labelled untrusted: %q", outputs["good"])
			}
			// A sealed agent's system prompt is its own, not the coding harness.
			if strings.Contains(runner.lastRequest.Instructions, "Master harness prompt") || !strings.Contains(runner.lastRequest.Instructions, "Answer order questions.") || len(runner.lastRequest.Instructions) > 2000 {
				t.Fatalf("sealed agent instructions: %d bytes: %.200q", len(runner.lastRequest.Instructions), runner.lastRequest.Instructions)
			}
			records, err := permissions.ListPermissions(created.ID, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 {
				t.Fatalf("only the schema-valid call may reach the gateway; records=%d", len(records))
			}
			notifications, err := notes.ListNotificationsForAccount(account, "swarm-local", 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(notifications) != 0 {
				t.Fatalf("client tool call raised person-facing notifications: %+v", notifications)
			}
			if outcome == "result" {
				return
			}
			if strings.Contains(outputs["good"], "permission") || strings.Contains(outputs["good"], "denied") || !strings.Contains(outputs["good"], "tool.client.v1") {
				t.Fatalf("client failure reached the model as a permission denial: %q", outputs["good"])
			}
			replay, err := sessions.ReplaySessionEvents(created.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			failed := false
			for _, event := range replay.Events {
				var payload map[string]any
				if json.Unmarshal(event.Payload, &payload) != nil || payload["call_id"] != "good" || event.EventType == "session.tool.started" {
					continue
				}
				if event.EventType != "session.tool.failed" {
					continue
				}
				failed = true
				if payload["error"] != want {
					t.Fatalf("tool failure error = %#v, want %q", payload["error"], want)
				}
				if output, _ := payload["output"].(string); !strings.Contains(output, want) || strings.Contains(output, `"permission"`) {
					t.Fatalf("tool failure output = %q", output)
				}
			}
			if !failed {
				t.Fatal("client tool failure was not recorded as session.tool.failed")
			}
		})
	}
}

// Purpose: client tool definitions are validated when saved, and no custom
// tool may take a built-in name (the runtime would run the built-in).
func TestClientToolDefinitionsAreValidated(t *testing.T) {
	server, sessions, permissions, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	server.runner = runruntime.NewService(sessions, server.model, nil, tool.NewRuntime(1), permissions, server.agents, nil, nil)
	handler := server.Handler()
	put := func(name, body string) (int, string) {
		req := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/v2/custom-tools/"+name, bytes.NewBufferString(body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	schema, _ := json.Marshal(lookupOrderSchema)
	for _, tc := range []struct{ name, body, want string }{
		{"bash", `{"kind":"client","input_schema":` + string(schema) + `}`, "built-in tool"},
		{"read", `{"kind":"fixed_bash","command":"cat /etc/shadow"}`, "built-in tool"},
		{"ask_user", `{"kind":"client","input_schema":` + string(schema) + `}`, "built-in tool"},
		{"lookup", `{"kind":"client","command":"curl x","input_schema":` + string(schema) + `}`, "take no command"},
		{"lookup", `{"kind":"client"}`, "input_schema is required"},
		{"lookup", `{"kind":"client","input_schema":{"type":"string"}}`, "must have"},
		{"lookup", `{"kind":"client","input_schema":{"type":"object","properties":{"a":{"$ref":"https://example.com/s.json"}}}}`, "local $ref"},
		{"lookup", `{"kind":"http","input_schema":` + string(schema) + `}`, "kind is required"},
	} {
		code, body := put(tc.name, tc.body)
		if code != http.StatusBadRequest || !strings.Contains(body, tc.want) {
			t.Fatalf("%s %s: %d %s (want 400 containing %q)", tc.name, tc.body, code, body, tc.want)
		}
	}
	if code, body := put("lookup_order", `{"kind":"client","effect":"write","timeout_ms":999999,"input_schema":`+string(schema)+`}`); code != http.StatusOK {
		t.Fatalf("valid client tool: %d %s", code, body)
	}
	stored, ok, err := server.agents.GetCustomToolForAccount(testPrincipal().AccountScopeID, "lookup_order")
	if err != nil || !ok || stored.Effect != "write" || stored.TimeoutMS != pebblestore.AgentClientToolMaxTimeoutMS {
		t.Fatalf("stored = %+v ok=%v err=%v", stored, ok, err)
	}
}

func waitForPendingClientToolCall(t *testing.T, permissions interface {
	ListPending(string, int) ([]pebblestore.PermissionRecord, error)
}, sessionID string) pebblestore.PermissionRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := permissions.ListPending(sessionID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) > 0 {
			return pending[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no pending client tool call")
	return pebblestore.PermissionRecord{}
}
