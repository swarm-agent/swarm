package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// queuedNoteFixture drives the real V3 executor and HTTP handlers with a
// scripted model. The model's first call posts a second message through the
// same POST /v3/sessions/{id}/messages route a client uses, while the run is
// active; that is the boundary under test.
type queuedNoteFixture struct {
	*confirmationFixture
	sessionID string

	mu            sync.Mutex
	queuedStatus  int
	queuedPayload map[string]any

	// stepErr, when set before the first message, fails a provider step
	// whose request it matches (an injected provider failure).
	stepErr func(req provideriface.Request) error
}

func newQueuedNoteFixture(t *testing.T, model func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response) *queuedNoteFixture {
	t.Helper()
	base := newConfirmationFixture(t, filepath.Join(t.TempDir(), "queued.pebble"), nil)
	f := &queuedNoteFixture{confirmationFixture: base}
	base.runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if f.stepErr != nil {
			if err := f.stepErr(req); err != nil {
				return provideriface.Response{}, err
			}
		}
		return model(f, req), nil
	}
	f.sessionID = base.createSession("queued-notes")
	return f
}

// postDuringRun sends a message while the run is active. It runs on the run's
// goroutine, so it records the outcome for the test goroutine to assert.
func (f *queuedNoteFixture) postDuringRun(requestID, content string) {
	body, _ := json.Marshal(map[string]any{"client_request_id": requestID, "role": "user", "content": content})
	code, out := f.do(http.MethodPost, "/v3/sessions/"+f.sessionID+"/messages", string(body))
	var payload map[string]any
	_ = json.Unmarshal([]byte(out), &payload)
	f.mu.Lock()
	f.queuedStatus, f.queuedPayload = code, payload
	f.mu.Unlock()
}

func (f *queuedNoteFixture) queuedResponse() (int, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queuedStatus, f.queuedPayload
}

func (f *queuedNoteFixture) messages() []pebblestore.MessageSnapshot {
	f.t.Helper()
	messages, err := f.sessions.ListSessionMessages(f.sessionID, 0, 500)
	if err != nil {
		f.t.Fatal(err)
	}
	return messages
}

func (f *queuedNoteFixture) receipts(noteID string) []pebblestore.MessageSnapshot {
	var out []pebblestore.MessageSnapshot
	for _, message := range f.messages() {
		if message.Role == "system" && message.Metadata["source"] == "feedback_delivery" && message.Metadata["message_id"] == noteID {
			out = append(out, message)
		}
	}
	return out
}

