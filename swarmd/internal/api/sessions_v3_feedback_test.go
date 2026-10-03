package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: the production V3 executor (not RunTurnWithOptions) must consume a
// note queued in flight at its next provider boundary, with no early receipt or
// automatic terminal restart. The real V3 store, tool dispatch and provider loop
// with a deterministic adapter are the narrowest hermetic layer proving this
// wiring, failed-step retry and message identity. Codex ToRequest verifies the
// actual transport input shape; this is not live-provider delivery evidence.
func TestSessionsV3LiveFeedbackBoundary(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancelled", "terminal"} {
		t.Run(outcome, func(t *testing.T) {
			server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
			runner := &sessionsV3RecordingProviderRunner{}
			providers := registry.New()
			providers.RegisterRunner(runner)
			server.providers = providers
			runtime := tool.NewRuntime(1)
			server.runner = runruntime.NewService(sessions, server.model, providers, runtime, nil, server.agents, nil, nil)
			if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{
				Name: "swarm", Mode: agentruntime.ModePrimary,
				Provider: "test-provider", Model: "test-model",
				RuntimeMode: pebblestore.AgentRuntimeModePlanAuto,
				Enabled: pebblestore.BoolPtr(true), Prompt: "Inspect only.",
				ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"list": {Enabled: pebblestore.BoolPtr(true)}}},
			}); err != nil {
				t.Fatal(err)
			}
			created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "feedback-create", "feedback", t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"})
			exec := newSessionV3Executor(server)
			job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: "feedback-run", EpochID: "epoch-00000000000000000001"}
			for _, status := range []string{sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning} {
				if _, err := exec.recordRunStatus(job, status, "", "session.assistant.started"); err != nil {
					t.Fatal(err)
				}
			}
			resolved, err := exec.resolveSessionV3Runtime(job)
			if err != nil {
				t.Fatal(err)
			}
			resolved.Session.Metadata = cloneSessionsV3Metadata(resolved.Session.Metadata)
			if resolved.Session.Metadata == nil {
				resolved.Session.Metadata = map[string]any{}
			}
			resolved.Session.Metadata["task_id"] = "feedback-task"
			// Keep a real read-only tool boundary without expanding authority.
			resolved.Tools = runruntime.FilterToolDefinitionsExcept(resolved.Tools, map[string]struct{}{"list": {}})
			baseReq, err := exec.sessionV3ProviderBaseRequest(job, resolved, []map[string]any{{"role": "user", "content": "Inspect once."}})
			if err != nil {
				t.Fatal(err)
			}
			cursor, err := exec.sessionV3FeedbackCursor(job.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			job.feedbackCursor = &cursor
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			const marker = "unique-live-feedback-token"
			messageID := ""
			calls := 0
			queue := func() error {
				args, _ := json.Marshal(map[string]any{"action": "send_message", "session_id": job.SessionID, "prompt": marker, "trigger": false, "client_request_id": "stable-feedback"})
				for i := 0; i < 2; i++ {
					output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{Principal: job.Principal, SessionID: "sender", PrimaryPath: resolved.Session.WorkspacePath}, tool.Call{Name: "manage-sessions", Arguments: string(args)})
					if err != nil {
						return err
					}
					var result map[string]any
					if err := json.Unmarshal([]byte(output), &result); err != nil {
						return err
					}
					id, _ := result["message_id"].(string)
					if id == "" || (i == 1 && (id != messageID || result["replayed"] != true)) || result["delivery_status"] != "unconfirmed" {
						return errors.New("feedback retry identity or queued status incorrect")
					}
					messageID = id
				}
				return nil
			}
			countReceipts := func() int {
				messages, err := sessions.ListSessionMessages(job.SessionID, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, message := range messages {
					if message.Metadata["source"] == "feedback_delivery" {
						count++
						if message.Metadata["message_id"] != messageID || message.Metadata["run_id"] != job.RunID || message.Metadata["incorporation_status"] != "unconfirmed" || message.Metadata["delivery_status"] != "delivered" {
							t.Fatalf("incorrect receipt: %+v", message)
						}
					}
				}
				return count
			}
			runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
				calls++
				input, _ := json.Marshal(codex.ToRequest(req).Input)
				if calls == 1 {
					if strings.Contains(string(input), marker) {
						return provideriface.Response{}, errors.New("feedback preceded queue")
					}
					if err := queue(); err != nil {
						return provideriface.Response{}, err
					}
					if outcome == "terminal" {
						return provideriface.Response{Text: "Done", StopReason: "stop"}, nil
					}
					return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "inspect", Name: "list", Arguments: `{"path":".","max_entries":1}`}}}, nil
				}
				if strings.Count(string(input), marker) != 1 {
					return provideriface.Response{}, errors.New("feedback absent or duplicated in Codex input")
				}
				messages, err := sessions.ListSessionMessages(job.SessionID, 0, 100)
				if err != nil {
					return provideriface.Response{}, err
				}
				for _, message := range messages {
					if message.Metadata["source"] == "feedback_delivery" {
						return provideriface.Response{}, errors.New("receipt emitted before consuming step succeeded")
					}
				}
				if outcome == "failure" && calls == 2 {
					return provideriface.Response{}, errors.New("injected step failure")
				}
				if outcome == "cancelled" {
					cancel()
					return provideriface.Response{}, context.Canceled
				}
				return provideriface.Response{Text: marker, StopReason: "stop"}, nil
			}
			runLoop := func() error {
				sink := newSessionV3DurableProgressSinkWithWriter(exec, job, func() {}, sessionsV3ReadLatencyNoopProgressWriter{})
				_, err := exec.runProviderToolLoop(ctx, job, resolved, runner, baseReq, sink, nil)
				closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
				defer closeCancel()
				closeErr := sink.CloseAndFlush(closeCtx)
				if err != nil {
					return err
				}
				return closeErr
			}
			err = runLoop()
			if outcome == "failure" || outcome == "cancelled" {
				if err == nil || countReceipts() != 0 {
					t.Fatalf("failed step acknowledged: %v", err)
				}
				if outcome == "failure" {
					// Explicit retry in the same run, not automatic task restart.
					if err := runLoop(); err != nil {
						t.Fatal(err)
					}
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := 1
			if outcome == "terminal" || outcome == "cancelled" {
				want = 0
			}
			if got := countReceipts(); got != want {
				t.Fatalf("receipts=%d want=%d", got, want)
			}
			messages, err := sessions.ListSessionMessages(job.SessionID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			notes := 0
			for _, message := range messages {
				if message.ID == messageID {
					notes++
				}
			}
			if notes != 1 {
				t.Fatalf("durable notes=%d, want one even after failure or termination", notes)
			}
			if outcome == "terminal" && calls != 1 {
				t.Fatal("terminal queue restarted execution")
			}
		})
	}
}
