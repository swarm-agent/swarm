package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// newCompanionRunFixture turns the queued-note fixture's session into the account
// user's Companion, created through POST /v3/companion, with the fixture's
// scripted test provider as its model preference.
func newCompanionRunFixture(t *testing.T, model func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response) *queuedNoteFixture {
	t.Helper()
	f := newQueuedNoteFixture(t, model)
	workspace := t.TempDir()
	seedSessionsV3PrimaryAuthority(t, f.server, workspace)
	// The Companion is a Swarm session: its model comes from the account's
	// Swarm assignment, captured at creation.
	if f.server.agentModelSettings == nil {
		t.Fatal("agent model settings service is not configured in the fixture")
	}
	ctx := identity.ContextWithPrincipal(context.Background(), testPrincipal())
	for _, slot := range []string{"action", "plan"} {
		if _, err := f.server.agentModelSettings.UpdateSwarmSlot(ctx, slot, pebblestore.AgentModelAssignment{Provider: "test-provider", Model: "test-model", Thinking: "low"}); err != nil {
			t.Fatalf("swarm %s model: %v", slot, err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"workspace_path": workspace})
	code, body := f.do(http.MethodPost, CompanionPath, string(raw))
	if code != http.StatusOK {
		t.Fatalf("ensure companion: %d %s", code, body)
	}
	var created struct {
		Companion CompanionView `json:"companion"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.Companion.SessionID == "" {
		t.Fatalf("companion view: %v %s", err, body)
	}
	f.sessionID = created.Companion.SessionID
	return f
}

func (f *queuedNoteFixture) companionActivity() CompanionActivity {
	f.t.Helper()
	code, body := f.do(http.MethodGet, CompanionPath, "")
	var out struct {
		Companion CompanionView `json:"companion"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil || out.Companion.Activity == nil {
		f.t.Fatalf("companion activity: %d %s", code, body)
	}
	return *out.Companion.Activity
}

// Purpose: the Companion stays responsive while it works. Requirement: a
// Companion run on the V3 executor sends the bounded Companion context (the
// context bank block), not the full transcript; the person's message posted
// while that run is busy with a tool is accepted at once as queued input
// (no competing run), shown honestly by GET /v3/companion activity (active
// run, the input queued for it), delivered to the running model as the
// person's own message after the tool result exactly once, and then no longer
// listed; retrying the same client_request_id after the run ended replays the
// original message instead of adding input or starting a turn. Threat: the
// foreground message lost, delivered twice, starting a second run that
// races the first, a dishonest "idle"/"delivered" state, or a Companion turn
// silently loading its whole history. Boundary: handleCompanion
// (companionActivity), acceptSessionsV3QueuedNote, runProviderToolLoop's
// claim and RecordClaimedFeedbackDelivery, sessionV3ProviderContextMessages
// (sessionV3CompanionContextMessages). Narrowest layer: the real HTTP routes,
// V3 executor and run loop with a scripted provider, since the contract spans
// all of them.
func TestCompanionAcceptsForegroundInputWhileItsRunWorks(t *testing.T) {
	const question = "quick question while that runs: which port does the printer use"
	var (
		mu          sync.Mutex
		during      CompanionActivity
		firstPrompt []map[string]any
	)
	f := newCompanionRunFixture(t, func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response {
		if hasFunctionCallOutput(req.Input) {
			return provideriface.Response{Text: "finished; the printer uses port 9100", StopReason: "stop"}
		}
		f.postDuringRun("question-1", question)
		activity := f.companionActivity()
		mu.Lock()
		during, firstPrompt = activity, req.Input
		mu.Unlock()
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "probe1", Name: "probe_tool", Arguments: `{}`}}}
	})
	f.say(f.sessionID, "start-1", "research the best laser printer drivers for my laptop")
	runID := stableSessionsV3PrimaryRunID(f.sessionID, "start-1")

	code, payload := f.queuedResponse()
	if code != http.StatusOK || payload["queued"] != true || payload["queued_for_run_id"] != runID {
		t.Fatalf("foreground message was not queued for the running turn: %d %v", code, payload)
	}
	note := queuedNoteFromResponse(t, payload)

	mu.Lock()
	activity, prompt := during, firstPrompt
	mu.Unlock()
	if _, users := countInInput(prompt, "Companion context bank"); users != 1 {
		t.Fatalf("the Companion run did not send its bounded context block: %v", prompt)
	}
	if !activity.Run.Active || activity.Run.RunID != runID {
		t.Fatalf("activity during the run = %+v, want the active run %s", activity.Run, runID)
	}
	if len(activity.QueuedInputs) != 1 || activity.QueuedInputs[0].MessageID != note.ID || activity.QueuedInputs[0].DeliveryStatus != CompanionInputQueued || activity.QueuedInputs[0].QueuedForRunID != runID {
		t.Fatalf("activity queued inputs = %+v, want the note queued for %s", activity.QueuedInputs, runID)
	}

	requests := f.requests()
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(requests))
	}
	if toolResults, users := countInInput(requests[1].Input, question); toolResults != 0 || users != 1 {
		t.Fatalf("question delivered %d times in tool output and %d as user input; want once as user input", toolResults, users)
	}
	if receipts := f.receipts(note.ID); len(receipts) != 1 || receipts[0].Metadata["run_id"] != runID {
		t.Fatalf("receipts = %+v, want exactly one in %s", receipts, runID)
	}
	if intents := f.runIntents(); len(intents) != 1 {
		t.Fatalf("run intents = %d, want only the original run (no competing or duplicate turn)", len(intents))
	}
	after := f.companionActivity()
	if after.Run.Active || len(after.QueuedInputs) != 0 {
		t.Fatalf("activity after the run = %+v, want idle with nothing queued", after)
	}

	// A client retry of the same input after the run ended replays it.
	before := len(f.messages())
	body, _ := json.Marshal(map[string]any{"client_request_id": "question-1", "role": "user", "content": question})
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+f.sessionID+"/messages", string(body)); code != http.StatusOK || !strings.Contains(out, note.ID) {
		t.Fatalf("retry did not replay the original input: %d %s", code, out)
	}
	if len(f.messages()) != before || len(f.runIntents()) != 1 {
		t.Fatalf("retry added input or a run: messages %d -> %d, intents %d", before, len(f.messages()), len(f.runIntents()))
	}
}