func (f *queuedNoteFixture) runIntents() []pebblestore.V3SessionRunIntent {
	f.t.Helper()
	intents, err := f.sessions.Store().ListRunIntents(f.sessionID, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return intents
}

func (f *queuedNoteFixture) requests() []provideriface.Request {
	f.runner.mu.Lock()
	defer f.runner.mu.Unlock()
	return append([]provideriface.Request(nil), f.runner.requests...)
}

func queuedNoteFromResponse(t *testing.T, payload map[string]any) pebblestore.MessageSnapshot {
	t.Helper()
	raw, _ := json.Marshal(payload["message"])
	var message pebblestore.MessageSnapshot
	if err := json.Unmarshal(raw, &message); err != nil || message.ID == "" {
		t.Fatalf("queued response carries no message: %v %s", err, raw)
	}
	return message
}

// countInInput counts provider input items mentioning text, split by whether
// the text arrived as a tool result or as a user message.
func countInInput(input []map[string]any, text string) (toolResults, userMessages int) {
	for _, item := range input {
		if stringMapValue(item, "type") == "function_call_output" {
			toolResults += strings.Count(stringMapValue(item, "output"), text)
			continue
		}
		if item["role"] == "user" {
			raw, _ := json.Marshal(item["content"])
			userMessages += strings.Count(string(raw), text)
		}
	}
	return toolResults, userMessages
}

// Purpose: a message the person sends while the session's run is active is
// queued, not a new turn: the POST answers queued, no run intent is recorded,
// and the note reaches the running model appended to its very next tool result,
// exactly once, after the tool's own output, then gets one delivery receipt.
// Threat: a mid-run message lost, starting a competing run, or delivered twice
// (as a tool-result block and again as a user turn).
// Boundary: acceptSessionsV3QueuedNote (handleSessionV3PrimaryMessages),
// sessionV3QueuedNoteToolInvoker / runruntime.QueuedNoteToolInvoker in
// runProviderToolLoop, RecordFeedbackDeliveryVia, closeQueuedNotes.
// Narrowest layer: the real executor, run loop, tool invoker and HTTP route
// with a scripted model, since the contract spans all of them.
func TestQueuedMessageDuringRunIsAppendedToNextToolResultOnce(t *testing.T) {
	const steer = "steer-toward-the-blue-door"
	f := newQueuedNoteFixture(t, func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response {
		if hasFunctionCallOutput(req.Input) {
			return provideriface.Response{Text: "finished", StopReason: "stop"}
		}
		f.postDuringRun("steer-1", steer)
		// probe_tool is not offered: the invoker records a durable refusal,
		// a real tool result boundary that executes nothing.
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "probe1", Name: "probe_tool", Arguments: `{}`}}}
	})
	f.say(f.sessionID, "start-1", "begin the task")

	code, payload := f.queuedResponse()
	if code != http.StatusOK || payload["queued"] != true || payload["delivery_status"] != runruntime.QueuedNoteStatusQueued {
		t.Fatalf("mid-run message was not queued: %d %v", code, payload)
	}
	if payload["run_intent"] != nil {
		t.Fatalf("queued message recorded a run intent: %v", payload["run_intent"])
	}
	startRun := stableSessionsV3PrimaryRunID(f.sessionID, "start-1")
	if payload["queued_for_run_id"] != startRun {
		t.Fatalf("queued_for_run_id = %v, want %s", payload["queued_for_run_id"], startRun)
	}
	note := queuedNoteFromResponse(t, payload)
	if !runruntime.IsQueuedNote(note) {
		t.Fatalf("stored message is not a queued note: %+v", note.Metadata)
	}

	requests := f.requests()
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d, want 2 (tool call, then answer)", len(requests))
	}
	toolResults, userMessages := countInInput(requests[1].Input, steer)
	if toolResults != 1 || userMessages != 0 {
		t.Fatalf("note delivered %d times in tool results and %d as user messages; want exactly once with the tool result", toolResults, userMessages)
	}
	for _, item := range requests[1].Input {
		if stringMapValue(item, "type") == "function_call_output" && stringMapValue(item, "call_id") == "probe1" {
			output := stringMapValue(item, "output")
			block := strings.Index(output, "<queued_message_from_the_person>")
			if block <= 0 || !strings.Contains(output[:block], "not available to this agent") {
				t.Fatalf("queued block must follow the tool's own output: %q", output)
			}
		}
	}

	receipts := f.receipts(note.ID)
	if len(receipts) != 1 || receipts[0].Metadata["delivery_status"] != "delivered" || receipts[0].Metadata["delivery_channel"] != runruntime.FeedbackDeliveryToolResult || receipts[0].Metadata["run_id"] != startRun {
		t.Fatalf("receipts = %+v, want one tool_result delivery in %s", receipts, startRun)
	}
	if intents := f.runIntents(); len(intents) != 1 || intents[0].RunID != startRun {
		t.Fatalf("run intents = %+v, want only the original run (no competing or follow-up run)", intents)
	}
}

