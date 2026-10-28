package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
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
}

func newQueuedNoteFixture(t *testing.T, model func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response) *queuedNoteFixture {
	t.Helper()
	base := newConfirmationFixture(t, filepath.Join(t.TempDir(), "queued.pebble"), nil)
	f := &queuedNoteFixture{confirmationFixture: base}
	base.runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
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
