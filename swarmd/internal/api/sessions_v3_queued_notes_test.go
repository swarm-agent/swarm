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