// Purpose: when the run ends with no further tool call, a note queued during
// it is not dropped: it starts the next turn, where the model sees it as an
// ordinary user message, and it is receipted once.
// Threat: a queued message silently stranded because the model answered
// without calling another tool.
// Boundary: closeQueuedNotes / startQueuedNoteFollowup (executor finish),
// sessionV3UndeliveredHistoryQueuedNotes (providerAssistantResponse).
// Narrowest layer: the real executor across two runs with a scripted model.
func TestQueuedMessageWithoutFurtherToolCallStartsNextTurn(t *testing.T) {
	const followup = "please-also-check-the-cellar"
	f := newQueuedNoteFixture(t, func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response {
		if strings.Contains(lastConfirmationTestUserText(req.Input), followup) {
			return provideriface.Response{Text: "second answer", StopReason: "stop"}
		}
		f.postDuringRun("later-1", followup)
		return provideriface.Response{Text: "first answer", StopReason: "stop"}
	})
	f.say(f.sessionID, "start-2", "begin the task")

	code, payload := f.queuedResponse()
	if code != http.StatusOK || payload["queued"] != true {
		t.Fatalf("mid-run message was not queued: %d %v", code, payload)
	}
	note := queuedNoteFromResponse(t, payload)
	followupRun := stableSessionsV3PrimaryRunID(f.sessionID, "queued-notes:"+note.ID)
	f.waitRun(f.sessionID, followupRun, sessionruntime.RunIntentCompleted)

	requests := f.requests()
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d, want 2 (one per turn)", len(requests))
	}
	if toolResults, userMessages := countInInput(requests[0].Input, followup); toolResults+userMessages != 0 {
		t.Fatalf("first turn already saw the note")
	}
	if _, userMessages := countInInput(requests[1].Input, followup); userMessages != 1 {
		t.Fatalf("next turn saw the note %d times as a user message, want 1: %v", userMessages, requests[1].Input)
	}
	receipts := f.receipts(note.ID)
	if len(receipts) != 1 || receipts[0].Metadata["run_id"] != followupRun || receipts[0].Metadata["delivery_channel"] != runruntime.FeedbackDeliveryUserTurn {
		t.Fatalf("receipts = %+v, want one user-turn delivery in %s", receipts, followupRun)
	}
	if intents := f.runIntents(); len(intents) != 2 {
		t.Fatalf("run intents = %+v, want the original and exactly one follow-up", intents)
	}
}

// Purpose: a message to an idle session keeps today's behaviour: it starts a
// run, is not queued, and carries no queued markers, including any a caller
// tries to set.
// Threat: queueing swallowing ordinary turns, or a caller forging a note.
// Boundary: acceptSessionsV3QueuedNote / sessionsV3QueuedNoteReservedMetadata.
// Narrowest layer: the HTTP route over the real executor.
func TestMessageToIdleSessionStillStartsRun(t *testing.T) {
	f := newQueuedNoteFixture(t, func(_ *queuedNoteFixture, _ provideriface.Request) provideriface.Response {
		return provideriface.Response{Text: "answer", StopReason: "stop"}
	})
	body, _ := json.Marshal(map[string]any{"client_request_id": "idle-1", "role": "user", "content": "hello", "metadata": map[string]any{"feedback_note": true, "queued_note": true}})
	code, out := f.do(http.MethodPost, "/v3/sessions/"+f.sessionID+"/messages", string(body))
	if code != http.StatusOK {
		t.Fatalf("message: %d %s", code, out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["queued"] != false || payload["run_intent"] == nil {
		t.Fatalf("idle message must start a run, not queue: %v", payload)
	}
	runID := stableSessionsV3PrimaryRunID(f.sessionID, "idle-1")
	f.waitRun(f.sessionID, runID, sessionruntime.RunIntentCompleted)
	for _, message := range f.messages() {
		if message.Role == "user" && (runruntime.IsFeedbackNote(message) || message.Metadata["queued_note"] != nil) {
			t.Fatalf("caller-supplied delivery markers were stored: %+v", message.Metadata)
		}
	}
	if len(f.requests()) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(f.requests()))
	}
}

