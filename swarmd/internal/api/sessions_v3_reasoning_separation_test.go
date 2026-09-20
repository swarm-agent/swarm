package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: reasoning summaries are not assistant output. The extraction
// boundary sessionV3ProviderStepAssistantText must reject reasoning-only responses
// (including tool calls) without suppressing or rewriting genuine text. A pure
// table test is the narrowest proof of source precedence and byte preservation.
func TestV3ProviderStepAssistantTextSeparatesReasoning(t *testing.T) {
	const summary = "Inspecting inputs\n\nChoosing a tool"
	calls := []provideriface.FunctionCall{{CallID: "call-read", Name: "read", Arguments: `{"path":"input.txt"}`}}
	for _, tc := range []struct {
		name     string
		response provideriface.Response
		streamed string
		want     string
	}{
		{name: "empty"},
		{name: "reasoning only", response: provideriface.Response{ReasoningSummary: summary}},
		{name: "reasoning with tool call", response: provideriface.Response{ReasoningSummary: summary, FunctionCalls: calls}},
		{name: "blank text with tool call", response: provideriface.Response{Text: " \n", ReasoningSummary: summary, FunctionCalls: calls}},
		{name: "streamed text wins", response: provideriface.Response{Text: "response", ReasoningSummary: summary}, streamed: "  héllo 🌍  ", want: "  héllo 🌍  "},
		{name: "response text wins", response: provideriface.Response{Text: "  response  ", ReasoningSummary: summary, AssistantMessages: []provideriface.AssistantMessage{{Text: "other"}}}, want: "  response  "},
		{name: "real pre-tool text", response: provideriface.Response{Text: "Reading inputs.", ReasoningSummary: summary, FunctionCalls: calls}, want: "Reading inputs."},
		{name: "real text equal to reasoning is retained", response: provideriface.Response{Text: summary, ReasoningSummary: summary}, want: summary},
		{name: "eligible assistant messages", response: provideriface.Response{ReasoningSummary: summary, AssistantMessages: []provideriface.AssistantMessage{
			{Text: "commentary", Phase: provideriface.AssistantPhaseCommentary},
			{Text: " \n", Phase: provideriface.AssistantPhaseFinalAnswer},
			{Text: "  first  ", Phase: provideriface.AssistantPhaseFinalAnswer},
			{Text: "second"},
		}}, want: "  first  \n\nsecond"},
		{name: "ineligible messages do not fall back to reasoning", response: provideriface.Response{ReasoningSummary: summary, AssistantMessages: []provideriface.AssistantMessage{{Text: "commentary", Phase: provideriface.AssistantPhaseCommentary}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionV3ProviderStepAssistantText(tc.response, tc.streamed); got != tc.want {
				t.Fatalf("assistant text = %q, want %q", got, tc.want)
			}
		})
	}
}

// Requirement: runProviderToolLoop must preserve reasoning events but must not
// pass their summary through EnsureResponseText/recordPreToolAssistantSegment or
// add it as assistant continuation input. This fake-provider, real-store test
// exercises those boundaries and a harmless read tool without a live provider.
// The positive case ensures genuine pre-tool commentary still persists exactly.
func TestV3ProviderToolLoopDoesNotDuplicateReasoning(t *testing.T) {
	for _, preToolText := range []string{"", "  Reading the input.  "} {
		name := "reasoning only"
		if preToolText != "" {
			name = "with assistant text"
		}
		t.Run(name, func(t *testing.T) {
			server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
			configureAssistantOrderTestProvider(t, server)
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte("fixture content"), 0o600); err != nil {
				t.Fatal(err)
			}
			const summary = "Inspecting inputs\n\nChoosing a tool\n\nPreparing a read"
			const finalText = "Finished reading."
			runner := installSessionsV3TestProvider(server, finalText)
			var continuation []map[string]any
			runner.handler = func(_ context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
				switch runner.callCount {
				case 1:
					for i, part := range strings.Split(summary, "\n\n") {
						onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, ReasoningKey: fmt.Sprintf("part-%d", i), Delta: part, DeltaMode: provideriface.StreamEventDeltaModeReplace})
					}
					onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventToolCallStarted, ToolCallID: "call-read", ToolName: "read"})
					return provideriface.Response{Text: preToolText, ReasoningSummary: summary, FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read", Name: "read", Arguments: `{"path":"input.txt"}`}}}, nil
				case 2:
					continuation = req.Input
					return provideriface.Response{Text: finalText, StopReason: "stop"}, nil
				default:
					return provideriface.Response{}, fmt.Errorf("unexpected provider call %d", runner.callCount)
				}
			}
			server.runner = runruntime.NewService(sessionSvc, server.model, server.providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
			server.SetBypassPermissions(true)
			created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "reasoning-separation-create", "reasoning separation", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
			exec := newSessionV3Executor(server)
			job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: "run-reasoning-separation", EpochID: "epoch-00000000000000000001"}
			if _, err := exec.recordRunStatus(job, sessionruntime.RunIntentPendingExecutor, "", "session.assistant.queued"); err != nil {
				t.Fatal(err)
			}
			if _, err := exec.recordRunStatus(job, sessionruntime.RunIntentRunning, "", "session.assistant.started"); err != nil {
				t.Fatal(err)
			}
			resolved, err := exec.resolveSessionV3Runtime(job)
			if err != nil {
				t.Fatal(err)
			}
			baseReq, err := exec.sessionV3ProviderBaseRequest(job, resolved, []map[string]any{{"role": "user", "content": "Read input.txt."}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sink := newSessionV3DurableProgressSink(exec, job, cancel)
			defer sink.CloseAndFlush(ctx)
			result, err := exec.runProviderToolLoop(ctx, job, resolved, runner, baseReq, sink, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := sink.CloseAndFlush(ctx); err != nil {
				t.Fatal(err)
			}
			if result.FinalContent != finalText || result.FinalStep != 2 || runner.callCount != 2 {
				t.Fatalf("unexpected final output: %q step=%d calls=%d", result.FinalContent, result.FinalStep, runner.callCount)
			}
			var assistantInputs int
			for _, item := range continuation {
				if item["role"] != "assistant" {
					continue
				}
				assistantInputs++
				if !sessionsV3TraceInputContains([]map[string]any{item}, preToolText) || preToolText == "" || sessionsV3TraceInputContains([]map[string]any{item}, summary) {
					t.Fatalf("unexpected assistant continuation: %+v", item)
				}
			}
			wantAssistant := 0
			if preToolText != "" {
				wantAssistant = 1
			}
			if assistantInputs != wantAssistant {
				t.Fatalf("assistant continuation count = %d, want %d", assistantInputs, wantAssistant)
			}
			messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			var assistantMessages, toolMessages int
			for _, message := range messages {
				switch message.Role {
				case "assistant":
					assistantMessages++
					if preToolText == "" || message.Content != preToolText {
						t.Fatalf("unexpected pre-tool assistant message: %q", message.Content)
					}
				case "tool":
					toolMessages++
					if !strings.Contains(message.Content, "fixture content") {
						t.Fatalf("read did not return fixture: %s", message.Content)
					}
				}
			}
			if assistantMessages != wantAssistant || toolMessages != 1 {
				t.Fatalf("message counts: assistant=%d tool=%d", assistantMessages, toolMessages)
			}
			events, err := sessionSvc.ListSessionEvents(created.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			var completedSummaries []string
			var assistantDeltas string
			for _, event := range events {
				var payload struct {
					Delta   string `json:"delta"`
					Summary string `json:"summary"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				switch event.EventType {
				case "session.reasoning.completed":
					completedSummaries = append(completedSummaries, payload.Summary)
				case "session.assistant.delta":
					assistantDeltas += payload.Delta
				}
			}
			if strings.Join(completedSummaries, "\n\n") != summary {
				t.Fatalf("reasoning was lost or duplicated: %q", completedSummaries)
			}
			if assistantDeltas != preToolText+finalText {
				t.Fatalf("assistant deltas = %q, want genuine text only", assistantDeltas)
			}
		})
	}
}