// Purpose: queued input is reported honestly and boundedly. Requirement:
// companionUndeliveredInputs lists only the person's queued notes without a
// delivery receipt, oldest first; a note whose run ended without delivering
// it waits for the next turn unless a run is active (which carries it);
// notes queued by a task (message_task) and receipted notes are not listed;
// at most companionActivityQueuedMax are listed, with more set. Threat: a
// stopped run's input shown as about to be read, delivered input shown as
// pending, task-authored notes shown as the person's, an unbounded list.
// Boundary: companionUndeliveredInputs. Narrowest layer: the pure function
// over stored message shapes (the API test covers the live route).
func TestCompanionActivityReportsQueuedInputHonestly(t *testing.T) {
	note := func(id string, seq uint64, extra map[string]any) pebblestore.MessageSnapshot {
		metadata := map[string]any{"feedback_note": true, runruntime.QueuedNoteMetadataKey: true, runruntime.QueuedNoteStatusMetadata: runruntime.QueuedNoteStatusQueued, runruntime.QueuedNoteRunMetadataKey: "run-stopped"}
		for k, v := range extra {
			metadata[k] = v
		}
		return pebblestore.MessageSnapshot{ID: id, GlobalSeq: seq, Role: "user", Content: "note " + id, Metadata: metadata}
	}
	messages := []pebblestore.MessageSnapshot{
		note("late", 5, nil),
		note("early", 2, nil),
		note("delivered", 3, nil),
		{ID: "receipt", GlobalSeq: 4, Role: "system", Content: "receipt", Metadata: map[string]any{"source": "feedback_delivery", "message_id": "delivered"}},
		note("from-task", 6, map[string]any{pebblestore.ProjectTaskMessageSourceMetadata: pebblestore.ProjectTaskMessageSourceTask}),
		{ID: "plain", GlobalSeq: 7, Role: "user", Content: "an ordinary turn message"},
	}
	idle, more := companionUndeliveredInputs(append([]pebblestore.MessageSnapshot(nil), messages...), "")
	if more || len(idle) != 2 || idle[0].MessageID != "early" || idle[1].MessageID != "late" {
		t.Fatalf("idle inputs = %+v more=%v, want early then late", idle, more)
	}
	for _, input := range idle {
		if input.DeliveryStatus != CompanionInputWaitingNextTurn || input.QueuedForRunID != "run-stopped" {
			t.Fatalf("stopped run's input = %+v, want waiting_next_turn", input)
		}
	}
	active, _ := companionUndeliveredInputs(append([]pebblestore.MessageSnapshot(nil), messages...), "run-next")
	if len(active) != 2 || active[0].DeliveryStatus != CompanionInputQueued {
		t.Fatalf("inputs with an active run = %+v, want queued", active)
	}
	var many []pebblestore.MessageSnapshot
	for i := 0; i < companionActivityQueuedMax+3; i++ {
		many = append(many, note(fmt.Sprintf("n%d", i), uint64(i+1), nil))
	}
	listed, more := companionUndeliveredInputs(many, "")
	if len(listed) != companionActivityQueuedMax || !more {
		t.Fatalf("listed %d more=%v, want %d and more", len(listed), more, companionActivityQueuedMax)
	}
}