// startFailingRun posts the first message and waits for its run to fail.
func (f *queuedNoteFixture) startFailingRun(requestID string) string {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"client_request_id": requestID, "role": "user", "content": "begin the task"})
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+f.sessionID+"/messages", string(body)); code != http.StatusOK {
		f.t.Fatalf("message: %d %s", code, out)
	}
	runID := stableSessionsV3PrimaryRunID(f.sessionID, requestID)
	f.waitRun(f.sessionID, runID, sessionruntime.RunIntentFailed)
	return runID
}

// assertHandedToNextTurn checks a note placed in input that no successful
// provider step consumed: the failed run recorded no receipt for it, and it
// started exactly one follow-up turn, which carried it once as a user message
// (not again in replayed tool output) and receipted it there.
func (f *queuedNoteFixture) assertHandedToNextTurn(note pebblestore.MessageSnapshot, failedRun, text string) {
	f.t.Helper()
	followupRun := stableSessionsV3PrimaryRunID(f.sessionID, "queued-notes:"+note.ID)
	f.waitRun(f.sessionID, followupRun, sessionruntime.RunIntentCompleted)
	receipts := f.receipts(note.ID)
	if len(receipts) != 1 || receipts[0].Metadata["run_id"] != followupRun || receipts[0].Metadata["delivery_channel"] != runruntime.FeedbackDeliveryUserTurn {
		f.t.Fatalf("receipts = %+v, want exactly one user-turn delivery in the follow-up %s and none in failed run %s", receipts, followupRun, failedRun)
	}
	requests := f.requests()
	last := requests[len(requests)-1]
	if toolResults, userMessages := countInInput(last.Input, text); toolResults != 0 || userMessages != 1 {
		f.t.Fatalf("follow-up carried the note %d times in tool results and %d as user messages, want 0 and 1: %v", toolResults, userMessages, last.Input)
	}
	if intents := f.runIntents(); len(intents) != 2 {
		f.t.Fatalf("run intents = %+v, want the failed run and exactly one follow-up", intents)
	}
}

// Purpose: a note appended to a tool result is receipted only after the next
// provider step that carries that result succeeds. When that step fails, the
// note gets no receipt in the failed run and is handed to the next turn.
// Threat: a note marked delivered (and shown delivered in clients) although no
// provider response consumed it, then skipped by the claim cursor and dropped.
// Boundary: QueuedNoteToolInvoker (claim only), runProviderToolLoop
// (TakeInFlight after a successful step, deferred ReturnInFlight),
// closeQueuedNotes (Returned notes start the follow-up).
// Narrowest layer: the real executor and HTTP route with a scripted model
// whose second step fails.
func TestQueuedNoteInToolResultIsNotReceiptedWhenNextStepFails(t *testing.T) {
	const steer = "steer-when-the-next-step-fails"
	// Call 1 posts the note and calls a tool; call 2 (carrying the tool
	// result) fails; call 3 is the follow-up turn.
	var calls atomic.Int32
	f := newQueuedNoteFixture(t, func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response {
		if calls.Load() > 1 {
			return provideriface.Response{Text: "picked up the note", StopReason: "stop"}
		}
		f.postDuringRun("steer-fail", steer)
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "probe1", Name: "probe_tool", Arguments: `{}`}}}
	})
	f.stepErr = func(provideriface.Request) error {
		if calls.Add(1) == 2 {
			return errors.New("injected provider failure")
		}
		return nil
	}
	failedRun := f.startFailingRun("start-fail")

	code, payload := f.queuedResponse()
	if code != http.StatusOK || payload["queued"] != true {
		t.Fatalf("mid-run message was not queued: %d %v", code, payload)
	}
	note := queuedNoteFromResponse(t, payload)
	sawInToolResult := false
	for _, req := range f.requests() {
		if toolResults, _ := countInInput(req.Input, steer); toolResults == 1 {
			sawInToolResult = true
		}
	}
	if !sawInToolResult {
		t.Fatal("the failing step's input never carried the note in a tool result")
	}
	f.assertHandedToNextTurn(note, failedRun, steer)
}

