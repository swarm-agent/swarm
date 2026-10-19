package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

func TestV3StepReplaysAnIncompleteStreamOnlyWhenNothingDurableWouldRepeat(t *testing.T) {
	// Purpose:
	// - Requirement: in the V3 executor step loop (the path delegated Coder
	//   jobs run on), a provider attempt that fails with
	//   provideriface.ErrStreamIncomplete is replayed once with the identical
	//   request when it left no assistant text and no provider tool-construction
	//   record on the step's durable stream; the cut-off attempt's function
	//   calls are never executed; the decision is always written as a durable
	//   session.run.recovery.stream_incomplete event; a second incomplete
	//   stream, durable partial output, or any other error ends the run without
	//   a replay.
	// - Threat/regression: one truncated Vertex stream killed 1 of 10 Coder jobs
	//   in two qualification runs. A careless replay would instead run a
	//   half-streamed edit, append the replacement after durable partial text,
	//   leave an orphaned tool-construction record, retry ordinary errors, or
	//   replay without any trace in session events.
	// - Boundary/authority: sessionV3Executor.runProviderToolLoop,
	//   replayIncompleteProviderStream, sessionV3ProviderStreamState and the
	//   ApplySessionMutation event log.
	// - Narrowest test layer: the real executor over a temporary store with a
	//   scripted provider; adapter tagging is covered in package google.
	incomplete := fmt.Errorf("vertex %w", provideriface.ErrStreamIncomplete)
	placeOrder := provideriface.FunctionCall{CallID: "call-1", Name: "place_order", Arguments: `{"item":"x"}`}
	toolStart := provideriface.StreamEvent{Type: provideriface.StreamEventToolCallStarted, ToolCallID: "call-1", ToolName: "place_order", ToolCallIndex: new(int)}

	for _, tc := range []struct {
		name      string
		attempts  []func(func(provideriface.StreamEvent)) (provideriface.Response, error)
		calls     int
		status    string
		decision  string // "" means no stream_incomplete record
		wantReply string
	}{
		{
			name: "reasoning only is replayed",
			attempts: []func(func(provideriface.StreamEvent)) (provideriface.Response, error){
				func(emit func(provideriface.StreamEvent)) (provideriface.Response, error) {
					emit(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, Delta: "thinking", DeltaMode: provideriface.StreamEventDeltaModeReplace, ReasoningKey: "google-thinking"})
					return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{placeOrder}}, incomplete
				},
				func(emit func(provideriface.StreamEvent)) (provideriface.Response, error) {
					emit(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "final answer"})
					return provideriface.Response{Text: "final answer", StopReason: "stop"}, nil
				},
			},
			calls: 2, status: sessionruntime.RunIntentCompleted, decision: sessionV3StreamIncompleteReplayed, wantReply: "final answer",
		},
		{
			name: "durable answer text is not replayed",
			attempts: []func(func(provideriface.StreamEvent)) (provideriface.Response, error){
				func(emit func(provideriface.StreamEvent)) (provideriface.Response, error) {
					emit(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "half an ans"})
					return provideriface.Response{}, incomplete
				},
			},
			calls: 1, status: sessionruntime.RunIntentFailed, decision: sessionV3StreamIncompleteOutputDurable,
		},
		{
			name: "durable tool construction is not replayed",
			attempts: []func(func(provideriface.StreamEvent)) (provideriface.Response, error){
				func(emit func(provideriface.StreamEvent)) (provideriface.Response, error) {
					emit(toolStart)
					return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{placeOrder}}, incomplete
				},
			},
			calls: 1, status: sessionruntime.RunIntentFailed, decision: sessionV3StreamIncompleteOutputDurable,
		},
		{
			name: "a second incomplete stream is terminal",
			attempts: []func(func(provideriface.StreamEvent)) (provideriface.Response, error){
				func(func(provideriface.StreamEvent)) (provideriface.Response, error) {
					return provideriface.Response{}, incomplete
				},
				func(func(provideriface.StreamEvent)) (provideriface.Response, error) {
					return provideriface.Response{}, incomplete
				},
			},
			calls: 2, status: sessionruntime.RunIntentFailed, decision: sessionV3StreamIncompleteReplayed,
		},
		{
			name: "other errors are not replayed",
			attempts: []func(func(provideriface.StreamEvent)) (provideriface.Response, error){
				func(func(provideriface.StreamEvent)) (provideriface.Response, error) {
					return provideriface.Response{}, errors.New("vertex streamGenerateContent failed status=400")
				},
			},
			calls: 1, status: sessionruntime.RunIntentFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "stream-incomplete"), nil)
			defer f.close()
			f.runner.handler = func(_ context.Context, _ provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
				f.runner.mu.Lock()
				call := f.runner.callCount
				f.runner.mu.Unlock()
				if call > len(tc.attempts) {
					t.Errorf("unexpected provider attempt %d", call)
					return provideriface.Response{}, errors.New("unexpected attempt")
				}
				return tc.attempts[call-1](onEvent)
			}
			sessionID := f.createSession("stream-incomplete")
			body, _ := json.Marshal(map[string]any{"client_request_id": "msg-1", "role": "user", "content": "order x"})
			if code, out := f.do(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", string(body)); code != http.StatusOK {
				t.Fatalf("message: %d %s", code, out)
			}
			f.waitRun(sessionID, stableSessionsV3PrimaryRunID(sessionID, "msg-1"), tc.status)

			if got := f.callCount(); got != tc.calls {
				t.Fatalf("provider attempts = %d, want %d", got, tc.calls)
			}
			if tc.calls == 2 {
				first, _ := json.Marshal(f.runner.requests[0].Input)
				second, _ := json.Marshal(f.runner.requests[1].Input)
				if string(first) != string(second) || f.runner.requests[0].Instructions != f.runner.requests[1].Instructions {
					t.Fatal("the replay did not resend the identical request")
				}
			}
			// place_order is a write client tool: dispatching it would open a
			// confirmation or leave a pending client call.
			if confirmation := f.confirmation(sessionID); confirmation.ID != "" {
				t.Fatalf("the cut-off attempt's tool call was dispatched: %+v", confirmation)
			}
			if pending := f.pendingClientCalls(sessionID); len(pending) != 0 {
				t.Fatalf("the cut-off attempt's tool call was dispatched: %+v", pending)
			}
			if tc.wantReply != "" {
				if got := f.lastReply(sessionID); got != tc.wantReply {
					t.Fatalf("reply = %q, want only the replacement's %q", got, tc.wantReply)
				}
			}
			events, err := f.sessions.ListSessionEvents(sessionID, 0, 1000)
			if err != nil {
				t.Fatal(err)
			}
			var decisions []string
			for _, event := range events {
				if event.EventType != sessionV3StreamIncompleteRecoveryEventTy {
					continue
				}
				var payload struct {
					Step     int    `json:"step"`
					Decision string `json:"decision"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.Step != 1 {
					t.Fatalf("recovery payload = %s, err = %v", event.Payload, err)
				}
				decisions = append(decisions, payload.Decision)
			}
			want := []string{}
			if tc.decision != "" {
				want = append(want, tc.decision)
			}
			if fmt.Sprint(decisions) != fmt.Sprint(want) {
				t.Fatalf("recovery decisions = %v, want %v", decisions, want)
			}
		})
	}
}

func TestV3StreamIncompleteRecordIsDistinctPerEpoch(t *testing.T) {
	// Purpose:
	// - Requirement: the durable stream_incomplete decision record for a step is
	//   keyed by run, step and execution epoch, so the same step number in the
	//   same run after a compaction (which restarts the step loop in a new
	//   epoch) writes its own record.
	// - Threat/regression: a run+step key reused after compaction conflicts
	//   with the earlier record (different payload hash), the write fails, and
	//   the policy then refuses the replay, killing the job on a stream the
	//   replay would have recovered.
	// - Boundary/authority: recordStreamIncompleteEvent and the V3
	//   ApplySessionMutation idempotency check.
	// - Narrowest test layer: the real executor's mutation path over a
	//   temporary store; no provider is involved.
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "stream-incomplete-epochs"), nil)
	defer f.close()
	sessionID := f.createSession("stream-incomplete-epochs")
	executor := f.server.v3SessionExecutor
	for _, epoch := range []string{"epoch-a", "epoch-b"} {
		job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sessionID, RunID: "run-1", EpochID: epoch}
		if err := executor.recordStreamIncompleteEvent(job, 3, sessionV3StreamIncompleteReplayed); err != nil {
			t.Fatalf("record in %s: %v", epoch, err)
		}
	}
	events, err := f.sessions.ListSessionEvents(sessionID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.EventType == sessionV3StreamIncompleteRecoveryEventTy {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("stream_incomplete records = %d, want one per epoch", count)
	}
}