// Purpose: when a later call in the same tool batch errors, the run ends
// before any provider step sees the batch's results; a note appended to an
// earlier result in that batch gets no receipt and starts the next turn.
// Threat: the note receipted at append time and lost with the discarded batch.
// Boundary: executeSessionV3ToolBatch error exit of runProviderToolLoop with
// the deferred ReturnInFlight; closeQueuedNotes.
// Narrowest layer: the real executor with a scripted model whose batch trips
// the identical-call limit on its last call.
func TestQueuedNoteInDiscardedToolBatchStartsNextTurn(t *testing.T) {
	const steer = "steer-when-the-batch-errors"
	// Call 1 posts the note and returns the batch; call 2 is the follow-up.
	var calls atomic.Int32
	f := newQueuedNoteFixture(t, func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response {
		if calls.Add(1) > 1 {
			return provideriface.Response{Text: "picked up the note", StopReason: "stop"}
		}
		f.postDuringRun("steer-batch", steer)
		calls := make([]provideriface.FunctionCall, sessionV3ProviderIdenticalToolCallLimit)
		for i := range calls {
			calls[i] = provideriface.FunctionCall{CallID: fmt.Sprintf("probe%d", i), Name: "probe_tool", Arguments: `{}`}
		}
		return provideriface.Response{FunctionCalls: calls}
	})
	failedRun := f.startFailingRun("start-batch")

	code, payload := f.queuedResponse()
	if code != http.StatusOK || payload["queued"] != true {
		t.Fatalf("mid-run message was not queued: %d %v", code, payload)
	}
	note := queuedNoteFromResponse(t, payload)
	if requests := f.requests(); len(requests) < 1 {
		t.Fatal("no provider request")
	} else if toolResults, userMessages := countInInput(requests[0].Input, steer); toolResults+userMessages != 0 {
		t.Fatal("the first step already saw the note")
	}
	f.assertHandedToNextTurn(note, failedRun, steer)
	if requests := f.requests(); len(requests) != 2 {
		t.Fatalf("provider calls = %d, want 2 (the failed batch's step and the follow-up)", len(requests))
	}
}

// appendPlainFeedbackNote stores a feedback note the way background-command
// reports and Orchestrator send_message trigger:false notes do: a user message
// with feedback_note and no queued_note.
func (f *queuedNoteFixture) appendPlainFeedbackNote(id, content string) {
	session, ok, err := f.sessions.GetSession(f.sessionID)
	if err != nil || !ok {
		f.t.Errorf("session: %v %v", ok, err)
		return
	}
	key := "plain-note:" + id
	result, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: f.sessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
		Kind: pebblestore.V3SessionMutationAppendMessage, NowUnixMs: 1,
		Message: &pebblestore.MessageSnapshot{
			ID: id, SessionID: f.sessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
			Role: "user", Content: content, CreatedAt: 1,
			Metadata: map[string]any{"source": "background_command", "feedback_note": true},
		},
	})
	if err != nil || result.Error != nil || result.Conflict != nil {
		f.t.Errorf("append plain note: %v %+v", err, result)
	}
}

// Purpose: in the V3 executor, a feedback note that is not the person's queued
// note (here shaped like a background command report) never appears inside a
// tool result or the person-attributed block; it keeps its separate user
// message at the next step and its user-message receipt, while the person's
// queued note posted in the same step still rides on the tool result.
// Threat: raw command or agent text presented to the model as the person's
// message (prompt-injection escalation), or such a note dropped.
// Boundary: sessionV3QueuedNoteToolInvoker -> QueuedNoteToolInvoker
// (ClaimMatching IsQueuedNote), the step-boundary claim in
// runProviderToolLoop, FormatQueuedNotesBlock.
// Narrowest layer: the real executor and HTTP route with a scripted model.
func TestNonPersonFeedbackNoteIsNotDeliveredInToolResult(t *testing.T) {
	const steer = "person-steer-in-mixed-batch"
	const report = "background-report-untrusted-output"
	f := newQueuedNoteFixture(t, func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response {
		if hasFunctionCallOutput(req.Input) {
			return provideriface.Response{Text: "finished", StopReason: "stop"}
		}
		f.appendPlainFeedbackNote("bg-note-1", report)
		f.postDuringRun("steer-mixed", steer)
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "probe1", Name: "probe_tool", Arguments: `{}`}}}
	})
	f.say(f.sessionID, "start-mixed", "begin the task")

	code, payload := f.queuedResponse()
	if code != http.StatusOK || payload["queued"] != true {
		t.Fatalf("mid-run message was not queued: %d %v", code, payload)
	}
	note := queuedNoteFromResponse(t, payload)
	requests := f.requests()
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(requests))
	}
	if toolResults, userMessages := countInInput(requests[1].Input, report); toolResults != 0 || userMessages != 1 {
		t.Fatalf("background note delivered %d times in tool results and %d as user messages, want 0 and 1", toolResults, userMessages)
	}
	if toolResults, userMessages := countInInput(requests[1].Input, steer); toolResults != 1 || userMessages != 0 {
		t.Fatalf("person's note delivered %d times in tool results and %d as user messages, want 1 and 0", toolResults, userMessages)
	}
	for _, item := range requests[1].Input {
		output := stringMapValue(item, "output")
		if block := strings.Index(output, "<queued_message_from_the_person>"); block >= 0 && strings.Contains(output[block:], report) {
			t.Fatalf("background note presented as the person's message: %q", output)
		}
	}
	startRun := stableSessionsV3PrimaryRunID(f.sessionID, "start-mixed")
	if receipts := f.receipts("bg-note-1"); len(receipts) != 1 || receipts[0].Metadata["delivery_channel"] != runruntime.FeedbackDeliveryUserTurn || receipts[0].Metadata["run_id"] != startRun {
		t.Fatalf("background note receipts = %+v, want one user-message delivery in %s", receipts, startRun)
	}
	if receipts := f.receipts(note.ID); len(receipts) != 1 || receipts[0].Metadata["delivery_channel"] != runruntime.FeedbackDeliveryToolResult {
		t.Fatalf("person's note receipts = %+v, want one tool-result delivery", receipts)
	}
	if intents := f.runIntents(); len(intents) != 1 {
		t.Fatalf("run intents = %+v, want only the original run", intents)
	}
}

// queuedNoteCheckpointRunner serves canonical checkpoint startup input with no
// transcript, as the run service's BuildPlanCheckpointRunInput does.
type queuedNoteCheckpointRunner struct {
	*runruntime.Service
	input []map[string]any
}

func (r queuedNoteCheckpointRunner) BuildPlanCheckpointRunInput(string, string, runruntime.RunRequest, runruntime.RunStartMeta) ([]map[string]any, bool, error) {
	return append([]map[string]any(nil), r.input...), true, nil
}

// stoppedRunThenCheckpoint drives the real executor through: a run whose model
// call lets the person queue note, the person stopping that run, then a
// checkpoint run whose startup input is checkpointInput only (no transcript).
// It returns the queued note, the checkpoint run's provider request, the
// checkpoint run ID and how many delivery receipts existed while that request
// was in flight.
func stoppedRunThenCheckpoint(t *testing.T, note, checkpointInput string) (*queuedNoteFixture, pebblestore.MessageSnapshot, provideriface.Request, string, int) {
	t.Helper()
	const marker = "checkpoint-startup-marker"
	f := newQueuedNoteFixture(t, func(*queuedNoteFixture, provideriface.Request) provideriface.Response {
		return provideriface.Response{Text: "unused", StopReason: "stop"}
	})
	service, ok := f.server.runner.(*runruntime.Service)
	if !ok {
		t.Fatalf("fixture runner is %T, want *run.Service", f.server.runner)
	}
	f.server.runner = queuedNoteCheckpointRunner{Service: service, input: []map[string]any{{"role": "user", "content": marker + " " + checkpointInput}}}
	entered := make(chan struct{}, 1)
	var receiptsDuringStep atomic.Int64
	receiptsDuringStep.Store(-1)
	f.runner.handler = func(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if raw, _ := json.Marshal(req.Input); strings.Contains(string(raw), marker) {
			count := 0
			for _, message := range f.messages() {
				if message.Role == "system" && message.Metadata["source"] == "feedback_delivery" {
					count++
				}
			}
			receiptsDuringStep.Store(int64(count))
			return provideriface.Response{Text: "checkpoint step done", StopReason: "stop"}, nil
		}
		f.postDuringRun("note-before-stop", note)
		entered <- struct{}{}
		<-ctx.Done()
		return provideriface.Response{}, ctx.Err()
	}

	body, _ := json.Marshal(map[string]any{"client_request_id": "start-stopped", "role": "user", "content": "begin the task"})
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+f.sessionID+"/messages", string(body)); code != http.StatusOK {
		t.Fatalf("message: %d %s", code, out)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first run never reached the provider")
	}
	stoppedRun := stableSessionsV3PrimaryRunID(f.sessionID, "start-stopped")
	if _, cancelled, err := f.server.v3SessionExecutor.CancelRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: f.sessionID, RunID: stoppedRun}, "the person stopped the run"); err != nil || !cancelled {
		t.Fatalf("CancelRun: cancelled=%v err=%v", cancelled, err)
	}
	f.waitRun(f.sessionID, stoppedRun, sessionruntime.RunIntentCancelled)
	code, payload := f.queuedResponse()
	if code != http.StatusOK || payload["queued"] != true {
		t.Fatalf("mid-run message was not queued: %d %v", code, payload)
	}
	queued := queuedNoteFromResponse(t, payload)
	if receipts := f.receipts(queued.ID); len(receipts) != 0 {
		t.Fatalf("the stopped run receipted the note: %+v", receipts)
	}
	if intents := f.runIntents(); len(intents) != 1 {
		t.Fatalf("run intents = %+v, want no follow-up after a stop", intents)
	}

	session, ok, err := f.sessions.GetSession(f.sessionID)
	if err != nil || !ok {
		t.Fatalf("session: %v %v", ok, err)
	}
	const key = "checkpoint-after-stop"
	checkpointRun := stableSessionsV3PrimaryRunID(f.sessionID, key)
	result, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: f.sessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
		Kind: sessionruntime.SessionMutationRecordRunIntent, NowUnixMs: time.Now().UnixMilli(),
		RunIntent: &pebblestore.V3SessionRunIntent{
			SessionID: f.sessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
			RunID: checkpointRun, RunSessionID: f.sessionID, ParentSessionID: f.sessionID,
			PlanID: "plan-queued", CheckpointID: "checkpoint-queued", AttemptID: "attempt-queued",
			Status: sessionruntime.RunIntentPendingExecutor, CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
		},
	})
	if err != nil || result.Error != nil || result.Conflict != nil {
		t.Fatalf("record checkpoint run intent: %v %+v", err, result)
	}
	if !f.server.v3SessionExecutor.EnqueueRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: f.sessionID, RunID: checkpointRun, PlanID: "plan-queued", CheckpointID: "checkpoint-queued", AttemptID: "attempt-queued", ParentSessionID: f.sessionID}) {
		t.Fatal("checkpoint run was not enqueued")
	}
	f.waitRun(f.sessionID, checkpointRun, sessionruntime.RunIntentCompleted)
	var checkpointReq *provideriface.Request
	for _, req := range f.requests() {
		if raw, _ := json.Marshal(req.Input); strings.Contains(string(raw), marker) {
			req := req
			checkpointReq = &req
		}
	}
	if checkpointReq == nil {
		t.Fatal("the checkpoint run never reached the provider")
	}
	return f, queued, *checkpointReq, checkpointRun, int(receiptsDuringStep.Load())
}

// Purpose: a queued note left unreceipted by a stopped run (which starts no
// follow-up) is receipted by a later run only when that run's successful
// provider step carried it. A checkpoint run, whose startup input is the
// canonical checkpoint payload without the transcript, carries the note
// explicitly as a user message and only then receipts it.
// Threat: a note marked delivered (and shown delivered in clients) by a run
// whose input never contained it, and so never sent to any model.
// Boundary: sessionV3UndeliveredHistoryQueuedNotes and
// carrySessionV3HistoryQueuedNotes in providerAssistantResponse, the history
// receipt in runProviderToolLoop, closeQueuedNotes for a cancelled run.
// Narrowest layer: the real executor, HTTP route and stop path with a scripted
// model and a checkpoint input builder; the omission happens in context
// selection that only the executor performs.
func TestQueuedNoteFromStoppedRunIsCarriedByCheckpointRunBeforeReceipt(t *testing.T) {
	const steer = "steer-left-by-a-stopped-run"
	f, note, checkpointReq, checkpointRun, during := stoppedRunThenCheckpoint(t, steer, "checkpoint-start-input-only")
	if toolResults, userMessages := countInInput(checkpointReq.Input, steer); toolResults != 0 || userMessages != 1 {
		t.Fatalf("checkpoint input carried the note %d times in tool results and %d as user messages, want 0 and 1: %v", toolResults, userMessages, checkpointReq.Input)
	}
	if during != 0 {
		t.Fatalf("%d receipts existed before the checkpoint step succeeded, want 0", during)
	}
	receipts := f.receipts(note.ID)
	if len(receipts) != 1 || receipts[0].Metadata["run_id"] != checkpointRun {
		t.Fatalf("receipts = %+v, want exactly one in checkpoint run %s", receipts, checkpointRun)
	}
}

// Purpose: whether a later run carries a queued note is decided by message
// ID, never by searching input text. A short note ("no") whose text also
// occurs inside the canonical checkpoint input ("not", "no") must still be
// appended as its own user message, and receipted only after that step
// succeeds.
// Threat: a substring match treats the note as already present, skips it, and
// then receipts it as delivered although the model never saw it.
// Boundary: carrySessionV3HistoryQueuedNotes with the replayed-ID set from
// providerAssistantResponse / sessionV3ProviderInitialContextInput, and the
// history receipt in runProviderToolLoop.
// Narrowest layer: the real executor and stop path with a scripted model and
// checkpoint input builder, where context selection happens.
func TestShortQueuedNoteIsCarriedByIDNotByInputText(t *testing.T) {
	f, note, checkpointReq, checkpointRun, during := stoppedRunThenCheckpoint(t, "no", "do not stop; no shortcuts")
	if len(checkpointReq.Input) < 2 {
		t.Fatalf("checkpoint input = %v, want the canonical input plus the note", checkpointReq.Input)
	}
	last, _ := json.Marshal(checkpointReq.Input[len(checkpointReq.Input)-1])
	if string(last) != `{"content":[{"text":"no","type":"input_text"}],"role":"user"}` {
		t.Fatalf("the note was not carried as its own user message: last input item %s (input %v)", last, checkpointReq.Input)
	}
	if during != 0 {
		t.Fatalf("%d receipts existed before the checkpoint step succeeded, want 0", during)
	}
	receipts := f.receipts(note.ID)
	if len(receipts) != 1 || receipts[0].Metadata["run_id"] != checkpointRun {
		t.Fatalf("receipts = %+v, want exactly one in checkpoint run %s", receipts, checkpointRun)
	}
}
